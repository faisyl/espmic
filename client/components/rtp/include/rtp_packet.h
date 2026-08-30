/*
 * rtp_packet.h - RTP/Opus packetizer (portable C99).
 *
 * Implements the RTP media contract from the ESP32 Audio Device Specification
 * (spec Section 8) and RFC 3550 / RFC 7587.
 *
 * The packetizer builds a standards-compliant 12-byte RTP header followed by
 * the raw Opus packet payload (no private application header). All multi-byte
 * header fields are serialised big-endian (network byte order).
 *
 * This module has no platform dependencies (no ESP-IDF, no FreeRTOS) and is
 * therefore host-testable.
 */
#ifndef RTP_PACKET_H
#define RTP_PACKET_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Fixed size of the RTP header this module emits: V/P/X/CC/M/PT + seq + ts +
 * SSRC = 12 bytes. No CSRC list, no header extension. */
#define RTP_HEADER_SIZE 12u

/* RTP version carried in the 2 MSBs of the first octet. */
#define RTP_VERSION 2u

/* Default dynamic payload type for Opus (spec Section 8). */
#define RTP_DEFAULT_PAYLOAD_TYPE 111u

/* Timestamp advance per 20 ms / 960-sample Opus frame at 48 kHz (spec Section 8). */
#define RTP_TIMESTAMP_INCREMENT 960u

/*
 * RTP packetizer state. One instance per RTP stream.
 *
 * SSRC is stable for the lifetime of the stream; sequence increments by 1 and
 * timestamp by RTP_TIMESTAMP_INCREMENT per serialised packet.
 */
typedef struct {
    uint8_t  payload_type; /* 7-bit RTP payload type */
    uint16_t sequence;     /* next sequence number to emit */
    uint32_t timestamp;    /* next RTP timestamp to emit */
    uint32_t ssrc;         /* synchronization source, constant for the stream */
} rtp_state_t;

/*
 * Initialise packetizer state.
 *
 * Initial sequence, timestamp and SSRC are derived deterministically from
 * `seed` (so tests are reproducible) while still spreading the values across
 * their ranges to approximate the "random initial value" requirement.
 *
 * payload_type: 0..127; if out of range it is coerced to RTP_DEFAULT_PAYLOAD_TYPE.
 */
void rtp_init(rtp_state_t *st, uint8_t payload_type, uint32_t seed);

/*
 * Serialise one RTP packet (12-byte header + Opus payload) into `out`.
 *
 * `opus` / `opus_len` : raw Opus packet bytes (opus_len may be 0).
 * `marker`            : marker bit (0 normally, may be 1 on the first packet).
 * `out` / `out_cap`   : destination buffer and its capacity.
 *
 * On success returns the total number of bytes written
 * (RTP_HEADER_SIZE + opus_len) and advances sequence by 1 and timestamp by
 * RTP_TIMESTAMP_INCREMENT.
 *
 * Returns a negative value on error and does NOT advance state:
 *   -1 : bad arguments (NULL st/out, or NULL opus with opus_len > 0)
 *   -2 : output buffer too small (would overrun)
 */
int rtp_serialize(rtp_state_t *st, const uint8_t *opus, size_t opus_len,
                  int marker, uint8_t *out, size_t out_cap);

#ifdef __cplusplus
}
#endif

#endif /* RTP_PACKET_H */
