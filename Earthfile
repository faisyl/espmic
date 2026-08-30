# Earthly build file for the ESP32-S3 network audio device firmware.
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

# ------------------------------------------------------------------ firmware
# Full ESP-IDF build of the client firmware inside the official espressif/idf
# container. idf.py's component manager fetches managed deps (78/esp-opus)
# from the registry in-container. This is the first real compile of P2/P3.
# The whole client/build dir (bins, elf, bootloader, partition table) is saved
# as the `firmware` artifact target.
firmware:
    FROM espressif/idf:release-v5.5
    WORKDIR /src
    COPY client ./client
    # export.sh needs bash (BASH_SOURCE) to locate the IDF dir; Earthly's
    # default RUN shell is /bin/sh, so run bash explicitly.
    RUN bash -lc 'cd client && . $IDF_PATH/export.sh && idf.py set-target esp32s3 && idf.py build'
    SAVE ARTIFACT client/build AS LOCAL client/build

# ----------------------------------------------------------------------- all
# Build everything: host tests + full firmware.
all:
    BUILD +host-test
    BUILD +firmware