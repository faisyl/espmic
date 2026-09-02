idf_component_register(
    SRCS "wifi_manager.c"
    INCLUDE_DIRS "include"
    PRIV_REQUIRES esp_wifi esp_event nvs_flash esp_netif esp_timer wifi_provisioning bt nvs_config
)
