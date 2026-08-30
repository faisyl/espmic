idf_component_register(
    SRCS "app_main.c"
    INCLUDE_DIRS "."
    PRIV_REQUIRES
        nvs_flash
        state_machine
        nvs_config
        audio_manager
        health_monitor
        wifi_manager
        control_task
)
