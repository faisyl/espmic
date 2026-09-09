/*
 * opus_encoder_task.c - Opus encode task (spec Section 7).
 */
#include "opus_encoder_task.h"

#include <stdlib.h>
#include <string.h>
#include "opus.h"
#include "freertos/task.h"
#include "esp_task_wdt.h"
#include "esp_log.h"

static const char *TAG = "opus_task";

/* Bounded wait for the ring/queue locks (Jim's P2 nit): never wait forever. On
 * timeout the frame/packet is dropped and the late counter is bumped. */
#define OPUS_LOCK_TIMEOUT_MS 50

/* Max compressed Opus packet we will emit for one 20 ms stereo frame. libopus
 * recommends up to ~4000 bytes; 1500 comfortably covers 128 kbps VBR and fits a
 * single UDP datagram after the 12-byte RTP header. */
#define OPUS_MAX_PACKET 1500

/* opus_encode() for 48 kHz stereo at complexity 6 has a very deep call stack.
 * Measured on hardware: an 8 KB task stack overflowed, and so did 20 KB (the
 * high-water probe showed only ~1.8 KB used at the top of the loop, i.e. the
 * first opus_encode() call itself consumed the remaining >18 KB before the next
 * probe). libopus float encode at this complexity/stereo peaks in the
 * ~25-35 KB range, so give the task 40 KB. The high-water log below reports the
 * true post-encode headroom so this can be trimmed later if desired. */
#define OPUS_TASK_STACK 40960

/*
 * Automatic Gain Control (AGC).
 *
 * The ICS43434 MEMS mic outputs speech at roughly -50 dBFS, so a straight
 * 24->16 narrowing (or the old fixed +12 dB) is far too quiet. The AGC tracks
 * the per-frame peak and adapts a digital gain to hold the output near a target
 * level with headroom, so normal speech is loud without loud/close sounds
 * clipping. Gains are expressed in the same units as gain_q8 (256 = unity,
 * applied as sample*g >> 16) so both paths share the narrowing math.
 *
 * Fast attack (drop gain quickly when a loud sound arrives, before it clips) and
 * slow release (raise gain gently when it goes quiet, so the noise floor doesn't
 * pump). A noise gate freezes the gain during near-silence so room noise isn't
 * cranked up between words. A hard int16 clamp is the final safety limiter.
 */
#define AGC_TARGET_PEAK   6000   /* target int16 output peak (~-14.7 dBFS headroom) */
#define AGC_G_MIN         256    /* unity (24->16 full-scale); never attenuate below this */
#define AGC_G_MAX         20000  /* ~+38 dB over unity; bounds noise amplification */
#define AGC_NOISE_FLOOR   30000  /* 24-bit peak below this = silence -> hold gain (~-49 dBFS) */
#define AGC_ATTACK_NUM    160    /* /256 per frame: fast gain reduction on loud onsets */
#define AGC_RELEASE_NUM   8      /* /256 per frame: slow gain rise (~0.6 s) when quiet */

/*
 * DC blocker. The ICS43434 presents a large constant DC bias (measured ~-9.6%
 * of full scale) on top of the audio, which otherwise dominates the level and
 * starves the real signal (and would pin the AGC to the DC). A first-order
 * high-pass removes it: y[n] = x[n] - x[n-1] + R*y[n-1], per channel.
 * R = 0.999 (Q15 32735) => ~7.6 Hz cutoff at 48 kHz, well below speech.
 */
#define DC_BLOCK_R_Q15    32735
#define MAX_CH            2

struct opus_task_ctx {
    opus_task_config_t cfg;
    OpusEncoder       *enc;
    TaskHandle_t       task;
    volatile bool      running;
    int32_t            agc_g;              /* current AGC gain (Q, 256=unity); state across frames */
    int32_t            dc_prev_x[MAX_CH];  /* DC blocker: last input, per channel */
    int64_t            dc_prev_y[MAX_CH];  /* DC blocker: last output, per channel */
};

/* Try to pull exactly one full frame (OPUS_FRAME_SAMPLES interleaved) from the
 * ring under the lock. Returns true if a full frame was copied to `frame`. */
