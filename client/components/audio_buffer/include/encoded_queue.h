/*
 * encoded_queue.h - Fixed-capacity encoded (Opus/RTP) packet queue (portable C99).
 *
 * Implements the "encoded queue" stage of the audio pipeline (spec Section 6):
 * a bounded FIFO of variable-length encoded packets sitting between the Opus
 * task and the RTP task. On overflow it applies the drop-oldest policy
 * (spec Section 14: continue from newest available audio) and counts drops.
 *
 * Storage is caller-provided as a flat byte arena plus per-slot bookkeeping;
 * no malloc in the hot path. Each slot holds up to `slot_size` bytes.
 *
 * Not thread-safe by itself. Pure portable logic (host-testable).
 */
#ifndef ENCODED_QUEUE_H
#define ENCODED_QUEUE_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    uint8_t *arena;      /* caller storage: slots * slot_size bytes */
    size_t  *lengths;    /* caller storage: `slots` length entries */
    size_t   slots;      /* number of packet slots (queue depth) */
    size_t   slot_size;  /* max bytes per packet */
    size_t   head;       /* index of oldest packet */
    size_t   count;      /* number of queued packets */

    /* Statistics. */
    size_t   high_water;   /* max `count` (queue depth) observed */
    uint64_t drop_count;   /* packets dropped due to overflow */
    uint64_t reject_count; /* packets rejected because they exceed slot_size */
    uint64_t total_pushed; /* packets successfully enqueued */
    uint64_t total_popped; /* packets dequeued */
} encoded_queue_t;

/*
 * Initialise the queue.
 *  arena   : storage of at least (slots * slot_size) bytes
 *  lengths : storage of at least `slots` size_t entries
 * slots and slot_size must be > 0.
 */
void eq_init(encoded_queue_t *q, uint8_t *arena, size_t *lengths,
             size_t slots, size_t slot_size);

/* Reset contents and statistics (keeps storage/geometry). */
void eq_reset(encoded_queue_t *q);

size_t eq_count(const encoded_queue_t *q);
int    eq_is_full(const encoded_queue_t *q);
int    eq_is_empty(const encoded_queue_t *q);

/*
 * Enqueue one packet (`data`/`len`). If the queue is full, the oldest packet is
 * dropped first (drop-oldest, spec Section 14) and drop_count is incremented.
 *
 * Returns:
 *    1 : packet enqueued (possibly after dropping the oldest)
 *    0 : rejected because len > slot_size (reject_count incremented); queue
 *        unchanged
 *   -1 : bad arguments
 */
int eq_push(encoded_queue_t *q, const uint8_t *data, size_t len);

/*
 * Dequeue the oldest packet into `out` (capacity `out_cap`).
 * On success writes the packet bytes and stores its length in *out_len.
 *
 * Returns:
 *    1 : packet dequeued
 *    0 : queue empty
 *   -1 : bad arguments, or out_cap too small for the head packet (queue
 *        unchanged)
 */
int eq_pop(encoded_queue_t *q, uint8_t *out, size_t out_cap, size_t *out_len);

#ifdef __cplusplus
}
#endif

#endif /* ENCODED_QUEUE_H */
