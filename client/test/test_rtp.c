/* Host tests for the RTP packetizer (spec Section 8). */
#include "rtp_packet.h"
#include "test_util.h"
#include <string.h>

int main(void)
{
    rtp_state_t st;
    rtp_init(&st, RTP_DEFAULT_PAYLOAD_TYPE, 0x12345678u);

    /* SSRC / initial values are deterministic for a given seed. */
    rtp_state_t st2;
    rtp_init(&st2, RTP_DEFAULT_PAYLOAD_TYPE, 0x12345678u);
    CHECK_EQ_INT(st.ssrc, st2.ssrc);
    CHECK_EQ_INT(st.sequence, st2.sequence);
    CHECK_EQ_INT(st.timestamp, st2.timestamp);

    uint32_t ssrc0 = st.ssrc;
    uint16_t seq0 = st.sequence;
    uint32_t ts0 = st.timestamp;

    uint8_t opus[4] = {0xDE, 0xAD, 0xBE, 0xEF};
    uint8_t out[64];

    /* First packet with marker=1. */
    int n = rtp_serialize(&st, opus, sizeof(opus), 1, out, sizeof(out));
    CHECK_EQ_INT(n, RTP_HEADER_SIZE + 4);

    /* Byte 0: version 2 (10______), no P/X/CC -> 0x80. */
    CHECK_EQ_INT(out[0], 0x80);
    /* Byte 1: marker + PT 111 -> 0x80 | 111 = 0xEF. */
    CHECK_EQ_INT(out[1], 0x80 | 111);

    /* Sequence big-endian. */
    CHECK_EQ_INT(((out[2] << 8) | out[3]), seq0);
    /* Timestamp big-endian. */
    uint32_t ts_be = ((uint32_t)out[4] << 24) | ((uint32_t)out[5] << 16) |
                     ((uint32_t)out[6] << 8) | out[7];
    CHECK_EQ_INT(ts_be, ts0);
    /* SSRC big-endian. */
    uint32_t ssrc_be = ((uint32_t)out[8] << 24) | ((uint32_t)out[9] << 16) |
                       ((uint32_t)out[10] << 8) | out[11];
    CHECK_EQ_INT(ssrc_be, ssrc0);

    /* Payload copied verbatim after 12-byte header. */
    CHECK(memcmp(out + RTP_HEADER_SIZE, opus, sizeof(opus)) == 0);

    /* State advanced: seq+1, ts+960, ssrc stable. */
    CHECK_EQ_INT(st.sequence, (uint16_t)(seq0 + 1));
    CHECK_EQ_INT(st.timestamp, ts0 + RTP_TIMESTAMP_INCREMENT);
    CHECK_EQ_INT(st.ssrc, ssrc0);

    /* Second packet, marker=0 -> byte1 == 111, seq/ts advanced again. */
    n = rtp_serialize(&st, opus, sizeof(opus), 0, out, sizeof(out));
    CHECK_EQ_INT(n, RTP_HEADER_SIZE + 4);
    CHECK_EQ_INT(out[1], 111);
    CHECK_EQ_INT(((out[2] << 8) | out[3]), (uint16_t)(seq0 + 1));
    ts_be = ((uint32_t)out[4] << 24) | ((uint32_t)out[5] << 16) |
            ((uint32_t)out[6] << 8) | out[7];
    CHECK_EQ_INT(ts_be, ts0 + RTP_TIMESTAMP_INCREMENT);
    CHECK_EQ_INT(st.ssrc, ssrc0); /* SSRC still stable */

    /* Buffer-overrun guard: too-small buffer returns -2 and does not advance. */
    uint16_t seq_before = st.sequence;
    uint8_t tiny[8];
    int r = rtp_serialize(&st, opus, sizeof(opus), 0, tiny, sizeof(tiny));
    CHECK_EQ_INT(r, -2);
    CHECK_EQ_INT(st.sequence, seq_before);

    /* Exact-fit buffer for header only + zero payload. */
    uint8_t hdronly[RTP_HEADER_SIZE];
    r = rtp_serialize(&st, NULL, 0, 0, hdronly, sizeof(hdronly));
    CHECK_EQ_INT(r, RTP_HEADER_SIZE);

    /* Bad args. */
    CHECK_EQ_INT(rtp_serialize(NULL, opus, 4, 0, out, sizeof(out)), -1);
    CHECK_EQ_INT(rtp_serialize(&st, NULL, 4, 0, out, sizeof(out)), -1);

    /* Sequence wrap at 0xFFFF. */
    rtp_state_t w;
    rtp_init(&w, 111, 1);
    w.sequence = 0xFFFF;
    (void)rtp_serialize(&w, opus, 4, 0, out, sizeof(out));
    CHECK_EQ_INT(w.sequence, 0);

    TEST_MAIN_END("rtp");
}
