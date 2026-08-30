idf_component_register(
    SRCS "i2s_capture.c"
    INCLUDE_DIRS "include"
    PRIV_REQUIRES driver esp_driver_i2s audio_buffer
)
