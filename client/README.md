# ESP32-S3 Network Audio Device — Client Firmware

ESP-IDF v5 firmware for ESP32-S3 + dual ICS43434 microphones.
Captures stereo 24-bit PCM at 48 kHz, encodes Opus, streams RTP/UDP,
and maintains a TLS/TCP control connection to the server.

## Build (requires ESP-IDF v5.x)

    idf.py set-target esp32s3
    idf.py build
    idf.py flash monitor

## Host Unit Tests (portable modules only)

    cd test
    make
    ./test_runner

## Containerized Build & Test (Earthly — no local toolchain)

Everything runs in containers via the `Earthfile` at the repo root. The only
host requirements are Docker and the `earthly` binary. No ESP-IDF toolchain,
cmake, or xtensa-gcc is installed on the host.

    # Host unit tests (P1 portable suite) in a slim gcc container — 197 checks
    earthly +host-test

    # Pre-fetch managed components (78/esp-opus) into managed_components/
    earthly +deps

    # Full ESP-IDF firmware build inside the espressif/idf:release-v5.5
    # container. Managed deps (78/esp-opus) are fetched by the component
    # manager in-container. Output (bin + elf + bootloader + partition table)
    # is exported to client/build-<TARGET>/.
    earthly +firmware

    # Build for a different chip (default target is esp32s3)
    earthly +firmware --TARGET=esp32

    # Build the firmware for every supported target (currently esp32s3, esp32)
    earthly +firmware-all

    # Both of the above
    earthly +all

Warm rebuilds are accelerated using `.earthlyignore` (prevents local build
artifacts from busting the build context cache) and persistent `CACHE` mounts for
`ccache` (`/root/.cache/ccache`) and component-manager downloads (`/root/.cache/Espressif`).

## Target selection & SDK configuration

The firmware build is parameterized by chip target via the Earthfile `TARGET`
ARG (default `esp32s3`). Artifacts are saved per target under
`client/build-<TARGET>/` so targets never clobber each other.

SDK configuration is split so the base `sdkconfig.defaults` stays
target-neutral and per-target options live in `sdkconfig.defaults.<target>`
files, which ESP-IDF auto-loads alongside the base file:

- `sdkconfig.defaults`            — target-neutral options (flash size, WiFi, TLS, FreeRTOS, partition table)
- `sdkconfig.defaults.esp32s3`    — ESP32-S3 octal PSRAM @ 80 MHz
- `sdkconfig.defaults.esp32`      — ESP32 quad PSRAM

Caveat: the firmware depends on `78/esp-opus` for Opus encoding; a new target
builds only if that component declares support for the target. Both `esp32s3`
and `esp32` currently compile green.

## Flashing & Serial Monitor

### Identifying the Serial Port

Connect the ESP32 development board via USB and identify the serial port:
- **Linux:** `/dev/ttyUSB0` or `/dev/ttyACM0` (check via `ls /dev/ttyUSB* /dev/ttyACM*` or `dmesg | grep tty`). Ensure your user is in the `dialout` or `plugdev` group (`sudo usermod -aG dialout $USER`).
- **macOS:** `/dev/cu.usbmodem*` or `/dev/cu.usbserial*` (check via `ls /dev/cu.*`).

### Flashing via Native ESP-IDF Toolchain

If you have ESP-IDF v5.x installed locally:

```bash
# Navigate to the client directory
cd client

# Set target chip (esp32s3 by default, or esp32)
idf.py set-target esp32s3

# Build, flash to target port, and open serial monitor
idf.py -p /dev/ttyUSB0 flash monitor
```

### Flashing Prebuilt Binaries (Earthly Artifacts)

When building via Earthly (`earthly +firmware`), artifacts are generated under `client/build-<TARGET>/` (e.g. `client/build-esp32s3/`). To flash the compiled binaries onto hardware without a local ESP-IDF toolchain, use `esptool.py`:

```bash
# Flash complete partition set to ESP32-S3 (default flash offsets)
esptool.py -p /dev/ttyUSB0 -b 460800 write_flash \
  0x0 client/build-esp32s3/bootloader/bootloader.bin \
  0x8000 client/build-esp32s3/partition_table/partition-table.bin \
  0x10000 client/build-esp32s3/espmic_client.bin

# Monitor serial output (using idf.py, minicom, or screen)
idf.py -p /dev/ttyUSB0 monitor
# OR
minicom -D /dev/ttyUSB0 -b 115200
```

*Note:* For target `esp32`, substitute `build-esp32s3` with `build-esp32` and set the appropriate bootloader offset if needed (typically `0x1000` for ESP32).

## Hardware Configuration & I2S Pin Mapping

Hardware I2S audio pins can be configured through two paths:

### Path A: Build-Time Defaults (Source Configuration)

Default pin mappings are defined in `client/components/nvs_config/nvs_config.c` inside `nvs_config_defaults()`:
- `i2s_bclk`: GPIO 5 (Bit Clock)
- `i2s_ws`:   GPIO 6 (Word Select / LR Clock)
- `i2s_din`:  GPIO 4 (Data In from ICS43434)

To change the default pin mapping permanently in source code:
1. Edit `client/components/nvs_config/nvs_config.c` (modify `i2s_bclk_gpio`, `i2s_ws_gpio`, `i2s_din_gpio`).
2. Rebuild the firmware (`earthly +firmware` or `idf.py build`).
3. Re-flash the board.

**NVS Override Precedence:** At boot, `nvs_config_load()` attempts to restore previously saved values from NVS (`KEY_I2S_BCLK`, `KEY_I2S_WS`, `KEY_I2S_DIN`). If valid values exist in NVS (e.g. from a prior runtime configuration or NVS provisioning), they override the compiled defaults.

### Path B: Runtime Configuration via `set_config` Control Message

The firmware supports dynamic pin reassignment over the TLS/TCP control channel using the `set_config` JSON message:

```json
{
  "type": "set_config",
  "request_id": "req-101",
  "default_bitrate": 128000,
  "server_host": "audio.example.local",
  "i2s_bclk": 5,
  "i2s_ws": 6,
  "i2s_din": 4
}
```

Validation & Semantics:
- `i2s_bclk`, `i2s_ws`, and `i2s_din` accept integer values in the valid GPIO range `0..47`.
- Provided pins are validated as a group; if any specified pin is outside `0..47`, the entire update is rejected with an `invalid_config` error.
- Valid pin settings persist immediately to NVS keys `i2s_bclk`, `i2s_ws`, and `i2s_din`.
- **Apply-on-restart:** Because the I2S peripheral channel is initialized once at device startup (`audio_manager_init`), updated pin configurations persist to NVS immediately and take effect on the next boot or stream (re)start.

> **Operator Delivery Path & Server Capability Note:**
> The client firmware processes `set_config` messages over its persistent control channel. However, the included Go server (`espmic-server`) does **not** currently implement a `set_config` message handler or expose an operator HTTP API / CLI tool to push configuration updates to connected devices. Sending `set_config` at runtime requires a custom control-channel client or tool that connects directly to the device's control protocol.

## Project Structure

- `main/`           — application entry point
- `components/`     — modular firmware components
- `test/`           — host-buildable unit tests (gcc, no ESP-IDF)
- `sdkconfig.defaults` — default SDK configuration
- `partitions.csv`  — custom partition table

See `ESP32_Audio_Device_Specification.md` for the full specification.
