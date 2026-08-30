idf_component_register(
    SRCS "opus_encoder_task.c"
    INCLUDE_DIRS "include"
    REQUIRES audio_buffer
    PRIV_REQUIRES opus
)
