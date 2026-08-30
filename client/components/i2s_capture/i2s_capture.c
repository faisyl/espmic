/*
 * i2s_capture.c - I2S/Philips stereo capture task (spec Sections 5, 6, 14).
 */
#include "i2s_capture.h"

#include <string.h>
#include <stdlib.h>
#include "driver/i2s_std.h"
#include "freertos/task.h"
#include "esp_log.h"

static const char *TAG = "i2s_capture";

/* DMA read chunk: 240 stereo frames = 5 ms at 48 kHz. Small enough to keep
 * latency low, large enough to amortise the read syscall. */
#define CAP_FRAMES_PER_READ 240
#define CAP_SAMPLES_PER_READ (CAP_FRAMES_PER_READ * I2S_CAP_CHANNELS)

struct i2s_capture_ctx {
    i2s_capture_config_t cfg;
    i2s_chan_handle_t    rx;
    TaskHandle_t         task;
    volatile bool        running;
};

/*
 * Convert one 32-bit I2S slot to the value stored in the ring. The ICS43434
 * presents its 24 valid bits MSB-justified in the 32-bit slot. We preserve the
 * 24-bit magnitude in a 32-bit signed container (spec Section 5) by an
 * arithmetic shift down by 8; the encoder boundary (opus_encoder) does the
 * final narrowing to 16-bit for Opus.
 */
static inline int32_t slot_to_sample(int32_t slot)
{
    return slot >> 8; /* arithmetic shift keeps sign; yields 24-bit range */
}

static void capture_task(void *arg)
{
    struct i2s_capture_ctx *ctx = (struct i2s_capture_ctx *)arg;
    static int32_t raw[CAP_SAMPLES_PER_READ]; /* DMA landing buffer */

    ESP_LOGI(TAG, "capture task started");
    while (ctx->running) {
        size_t got = 0;
        esp_err_t err = i2s_channel_read(ctx->rx, raw, sizeof(raw), &got,
                                         pdMS_TO_TICKS(200));
        if (err != ESP_OK || got == 0) {
            if (ctx->cfg.late_counter) (*ctx->cfg.late_counter)++;
            ESP_LOGW(TAG, "i2s read: %s got=%u", esp_err_to_name(err),
                     (unsigned)got);
            continue;
        }

        size_t n = got / sizeof(int32_t); /* interleaved L/R samples */
        for (size_t i = 0; i < n; i++) {
            raw[i] = slot_to_sample(raw[i]);
        }

        /* Push under the ring lock. Never block capture indefinitely on the
         * lock; the ring itself drops oldest on overflow (spec Section 14). */
        if (ctx->cfg.ring_lock) {
            xSemaphoreTake(ctx->cfg.ring_lock, portMAX_DELAY);
        }
        pcm_ring_write(ctx->cfg.ring, raw, n);
        if (ctx->cfg.ring_lock) {
            xSemaphoreGive(ctx->cfg.ring_lock);
        }
    }

    ESP_LOGI(TAG, "capture task exiting");
    ctx->task = NULL;
    vTaskDelete(NULL);
}

esp_err_t i2s_capture_start(const i2s_capture_config_t *cfg,
                            i2s_capture_handle_t *out)
{
    if (!cfg || !out || !cfg->ring) return ESP_ERR_INVALID_ARG;

    struct i2s_capture_ctx *ctx = calloc(1, sizeof(*ctx));
    if (!ctx) return ESP_ERR_NO_MEM;
    ctx->cfg = *cfg;
    if (ctx->cfg.sample_rate == 0) ctx->cfg.sample_rate = I2S_CAP_SAMPLE_RATE;

    /* Create the RX channel (master, since the ESP32-S3 drives BCLK/WS). */
    i2s_chan_config_t chan_cfg = I2S_CHANNEL_DEFAULT_CONFIG(I2S_NUM_0,
                                                            I2S_ROLE_MASTER);
    esp_err_t err = i2s_new_channel(&chan_cfg, NULL, &ctx->rx);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "i2s_new_channel: %s", esp_err_to_name(err));
        free(ctx);
        return err;
    }

    i2s_std_config_t std_cfg = {
        .clk_cfg  = I2S_STD_CLK_DEFAULT_CONFIG(ctx->cfg.sample_rate),
        .slot_cfg = I2S_STD_PHILIPS_SLOT_DEFAULT_CONFIG(I2S_DATA_BIT_WIDTH_32BIT,
                                                        I2S_SLOT_MODE_STEREO),
        .gpio_cfg = {
            .mclk = I2S_GPIO_UNUSED,
            .bclk = ctx->cfg.bclk_gpio,
            .ws   = ctx->cfg.ws_gpio,
            .dout = I2S_GPIO_UNUSED,
            .din  = ctx->cfg.din_gpio,
            .invert_flags = {
                .mclk_inv = false,
                .bclk_inv = false,
                .ws_inv   = false,
            },
        },
    };

    err = i2s_channel_init_std_mode(ctx->rx, &std_cfg);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "init_std_mode: %s", esp_err_to_name(err));
        i2s_del_channel(ctx->rx);
        free(ctx);
        return err;
    }

    err = i2s_channel_enable(ctx->rx);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "channel_enable: %s", esp_err_to_name(err));
        i2s_del_channel(ctx->rx);
        free(ctx);
        return err;
    }

    ctx->running = true;
    int prio = ctx->cfg.task_priority > 0 ? ctx->cfg.task_priority
                                          : (configMAX_PRIORITIES - 3);
    BaseType_t ok;
    if (ctx->cfg.task_core >= 0) {
        ok = xTaskCreatePinnedToCore(capture_task, "i2s_cap", 4096, ctx, prio,
                                     &ctx->task, ctx->cfg.task_core);
    } else {
        ok = xTaskCreate(capture_task, "i2s_cap", 4096, ctx, prio, &ctx->task);
    }
    if (ok != pdPASS) {
        ctx->running = false;
        i2s_channel_disable(ctx->rx);
        i2s_del_channel(ctx->rx);
        free(ctx);
        return ESP_ERR_NO_MEM;
    }

    *out = ctx;
    ESP_LOGI(TAG, "started: bclk=%d ws=%d din=%d @ %u Hz",
             ctx->cfg.bclk_gpio, ctx->cfg.ws_gpio, ctx->cfg.din_gpio,
             (unsigned)ctx->cfg.sample_rate);
    return ESP_OK;
}

esp_err_t i2s_capture_stop(i2s_capture_handle_t h)
{
    if (!h) return ESP_ERR_INVALID_ARG;

    h->running = false;
    /* Give the task a moment to leave its read loop. */
    for (int i = 0; i < 20 && h->task != NULL; i++) {
        vTaskDelay(pdMS_TO_TICKS(10));
    }
    if (h->rx) {
        i2s_channel_disable(h->rx);
        i2s_del_channel(h->rx);
        h->rx = NULL;
    }
    free(h);
    return ESP_OK;
}
