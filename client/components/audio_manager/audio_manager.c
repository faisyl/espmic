/*
 * audio_manager.c - Stream lifecycle + pipeline orchestration (spec Sections
 * 4, 6, 11, 12, 14).
 */
#include "audio_manager.h"

#include <string.h>
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "esp_log.h"
#include "esp_heap_caps.h"

#include "pcm_ring.h"
#include "encoded_queue.h"
#include "i2s_capture.h"
#include "opus_encoder_task.h"
#include "rtp_sender.h"

static const char *TAG = "audio_mgr";

/* Buffer geometry (spec Section 6 recommended sizes). */
#define PCM_RING_MS        200
#define PCM_RING_SAMPLES   ((I2S_CAP_SAMPLE_RATE * PCM_RING_MS / 1000) * I2S_CAP_CHANNELS)
#define ENC_QUEUE_SLOTS    64
#define ENC_QUEUE_SLOT_SZ  1500

typedef struct {
    bool                    inited;
    audio_manager_config_t  cfg;

    /* pipeline state */
    bool                    streaming;
    char                    stream_id[64];

    /* owned storage (heap, allocated per stream) */
    int32_t                *ring_storage;
    uint8_t                *eq_arena;
    size_t                 *eq_lengths;

    pcm_ring_t              ring;
    encoded_queue_t         queue;
    SemaphoreHandle_t       ring_lock;
    SemaphoreHandle_t       queue_lock;

    /* task handles */
    i2s_capture_handle_t    i2s;
    opus_task_handle_t      opus;
    rtp_sender_handle_t     rtp;

    /* late counters shared with tasks */
    volatile uint64_t       capture_late;
    volatile uint64_t       encoder_late;

    uint32_t                stream_seq; /* seeds RTP SSRC/seq per stream */
} amgr_t;

static amgr_t g;

