/*
 * i2s_capture.h - I2S/Philips stereo capture task (spec Sections 5, 6).
 *
 * Drives the ESP32-S3 I2S peripheral in standard Philips mode for two ICS43434
 * microphones sharing BCLK / WS / SD, one wired LEFT and one RIGHT. Captures
 * 24-bit samples in 32-bit DMA slots at 48 kHz stereo and pushes interleaved
 * L/R int32 samples into a caller-owned pcm_ring (drop-oldest overflow policy,
 * spec Section 14).
 *
 * The capture task is real-time and MUST NOT perform network I/O (spec Section
 * 4). It only reads DMA and writes the ring under the caller-supplied lock.
 */
#ifndef I2S_CAPTURE_H
#define I2S_CAPTURE_H

#include <stdint.h>
#include "esp_err.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "pcm_ring.h"

#ifdef __cplusplus
extern "C" {
#endif

/* Fixed capture geometry (spec Section 5). */
#define I2S_CAP_SAMPLE_RATE   48000
#define I2S_CAP_CHANNELS      2
#define I2S_CAP_BITS_PER_SLOT 32   /* 24-bit sample MSB-justified in 32-bit slot */

/*
 * Capture configuration.
 *
 * ring / ring_lock : destination ring and the mutex that serialises access to
 *                    it (pcm_ring is not thread-safe on its own). One "sample"
 *                    written to the ring is one interleaved L or R int32 value,
 *                    i.e. a stereo frame is two ring samples.
 * late_counter     : optional; incremented when a DMA read errors/underruns.
 */
typedef struct {
    int                bclk_gpio;
    int                ws_gpio;
    int                din_gpio;
    uint32_t           sample_rate;   /* normally I2S_CAP_SAMPLE_RATE */
    pcm_ring_t        *ring;
    SemaphoreHandle_t  ring_lock;
    volatile uint64_t *late_counter;  /* may be NULL */
    int                task_priority;  /* 0 => default (high) */
    int                task_core;      /* -1 => no affinity */
} i2s_capture_config_t;

typedef struct i2s_capture_ctx *i2s_capture_handle_t;

/*
 * Initialise the I2S RX channel and start the capture task. On success returns
 * ESP_OK and writes an opaque handle to *out. The config is copied internally.
 */
esp_err_t i2s_capture_start(const i2s_capture_config_t *cfg,
                            i2s_capture_handle_t *out);

/*
 * Stop the capture task, disable and delete the I2S channel, free the handle.
 * Safe to call once per successful start.
 */
esp_err_t i2s_capture_stop(i2s_capture_handle_t h);

#ifdef __cplusplus
}
#endif

#endif /* I2S_CAPTURE_H */
