/*
 * control_task.h - Persistent control connection (spec Sections 9, 10, 12, 14).
 *
 * Maintains a persistent TLS/TCP connection to the server, frames messages with
 * the portable control_frame layer (uint32_be length + UTF-8 JSON, 16 KiB cap),
 * and handles every message type in spec Section 10: hello / hello_ack /
 * ping / pong / start_stream / stop_stream / get_status / status / set_config /
 * error. It sends hello on connect, answers heartbeats, and reconnects with
 * backoff on loss (spec Section 14), driving the device state machine through a
 * caller-supplied event callback.
 *
 * Command bodies are parsed with cJSON; control_frame's cp_message_type() is
 * kept only as the cheap top-level "type" peek for dispatch.
 */
#ifndef CONTROL_TASK_H
#define CONTROL_TASK_H

#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"
#include "device_state.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    char     host[128];
    uint16_t port;
    bool     tls_enabled;         /* spec Section 15/16 control_tls_enabled */
    char     device_id[64];
    char     firmware[32];

    /* Optional PEM CA bundle for server verification. If NULL and tls_enabled,
     * the connection is made without CA verification (acceptable only on a
     * controlled LAN, spec Section 15) - noted as a P3 hardening TODO. */
    const char *server_ca_pem;
    size_t      server_ca_pem_len;

    /* Feed the device state machine (spec Section 12). Called from the control
     * task context; keep it non-blocking. */
    void (*on_state_event)(sm_event_t ev, void *user);
    void *user;

    int task_priority; /* 0 => default */
    int task_core;     /* -1 => no affinity */
} control_task_config_t;

/* Start the control task. Config is copied internally. */
esp_err_t control_task_start(const control_task_config_t *cfg);

/* Request the control task to stop and disconnect. */
esp_err_t control_task_stop(void);

/* True once a hello_ack has established the session on the current connection. */
bool control_task_is_connected(void);

#ifdef __cplusplus
}
#endif

#endif /* CONTROL_TASK_H */
