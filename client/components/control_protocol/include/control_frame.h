/*
 * control_frame.h - Length-prefixed control-protocol framing (portable C99).
 *
 * Implements the wire framing from the ESP32 Audio Device Specification
 * (spec Section 9):
 *
 *     uint32_be payload_length
 *     uint8[payload_length] UTF-8 JSON
 *
 * Maximum accepted JSON payload is 16 KiB; larger frames are rejected.
 *
 * This module implements only the framing layer plus a light message-type
 * dispatch helper. It intentionally does NOT contain a full JSON parser: it
 * validates and reassembles frames and lets a higher layer interpret the JSON
 * body. A minimal `cp_message_type()` helper extracts the "type" string so the
 * control task can dispatch the messages listed in spec Section 10.
 *
 * No platform dependencies (no ESP-IDF / FreeRTOS): host-testable.
 */
#ifndef CONTROL_FRAME_H
#define CONTROL_FRAME_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Size of the big-endian length prefix. */
#define CP_LENGTH_PREFIX_SIZE 4u

/* Maximum accepted JSON payload length (spec Section 9): 16 KiB. */
#define CP_MAX_PAYLOAD 16384u

/* Result / error codes. */
typedef enum {
    CP_OK           = 0,
    CP_ERR_ARG      = -1, /* NULL / bad argument */
    CP_ERR_OVERSIZE = -2, /* payload length exceeds CP_MAX_PAYLOAD */
    CP_ERR_NOSPACE  = -3  /* caller output buffer too small */
} cp_result_t;

/*
 * Encode one frame: prepend the big-endian length to `json`.
 *
 * Returns the total bytes written (CP_LENGTH_PREFIX_SIZE + json_len) on
 * success, or a negative cp_result_t on error (CP_ERR_ARG, CP_ERR_OVERSIZE,
 * CP_ERR_NOSPACE). A zero-length payload is permitted.
 */
int cp_frame_encode(const uint8_t *json, uint32_t json_len,
                    uint8_t *out, size_t out_cap);

/*
 * Callback invoked once per complete frame decoded. `payload`/`len` reference
 * the JSON body (length prefix stripped) inside the decoder's buffer and are
 * only valid for the duration of the callback.
 */
typedef void (*cp_frame_cb)(const uint8_t *payload, uint32_t len, void *user);

/*
 * Streaming frame decoder. Accepts arbitrarily chunked byte input (partial
 * length prefixes and partial payloads are handled) and yields complete frames
 * via the callback. Fixed-capacity: no dynamic allocation.
 */
typedef struct {
    uint8_t  buf[CP_LENGTH_PREFIX_SIZE + CP_MAX_PAYLOAD];
    uint32_t used;   /* bytes currently buffered */
    int      failed; /* set once an oversize frame is seen; decoder is then wedged */
} cp_decoder_t;

/* Reset a decoder to empty. */
void cp_decoder_init(cp_decoder_t *d);

/*
 * Feed `len` bytes into the decoder. Emits every complete frame found via `cb`.
 *
 * Returns CP_OK on success. Returns CP_ERR_OVERSIZE if an announced payload
 * length exceeds CP_MAX_PAYLOAD; the decoder latches into a failed state and
 * rejects further input until cp_decoder_init() is called again (the caller
 * should drop the control connection). Returns CP_ERR_ARG for bad arguments.
 */
int cp_decoder_push(cp_decoder_t *d, const uint8_t *data, size_t len,
                    cp_frame_cb cb, void *user);

/*
 * Minimal message-type extraction helper.
 *
 * Scans a JSON object body for a top-level  "type": "<value>"  member and
 * copies the value into `out` (NUL-terminated, truncated to out_cap). This is
 * a deliberately small scanner, not a JSON parser: it is sufficient to
 * dispatch the message types in spec Section 10.
 *
 * Returns the length of the extracted type string (excluding NUL) on success,
 * or a negative value if no "type" member is found or on bad arguments.
 */
int cp_message_type(const uint8_t *json, uint32_t len, char *out, size_t out_cap);

#ifdef __cplusplus
}
#endif

#endif /* CONTROL_FRAME_H */
