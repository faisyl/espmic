idf_component_register(
    SRCS "audio_manager.c"
    INCLUDE_DIRS "include"
    PRIV_REQUIRES i2s_capture opus_encoder rtp_sender audio_buffer esp_psram
)
