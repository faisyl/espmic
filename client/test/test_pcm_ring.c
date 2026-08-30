/* Host tests for the PCM ring buffer (spec Sections 6 & 14). */
#include "pcm_ring.h"
#include "test_util.h"

int main(void)
{
    int32_t storage[8];
    pcm_ring_t r;
    pcm_ring_init(&r, storage, 8);

    CHECK_EQ_INT(pcm_ring_count(&r), 0);
    CHECK_EQ_INT(pcm_ring_free(&r), 8);

    /* Fill with 5 samples. */
    int32_t in[5] = {1, 2, 3, 4, 5};
    size_t w = pcm_ring_write(&r, in, 5);
    CHECK_EQ_INT(w, 5);
    CHECK_EQ_INT(pcm_ring_count(&r), 5);
    CHECK_EQ_INT(r.high_water, 5);
    CHECK_EQ_INT(r.overflow_count, 0);

    /* Drain 3 samples FIFO order. */
    int32_t out[8];
    size_t rd = pcm_ring_read(&r, out, 3);
    CHECK_EQ_INT(rd, 3);
    CHECK_EQ_INT(out[0], 1);
    CHECK_EQ_INT(out[1], 2);
    CHECK_EQ_INT(out[2], 3);
    CHECK_EQ_INT(pcm_ring_count(&r), 2);

    /* Wrap-around write: 2 present (4,5) + write 6 more -> total 8, fits. */
    int32_t more[6] = {6, 7, 8, 9, 10, 11};
    w = pcm_ring_write(&r, more, 6);
    CHECK_EQ_INT(w, 6);
    CHECK_EQ_INT(pcm_ring_count(&r), 8);
    CHECK_EQ_INT(r.high_water, 8);
    CHECK_EQ_INT(r.overflow_count, 0);

    /* Verify contents in order 4,5,6,7,8,9,10,11. */
    rd = pcm_ring_read(&r, out, 8);
    CHECK_EQ_INT(rd, 8);
    int32_t expect[8] = {4, 5, 6, 7, 8, 9, 10, 11};
    for (int i = 0; i < 8; ++i) CHECK_EQ_INT(out[i], expect[i]);

    /* --- overflow: drop-oldest policy --- */
    pcm_ring_reset(&r);
    int32_t fill[8] = {0, 1, 2, 3, 4, 5, 6, 7};
    pcm_ring_write(&r, fill, 8); /* full */
    CHECK_EQ_INT(pcm_ring_free(&r), 0);
    /* write 3 more -> drops oldest 3 (0,1,2), overflow += 3 */
    int32_t x3[3] = {8, 9, 10};
    w = pcm_ring_write(&r, x3, 3);
    CHECK_EQ_INT(w, 3);
    CHECK_EQ_INT(r.overflow_count, 3);
    CHECK_EQ_INT(pcm_ring_count(&r), 8);
    /* remaining should be 3,4,5,6,7,8,9,10 (newest retained) */
    rd = pcm_ring_read(&r, out, 8);
    int32_t expect2[8] = {3, 4, 5, 6, 7, 8, 9, 10};
    for (int i = 0; i < 8; ++i) CHECK_EQ_INT(out[i], expect2[i]);

    /* --- write bigger than capacity: keep last `capacity`, rest overflow --- */
    pcm_ring_reset(&r);
    int32_t big[12] = {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11};
    w = pcm_ring_write(&r, big, 12);
    CHECK_EQ_INT(w, 8);                 /* only capacity retained */
    CHECK_EQ_INT(r.overflow_count, 4);  /* 12 - 8 dropped */
    rd = pcm_ring_read(&r, out, 8);
    int32_t expect3[8] = {4, 5, 6, 7, 8, 9, 10, 11};
    for (int i = 0; i < 8; ++i) CHECK_EQ_INT(out[i], expect3[i]);

    /* --- low-water mark tracks minimum non-empty fill --- */
    pcm_ring_reset(&r);
    pcm_ring_write(&r, fill, 6);      /* count 6 */
    pcm_ring_read(&r, out, 4);        /* count 2 -> low_water 2 */
    pcm_ring_write(&r, x3, 3);        /* count 5 */
    CHECK_EQ_INT(r.low_water, 2);
    CHECK_EQ_INT(r.high_water, 6);

    /* Read from empty returns 0. */
    pcm_ring_reset(&r);
    CHECK_EQ_INT(pcm_ring_read(&r, out, 4), 0);

    TEST_MAIN_END("pcm_ring");
}
