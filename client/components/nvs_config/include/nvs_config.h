/*
 * nvs_config.h - Persistent device configuration store (spec Section 16).
 *
 * Thin wrapper over NVS that loads/saves/erases the persistent keys named in
 * spec Section 16:
 *   device_id, server_host, server_port, control_tls_enabled, default_bitrate,
 *   and the board-specific I2S GPIO configuration.
 *
 * Wi-Fi credentials are intentionally NOT managed here: they are owned by the
 * ESP-IDF provisioning subsystem (spec Section 13) via wifi_manager.
 */
#ifndef NVS_CONFIG_H
#define NVS_CONFIG_H

#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

/* NVS namespace used for all keys below. */
#define NVS_CONFIG_NAMESPACE "audiocfg"

/* Field size limits (include room for NUL). */
#define NVS_CFG_DEVICE_ID_MAX 64
#define NVS_CFG_HOST_MAX      128

/*
 * In-memory representation of the persistent configuration. Loaded once at boot
 * and handed to the tasks that need it (control_task, i2s_capture, opus).
 */
typedef struct {
    char     device_id[NVS_CFG_DEVICE_ID_MAX];  /* e.g. "esp32-001" */
    char     server_host[NVS_CFG_HOST_MAX];     /* e.g. "audio.example.local" */
    uint16_t server_port;                       /* e.g. 4433 */
    bool     control_tls_enabled;               /* TLS for control (spec Section 15) */
    uint32_t default_bitrate;                   /* e.g. 128000 */

    /* Board-specific I2S GPIO configuration (spec Section 5/16). */
    int32_t  i2s_bclk_gpio;
    int32_t  i2s_ws_gpio;
    int32_t  i2s_din_gpio;
} device_config_t;

/*
 * Populate `cfg` with compiled-in defaults (does not touch NVS). Useful as a
 * base before nvs_config_load(), and as the fallback on first boot.
 */
void nvs_config_defaults(device_config_t *cfg);

/*
 * Load configuration from NVS. Any key that is missing keeps the value already
 * present in `*cfg` (so call nvs_config_defaults() first). Returns ESP_OK even
 * when some keys are absent; returns an error only on an NVS access failure.
 */
esp_err_t nvs_config_load(device_config_t *cfg);

/* Persist every field of `cfg` to NVS (commit included). */
esp_err_t nvs_config_save(const device_config_t *cfg);

/* Erase every key in the config namespace (factory reset of config, not creds). */
esp_err_t nvs_config_erase(void);

/* Convenience single-key setters that load/modify/commit atomically. */
esp_err_t nvs_config_set_str(const char *key, const char *value);
esp_err_t nvs_config_set_u32(const char *key, uint32_t value);
esp_err_t nvs_config_set_i32(const char *key, int32_t value);

#ifdef __cplusplus
}
#endif

#endif /* NVS_CONFIG_H */
