/* Host tests for the encoded packet queue (spec Sections 6 & 14). */
#include "encoded_queue.h"
#include "test_util.h"
#include <string.h>

int main(void)
{
    uint8_t arena[4 * 16];
    size_t lengths[4];
    encoded_queue_t q;
    eq_init(&q, arena, lengths, 4, 16);

    CHECK(eq_is_empty(&q));
    CHECK(!eq_is_full(&q));

    uint8_t p1[3] = {0xA, 0xB, 0xC};
    uint8_t p2[5] = {1, 2, 3, 4, 5};
    CHECK_EQ_INT(eq_push(&q, p1, 3), 1);
    CHECK_EQ_INT(eq_push(&q, p2, 5), 1);
    CHECK_EQ_INT(eq_count(&q), 2);
    CHECK_EQ_INT(q.high_water, 2);

    uint8_t out[16];
    size_t olen = 0;
    CHECK_EQ_INT(eq_pop(&q, out, sizeof(out), &olen), 1);
    CHECK_EQ_INT(olen, 3);
    CHECK(memcmp(out, p1, 3) == 0);
    CHECK_EQ_INT(eq_pop(&q, out, sizeof(out), &olen), 1);
    CHECK_EQ_INT(olen, 5);
    CHECK(memcmp(out, p2, 5) == 0);
    CHECK_EQ_INT(eq_pop(&q, out, sizeof(out), &olen), 0); /* empty */

    /* --- fill to capacity then overflow (drop-oldest) --- */
    eq_reset(&q);
    uint8_t s[4] = {10, 20, 30, 40};
    for (int i = 0; i < 4; ++i) CHECK_EQ_INT(eq_push(&q, &s[i], 1), 1);
    CHECK(eq_is_full(&q));
    CHECK_EQ_INT(q.high_water, 4);
    /* push a 5th -> drops oldest (10) */
    uint8_t five = 50;
    CHECK_EQ_INT(eq_push(&q, &five, 1), 1);
    CHECK_EQ_INT(q.drop_count, 1);
    CHECK_EQ_INT(eq_count(&q), 4);
    /* oldest now 20, order 20,30,40,50 */
    uint8_t expect[4] = {20, 30, 40, 50};
    for (int i = 0; i < 4; ++i) {
        CHECK_EQ_INT(eq_pop(&q, out, sizeof(out), &olen), 1);
        CHECK_EQ_INT(olen, 1);
        CHECK_EQ_INT(out[0], expect[i]);
    }

    /* --- reject packet larger than slot_size --- */
    eq_reset(&q);
    uint8_t toobig[20];
    memset(toobig, 7, sizeof(toobig));
    CHECK_EQ_INT(eq_push(&q, toobig, 20), 0);
    CHECK_EQ_INT(q.reject_count, 1);
    CHECK_EQ_INT(eq_count(&q), 0);

    /* --- pop into too-small buffer leaves queue intact --- */
    eq_reset(&q);
    eq_push(&q, p2, 5);
    uint8_t tiny[2];
    CHECK_EQ_INT(eq_pop(&q, tiny, sizeof(tiny), &olen), -1);
    CHECK_EQ_INT(eq_count(&q), 1); /* still there */

    /* --- zero-length packet allowed --- */
    eq_reset(&q);
    CHECK_EQ_INT(eq_push(&q, NULL, 0), 1);
    CHECK_EQ_INT(eq_pop(&q, out, sizeof(out), &olen), 1);
    CHECK_EQ_INT(olen, 0);

    TEST_MAIN_END("encoded_queue");
}
