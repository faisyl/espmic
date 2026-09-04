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

### Build cache behavior

**Base image (`espressif/idf`):** The base image (~4 GB) is tagged by a mutable release
tag (`release-v5.5`). On every build Earthly prints `--> Load metadata espressif/idf...`
— this is a lightweight registry HEAD request to check if the tag's digest changed,
**NOT** a re-pull. If the digest is unchanged, the build proceeds instantly from cache
(warm build ~17s). Only when the remote digest changes does Earthly re-download layers.

For reproducibility, you can pin the image by digest in the Earthfile (`FROM
espressif/idf:release-v5.5@sha256:<digest>`). Note: a digest pin uses a different buildkit
cache key, so the first warm build after the change will re-pull; subsequent warm builds
remain fast. To see the current digest: `docker image inspect espressif/idf:release-v5.5
--format '{{index .RepoDigests 0}}'`.

**Buildkit cache:** The buildkit daemon's cache (visible via `docker exec earthly-buildkitd
buildctl du -v`) should comfortably hold the base image + build layers (~16 GB default cap).
If the base image seems to be re-pulled every build (multi-minute cold times repeatedly),
the buildkit cache may be below the image size — increase the cap:
`earthly config global.cache_size_mb <value>` and restart `earthly buildkit restart`.

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

## Device Onboarding (after flashing)

Once the firmware is flashed and powered on, the device boots through provisioning
and connects to your server. This section walks through that first-boot flow.

### 1. Boot Sequence

On every boot the firmware runs (see `client/main/app_main.c`):

1. **NVS init** — required for Wi-Fi credentials and persistent config.
2. **Load config** — `nvs_config_defaults()` applies compiled defaults, then
   `nvs_config_load()` overrides any values previously stored in NVS.
3. **Device state machine** starts at `BOOT`.
4. **Health monitor** starts (1 s stats snapshot) and the Task WDT is enabled.
5. **Audio pipeline** (`audio_manager_init`) initializes I2S with the configured pins.
6. **Wi-Fi manager** initializes and attempts to connect.
7. **On got-IP** — the control task starts and connects to `server_host:server_port`.

### 2. Provision Wi-Fi (BLE)

On first boot (or after a credentials reset) the device has no stored Wi-Fi
credentials, so it automatically enters **BLE provisioning** via Espressif Unified
Provisioning over NimBLE.

- The device advertises as a BLE peripheral using the standard Espressif provisioning
  service UUID `0000ffff-0000-1000-8000-00805f9b34fb`.
- The **advertised name** defaults to `PROV_ESP32` (configurable via
  `wifi_manager_config_t.service_name`, set in `app_main.c`).
- Provisioning uses **Security 1** (encrypted, X25519+AES-CTR) with the default
  Proof-of-Possession `espmic-setup`. The ESP BLE Provisioning app expects this PoP.
- Wi-Fi credentials are stored by the provisioning subsystem in NVS — **not** by
  `nvs_config` (see `nvs_config.h`).

**To provision the device's Wi-Fi**, use one of the following while the device is in
provisioning mode:

- **Mobile app:** Espressif "ESP BLE Provisioning" app (Android/iOS) — scan for the
  device by its advertised name (`PROV_ESP32`), connect, enter the PoP `espmic-setup`,
  then send your home Wi-Fi SSID and password.
- **CLI tool:** `esp-prov --transport ble --sec_ver 1 --pop espmic-setup` — select the
  device by its advertised name and send home Wi-Fi credentials.

On success the device stores the credentials, releases BLE stack RAM
(`esp_bt_mem_release`), restarts, and connects as a station. The provisioning service
registers **no custom endpoints** — it handles Wi-Fi credentials only.

> **Note:** Re-provisioning only happens if credentials are cleared (via
> `wifi_manager_reset_credentials()` or `earthly +erase` wiping NVS).

### 3. Server Endpoint Configuration

After connecting to Wi-Fi, the control task connects to the server using the
configured endpoint:

| Setting | Default | Configured via |
|---|---|---|
| `device_id` | `esp32-<MAC>` (e.g. `esp32-a1b2c3`, unique per chip) | `board_config.h` prefix + eFuse MAC / runtime `set_config` |
| `server_host` | `audio.example.local` | `board_config.h` / runtime `set_config` |
| `server_port` | `4433` | `board_config.h` / runtime `set_config` |
| `control_tls_enabled` | `true` | `board_config.h` / runtime `set_config` |

> **Placeholder host gap:** The device ships pointing at the placeholder hostname
> `audio.example.local:4433`. The runtime way to change it is the `set_config`
> control message (`POST /api/devices/{id}/config` on the server) — but that
> requires the device to **already be connected** to a server (chicken-and-egg).
>
> Realistic ways to point a fresh device at your server:
> 1. **DNS / hosts resolution** — make `audio.example.local` resolve to your server
>    (via your router's DNS, a local DNS server, or the operator's `/etc/hosts`).
>    The device connects to the placeholder name, which now reaches your server;
>    then use `set_config` to persist a real hostname.
> 2. **Change compiled defaults** — edit the `BOARD_*` macros in
>    `client/board_config.h` before flashing.
>
> There is no provisioning-time path to set the server endpoint.

### 4. TLS for the Control Channel

`control_tls_enabled` is `true` by default. The TLS behavior depends on CA
configuration (see `control_task.c`):

- **LAN mode (default):** No CA is configured. The device connects via TLS but
  **does not verify** the server certificate (`skip_common_name = true`). This is
  acceptable only on a controlled, trusted LAN.
- **Hardened mode:** Provision a PEM CA bundle into NVS under the key `server_ca`
  (in the `audiocfg` namespace). On boot the device loads it and pins the server
  certificate. Optionally provision `server_cn` to verify the server's Common Name.

The device ships with **no bundled CA** — operators must provision `server_ca` into
NVS to enable certificate pinning. Plain TCP (no TLS) can be selected by setting
`control_tls_enabled = false`.

### 5. Verify Onboarding Succeeded

**Serial monitor** — watch for these log lines during a successful boot:

```
state BOOT --BOOT_DONE--> WIFI_CONNECTING
... (or PROVISIONING if Wi-Fi not yet configured)
wifi_mgr: got ip: 192.168.x.x
app_main: control task started -> audio.example.local:4433 (tls=1)
control_task: session established (hello_ack)
state CONTROL_CONNECTING --CONTROL_CONNECTED--> IDLE
```

**Server-side check** — once connected, the device appears in the server's device list:

```sh
curl http://localhost:8080/api/devices
# -> [{"device_id":"esp32-a1b2c3", ...}]
```

### 6. Re-provision / Factory Reset

**Re-provision Wi-Fi only:** The API `wifi_manager_reset_credentials()` exists and
clears stored Wi-Fi credentials, causing the device to re-enter provisioning mode
on next boot. However, **no trigger is currently wired** — there is no physical
reset button, GPIO, or software command bound to it in this build (see
`client/P3_NOTES.md`). To re-provision Wi-Fi, erase the full flash (below) or
re-flash the firmware.

**Full factory reset:** Erase the device flash via Earthly's `+erase` target, which
runs `esptool.py erase_flash` on the host. This erases NVS (including Wi-Fi
credentials and all persistent config), so on next boot the device returns to
provisioning mode with default settings:

```bash
earthly +erase --PORT=/dev/ttyUSB0
```

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
