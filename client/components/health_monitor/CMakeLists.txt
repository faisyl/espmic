idf_component_register(
    SRCS "health_monitor.c"
    INCLUDE_DIRS "include"
    REQUIRES audio_manager
    PRIV_REQUIRES esp_timer esp_system esp_psram esp_wifi
)
