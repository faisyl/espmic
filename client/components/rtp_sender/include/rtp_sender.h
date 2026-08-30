/*
 * rtp_sender.h - RTP/UDP send task (spec Sections 6, 8, 14).
 *
 * Drains raw Opus packets from the encoded_queue, wraps each in a standards-
 * compliant RTP header via the portable rtp_packet serializer, and sends it as
 * one UDP datagram to the stream destination. Tracks send statistics.
 *
 * The task never reads I2S directly (spec Section 4) and never blocks the
 * pipeline indefinitely on the socket (spec Section 14): the socket uses a send
 * timeout and failures are counted, not fatal.
 */
#ifndef RTP_SENDER_H
#define RTP_SENDER_H

#include <stdint.h>
#include "esp_err.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "encoded_queue.h"

#ifdef __cplusplus
extern "C" {
#endif

/* Snapshot of RTP send statistics for the `status` control message. */
typedef struct {
    uint64_t packets_sent;
    uint64_t bytes_sent;
    uint64_t send_errors;
    uint16_t last_sequence;
    uint32_t last_timestamp;
    uint32_t ssrc;
} rtp_sender_stats_t;

typedef struct {
    encoded_queue_t   *queue;       /* source of raw Opus packets */
    SemaphoreHandle_t  queue_lock;

    char               dest_ip[46]; /* IPv4/IPv6 literal from start_stream */
    uint16_t           dest_port;
    uint8_t            payload_type; /* default 111 (spec Section 8) */
    uint32_t           ssrc_seed;    /* seed for rtp_init (random per stream) */

    int                task_priority; /* 0 => default */
    int                task_core;     /* -1 => no affinity */
} rtp_sender_config_t;

typedef struct rtp_sender_ctx *rtp_sender_handle_t;

/* Create the UDP socket, initialise the RTP state, and start the send task. */
esp_err_t rtp_sender_start(const rtp_sender_config_t *cfg,
                           rtp_sender_handle_t *out);

/* Stop the task and close the socket. */
esp_err_t rtp_sender_stop(rtp_sender_handle_t h);

/* Copy the current stats out (thread-safe: stats are plain counters). */
void rtp_sender_get_stats(rtp_sender_handle_t h, rtp_sender_stats_t *out);

#ifdef __cplusplus
}
#endif

#endif /* RTP_SENDER_H */
