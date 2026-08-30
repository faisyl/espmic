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

    # Full ESP-IDF firmware build inside the espressif/idf:release-v5.5
    # container. Managed deps (78/esp-opus) are fetched by the component
    # manager in-container. Output (bin + elf + bootloader + partition table)
    # is exported to client/build/.
    earthly +firmware

    # Both of the above
    earthly +all

## Project Structure

- `main/`           — application entry point
- `components/`     — modular firmware components
- `test/`           — host-buildable unit tests (gcc, no ESP-IDF)
- `sdkconfig.defaults` — default SDK configuration
- `partitions.csv`  — custom partition table

See `ESP32_Audio_Device_Specification.md` for the full specification.
