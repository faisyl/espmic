/*
 * board_config.h — Single place to set your board's wiring defaults.
 *
 * Every other module reads YOUR board's pin choices from THIS file, so if your
 * hardware is wired differently, edit the three values below and rebuild.
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
 *     Reference      5      6     4
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
#define BOARD_I2S_DIN_GPIO  24

#ifdef __cplusplus
}
#endif

#endif /* BOARD_CONFIG_H */
