# Earthly build file for the ESP32 network audio device firmware.
#
# Everything runs in containers — NO ESP-IDF toolchain, cmake, or xtensa-gcc
# is required on the host. The only host dependency is Docker (and Earthly).
VERSION 0.8

# ---------------------------------------------------------------- host-test
# Host unit tests for the portable P1 modules (rtp, control_frame, pcm_ring,
# encoded_queue, device_state). Plain gcc + make — no ESP-IDF involvement.
host-test:
    FROM gcc:13
    WORKDIR /src
    COPY client ./client
    RUN cd client/test && make test

# ----------------------------------------------------------------------- deps
# Shared dependency layer for every firmware target. Inputs are ONLY the
# dependency manifest + the minimal files `idf.py set-target` needs to run the
# component manager, which fetches managed components (78/esp-opus) into
# managed_components/. Because the C sources / local components are NOT copied
# here, this layer cache-hits across source edits — the fetch + initial
# configure only re-runs when a manifest or sdkconfig.defaults* changes.
#
# Note: set-target cannot fully resolve the project here (main's PRIV_REQUIRES
# list local components that only exist once the rest of client/ is copied in
# +firmware), so it exits non-zero AFTER the component manager has populated
# managed_components/. The guard treats "esp-opus was fetched" as success;
# any real failure (e.g. a registry/network error) still fails the layer.
#
# Persistent CACHE mounts (survive across builds on this host):
#   /root/.cache/ccache     — ccache objects (IDF_CCACHE_ENABLE=1) make
#                             recompiles near-incremental across cache misses
#   /root/.cache/Espressif  — component-manager download cache, so esp-opus is
#                             never re-downloaded once fetched
deps:
    ARG TARGET=esp32s3
    FROM espressif/idf:release-v5.5
    WORKDIR /src
    ENV IDF_CCACHE_ENABLE=1
    CACHE /root/.cache/ccache
    CACHE /root/.cache/Espressif
    COPY client/main/idf_component.yml ./client/main/idf_component.yml
    COPY client/main/CMakeLists.txt ./client/main/CMakeLists.txt
    COPY client/CMakeLists.txt ./client/CMakeLists.txt
    COPY client/partitions.csv ./client/partitions.csv
    COPY client/sdkconfig.defaults ./client/sdkconfig.defaults
    COPY client/sdkconfig.defaults.esp32 ./client/sdkconfig.defaults.esp32
    COPY client/sdkconfig.defaults.esp32s3 ./client/sdkconfig.defaults.esp32s3
    RUN bash -lc 'cd client && . $IDF_PATH/export.sh && (idf.py set-target $TARGET || test -d managed_components/78__esp-opus)'

# ------------------------------------------------------------------ firmware
# Full ESP-IDF build of the client firmware, layered on top of +deps. The deps
# layer already fetched managed components and ran the initial configure; here
# we copy the remaining source and build. With the CACHE mounts, editing a
# source file re-compiles incrementally through ccache instead of re-downloading
# deps or recompiling the whole tree.
# Parametrized by chip target: `earthly +firmware --TARGET=esp32` (or any
# ESP-IDF target string) builds that target. The whole client/build-$TARGET dir
# (bins, elf, bootloader, partition table) is saved as the `firmware` artifact
# so targets never clobber each other's artifacts.
firmware:
    ARG TARGET=esp32s3
    FROM +deps --TARGET=$TARGET
    # Re-declare the cache mounts so the actual `idf.py build` (which runs
    # HERE, not in +deps) writes into the persistent shared caches — otherwise
    # the cold build's ccache is lost and every warm build recompiles all.
    CACHE /root/.cache/ccache
    CACHE /root/.cache/Espressif
    COPY client ./client
    # export.sh needs bash (BASH_SOURCE) to locate the IDF dir; Earthly's
    # default RUN shell is /bin/sh, so run bash explicitly.
    # SDKCONFIG_DEFAULTS default is target-neutral sdkconfig.defaults; the
    # build system auto-appends sdkconfig.defaults.<target> for the chosen
    # target, so the base + per-target fragments are picked up automatically.
    RUN bash -lc 'cd client && . $IDF_PATH/export.sh && idf.py build && mv build build-$TARGET'
    SAVE ARTIFACT client/build-$TARGET AS LOCAL client/build-$TARGET

# ------------------------------------------------------------ firmware-all
# Build the firmware for every supported target.
firmware-all:
    BUILD +firmware --TARGET=esp32s3
    BUILD +firmware --TARGET=esp32

# ----------------------------------------------------------------------- erase
# Erase the client device flash via esptool. Runs on the HOST via LOCALLY
# because BuildKit containers cannot see the host USB serial port
# (/dev/ttyUSB*). Requires esptool (+pyserial) installed on the host.
erase:
    ARG TARGET=esp32s3
    ARG PORT=/dev/ttyUSB0
    ARG BAUD=460800
    LOCALLY
    RUN esptool --chip $TARGET -p $PORT -b $BAUD erase-flash

# ----------------------------------------------------------------------- flash
# Flash the built client firmware to the device via esptool. Runs on the HOST
# via LOCALLY (BuildKit containers cannot see the host USB serial port).
# Requires the firmware to be built first (`earthly +firmware --TARGET=$TARGET`)
# so the artifacts exist locally; the bootloader offset depends on the target
# chip (0x0 for esp32s3, 0x1000 for esp32). Offsets match client/partitions.csv.
# --flash_size detect auto-detects the chip and patches the image header to
# match, so this works for both 4MB and 8MB boards.
flash:
    ARG TARGET=esp32s3
    ARG PORT=/dev/ttyUSB0
    ARG BAUD=460800
    LOCALLY
    RUN if [ "$TARGET" = "esp32" ]; then \
            BOOT_OFFSET=0x1000; \
        else \
            BOOT_OFFSET=0x0; \
        fi; \
        esptool --chip $TARGET -p $PORT -b $BAUD \
            --before default_reset --after hard_reset \
            write_flash --flash_mode dio --flash_size detect --flash_freq 40m \
            $BOOT_OFFSET client/build-$TARGET/bootloader/bootloader.bin \
            0x8000 client/build-$TARGET/partition_table/partition-table.bin \
            0x10000 client/build-$TARGET/espmic_client.bin

# ----------------------------------------------------------------------- all
# Build everything: host tests + full firmware.
all:
    BUILD +host-test
    BUILD +firmware
