idf_component_register(
    SRCS "control_task.c"
    INCLUDE_DIRS "include"
    REQUIRES state_machine
    PRIV_REQUIRES esp-tls control_protocol json lwip
                  audio_manager health_monitor nvs_config
)
