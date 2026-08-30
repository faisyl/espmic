/*
 * opus_encoder_task.c - Opus encode task (spec Section 7).
 */
#include "opus_encoder_task.h"

#include <stdlib.h>
#include <string.h>
#include "opus.h"
#include "freertos/task.h"
#include "esp_log.h"

static const char *TAG = "opus_task";

/* Max compressed Opus packet we will emit for one 20 ms stereo frame. libopus
 * recommends up to ~4000 bytes; 1500 comfortably covers 128 kbps VBR and fits a
 * single UDP datagram after the 12-byte RTP header. */
#define OPUS_MAX_PACKET 1500

struct opus_task_ctx {
    opus_task_config_t cfg;
    OpusEncoder       *enc;
    TaskHandle_t       task;
    volatile bool      running;
};

/* Try to pull exactly one full frame (OPUS_FRAME_SAMPLES interleaved) from the
 * ring under the lock. Returns true if a full frame was copied to `frame`. */
static bool take_frame(struct opus_task_ctx *ctx, int32_t *frame)
{
    bool have = false;
    if (ctx->cfg.ring_lock) xSemaphoreTake(ctx->cfg.ring_lock, portMAX_DELAY);
    if (pcm_ring_count(ctx->cfg.ring) >= OPUS_FRAME_SAMPLES) {
        size_t got = pcm_ring_read(ctx->cfg.ring, frame, OPUS_FRAME_SAMPLES);
        have = (got == OPUS_FRAME_SAMPLES);
    }
    if (ctx->cfg.ring_lock) xSemaphoreGive(ctx->cfg.ring_lock);
    return have;
}

static void opus_task(void *arg)
{
    struct opus_task_ctx *ctx = (struct opus_task_ctx *)arg;
    static int32_t     frame[OPUS_FRAME_SAMPLES];
    static opus_int16  pcm16[OPUS_FRAME_SAMPLES];
    static uint8_t     packet[OPUS_MAX_PACKET];

    ESP_LOGI(TAG, "opus task started");
    while (ctx->running) {
        if (!take_frame(ctx, frame)) {
            /* Not enough audio yet; wait ~ half a frame and retry. Bounded wait,
             * never blocks forever (spec Section 14). */
            vTaskDelay(pdMS_TO_TICKS(5));
            continue;
        }

        /* Narrow the 24-bit-in-int32 samples to the int16 libopus expects
         * (encoder boundary conversion, spec Sections 5/7). */
        for (int i = 0; i < OPUS_FRAME_SAMPLES; i++) {
            pcm16[i] = (opus_int16)(frame[i] >> 8); /* 24-bit -> 16-bit */
        }

        int n = opus_encode(ctx->enc, pcm16, OPUS_FRAME_SAMPLES_PER_CH,
                            packet, sizeof(packet));
        if (n < 0) {
            if (ctx->cfg.late_counter) (*ctx->cfg.late_counter)++;
            ESP_LOGW(TAG, "opus_encode: %s", opus_strerror(n));
            continue;
        }
        if (n <= 2) {
            /* DTX/empty packet: nothing to send. */
            continue;
        }

        if (ctx->cfg.queue_lock) xSemaphoreTake(ctx->cfg.queue_lock, portMAX_DELAY);
        eq_push(ctx->cfg.queue, packet, (size_t)n);
        if (ctx->cfg.queue_lock) xSemaphoreGive(ctx->cfg.queue_lock);
    }

    ESP_LOGI(TAG, "opus task exiting");
    ctx->task = NULL;
    vTaskDelete(NULL);
}

esp_err_t opus_task_start(const opus_task_config_t *cfg, opus_task_handle_t *out)
{
    if (!cfg || !out || !cfg->ring || !cfg->queue) return ESP_ERR_INVALID_ARG;

    struct opus_task_ctx *ctx = calloc(1, sizeof(*ctx));
    if (!ctx) return ESP_ERR_NO_MEM;
    ctx->cfg = *cfg;
    if (ctx->cfg.sample_rate == 0) ctx->cfg.sample_rate = 48000;
    if (ctx->cfg.channels == 0)    ctx->cfg.channels = OPUS_FRAME_CHANNELS;
    if (ctx->cfg.bitrate == 0)     ctx->cfg.bitrate = 128000;
    if (ctx->cfg.complexity == 0)  ctx->cfg.complexity = 5;

    int err = OPUS_OK;
    ctx->enc = opus_encoder_create(ctx->cfg.sample_rate, ctx->cfg.channels,
                                   OPUS_APPLICATION_AUDIO, &err);
    if (!ctx->enc || err != OPUS_OK) {
        ESP_LOGE(TAG, "opus_encoder_create: %s", opus_strerror(err));
        free(ctx);
        return ESP_FAIL;
    }

    /* Spec Section 7 initial configuration. */
    opus_encoder_ctl(ctx->enc, OPUS_SET_BITRATE(ctx->cfg.bitrate));
    opus_encoder_ctl(ctx->enc, OPUS_SET_VBR(ctx->cfg.vbr ? 1 : 0));
    opus_encoder_ctl(ctx->enc, OPUS_SET_VBR_CONSTRAINT(0));
    opus_encoder_ctl(ctx->enc, OPUS_SET_INBAND_FEC(ctx->cfg.fec ? 1 : 0));
    opus_encoder_ctl(ctx->enc, OPUS_SET_DTX(ctx->cfg.dtx ? 1 : 0));
    opus_encoder_ctl(ctx->enc, OPUS_SET_COMPLEXITY(ctx->cfg.complexity));
    opus_encoder_ctl(ctx->enc, OPUS_SET_SIGNAL(OPUS_AUTO));

    ctx->running = true;
    int prio = ctx->cfg.task_priority > 0 ? ctx->cfg.task_priority
                                          : (configMAX_PRIORITIES - 4);
    BaseType_t ok;
    if (ctx->cfg.task_core >= 0) {
        ok = xTaskCreatePinnedToCore(opus_task, "opus", 8192, ctx, prio,
                                     &ctx->task, ctx->cfg.task_core);
    } else {
        ok = xTaskCreate(opus_task, "opus", 8192, ctx, prio, &ctx->task);
    }
    if (ok != pdPASS) {
        ctx->running = false;
        opus_encoder_destroy(ctx->enc);
        free(ctx);
        return ESP_ERR_NO_MEM;
    }

    *out = ctx;
    ESP_LOGI(TAG, "started: %d ch @ %u Hz, %d bps, vbr=%d cx=%d",
             ctx->cfg.channels, (unsigned)ctx->cfg.sample_rate,
             ctx->cfg.bitrate, ctx->cfg.vbr, ctx->cfg.complexity);
    return ESP_OK;
}

esp_err_t opus_task_stop(opus_task_handle_t h)
{
    if (!h) return ESP_ERR_INVALID_ARG;
    h->running = false;
    for (int i = 0; i < 20 && h->task != NULL; i++) {
        vTaskDelay(pdMS_TO_TICKS(10));
    }
    if (h->enc) {
        opus_encoder_destroy(h->enc);
        h->enc = NULL;
    }
    free(h);
    return ESP_OK;
}
