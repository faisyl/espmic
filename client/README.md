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

### Flashing via Earthly (LOCALLY targets)

The `+erase` and `+flash` Earthly targets run `esptool.py` **on the host** (via
Earthly's `LOCALLY` directive), because BuildKit containers cannot see the host
USB serial port. Build the firmware first, then flash:

```bash
# Build the firmware (produces client/build-esp32s3/ locally)
earthly +firmware

# Erase the device flash
earthly +erase --PORT=/dev/ttyUSB0

# Flash the built artifacts (esp32s3)
earthly +flash --TARGET=esp32s3 --PORT=/dev/ttyUSB0

# For ESP32 (note the different bootloader offset)
earthly +flash --TARGET=esp32 --PORT=/dev/ttyUSB0
```

**Host requirements:**
- `esptool.py` + `pyserial` installed on the host (`pip install esptool pyserial`).
- Your user must have permission to the serial port (add yourself to the `dialout`
  or `plugdev` group: `sudo usermod -aG dialout $USER`, then re-login).
- These targets run on the host by design — they are not hermetic and are never cached.

Flash offsets match `client/partitions.csv`: bootloader at `0x0` (esp32s3) or
`0x1000` (esp32), partition table at `0x8000`, app at `0x10000`.

## Hardware Configuration & I2S Pin Mapping

Hardware I2S audio pins can be configured through two paths:

### Path A: Build-Time Defaults (Source Configuration)

The boot-default pin mapping lives in a single top-level header, `client/board_config.h`
(next to `client/CMakeLists.txt`). Every module reads your board's pin choices from this
file via three macros:
- `BOARD_I2S_BCLK_GPIO`: GPIO 5 (Bit Clock)
- `BOARD_I2S_WS_GPIO`:   GPIO 6 (Word Select / LR Clock)
- `BOARD_I2S_DIN_GPIO`:  GPIO 4 (Data In from ICS43434)

To change the boot-default pin mapping:
1. Edit `client/board_config.h` — set the macro value(s) to match your wiring.
2. Rebuild the firmware (`earthly +firmware` or `idf.py build`).
3. Re-flash the board.

**NVS Override Precedence:** These are *boot defaults only*. At boot, `nvs_config_load()`
restores any previously saved values from NVS (`KEY_I2S_BCLK`, `KEY_I2S_WS`, `KEY_I2S_DIN`);
if valid values exist in NVS they override the header defaults without touching
`board_config.h`. Runtime `set_config` updates (below) behave the same way.

### Path B: Runtime Configuration via the Server Device-Config API

To change I2S pins on a deployed device at runtime, push a config update to the device
over its live control session via the server's device-config endpoint:

```
POST /api/devices/{id}/config
```

with a JSON body containing the fields to change, e.g. `{"i2s_bclk":5,"i2s_ws":6,"i2s_din":4}`.
The device must be connected to the server's control channel. Pin values are validated
to the GPIO range `0..47`; changes persist to NVS and take effect on the next boot or
stream (re)start.

Full endpoint reference, request/response table, and `curl` examples live in the server
README's "Push runtime config" section — see `server/README.md`.

Validation & Semantics:
- `i2s_bclk`, `i2s_ws`, and `i2s_din` accept integer values in the valid GPIO range `0..47`.
- Provided pins are validated as a group; if any specified pin is outside `0..47`, the update is rejected.
- Valid pin settings persist immediately to NVS keys `i2s_bclk`, `i2s_ws`, and `i2s_din`.
- **Apply-on-restart:** Because the I2S peripheral channel is initialized once at device startup (`audio_manager_init`), updated pin configurations persist to NVS immediately and take effect on the next boot or stream (re)start.

## Project Structure

- `main/`           — application entry point
- `components/`     — modular firmware components
- `test/`           — host-buildable unit tests (gcc, no ESP-IDF)
- `sdkconfig.defaults` — default SDK configuration
- `partitions.csv`  — custom partition table

See `ESP32_Audio_Device_Specification.md` for the full specification.
