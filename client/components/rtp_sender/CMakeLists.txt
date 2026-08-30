idf_component_register(
    SRCS "rtp_sender.c"
    INCLUDE_DIRS "include"
    REQUIRES audio_buffer
    PRIV_REQUIRES rtp lwip
)
