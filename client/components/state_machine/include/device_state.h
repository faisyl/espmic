/*
 * device_state.h - Device state machine (portable C99).
 *
 * Implements the device state machine of spec Section 12 as a pure,
 * event-driven transition function: sm_handle_event(state, event) -> new_state.
 * No side effects, no platform dependencies (host-testable).
 *
 * Nominal progression (spec Section 12):
 *   BOOT -> PROVISIONING -> WIFI_CONNECTING -> WIFI_CONNECTED
 *        -> CONTROL_CONNECTING -> IDLE -> STREAM_STARTING -> STREAMING -> IDLE
 *
 * Global overrides that apply from any "connected-enough" state:
 *   Wi-Fi loss          -> WIFI_CONNECTING
 *   control loss        -> CONTROL_CONNECTING
 *   fatal internal error -> ERROR (-> recovery/reboot)
 */
#ifndef DEVICE_STATE_H
#define DEVICE_STATE_H

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    SM_STATE_BOOT = 0,
    SM_STATE_PROVISIONING,
    SM_STATE_WIFI_CONNECTING,
    SM_STATE_WIFI_CONNECTED,
    SM_STATE_CONTROL_CONNECTING,
    SM_STATE_IDLE,
    SM_STATE_STREAM_STARTING,
    SM_STATE_STREAMING,
    SM_STATE_ERROR,
    SM_STATE_COUNT
} sm_state_t;

typedef enum {
    /* Nominal progression events. */
    SM_EV_BOOT_DONE = 0,      /* credentials present: begin Wi-Fi connect */
    SM_EV_NEED_PROVISION,     /* credentials absent / reset requested */
    SM_EV_PROVISIONED,        /* provisioning finished successfully */
    SM_EV_WIFI_CONNECTED,     /* station got IP */
    SM_EV_CONTROL_CONNECT,    /* begin establishing the control connection */
    SM_EV_CONTROL_CONNECTED,  /* control session established (hello_ack) */
    SM_EV_START_STREAM,       /* server start_stream accepted/validated */
    SM_EV_STREAM_STARTED,     /* RTP stream running */
    SM_EV_STOP_STREAM,        /* server stop_stream (or grace-period stop) */

    /* Global override events (may arrive in "any" state). */
    SM_EV_WIFI_LOST,          /* -> WIFI_CONNECTING */
    SM_EV_CONTROL_LOST,       /* -> CONTROL_CONNECTING */
    SM_EV_FATAL,              /* -> ERROR */
    SM_EV_RECOVER,            /* ERROR -> BOOT (recovery/reboot) */

    SM_EV_COUNT
} sm_event_t;

/*
 * Pure transition function. Given the current `state` and an `event`, returns
 * the next state. An event that is not meaningful in the given state returns
 * `state` unchanged (no spurious transitions).
 *
 * Global overrides take precedence and are handled uniformly:
 *   - SM_EV_FATAL       : from any state          -> ERROR
 *   - SM_EV_WIFI_LOST   : from any state that has  -> WIFI_CONNECTING
 *                         reached the network (not
 *                         BOOT/PROVISIONING/ERROR)
 *   - SM_EV_CONTROL_LOST: from any state that owns -> CONTROL_CONNECTING
 *                         a control link (IDLE,
 *                         STREAM_STARTING, STREAMING,
 *                         CONTROL_CONNECTING)
 */
sm_state_t sm_handle_event(sm_state_t state, sm_event_t event);

/* Human-readable names for logging / tests. Returns "?" for out-of-range. */
const char *sm_state_name(sm_state_t state);
const char *sm_event_name(sm_event_t event);

#ifdef __cplusplus
}
#endif

#endif /* DEVICE_STATE_H */
