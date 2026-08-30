idf_component_register(
    SRCS "app_main.c"
    INCLUDE_DIRS "."
    PRIV_REQUIRES
        state_machine
        audio_buffer
        rtp
        control_protocol
        wifi_manager
        control_task
        i2s_capture
        opus_encoder
        rtp_sender
        audio_manager
        health_monitor
        nvs_config
)
