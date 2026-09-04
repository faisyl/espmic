/*
 * board_config.h — Single place to set your board's wiring + identity defaults.
 *
 * Every other module reads YOUR board's choices from THIS file, so if your
 * hardware or deployment differs, edit the values below and rebuild.
 *
 * These are the *boot defaults only*: at runtime the values stored in NVS
 * (if any) take precedence — see components/nvs_config (spec Section 16) and
 * the `set_config` control command (spec Section 10), which both override
 * these defaults without touching this header.
 *
 * GPIO numbers are chip-relative (ESP32-S3 / ESP32 native GPIO pins, not the
 * Arduino-style numbering).
 *
 *     Board          BCLK   WS    DIN
 *     ----------     ----   ---   ---
 *     Reference      18     19    22
 *
 * NOTE: BOARD_SERVER_HOST defaults to a placeholder ("audio.example.local").
 * Moving it here centralizes the default; it does NOT fix the onboarding gap
 * (device needs a connection before set_config can change it). A real
 * deployment should set a real default here before first flash.
 *
 * Pam's docs reference the canonical path: client/board_config.h
 */

#ifndef BOARD_CONFIG_H
#define BOARD_CONFIG_H

#ifdef __cplusplus
extern "C" {
#endif

/* I2S bit-clock (BCLK) GPIO — boot default. Overridden by NVS / set_config. */
#define BOARD_I2S_BCLK_GPIO 18

/* I2S word-select (WS / LRCK) GPIO — boot default. Overridden by NVS / set_config. */
#define BOARD_I2S_WS_GPIO   19

/* I2S serial data-in (DIN) GPIO — boot default. Overridden by NVS / set_config. */
#define BOARD_I2S_DIN_GPIO  22

/* Device identity + server connection — boot defaults. Overridden by NVS / set_config. */
#define BOARD_DEVICE_ID         "esp32-000"
#define BOARD_SERVER_HOST       "audio.example.local"
#define BOARD_SERVER_PORT       4433
#define BOARD_CONTROL_TLS_ENABLED true
#define BOARD_DEFAULT_BITRATE   128000

#ifdef __cplusplus
}
#endif

#endif /* BOARD_CONFIG_H */
