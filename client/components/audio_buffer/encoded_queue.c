/*
 * encoded_queue.c - Fixed-capacity encoded packet queue (portable C99).
 * See encoded_queue.h and spec Sections 6 & 14.
 */
#include "encoded_queue.h"

#include <string.h>

void eq_init(encoded_queue_t *q, uint8_t *arena, size_t *lengths,
             size_t slots, size_t slot_size)
{
    if (q == NULL) {
        return;
    }
    q->arena = arena;
    q->lengths = lengths;
    q->slots = slots;
    q->slot_size = slot_size;
    eq_reset(q);
}

void eq_reset(encoded_queue_t *q)
{
    if (q == NULL) {
        return;
    }
    q->head = 0;
    q->count = 0;
    q->high_water = 0;
    q->drop_count = 0;
    q->reject_count = 0;
    q->total_pushed = 0;
    q->total_popped = 0;
}

size_t eq_count(const encoded_queue_t *q)
{
    return (q != NULL) ? q->count : 0;
}

int eq_is_full(const encoded_queue_t *q)
{
    return (q != NULL && q->count == q->slots) ? 1 : 0;
}

int eq_is_empty(const encoded_queue_t *q)
{
    return (q == NULL || q->count == 0) ? 1 : 0;
}

int eq_push(encoded_queue_t *q, const uint8_t *data, size_t len)
{
    if (q == NULL || q->arena == NULL || q->lengths == NULL ||
        q->slots == 0 || (data == NULL && len > 0)) {
        return -1;
    }
    if (len > q->slot_size) {
        q->reject_count++;
        return 0; /* would not fit a slot; reject */
    }

    /* Drop oldest to make room. */
    if (q->count == q->slots) {
        q->head = (q->head + 1) % q->slots;
        q->count--;
        q->drop_count++;
    }

    size_t tail = (q->head + q->count) % q->slots;
    if (len > 0) {
        memcpy(q->arena + tail * q->slot_size, data, len);
    }
    q->lengths[tail] = len;
    q->count++;
    q->total_pushed++;

    if (q->count > q->high_water) {
        q->high_water = q->count;
    }
    return 1;
}

int eq_pop(encoded_queue_t *q, uint8_t *out, size_t out_cap, size_t *out_len)
{
    if (q == NULL || q->arena == NULL || q->lengths == NULL || out == NULL) {
        return -1;
    }
    if (q->count == 0) {
        return 0;
    }
    size_t len = q->lengths[q->head];
    if (len > out_cap) {
        return -1; /* caller buffer too small; leave queue intact */
    }
    if (len > 0) {
        memcpy(out, q->arena + q->head * q->slot_size, len);
    }
    if (out_len != NULL) {
        *out_len = len;
    }
    q->head = (q->head + 1) % q->slots;
    q->count--;
    q->total_popped++;
    return 1;
}