static bool take_frame(struct opus_task_ctx *ctx, int32_t *frame)
{
    bool have = false;
    if (ctx->cfg.ring_lock) {
        if (xSemaphoreTake(ctx->cfg.ring_lock,
                           pdMS_TO_TICKS(OPUS_LOCK_TIMEOUT_MS)) != pdTRUE) {
            if (ctx->cfg.late_counter) (*ctx->cfg.late_counter)++;
            return false; /* lock contended; treat as underrun, retry later */
        }
    }
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

    /* Watchdog-subscribe this critical loop (spec Section 14). */
    esp_task_wdt_add(NULL);

    ESP_LOGI(TAG, "opus task started");
    uint32_t dbg_n = 0;
    while (ctx->running) {
        esp_task_wdt_reset();

        /* Periodically report the minimum free stack so headroom is visible
         * on-device; a small/shrinking value means OPUS_TASK_STACK is too tight.
         * NOTE: on ESP-IDF uxTaskGetStackHighWaterMark() returns BYTES (not
         * words as in vanilla FreeRTOS). Logged every ~50 frames (~1 s at
         * 20 ms/frame) so the true post-encode peak shows up quickly. */
        if ((dbg_n++ % 50u) == 0u) {
            ESP_LOGI(TAG, "opus stack high-water (min free bytes): %u of %u",
                     (unsigned)uxTaskGetStackHighWaterMark(NULL),
                     (unsigned)OPUS_TASK_STACK);
        }

        if (!take_frame(ctx, frame)) {
            /* Not enough audio yet; wait ~ half a frame and retry. Bounded wait,
             * never blocks forever (spec Section 14). */
            vTaskDelay(pdMS_TO_TICKS(5));
            continue;
        }

        /* Remove the mic's DC bias in place before any level analysis so the
         * peak/AGC and the final narrowing all operate on the real (AC) signal.
         * channels is 1 or 2; state is kept per channel across frames. */
        int nch = ctx->cfg.channels > 0 && ctx->cfg.channels <= MAX_CH
                      ? ctx->cfg.channels : 1;
        for (int i = 0; i < OPUS_FRAME_SAMPLES; i++) {
            int ch = i % nch;
            int32_t x = frame[i];
            int64_t y = (int64_t)x - ctx->dc_prev_x[ch]
                        + ((DC_BLOCK_R_Q15 * ctx->dc_prev_y[ch]) >> 15);
            ctx->dc_prev_x[ch] = x;
            ctx->dc_prev_y[ch] = y;
            frame[i] = (int32_t)y;
        }

        /* Choose the digital gain for this frame. AGC adapts it to the signal
         * (see the AGC_* constants above); otherwise a static gain_q8 is used.
         * Both are applied as v = frame * g >> 16, where g=256 is unity 24->16. */
        int32_t g;
        if (ctx->cfg.agc) {
            /* Peak of this 24-bit-in-int32 frame (both channels). Also track
             * sum/min/max to expose any DC offset in the debug log (a large
             * non-zero mean means the mic pipeline needs DC blocking). */
            int32_t peak = 1, fmin = frame[0], fmax = frame[0];
            int64_t sum = 0;
            for (int i = 0; i < OPUS_FRAME_SAMPLES; i++) {
                int32_t s = frame[i];
                int32_t a = s < 0 ? -s : s;
                if (a > peak) peak = a;
                if (s < fmin) fmin = s;
                if (s > fmax) fmax = s;
                sum += s;
            }
            int32_t mean = (int32_t)(sum / OPUS_FRAME_SAMPLES);
            /* Gain that would bring this peak to the target: peak*g>>16 = target. */
            int32_t desired = (int32_t)(((int64_t)AGC_TARGET_PEAK << 16) / peak);
            if (desired > AGC_G_MAX) desired = AGC_G_MAX;
            if (desired < AGC_G_MIN) desired = AGC_G_MIN;
            if (peak < AGC_NOISE_FLOOR) desired = ctx->agc_g; /* silence: hold */

            /* Fast attack when reducing gain, slow release when raising it. */
            int32_t num = (desired < ctx->agc_g) ? AGC_ATTACK_NUM : AGC_RELEASE_NUM;
            ctx->agc_g += (int32_t)(((int64_t)(desired - ctx->agc_g) * num) >> 8);
            if (ctx->agc_g < AGC_G_MIN) ctx->agc_g = AGC_G_MIN;
            if (ctx->agc_g > AGC_G_MAX) ctx->agc_g = AGC_G_MAX;
            g = ctx->agc_g;

            if ((dbg_n % 50u) == 0u) {
                ESP_LOGI(TAG, "agc: peak=%d gain=%d mean=%d min=%d max=%d ac=%d",
                         (int)peak, (int)g, (int)mean, (int)fmin, (int)fmax,
                         (int)(fmax - fmin));
            }
        } else {
            g = ctx->cfg.gain_q8;
        }

        /* Narrow 24-bit-in-int32 to the int16 libopus expects, applying g. The
         * clamp is the final hard limiter against any residual over-range. */
        for (int i = 0; i < OPUS_FRAME_SAMPLES; i++) {
            int64_t v = ((int64_t)frame[i] * g + (1 << 15)) >> 16;
            if (v > 32767) v = 32767;
            if (v < -32768) v = -32768;
            pcm16[i] = (opus_int16)v;
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

        if (ctx->cfg.queue_lock) {
            if (xSemaphoreTake(ctx->cfg.queue_lock,
                               pdMS_TO_TICKS(OPUS_LOCK_TIMEOUT_MS)) != pdTRUE) {
                if (ctx->cfg.late_counter) (*ctx->cfg.late_counter)++;
                ESP_LOGW(TAG, "queue lock timeout; dropping encoded packet");
                continue; /* drop rather than block the encoder */
            }
        }
        eq_push(ctx->cfg.queue, packet, (size_t)n);
        if (ctx->cfg.queue_lock) xSemaphoreGive(ctx->cfg.queue_lock);
    }

    esp_task_wdt_delete(NULL);
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
    if (ctx->cfg.gain_q8 == 0)     ctx->cfg.gain_q8 = 1024;
    ctx->agc_g = AGC_G_MIN;  /* start at unity; release ramps up to the signal */

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
        ok = xTaskCreatePinnedToCore(opus_task, "opus", OPUS_TASK_STACK, ctx, prio,
                                     &ctx->task, ctx->cfg.task_core);
    } else {
        ok = xTaskCreate(opus_task, "opus", OPUS_TASK_STACK, ctx, prio, &ctx->task);
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
