/*
 * pcm_ring.c - Fixed-capacity PCM sample ring buffer (portable C99).
 * See pcm_ring.h and spec Sections 6 & 14.
 */
#include "pcm_ring.h"

static void update_watermarks(pcm_ring_t *r)
{
    if (r->count > r->high_water) {
        r->high_water = r->count;
    }
    if (r->count > 0 && r->count < r->low_water) {
        r->low_water = r->count;
    }
}

void pcm_ring_init(pcm_ring_t *r, int32_t *storage, size_t capacity)
{
    if (r == NULL) {
        return;
    }
    r->buf = storage;
    r->capacity = capacity;
    pcm_ring_reset(r);
}

void pcm_ring_reset(pcm_ring_t *r)
{
    if (r == NULL) {
        return;
    }
    r->head = 0;
    r->count = 0;
    r->high_water = 0;
    r->low_water = (size_t)-1; /* sentinel; first non-empty state sets it */
    r->overflow_count = 0;
    r->total_written = 0;
    r->total_read = 0;
}

size_t pcm_ring_count(const pcm_ring_t *r)
{
    return (r != NULL) ? r->count : 0;
}

size_t pcm_ring_free(const pcm_ring_t *r)
{
    if (r == NULL) {
        return 0;
    }
    return r->capacity - r->count;
}

size_t pcm_ring_write(pcm_ring_t *r, const int32_t *samples, size_t n)
{
    if (r == NULL || r->buf == NULL || r->capacity == 0 ||
        (samples == NULL && n > 0)) {
        return 0;
    }

    /* If the incoming block alone exceeds capacity, only the last `capacity`
     * samples can be retained; the earlier ones are overflow. */
    if (n > r->capacity) {
        r->overflow_count += (uint64_t)(n - r->capacity);
        samples += (n - r->capacity);
        n = r->capacity;
    }

    /* Make room by dropping oldest samples if needed. */
    size_t freeslots = r->capacity - r->count;
    if (n > freeslots) {
        size_t drop = n - freeslots;
        r->head = (r->head + drop) % r->capacity;
        r->count -= drop;
        r->overflow_count += (uint64_t)drop;
    }

    /* Append the new samples at the tail. */
    size_t tail = (r->head + r->count) % r->capacity;
    for (size_t i = 0; i < n; ++i) {
        r->buf[tail] = samples[i];
        tail = (tail + 1) % r->capacity;
    }
    r->count += n;
    r->total_written += (uint64_t)n;

    update_watermarks(r);
    return n;
}

size_t pcm_ring_read(pcm_ring_t *r, int32_t *out, size_t n)
{
    if (r == NULL || r->buf == NULL || out == NULL) {
        return 0;
    }
    if (n > r->count) {
        n = r->count;
    }
    for (size_t i = 0; i < n; ++i) {
        out[i] = r->buf[r->head];
        r->head = (r->head + 1) % r->capacity;
    }
    r->count -= n;
    r->total_read += (uint64_t)n;

    update_watermarks(r);
    return n;
}