/* Prefer PSRAM (spec Section 2) but fall back to internal RAM. */
static void *aud_alloc(size_t n)
{
    void *p = heap_caps_malloc(n, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
    if (!p) p = heap_caps_malloc(n, MALLOC_CAP_8BIT);
    return p;
}

esp_err_t audio_manager_init(const audio_manager_config_t *cfg)
{
    if (!cfg) return ESP_ERR_INVALID_ARG;
    memset(&g, 0, sizeof(g));
    g.cfg = *cfg;
    if (g.cfg.complexity == 0) g.cfg.complexity = 6;
    if (g.cfg.default_bitrate == 0) g.cfg.default_bitrate = 128000;
    g.inited = true;
    ESP_LOGI(TAG, "init: bclk=%d ws=%d din=%d br=%u",
             g.cfg.i2s_bclk_gpio, g.cfg.i2s_ws_gpio, g.cfg.i2s_din_gpio,
             (unsigned)g.cfg.default_bitrate);
    return ESP_OK;
}

/* Validate a start_stream request (spec Section 11). */
static esp_err_t validate(const audio_stream_params_t *p)
{
    if (p->sample_rate != I2S_CAP_SAMPLE_RATE) {
        ESP_LOGW(TAG, "reject: sample_rate=%u", (unsigned)p->sample_rate);
        return ESP_ERR_INVALID_ARG;
    }
    if (p->channels != I2S_CAP_CHANNELS) {
        ESP_LOGW(TAG, "reject: channels=%u", (unsigned)p->channels);
        return ESP_ERR_INVALID_ARG;
    }
    if (p->frame_ms != 20) {
        ESP_LOGW(TAG, "reject: frame_ms=%u", (unsigned)p->frame_ms);
        return ESP_ERR_INVALID_ARG;
    }
    if (p->dest_ip[0] == '\0' || p->dest_port == 0) {
        ESP_LOGW(TAG, "reject: bad destination");
        return ESP_ERR_INVALID_ARG;
    }
    return ESP_OK;
}

static void free_storage(void)
{
    if (g.ring_storage) { heap_caps_free(g.ring_storage); g.ring_storage = NULL; }
    if (g.eq_arena)     { heap_caps_free(g.eq_arena);     g.eq_arena = NULL; }
    if (g.eq_lengths)   { heap_caps_free(g.eq_lengths);   g.eq_lengths = NULL; }
    if (g.ring_lock)    { vSemaphoreDelete(g.ring_lock);  g.ring_lock = NULL; }
    if (g.queue_lock)   { vSemaphoreDelete(g.queue_lock); g.queue_lock = NULL; }
}

esp_err_t audio_manager_start_stream(const audio_stream_params_t *params)
{
    if (!g.inited || !params) return ESP_ERR_INVALID_ARG;
    if (g.streaming) return ESP_ERR_INVALID_STATE;

    esp_err_t err = validate(params);
    if (err != ESP_OK) return err;

    /* Allocate ring + queue storage. */
    g.ring_storage = aud_alloc(PCM_RING_SAMPLES * sizeof(int32_t));
    g.eq_arena     = aud_alloc(ENC_QUEUE_SLOTS * ENC_QUEUE_SLOT_SZ);
    g.eq_lengths   = aud_alloc(ENC_QUEUE_SLOTS * sizeof(size_t));
    g.ring_lock    = xSemaphoreCreateMutex();
    g.queue_lock   = xSemaphoreCreateMutex();
    if (!g.ring_storage || !g.eq_arena || !g.eq_lengths ||
        !g.ring_lock || !g.queue_lock) {
        ESP_LOGE(TAG, "alloc failed");
        free_storage();
        return ESP_ERR_NO_MEM;
    }

    pcm_ring_init(&g.ring, g.ring_storage, PCM_RING_SAMPLES);
    eq_init(&g.queue, g.eq_arena, g.eq_lengths, ENC_QUEUE_SLOTS, ENC_QUEUE_SLOT_SZ);
    g.capture_late = 0;
    g.encoder_late = 0;
    g.stream_seq++;

    uint32_t bitrate = params->bitrate ? params->bitrate : g.cfg.default_bitrate;

    /* Start the send stage first so early packets are not dropped. */
    rtp_sender_config_t rcfg = {
        .queue = &g.queue, .queue_lock = g.queue_lock,
        .dest_port = params->dest_port,
        .payload_type = params->payload_type ? params->payload_type : 111,
        .ssrc_seed = (uint32_t)(0xA5A50000u ^ g.stream_seq),
        .task_core = -1,
    };
    strncpy(rcfg.dest_ip, params->dest_ip, sizeof(rcfg.dest_ip) - 1);
    err = rtp_sender_start(&rcfg, &g.rtp);
    if (err != ESP_OK) { free_storage(); return err; }

    opus_task_config_t ocfg = {
        .ring = &g.ring, .ring_lock = g.ring_lock,
        .queue = &g.queue, .queue_lock = g.queue_lock,
        .sample_rate = params->sample_rate,
        .channels = params->channels,
        .bitrate = (int)bitrate,
        .complexity = g.cfg.complexity,
        .vbr = params->vbr ? 1 : 0,
        .fec = params->fec ? 1 : 0,
        .dtx = params->dtx ? 1 : 0,
        .late_counter = &g.encoder_late,
        .task_core = -1,
    };
    err = opus_task_start(&ocfg, &g.opus);
    if (err != ESP_OK) { rtp_sender_stop(g.rtp); g.rtp = NULL; free_storage(); return err; }

    i2s_capture_config_t icfg = {
        .bclk_gpio = g.cfg.i2s_bclk_gpio,
        .ws_gpio   = g.cfg.i2s_ws_gpio,
        .din_gpio  = g.cfg.i2s_din_gpio,
        .sample_rate = params->sample_rate,
        .ring = &g.ring, .ring_lock = g.ring_lock,
        .late_counter = &g.capture_late,
        .task_core = -1,
    };
    err = i2s_capture_start(&icfg, &g.i2s);
    if (err != ESP_OK) {
        opus_task_stop(g.opus); g.opus = NULL;
        rtp_sender_stop(g.rtp); g.rtp = NULL;
        free_storage();
        return err;
    }

    g.streaming = true;
    strncpy(g.stream_id, params->stream_id, sizeof(g.stream_id) - 1);
    g.stream_id[sizeof(g.stream_id) - 1] = '\0';
    ESP_LOGI(TAG, "stream started id=%s -> %s:%u br=%u",
             g.stream_id, params->dest_ip, (unsigned)params->dest_port,
             (unsigned)bitrate);
    return ESP_OK;
}

esp_err_t audio_manager_stop_stream(void)
{
    if (!g.streaming) return ESP_OK; /* idempotent */

    /* Stop capture first so no new PCM is produced, then encode, then send. */
    if (g.i2s)  { i2s_capture_stop(g.i2s);  g.i2s = NULL; }
    if (g.opus) { opus_task_stop(g.opus);   g.opus = NULL; }
    if (g.rtp)  { rtp_sender_stop(g.rtp);   g.rtp = NULL; }

    free_storage();
    g.streaming = false;
    ESP_LOGI(TAG, "stream stopped id=%s", g.stream_id);
    g.stream_id[0] = '\0';
    return ESP_OK;
}

esp_err_t audio_manager_apply_config(uint32_t default_bitrate)
{
    if (!g.inited) return ESP_ERR_INVALID_STATE;
    if (default_bitrate) g.cfg.default_bitrate = default_bitrate;
    ESP_LOGI(TAG, "config applied: default_bitrate=%u",
             (unsigned)g.cfg.default_bitrate);
    return ESP_OK;
}

bool audio_manager_is_streaming(void) { return g.streaming; }

const char *audio_manager_stream_id(void) { return g.stream_id; }

void audio_manager_get_stats(audio_stats_t *out)
{
    if (!out) return;
    memset(out, 0, sizeof(*out));
    out->streaming = g.streaming;
    if (!g.streaming) return;

    if (g.ring_lock) xSemaphoreTake(g.ring_lock, portMAX_DELAY);
    out->pcm_overflow   = g.ring.overflow_count;
    out->pcm_written    = g.ring.total_written;
    out->pcm_read       = g.ring.total_read;
    out->pcm_high_water = g.ring.high_water;
    out->pcm_count      = g.ring.count;
    if (g.ring_lock) xSemaphoreGive(g.ring_lock);

    if (g.queue_lock) xSemaphoreTake(g.queue_lock, portMAX_DELAY);
    out->enc_drops      = g.queue.drop_count;
    out->enc_rejects    = g.queue.reject_count;
    out->enc_pushed     = g.queue.total_pushed;
    out->enc_popped     = g.queue.total_popped;
    out->enc_high_water = g.queue.high_water;
    out->enc_count      = g.queue.count;
    if (g.queue_lock) xSemaphoreGive(g.queue_lock);

    out->encoder_late = g.encoder_late;
    out->capture_late = g.capture_late;

    if (g.rtp) {
        rtp_sender_stats_t rs;
        rtp_sender_get_stats(g.rtp, &rs);
        out->rtp_packets_sent = rs.packets_sent;
        out->rtp_bytes_sent   = rs.bytes_sent;
        out->rtp_send_errors  = rs.send_errors;
        out->rtp_ssrc         = rs.ssrc;
    }
}
