/*
 * control_frame.c - Length-prefixed control framing (portable C99).
 * See control_frame.h and spec Section 9.
 */
#include "control_frame.h"

#include <string.h>

static void put_be32(uint8_t *buf, uint32_t v)
{
    buf[0] = (uint8_t)((v >> 24) & 0xFFu);
    buf[1] = (uint8_t)((v >> 16) & 0xFFu);
    buf[2] = (uint8_t)((v >> 8) & 0xFFu);
    buf[3] = (uint8_t)(v & 0xFFu);
}

static uint32_t get_be32(const uint8_t *buf)
{
    return ((uint32_t)buf[0] << 24) |
           ((uint32_t)buf[1] << 16) |
           ((uint32_t)buf[2] << 8) |
           ((uint32_t)buf[3]);
}

int cp_frame_encode(const uint8_t *json, uint32_t json_len,
                    uint8_t *out, size_t out_cap)
{
    if (out == NULL || (json == NULL && json_len > 0u)) {
        return CP_ERR_ARG;
    }
    if (json_len > CP_MAX_PAYLOAD) {
        return CP_ERR_OVERSIZE;
    }
    if (out_cap < (size_t)CP_LENGTH_PREFIX_SIZE + json_len) {
        return CP_ERR_NOSPACE;
    }

    put_be32(out, json_len);
    if (json_len > 0u) {
        memcpy(out + CP_LENGTH_PREFIX_SIZE, json, json_len);
    }
    return (int)((size_t)CP_LENGTH_PREFIX_SIZE + json_len);
}

void cp_decoder_init(cp_decoder_t *d)
{
    if (d == NULL) {
        return;
    }
    d->used = 0;
    d->failed = 0;
}

int cp_decoder_push(cp_decoder_t *d, const uint8_t *data, size_t len,
                    cp_frame_cb cb, void *user)
{
    if (d == NULL || (data == NULL && len > 0u)) {
        return CP_ERR_ARG;
    }
    if (d->failed) {
        return CP_ERR_OVERSIZE;
    }

    size_t off = 0;
    while (off < len) {
        /* Fast path: nothing buffered and a full frame is contiguous in
         * `data` -> deliver directly without copying into buf. Otherwise fall
         * back to buffered reassembly. To keep the logic simple and correct we
         * always buffer; the buffer is bounded by 4 + CP_MAX_PAYLOAD. */
        size_t space = sizeof(d->buf) - d->used;
        size_t take = len - off;
        if (take > space) {
            take = space;
        }
        memcpy(d->buf + d->used, data + off, take);
        d->used += (uint32_t)take;
        off += take;

        /* Extract as many complete frames as are now available. */
        for (;;) {
            if (d->used < CP_LENGTH_PREFIX_SIZE) {
                break; /* need more bytes for the length prefix */
            }
            uint32_t plen = get_be32(d->buf);
            if (plen > CP_MAX_PAYLOAD) {
                d->failed = 1;
                return CP_ERR_OVERSIZE;
            }
            uint32_t frame_total = CP_LENGTH_PREFIX_SIZE + plen;
            if (d->used < frame_total) {
                break; /* need more bytes for the payload */
            }
            if (cb != NULL) {
                cb(d->buf + CP_LENGTH_PREFIX_SIZE, plen, user);
            }
            /* Shift any trailing bytes of the next frame down to the front. */
            uint32_t remaining = d->used - frame_total;
            if (remaining > 0u) {
                memmove(d->buf, d->buf + frame_total, remaining);
            }
            d->used = remaining;
        }

        /* If we consumed all input but couldn't advance and the buffer is
         * full without a complete frame, that can only happen for an oversize
         * payload, which is already caught above via plen > CP_MAX_PAYLOAD.
         * The prefix (4B) + max payload always fits, so no deadlock here. */
    }
    return CP_OK;
}

/* Minimal top-level  "type": "<value>"  extractor. Not a JSON parser. */
int cp_message_type(const uint8_t *json, uint32_t len, char *out, size_t out_cap)
{
    if (json == NULL || out == NULL || out_cap == 0u) {
        return CP_ERR_ARG;
    }

    const char key[] = "\"type\"";
    const size_t keylen = sizeof(key) - 1u;

    uint32_t i = 0;
    while (i + keylen <= len) {
        if (memcmp(json + i, key, keylen) == 0) {
            uint32_t j = i + (uint32_t)keylen;
            /* skip whitespace and ':' */
            while (j < len && (json[j] == ' ' || json[j] == '\t' ||
                               json[j] == '\n' || json[j] == '\r' ||
                               json[j] == ':')) {
                j++;
            }
            if (j >= len || json[j] != '"') {
                return CP_ERR_ARG; /* malformed */
            }
            j++; /* opening quote of value */
            size_t o = 0;
            while (j < len && json[j] != '"') {
                if (o + 1 < out_cap) {
                    out[o++] = (char)json[j];
                }
                j++;
            }
            if (j >= len) {
                return CP_ERR_ARG; /* unterminated */
            }
            out[o] = '\0';
            return (int)o;
        }
        i++;
    }
    return CP_ERR_ARG; /* no "type" member */
}
