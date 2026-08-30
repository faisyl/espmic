/* Host tests for the device state machine (spec Section 12). */
#include "device_state.h"
#include "test_util.h"

int main(void)
{
    /* --- nominal progression --- */
    sm_state_t s = SM_STATE_BOOT;
    s = sm_handle_event(s, SM_EV_BOOT_DONE);
    CHECK_EQ_INT(s, SM_STATE_WIFI_CONNECTING);
    s = sm_handle_event(s, SM_EV_WIFI_CONNECTED);
    CHECK_EQ_INT(s, SM_STATE_WIFI_CONNECTED);
    s = sm_handle_event(s, SM_EV_CONTROL_CONNECT);
    CHECK_EQ_INT(s, SM_STATE_CONTROL_CONNECTING);
    s = sm_handle_event(s, SM_EV_CONTROL_CONNECTED);
    CHECK_EQ_INT(s, SM_STATE_IDLE);
    s = sm_handle_event(s, SM_EV_START_STREAM);
    CHECK_EQ_INT(s, SM_STATE_STREAM_STARTING);
    s = sm_handle_event(s, SM_EV_STREAM_STARTED);
    CHECK_EQ_INT(s, SM_STATE_STREAMING);
    s = sm_handle_event(s, SM_EV_STOP_STREAM);
    CHECK_EQ_INT(s, SM_STATE_IDLE);

    /* --- provisioning branch --- */
    s = sm_handle_event(SM_STATE_BOOT, SM_EV_NEED_PROVISION);
    CHECK_EQ_INT(s, SM_STATE_PROVISIONING);
    s = sm_handle_event(s, SM_EV_PROVISIONED);
    CHECK_EQ_INT(s, SM_STATE_WIFI_CONNECTING);

    /* --- fatal from ANY state -> ERROR --- */
    for (int st = 0; st < SM_STATE_COUNT; ++st) {
        CHECK_EQ_INT(sm_handle_event((sm_state_t)st, SM_EV_FATAL), SM_STATE_ERROR);
    }
    /* ERROR recovers to BOOT. */
    CHECK_EQ_INT(sm_handle_event(SM_STATE_ERROR, SM_EV_RECOVER), SM_STATE_BOOT);

    /* --- wifi loss from networked states -> WIFI_CONNECTING --- */
    sm_state_t net_states[] = {
        SM_STATE_WIFI_CONNECTED, SM_STATE_CONTROL_CONNECTING, SM_STATE_IDLE,
        SM_STATE_STREAM_STARTING, SM_STATE_STREAMING
    };
    for (unsigned i = 0; i < sizeof(net_states)/sizeof(net_states[0]); ++i) {
        CHECK_EQ_INT(sm_handle_event(net_states[i], SM_EV_WIFI_LOST),
                     SM_STATE_WIFI_CONNECTING);
    }
    /* wifi loss while still in BOOT/PROVISIONING is a no-op (no network yet). */
    CHECK_EQ_INT(sm_handle_event(SM_STATE_BOOT, SM_EV_WIFI_LOST), SM_STATE_BOOT);
    CHECK_EQ_INT(sm_handle_event(SM_STATE_PROVISIONING, SM_EV_WIFI_LOST),
                 SM_STATE_PROVISIONING);

    /* --- control loss from control-owning states -> CONTROL_CONNECTING --- */
    sm_state_t ctl_states[] = {
        SM_STATE_IDLE, SM_STATE_STREAM_STARTING, SM_STATE_STREAMING,
        SM_STATE_CONTROL_CONNECTING
    };
    for (unsigned i = 0; i < sizeof(ctl_states)/sizeof(ctl_states[0]); ++i) {
        CHECK_EQ_INT(sm_handle_event(ctl_states[i], SM_EV_CONTROL_LOST),
                     SM_STATE_CONTROL_CONNECTING);
    }
    /* control loss before we have a link (WIFI_CONNECTING) is a no-op. */
    CHECK_EQ_INT(sm_handle_event(SM_STATE_WIFI_CONNECTING, SM_EV_CONTROL_LOST),
                 SM_STATE_WIFI_CONNECTING);

    /* --- aborted stream start via stop_stream returns to IDLE --- */
    CHECK_EQ_INT(sm_handle_event(SM_STATE_STREAM_STARTING, SM_EV_STOP_STREAM),
                 SM_STATE_IDLE);

    /* --- irrelevant events do not cause spurious transitions --- */
    CHECK_EQ_INT(sm_handle_event(SM_STATE_IDLE, SM_EV_WIFI_CONNECTED),
                 SM_STATE_IDLE);
    CHECK_EQ_INT(sm_handle_event(SM_STATE_STREAMING, SM_EV_START_STREAM),
                 SM_STATE_STREAMING);

    /* names are non-NULL and defined. */
    CHECK(sm_state_name(SM_STATE_STREAMING)[0] == 'S');
    CHECK(sm_event_name(SM_EV_FATAL)[0] == 'F');

    TEST_MAIN_END("state_machine");
}
