/* Host tests for control-protocol framing (spec Section 9). */
#include "control_frame.h"
#include "test_util.h"
#include <string.h>

/* Collector for decoded frames. */
#define MAX_FRAMES 16
static struct {
    int count;
    size_t lens[MAX_FRAMES];
    unsigned char data[MAX_FRAMES][64];
} g_col;

static void reset_col(void) { memset(&g_col, 0, sizeof(g_col)); }

static void collect(const uint8_t *p, uint32_t len, void *user)
{
    (void)user;
    if (g_col.count < MAX_FRAMES) {
        g_col.lens[g_col.count] = len;
        if (len <= sizeof(g_col.data[0])) {
            memcpy(g_col.data[g_col.count], p, len);
        }
        g_col.count++;
    }
}

int main(void)
{
    /* --- encode/decode round trip --- */
    const char *msg = "{\"type\":\"ping\"}";
    uint32_t mlen = (uint32_t)strlen(msg);
    uint8_t frame[128];
    int n = cp_frame_encode((const uint8_t *)msg, mlen, frame, sizeof(frame));
    CHECK_EQ_INT(n, (int)(CP_LENGTH_PREFIX_SIZE + mlen));
    /* BE length prefix. */
    uint32_t declared = ((uint32_t)frame[0] << 24) | ((uint32_t)frame[1] << 16) |
                        ((uint32_t)frame[2] << 8) | frame[3];
    CHECK_EQ_INT(declared, mlen);

    cp_decoder_t dec;
    cp_decoder_init(&dec);
    reset_col();
    int r = cp_decoder_push(&dec, frame, (size_t)n, collect, NULL);
    CHECK_EQ_INT(r, CP_OK);
    CHECK_EQ_INT(g_col.count, 1);
    CHECK_EQ_INT(g_col.lens[0], mlen);
    CHECK(memcmp(g_col.data[0], msg, mlen) == 0);

    /* --- partial-frame reassembly: feed one byte at a time --- */
    cp_decoder_init(&dec);
    reset_col();
    for (int i = 0; i < n; ++i) {
        r = cp_decoder_push(&dec, &frame[i], 1, collect, NULL);
        CHECK_EQ_INT(r, CP_OK);
    }
    CHECK_EQ_INT(g_col.count, 1);
    CHECK_EQ_INT(g_col.lens[0], mlen);

    /* --- two frames in one push, plus a trailing partial --- */
    uint8_t two[300];
    int a = cp_frame_encode((const uint8_t *)"{\"a\":1}", 7, two, sizeof(two));
    int b = cp_frame_encode((const uint8_t *)"{\"bb\":2}", 8, two + a, sizeof(two) - a);
    int total = a + b;
    cp_decoder_init(&dec);
    reset_col();
    /* push everything but the last 3 bytes first */
    r = cp_decoder_push(&dec, two, (size_t)(total - 3), collect, NULL);
    CHECK_EQ_INT(r, CP_OK);
    CHECK_EQ_INT(g_col.count, 1); /* only first frame complete */
    /* push remainder */
    r = cp_decoder_push(&dec, two + (total - 3), 3, collect, NULL);
    CHECK_EQ_INT(r, CP_OK);
    CHECK_EQ_INT(g_col.count, 2);
    CHECK_EQ_INT(g_col.lens[0], 7);
    CHECK_EQ_INT(g_col.lens[1], 8);

    /* --- zero-length payload frame --- */
    uint8_t empty[8];
    int en = cp_frame_encode(NULL, 0, empty, sizeof(empty));
    CHECK_EQ_INT(en, (int)CP_LENGTH_PREFIX_SIZE);
    cp_decoder_init(&dec);
    reset_col();
    r = cp_decoder_push(&dec, empty, (size_t)en, collect, NULL);
    CHECK_EQ_INT(r, CP_OK);
    CHECK_EQ_INT(g_col.count, 1);
    CHECK_EQ_INT(g_col.lens[0], 0);

    /* --- oversize rejection on encode --- */
    static uint8_t big[CP_MAX_PAYLOAD + 16];
    uint8_t obuf[16];
    CHECK_EQ_INT(cp_frame_encode(big, CP_MAX_PAYLOAD + 1, obuf, sizeof(obuf)),
                 CP_ERR_OVERSIZE);
    /* encode of exactly max is allowed (nospace here since obuf tiny). */
    CHECK_EQ_INT(cp_frame_encode(big, CP_MAX_PAYLOAD, obuf, sizeof(obuf)),
                 CP_ERR_NOSPACE);

    /* --- oversize rejection on decode: a length prefix > max latches fail --- */
    uint8_t badhdr[4];
    uint32_t huge = CP_MAX_PAYLOAD + 1;
    badhdr[0] = (uint8_t)(huge >> 24); badhdr[1] = (uint8_t)(huge >> 16);
    badhdr[2] = (uint8_t)(huge >> 8);  badhdr[3] = (uint8_t)huge;
    cp_decoder_init(&dec);
    reset_col();
    r = cp_decoder_push(&dec, badhdr, 4, collect, NULL);
    CHECK_EQ_INT(r, CP_ERR_OVERSIZE);
    CHECK_EQ_INT(g_col.count, 0);
    /* further pushes stay rejected until re-init */
    r = cp_decoder_push(&dec, badhdr, 4, collect, NULL);
    CHECK_EQ_INT(r, CP_ERR_OVERSIZE);

    /* --- message-type extraction --- */
    char type[32];
    int tl = cp_message_type((const uint8_t *)msg, mlen, type, sizeof(type));
    CHECK_EQ_INT(tl, 4);
    CHECK(strcmp(type, "ping") == 0);

    const char *sc = "{ \"protocol\":1, \"type\" : \"start_stream\" }";
    tl = cp_message_type((const uint8_t *)sc, (uint32_t)strlen(sc), type, sizeof(type));
    CHECK(strcmp(type, "start_stream") == 0);

    const char *notype = "{\"foo\":\"bar\"}";
    tl = cp_message_type((const uint8_t *)notype, (uint32_t)strlen(notype), type, sizeof(type));
    CHECK(tl < 0);

    TEST_MAIN_END("control_frame");
}
