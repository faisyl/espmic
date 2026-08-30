# Host unit-test harness for the portable P1 modules.
# Plain gcc + make; NO cmake, NO ESP-IDF. The modules under test are provider-
# agnostic C99 and are compiled directly into each host test binary.

CC      ?= gcc
CFLAGS  := -std=c99 -Wall -Wextra -Werror -g
COMPONENTS := ../components

INC := \
	-I$(COMPONENTS)/rtp/include \
	-I$(COMPONENTS)/control_protocol/include \
	-I$(COMPONENTS)/audio_buffer/include \
	-I$(COMPONENTS)/state_machine/include \
	-I.

BIN := \
	test_rtp \
	test_control_frame \
	test_pcm_ring \
	test_encoded_queue \
	test_state_machine

.PHONY: all test clean
all: test

test_rtp: test_rtp.c $(COMPONENTS)/rtp/rtp_packet.c
	$(CC) $(CFLAGS) $(INC) $^ -o $@

test_control_frame: test_control_frame.c $(COMPONENTS)/control_protocol/control_frame.c
	$(CC) $(CFLAGS) $(INC) $^ -o $@

test_pcm_ring: test_pcm_ring.c $(COMPONENTS)/audio_buffer/pcm_ring.c
	$(CC) $(CFLAGS) $(INC) $^ -o $@

test_encoded_queue: test_encoded_queue.c $(COMPONENTS)/audio_buffer/encoded_queue.c
	$(CC) $(CFLAGS) $(INC) $^ -o $@

test_state_machine: test_state_machine.c $(COMPONENTS)/state_machine/device_state.c
	$(CC) $(CFLAGS) $(INC) $^ -o $@

# Build all binaries then run them; any non-zero exit aborts (set -e).
test: $(BIN)
	@echo "=== running host tests ==="
	@set -e; for t in $(BIN); do ./$$t; done
	@echo "=== all tests passed ==="

clean:
	rm -f $(BIN)
