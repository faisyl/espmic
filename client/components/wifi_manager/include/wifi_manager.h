/*
 * wifi_manager.h - Wi-Fi station + provisioning (spec Sections 11, 12, 13, 16).
 *
 * Owns the Wi-Fi lifecycle: attempts NVS-stored credentials on boot, enters
 * ESP-IDF BLE provisioning (wifi_prov_scheme_ble) when credentials are absent
 * or reset, and auto-reconnects the station on disconnect. Wi-Fi credentials are
 * persisted by
 * the provisioning subsystem in NVS (they are NOT stored by nvs_config).
 *
 * Connection state changes are surfaced through callbacks so the higher layer
 * can drive the device state machine (spec Section 12: Wi-Fi loss ->
 * WIFI_CONNECTING).
 */
#ifndef WIFI_MANAGER_H
#define WIFI_MANAGER_H

#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    /* Called (from the wifi/event context) on state changes. Keep them short;
     * do not block. `user` is passed through. */
    void (*on_got_ip)(uint32_t ip4, void *user);       /* station has IP */
    void (*on_disconnected)(void *user);               /* link lost */
    void (*on_provisioning_started)(void *user);
    void (*on_provisioned)(void *user);
    void *user;

    /* Provisioning service identity. If pop[0]==0 no proof-of-possession is
     * used. The server auth secret is never exposed here (spec Section 13). */
     char service_name[32]; /* BLE advertised service/device name, e.g. "PROV_esp32" */
    char pop[33];          /* proof of possession, optional */
} wifi_manager_config_t;

/* Initialise netif, the default event loop, and the Wi-Fi driver. Call once
 * after nvs_flash_init(). */
esp_err_t wifi_manager_init(const wifi_manager_config_t *cfg);

/*
 * Start Wi-Fi. If provisioned, connect as a station using stored credentials;
 * otherwise start provisioning (spec Section 13) and connect once complete.
 */
esp_err_t wifi_manager_start(void);

/* Erase stored Wi-Fi credentials and (re)enter provisioning (spec Section 13). */
esp_err_t wifi_manager_reset_credentials(void);

bool wifi_manager_is_connected(void);

#ifdef __cplusplus
}
#endif

#endif /* WIFI_MANAGER_H */
