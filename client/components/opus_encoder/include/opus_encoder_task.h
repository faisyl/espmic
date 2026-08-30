/*
 * opus_encoder_task.h - Opus encode task (spec Section 7).
 *
 * Drains stereo PCM from the pcm_ring in 20 ms / 960-sample-per-channel frames,
 * encodes with libopus (OPUS_APPLICATION_AUDIO, 48 kHz stereo, VBR) and pushes
 * each raw Opus packet (the RTP payload, spec Section 8) into the encoded_queue.
 *
 * This header is named opus_encoder_task.h to avoid colliding with libopus's
 * own <opus.h>/OpusEncoder symbols. The component is still "opus_encoder".
 *
 * The task must not wait indefinitely for the network (spec Section 4/14): it
 * only touches the ring and the encoded queue, both under caller locks.
 */
#ifndef OPUS_ENCODER_TASK_H
#define OPUS_ENCODER_TASK_H

#include <stdint.h>
#include "esp_err.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "pcm_ring.h"
#include "encoded_queue.h"

#ifdef __cplusplus
extern "C" {
#endif

/* Opus framing (spec Sections 5, 7). */
#define OPUS_FRAME_SAMPLES_PER_CH 960   /* 20 ms @ 48 kHz */
#define OPUS_FRAME_CHANNELS       2
#define OPUS_FRAME_SAMPLES        (OPUS_FRAME_SAMPLES_PER_CH * OPUS_FRAME_CHANNELS)

typedef struct {
    /* Source ring (interleaved L/R int32 samples) and its lock. */
    pcm_ring_t        *ring;
    SemaphoreHandle_t  ring_lock;

    /* Destination encoded queue (raw Opus packets) and its lock. */
    encoded_queue_t   *queue;
    SemaphoreHandle_t  queue_lock;

    uint32_t           sample_rate;   /* 48000 */
    int                channels;      /* 2 */
    int                bitrate;       /* e.g. 128000 */
    int                complexity;    /* 5..8; 0 => default (5) */
    int                vbr;           /* nonzero => VBR (spec Section 7) */
    int                fec;           /* inband FEC; 0 initially */
    int                dtx;           /* 0 initially */

    volatile uint64_t *late_counter;  /* incremented on encoder-late/underrun */
    int                task_priority; /* 0 => default */
    int                task_core;     /* -1 => no affinity */
} opus_task_config_t;

typedef struct opus_task_ctx *opus_task_handle_t;

/* Create the Opus encoder, apply spec Section 7 settings, and start the task. */
esp_err_t opus_task_start(const opus_task_config_t *cfg, opus_task_handle_t *out);

/* Stop the task and destroy the Opus encoder. */
esp_err_t opus_task_stop(opus_task_handle_t h);

#ifdef __cplusplus
}
#endif

#endif /* OPUS_ENCODER_TASK_H */
