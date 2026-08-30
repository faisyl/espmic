/*
 * rtp_packet.c - RTP/Opus packetizer implementation (portable C99).
 * See rtp_packet.h and spec Section 8 (RTP media contract).
 */
#include "rtp_packet.h"

/* Store a 32-bit value big-endian into buf. */
static void put_be32(uint8_t *buf, uint32_t v)
{
    buf[0] = (uint8_t)((v >> 24) & 0xFFu);
    buf[1] = (uint8_t)((v >> 16) & 0xFFu);
    buf[2] = (uint8_t)((v >> 8) & 0xFFu);
    buf[3] = (uint8_t)(v & 0xFFu);
}

/* Store a 16-bit value big-endian into buf. */
static void put_be16(uint8_t *buf, uint16_t v)
{
    buf[0] = (uint8_t)((v >> 8) & 0xFFu);
    buf[1] = (uint8_t)(v & 0xFFu);
}

/*
 * Deterministic 32-bit mix (SplitMix32-style). Used to derive spread-out
 * initial seq/timestamp/SSRC from a caller seed so tests stay reproducible.
 */
static uint32_t mix32(uint32_t x)
{
    x += 0x9E3779B9u;
    x ^= x >> 16;
    x *= 0x21F0AAADu;
    x ^= x >> 15;
    x *= 0x735A2D97u;
    x ^= x >> 15;
    return x;
}

void rtp_init(rtp_state_t *st, uint8_t payload_type, uint32_t seed)
{
    if (st == NULL) {
        return;
    }
    if (payload_type > 127u) {
        payload_type = RTP_DEFAULT_PAYLOAD_TYPE;
    }
    st->payload_type = (uint8_t)(payload_type & 0x7Fu);

    uint32_t a = mix32(seed);
    uint32_t b = mix32(a);
    uint32_t c = mix32(b);

    st->sequence  = (uint16_t)(a & 0xFFFFu);
    st->timestamp = b;
    st->ssrc      = c;
}

int rtp_serialize(rtp_state_t *st, const uint8_t *opus, size_t opus_len,
                  int marker, uint8_t *out, size_t out_cap)
{
    if (st == NULL || out == NULL) {
        return -1;
    }
    if (opus == NULL && opus_len > 0u) {
        return -1;
    }
    if (out_cap < (size_t)RTP_HEADER_SIZE + opus_len) {
        return -2; /* would overrun */
    }

    /* Octet 0: V(2) P(1) X(1) CC(4). Version 2, no padding, no extension, CC=0. */
    out[0] = (uint8_t)(RTP_VERSION << 6);

    /* Octet 1: M(1) PT(7). */
    out[1] = (uint8_t)(((marker ? 1u : 0u) << 7) | (st->payload_type & 0x7Fu));

    put_be16(&out[2], st->sequence);
    put_be32(&out[4], st->timestamp);
    put_be32(&out[8], st->ssrc);

    /* Payload: raw Opus packet, no private header. */
    for (size_t i = 0; i < opus_len; ++i) {
        out[RTP_HEADER_SIZE + i] = opus[i];
    }

    /* Advance only on success. Wrap is defined for unsigned types. */
    st->sequence  = (uint16_t)(st->sequence + 1u);
    st->timestamp = st->timestamp + RTP_TIMESTAMP_INCREMENT;

    return (int)((size_t)RTP_HEADER_SIZE + opus_len);
}
