/*
 * audio_manager.h - Stream lifecycle + pipeline orchestration (spec Sections
 * 4, 6, 11, 12).
 *
 * The audio_manager is the glue between control_task commands and the audio
 * pipeline. It owns the pcm_ring and encoded_queue storage and their locks, and
 * starts/stops the i2s_capture, opus and rtp_sender tasks as a unit. It applies
 * set_config, validates start_stream parameters, and exposes a stats snapshot
 * for the health_monitor / status message.
 *
 * There is exactly one pipeline, so the manager keeps process-wide singleton
 * state; the API is intentionally handle-free for P3 to call directly.
 */
#ifndef AUDIO_MANAGER_H
#define AUDIO_MANAGER_H

#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

/*
 * Parsed + validated parameters of a start_stream command (spec Section 11).
 * control_task fills this from JSON and hands it to audio_manager_start_stream.
 */
typedef struct {
    char     stream_id[64];
    char     dest_ip[46];
    uint16_t dest_port;
    uint8_t  payload_type;   /* rtp.payload_type; default 111 */

    /* codec block */
    uint32_t sample_rate;    /* 48000 */
    uint8_t  channels;       /* 2 */
    uint16_t frame_ms;       /* 20 */
    uint32_t bitrate;        /* e.g. 128000 */
    bool     vbr;
    bool     fec;
    bool     dtx;
} audio_stream_params_t;

/* One-time manager configuration (board + codec defaults). */
typedef struct {
    int      i2s_bclk_gpio;
    int      i2s_ws_gpio;
    int      i2s_din_gpio;
    uint32_t default_bitrate; /* used when a stream omits bitrate */
    int      complexity;      /* Opus complexity 5..8 */
} audio_manager_config_t;

/* Aggregated statistics snapshot (spec Section 6/10 status). */
typedef struct {
    bool     streaming;
    /* PCM ring */
    uint64_t pcm_overflow;
    uint64_t pcm_written;
    uint64_t pcm_read;
    size_t   pcm_high_water;
    size_t   pcm_low_water;   /* (size_t)-1 sentinel until first non-empty read */
    size_t   pcm_count;
    /* encoded queue */
    uint64_t enc_drops;
    uint64_t enc_rejects;
    uint64_t enc_pushed;
    uint64_t enc_popped;
    size_t   enc_high_water;
    size_t   enc_count;
    /* encoder-late / underruns */
    uint64_t encoder_late;
    uint64_t capture_late;
    /* RTP */
    uint64_t rtp_packets_sent;
    uint64_t rtp_bytes_sent;
    uint64_t rtp_send_errors;
    uint32_t rtp_ssrc;
} audio_stats_t;

/* Initialise manager singleton with board/codec defaults. Call once at boot. */
esp_err_t audio_manager_init(const audio_manager_config_t *cfg);

/*
 * Validate `params` and start the full capture->encode->send pipeline. Returns
 * ESP_ERR_INVALID_ARG (and stays idle, spec Section 11) if validation fails,
 * ESP_ERR_INVALID_STATE if already streaming.
 */
esp_err_t audio_manager_start_stream(const audio_stream_params_t *params);

/* Stop the pipeline and release all stream resources (spec acceptance #10). */
esp_err_t audio_manager_stop_stream(void);

/* Apply a set_config command. Persistent fields (e.g. default bitrate) are
 * updated immediately; codec fields take effect on the next start_stream. */
esp_err_t audio_manager_apply_config(uint32_t default_bitrate);

bool audio_manager_is_streaming(void);

/* Copy the current aggregated stats. Safe any time. */
void audio_manager_get_stats(audio_stats_t *out);

/* Active stream id (empty string when idle). */
const char *audio_manager_stream_id(void);

#ifdef __cplusplus
}
#endif

#endif /* AUDIO_MANAGER_H */
