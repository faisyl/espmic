/*
 * nvs_config.c - Persistent device configuration store (spec Section 16).
 */
#include "nvs_config.h"
#include "board_config.h"

#include <string.h>
#include "nvs.h"
#include "nvs_flash.h"
#include "esp_log.h"

static const char *TAG = "nvs_config";

/* Key names (kept <= 15 chars, the NVS key limit). */
#define KEY_DEVICE_ID   "device_id"
#define KEY_SERVER_HOST "server_host"
#define KEY_SERVER_PORT "server_port"
#define KEY_TLS_EN      "tls_enabled"
#define KEY_BITRATE     "def_bitrate"
#define KEY_I2S_BCLK    "i2s_bclk"
#define KEY_I2S_WS      "i2s_ws"
#define KEY_I2S_DIN     "i2s_din"

/* Compiled-in defaults. GPIO defaults come from board_config.h (single source
 * of truth at the top of the client tree); real boards override via NVS at
 * runtime (spec Section 5 notes GPIO is board-specific, Section 16 NVS). */
void nvs_config_defaults(device_config_t *cfg)
{
    if (!cfg) return;
    memset(cfg, 0, sizeof(*cfg));
    strncpy(cfg->device_id, "esp32-000", sizeof(cfg->device_id) - 1);
    strncpy(cfg->server_host, "audio.example.local", sizeof(cfg->server_host) - 1);
    cfg->server_port         = 4433;
    cfg->control_tls_enabled = true;
    cfg->default_bitrate     = 128000;
    cfg->i2s_bclk_gpio       = BOARD_I2S_BCLK_GPIO;
    cfg->i2s_ws_gpio         = BOARD_I2S_WS_GPIO;
    cfg->i2s_din_gpio        = BOARD_I2S_DIN_GPIO;
}

/* Read a string key into dst; leaves dst unchanged if the key is absent. */
static void load_str(nvs_handle_t h, const char *key, char *dst, size_t dst_sz)
{
    size_t len = dst_sz;
    esp_err_t err = nvs_get_str(h, key, dst, &len);
    if (err != ESP_OK && err != ESP_ERR_NVS_NOT_FOUND) {
        ESP_LOGW(TAG, "get_str %s: %s", key, esp_err_to_name(err));
    }
}

esp_err_t nvs_config_load(device_config_t *cfg)
{
    if (!cfg) return ESP_ERR_INVALID_ARG;

    nvs_handle_t h;
    esp_err_t err = nvs_open(NVS_CONFIG_NAMESPACE, NVS_READONLY, &h);
    if (err == ESP_ERR_NVS_NOT_FOUND) {
        ESP_LOGI(TAG, "no stored config; using defaults");
        return ESP_OK;
    }
    if (err != ESP_OK) return err;

    load_str(h, KEY_DEVICE_ID, cfg->device_id, sizeof(cfg->device_id));
    load_str(h, KEY_SERVER_HOST, cfg->server_host, sizeof(cfg->server_host));

    uint16_t u16;
    if (nvs_get_u16(h, KEY_SERVER_PORT, &u16) == ESP_OK) cfg->server_port = u16;

    uint8_t u8;
    if (nvs_get_u8(h, KEY_TLS_EN, &u8) == ESP_OK) cfg->control_tls_enabled = (u8 != 0);

    uint32_t u32;
    if (nvs_get_u32(h, KEY_BITRATE, &u32) == ESP_OK) cfg->default_bitrate = u32;

    int32_t i32;
    if (nvs_get_i32(h, KEY_I2S_BCLK, &i32) == ESP_OK) cfg->i2s_bclk_gpio = i32;
    if (nvs_get_i32(h, KEY_I2S_WS, &i32) == ESP_OK)   cfg->i2s_ws_gpio = i32;
    if (nvs_get_i32(h, KEY_I2S_DIN, &i32) == ESP_OK)  cfg->i2s_din_gpio = i32;

    nvs_close(h);
    ESP_LOGI(TAG, "loaded config: id=%s host=%s:%u tls=%d br=%u",
             cfg->device_id, cfg->server_host, (unsigned)cfg->server_port,
             (int)cfg->control_tls_enabled, (unsigned)cfg->default_bitrate);
    return ESP_OK;
}

esp_err_t nvs_config_save(const device_config_t *cfg)
{
    if (!cfg) return ESP_ERR_INVALID_ARG;

    nvs_handle_t h;
    esp_err_t err = nvs_open(NVS_CONFIG_NAMESPACE, NVS_READWRITE, &h);
    if (err != ESP_OK) return err;

    err  = nvs_set_str(h, KEY_DEVICE_ID, cfg->device_id);
    err |= nvs_set_str(h, KEY_SERVER_HOST, cfg->server_host);
    err |= nvs_set_u16(h, KEY_SERVER_PORT, cfg->server_port);
    err |= nvs_set_u8(h, KEY_TLS_EN, cfg->control_tls_enabled ? 1 : 0);
    err |= nvs_set_u32(h, KEY_BITRATE, cfg->default_bitrate);
    err |= nvs_set_i32(h, KEY_I2S_BCLK, cfg->i2s_bclk_gpio);
    err |= nvs_set_i32(h, KEY_I2S_WS, cfg->i2s_ws_gpio);
    err |= nvs_set_i32(h, KEY_I2S_DIN, cfg->i2s_din_gpio);

    if (err == ESP_OK) err = nvs_commit(h);
    nvs_close(h);
    return err;
}

esp_err_t nvs_config_erase(void)
{
    nvs_handle_t h;
    esp_err_t err = nvs_open(NVS_CONFIG_NAMESPACE, NVS_READWRITE, &h);
    if (err != ESP_OK) return err;
    err = nvs_erase_all(h);
    if (err == ESP_OK) err = nvs_commit(h);
    nvs_close(h);
    return err;
}

esp_err_t nvs_config_set_str(const char *key, const char *value)
{
    if (!key || !value) return ESP_ERR_INVALID_ARG;
    nvs_handle_t h;
    esp_err_t err = nvs_open(NVS_CONFIG_NAMESPACE, NVS_READWRITE, &h);
    if (err != ESP_OK) return err;
    err = nvs_set_str(h, key, value);
    if (err == ESP_OK) err = nvs_commit(h);
    nvs_close(h);
    return err;
}

esp_err_t nvs_config_set_u32(const char *key, uint32_t value)
{
    if (!key) return ESP_ERR_INVALID_ARG;
    nvs_handle_t h;
    esp_err_t err = nvs_open(NVS_CONFIG_NAMESPACE, NVS_READWRITE, &h);
    if (err != ESP_OK) return err;
    err = nvs_set_u32(h, key, value);
    if (err == ESP_OK) err = nvs_commit(h);
    nvs_close(h);
    return err;
}

esp_err_t nvs_config_set_i32(const char *key, int32_t value)
{
    if (!key) return ESP_ERR_INVALID_ARG;
    nvs_handle_t h;
    esp_err_t err = nvs_open(NVS_CONFIG_NAMESPACE, NVS_READWRITE, &h);
    if (err != ESP_OK) return err;
    err = nvs_set_i32(h, key, value);
    if (err == ESP_OK) err = nvs_commit(h);
    nvs_close(h);
    return err;
}
