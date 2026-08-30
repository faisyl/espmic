/*
 * pcm_ring.h - Fixed-capacity PCM sample ring buffer (portable C99).
 *
 * Implements the PCM ring stage of the audio pipeline (spec Section 6) with the
 * overflow policy required by spec Section 14: on overflow, drop the OLDEST
 * samples and continue from the newest available audio, incrementing an
 * overflow counter.
 *
 * Samples are stored as int32_t containers (24-bit mic samples preserved in
 * 32-bit containers, spec Section 5). The ring is agnostic to channel
 * interleaving: the caller decides whether one "sample" is a mono sample or one
 * interleaved L/R value. Storage is caller-provided; no malloc.
 *
 * Not thread-safe by itself; the real firmware wraps access with the
 * appropriate task synchronisation. Pure portable logic here (host-testable).
 */
#ifndef PCM_RING_H
#define PCM_RING_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    int32_t *buf;      /* caller-provided storage, `capacity` slots */
    size_t   capacity; /* number of int32_t slots */
    size_t   head;     /* read index (oldest sample) */
    size_t   count;    /* number of valid samples */

    /* Statistics. */
    size_t   high_water;     /* max `count` observed */
    size_t   low_water;      /* min `count` observed while non-empty */
    uint64_t overflow_count; /* total samples dropped due to overflow */
    uint64_t total_written;  /* total samples successfully stored */
    uint64_t total_read;     /* total samples drained */
} pcm_ring_t;

/*
 * Initialise a ring over caller-provided storage.
 * `storage` must point to `capacity` int32_t slots. capacity must be > 0.
 */
void pcm_ring_init(pcm_ring_t *r, int32_t *storage, size_t capacity);

/* Reset contents and statistics (keeps the same storage/capacity). */
void pcm_ring_reset(pcm_ring_t *r);

/* Current number of buffered samples / free slots. */
size_t pcm_ring_count(const pcm_ring_t *r);
size_t pcm_ring_free(const pcm_ring_t *r);

/*
 * Write `n` samples. Never fails on a full buffer: if there is not enough room,
 * the oldest samples are dropped (drop-oldest policy, spec Section 14) so the
 * newest `n` samples are retained, and overflow_count is increased by the
 * number of dropped samples. If `n` itself exceeds capacity, only the last
 * `capacity` samples of the input are kept.
 *
 * Returns the number of samples now resident from this call (min(n, capacity)).
 */
size_t pcm_ring_write(pcm_ring_t *r, const int32_t *samples, size_t n);

/*
 * Read up to `n` samples into `out`. Returns the number actually copied
 * (min(n, count)).
 */
size_t pcm_ring_read(pcm_ring_t *r, int32_t *out, size_t n);

#ifdef __cplusplus
}
#endif

#endif /* PCM_RING_H */
