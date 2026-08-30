/*
 * device_state.c - Device state machine (portable C99).
 * See device_state.h and spec Section 12.
 */
#include "device_state.h"

/* Does this state sit at or beyond "network reachable"? Used to decide whether
 * a Wi-Fi-loss override applies. BOOT/PROVISIONING have no network yet, and
 * ERROR is handled by its own recovery path. */
static int has_network(sm_state_t s)
{
    switch (s) {
        case SM_STATE_WIFI_CONNECTED:
        case SM_STATE_CONTROL_CONNECTING:
        case SM_STATE_IDLE:
        case SM_STATE_STREAM_STARTING:
        case SM_STATE_STREAMING:
            return 1;
        default:
            return 0;
    }
}

/* Does this state own (or is establishing) the control connection? Used to
 * decide whether a control-loss override applies. */
static int has_control(sm_state_t s)
{
    switch (s) {
        case SM_STATE_CONTROL_CONNECTING:
        case SM_STATE_IDLE:
        case SM_STATE_STREAM_STARTING:
        case SM_STATE_STREAMING:
            return 1;
        default:
            return 0;
    }
}

sm_state_t sm_handle_event(sm_state_t state, sm_event_t event)
{
    if (state < 0 || state >= SM_STATE_COUNT) {
        return state;
    }

    /* --- Global overrides (spec Section 12 "Any state"). --- */
    switch (event) {
        case SM_EV_FATAL:
            return SM_STATE_ERROR;
        case SM_EV_WIFI_LOST:
            /* Wi-Fi loss short-circuits control loss: without a link there is
             * no control connection to speak of. */
            if (has_network(state)) {
                return SM_STATE_WIFI_CONNECTING;
            }
            return state;
        case SM_EV_CONTROL_LOST:
            if (has_control(state)) {
                return SM_STATE_CONTROL_CONNECTING;
            }
            return state;
        case SM_EV_RECOVER:
            /* Only meaningful out of ERROR: go back to BOOT for reboot/recovery. */
            return (state == SM_STATE_ERROR) ? SM_STATE_BOOT : state;
        default:
            break;
    }

    /* --- Nominal, state-specific transitions. --- */
    switch (state) {
        case SM_STATE_BOOT:
            if (event == SM_EV_NEED_PROVISION) return SM_STATE_PROVISIONING;
            if (event == SM_EV_BOOT_DONE)      return SM_STATE_WIFI_CONNECTING;
            break;

        case SM_STATE_PROVISIONING:
            if (event == SM_EV_PROVISIONED) return SM_STATE_WIFI_CONNECTING;
            break;

        case SM_STATE_WIFI_CONNECTING:
            if (event == SM_EV_WIFI_CONNECTED) return SM_STATE_WIFI_CONNECTED;
            break;

        case SM_STATE_WIFI_CONNECTED:
            if (event == SM_EV_CONTROL_CONNECT) return SM_STATE_CONTROL_CONNECTING;
            break;

        case SM_STATE_CONTROL_CONNECTING:
            if (event == SM_EV_CONTROL_CONNECTED) return SM_STATE_IDLE;
            break;

        case SM_STATE_IDLE:
            if (event == SM_EV_START_STREAM) return SM_STATE_STREAM_STARTING;
            break;

        case SM_STATE_STREAM_STARTING:
            if (event == SM_EV_STREAM_STARTED) return SM_STATE_STREAMING;
            if (event == SM_EV_STOP_STREAM)    return SM_STATE_IDLE; /* aborted start */
            break;

        case SM_STATE_STREAMING:
            if (event == SM_EV_STOP_STREAM) return SM_STATE_IDLE;
            break;

        case SM_STATE_ERROR:
            /* Leaves ERROR only via SM_EV_RECOVER (handled above). */
            break;

        default:
            break;
    }

    return state; /* no meaningful transition */
}

const char *sm_state_name(sm_state_t state)
{
    switch (state) {
        case SM_STATE_BOOT:               return "BOOT";
        case SM_STATE_PROVISIONING:       return "PROVISIONING";
        case SM_STATE_WIFI_CONNECTING:    return "WIFI_CONNECTING";
        case SM_STATE_WIFI_CONNECTED:     return "WIFI_CONNECTED";
        case SM_STATE_CONTROL_CONNECTING: return "CONTROL_CONNECTING";
        case SM_STATE_IDLE:               return "IDLE";
        case SM_STATE_STREAM_STARTING:    return "STREAM_STARTING";
        case SM_STATE_STREAMING:          return "STREAMING";
        case SM_STATE_ERROR:              return "ERROR";
        default:                          return "?";
    }
}

const char *sm_event_name(sm_event_t event)
{
    switch (event) {
        case SM_EV_BOOT_DONE:        return "BOOT_DONE";
        case SM_EV_NEED_PROVISION:   return "NEED_PROVISION";
        case SM_EV_PROVISIONED:      return "PROVISIONED";
        case SM_EV_WIFI_CONNECTED:   return "WIFI_CONNECTED";
        case SM_EV_CONTROL_CONNECT:  return "CONTROL_CONNECT";
        case SM_EV_CONTROL_CONNECTED:return "CONTROL_CONNECTED";
        case SM_EV_START_STREAM:     return "START_STREAM";
        case SM_EV_STREAM_STARTED:   return "STREAM_STARTED";
        case SM_EV_STOP_STREAM:      return "STOP_STREAM";
        case SM_EV_WIFI_LOST:        return "WIFI_LOST";
        case SM_EV_CONTROL_LOST:     return "CONTROL_LOST";
        case SM_EV_FATAL:            return "FATAL";
        case SM_EV_RECOVER:          return "RECOVER";
        default:                     return "?";
    }
}
