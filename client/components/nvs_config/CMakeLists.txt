idf_component_register(
    SRCS "nvs_config.c"
    # "include" = this component's public headers; "../.." = client tree root,
    # so any module can #include "board_config.h" (single source of board pins).
    INCLUDE_DIRS "include" "../.."
    PRIV_REQUIRES nvs_flash
)
