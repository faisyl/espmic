# Acceptance Test Mapping — ESP32-S3 Network Audio Device

This maps each spec §17 acceptance test to the firmware that implements it, the
expected observable behaviour, and concrete steps to verify it on real hardware.

## Prerequisites (once)

- ESP-IDF v5.x installed and exported (`. $IDF_PATH/export.sh`), an ESP32-S3
  board with PSRAM, and two ICS43434 mics sharing BCLK/WS/SD (one strapped
  LEFT, one RIGHT).
- Board GPIOs, `device_id`, `server_host`, `server_port`, `control_tls_enabled`
  and `default_bitrate` provisioned into NVS (spec §16). Defaults come from
  `nvs_config_defaults()`; override via `set_config` or an NVS editor.
- A control server that speaks the §9/§10 length-prefixed JSON protocol and can
  send `start_stream` / `stop_stream` / `get_status`, plus a host running
  `ffmpeg`/`gstreamer`, `tcpdump`/`wireshark`, and the Wi-Fi AP under your
  control.

Build / flash / monitor:

```bash
cd client
idf.py set-target esp32s3
idf.py build
idf.py -p /dev/ttyUSB0 flash monitor
```

The device logs its state machine transitions (`state X --EV--> Y`) and, while
streaming, a 1 Hz health line from the `health` tag. The server receives real
counters in every `status` / `stream_stopped` message (see `send_status()` /
`add_stats_block()` in `components/control_task/control_task.c`).

---

## 1. Left/right microphone separation and channel order

- **Implements:** `components/i2s_capture/i2s_capture.c` (Philips stereo slots,
  slot0=LEFT/slot1=RIGHT, 24-bit-in-32-bit via `slot_to_sample()`); interleaving
  preserved through `pcm_ring` → `opus_encoder_task.c` (stereo encode) →
  `rtp_sender.c`.
- **Expected:** A signal applied only to the LEFT mic appears only in the left
  decoded channel, and likewise for RIGHT; channels are not swapped or summed.
- **Verify:**
  1. `start_stream` to your host (see test 4 to capture/decode).
  2. Tap/whistle at the LEFT mic only; decode and open in Audacity/`ffprobe`.
  3. Confirm energy is in the left channel; repeat for RIGHT. If swapped, swap
     the mic L/R strap or the slot mapping — it is a wiring/board config, not a
     protocol change.

## 2. 30 minutes at 48 kHz stereo with zero PCM overrun

- **Implements:** real-time capture task at the highest app priority
  (`i2s_capture.c`), 200 ms PCM ring (`audio_manager.c` `PCM_RING_MS`),
  drop-oldest overflow counted in `pcm_ring` (`overflow_count`). Overflow is
  surfaced as `pcm_overflow` in `status`.
- **Expected:** After 30 min continuous streaming, `pcm_overflow == 0` and no
  watchdog reset.
- **Verify:**
  1. `start_stream`; let it run 30 min on a healthy network.
  2. Poll `get_status` periodically; watch `statistics.pcm_overflow`.
  3. Pass = it stays `0` for the full run and `rtp_packets_sent` keeps rising at
     ~50/s. The 1 Hz `health` monitor log corroborates (`ovf=0`).

## 3. Start/stop at least 100 times without memory growth

- **Implements:** `audio_manager_start_stream()` / `audio_manager_stop_stream()`
  allocate all ring/queue storage and locks per stream and free them in
  `free_storage()`; tasks are created on start and joined+deleted on stop
  (`i2s_capture_stop` / `opus_task_stop` / `rtp_sender_stop`).
- **Expected:** Free heap / PSRAM return to the same baseline after each cycle;
  no monotonic downward drift over 100 cycles.
- **Verify:**
  1. Script the server to loop `start_stream` → wait ~2 s → `stop_stream`
     ≥100 times.
  2. After each `stop_stream`, read `statistics.free_heap` /
     `min_free_heap` / `free_psram` from the returned `stream_stopped` or a
     `get_status`.
  3. Pass = `free_heap` after cycle N ≈ after cycle 1 (small jitter only), and
     `min_free_heap` stops dropping after the first cycle.

## 4. Decode RTP/Opus with ffmpeg/GStreamer; verify 48 kHz stereo

- **Implements:** `components/rtp/rtp_packet.c` (RFC 3550 header) +
  `rtp_sender.c` (one Opus packet per UDP datagram, PT 111) +
  `opus_encoder_task.c` (48 kHz stereo, 20 ms, RFC 7587 payload).
- **Expected:** A standard tool decodes the stream as 48 kHz, 2 channels.
- **Verify (ffmpeg):**
  1. Create an SDP describing the stream, e.g. `stream.sdp`:
     ```
     v=0
     o=- 0 0 IN IP4 <listener-ip>
     s=esp32
     c=IN IP4 <listener-ip>
     t=0 0
     m=audio 5004 RTP/AVP 111
     a=rtpmap:111 opus/48000/2
     ```
  2. `start_stream` with `destination.ip=<listener-ip>`, `port=5004`,
     `payload_type=111`.
  3. `ffmpeg -protocol_whitelist file,udp,rtp -i stream.sdp out.wav` then
     `ffprobe out.wav` → expect `48000 Hz, stereo`.
  4. GStreamer alternative:
     `gst-launch-1.0 udpsrc port=5004 caps="application/x-rtp,media=audio,clock-rate=48000,encoding-name=OPUS,payload=111" ! rtpopusdepay ! opusdec ! audioconvert ! autoaudiosink`

## 5. RTP sequence increments by 1 and timestamp by 960 per 20 ms frame

- **Implements:** `components/rtp/rtp_packet.c` `rtp_serialize()` (seq += 1,
  timestamp += 960, stable SSRC, random initial seq/ts/SSRC per §8); driven once
  per encoded frame by `rtp_sender.c`.
- **Expected:** Consecutive packets differ by seq +1 and timestamp +960; SSRC
  constant for the stream lifetime.
- **Verify:**
  1. `tcpdump -i <if> udp port 5004 -w rtp.pcap` during a stream.
  2. Open in Wireshark → Decode As → RTP. Inspect the RTP sequence and timestamp
     columns, or Telephony → RTP → Stream Analysis.
  3. Pass = monotonic seq (+1, wrapping at 16-bit), timestamp deltas of exactly
     960, one constant SSRC. `status.rtp_ssrc` matches the SSRC on the wire.

## 6. Disconnect/reconnect Wi-Fi during streaming; recover without watchdog reset

- **Implements:** off-event-loop exponential reconnect (1→16 s) in
  `wifi_manager.c` (`schedule_reconnect()` on `esp_timer`); state machine
  overrides `SM_EV_WIFI_LOST → WIFI_CONNECTING` (app_main `on_disconnected`);
  control task backoff-reconnect and the active-stream grace stop
  (`CONTROL_LOSS_GRACE_MS`) in `control_task.c`. All critical loops feed the
  Task WDT (`esp_task_wdt_add/reset` in i2s/opus/rtp/control).
- **Expected:** On AP loss the device logs `WIFI_LOST`, keeps retrying with
  growing backoff, and on AP return re-associates, re-establishes control, and
  can stream again — with no `Task watchdog got triggered` / reboot.
- **Verify:**
  1. `start_stream`; confirm media flowing.
  2. Kill the AP (or `wifi_manager_reset` is NOT used here — just drop the link).
  3. Watch the monitor: expect `WIFI_LOST`, reconnect attempts at ~1,2,4,8,16 s,
     then `got ip`, `CONTROL_CONNECT`, `session established`, and no WDT panic.
  4. Re-issue `start_stream`; confirm recovery. Check `status.wifi_rssi` is
     populated again.

## 7. Send malformed/oversized control frames; verify safe rejection

- **Implements:** `components/control_protocol/control_frame.c`
  (`cp_decoder_push` returns `CP_ERR_OVERSIZE` above the 16 KiB cap);
  `control_task.c` drops the connection on oversize and ignores malformed JSON /
  typeless frames (`handle_message()` guards `cJSON_ParseWithLength` and
  `cp_message_type`).
- **Expected:** No crash, no overrun; the device rejects the frame (drops the
  connection on oversize, ignores malformed) and reconnects cleanly.
- **Verify:**
  1. From the server, send a length prefix > 16 KiB (e.g. `0x00040000`) or a
     truncated/garbage JSON body.
  2. Monitor shows `oversize control frame; dropping connection` or
     `malformed JSON`; the device does not reset and reconnects via backoff.
  3. Fuzz a few malformed `start_stream` bodies → device returns `error`
     (`invalid_request` / `invalid_config`) and stays `IDLE`.

## 8. Verify unauthenticated clients cannot start streams

- **Implements:** the session gate — `start_stream` is only honoured on the
  persistent control connection after the server's `hello_ack`
  (`control_task.c`, `g.connected`), and the RTP destination comes only from
  that authenticated `start_stream` (spec §15: no arbitrary destinations from
  unauthenticated clients). TLS + optional NVS CA/CN pinning
  (`load_pinning_from_nvs()`) protects the channel.
- **Expected:** A client that has not completed the authenticated control
  handshake cannot cause the device to emit RTP to an attacker-chosen address.
- **Verify:**
  1. From an unauthenticated TCP/TLS peer (no valid `hello_ack` flow on the
     device's server session), attempt to inject a `start_stream`.
  2. Confirm no RTP appears at the requested destination (`tcpdump` on that
     host stays silent); the device only streams for the server it completed
     `hello`/`hello_ack` with.
  3. For hardened mode, provision an NVS `server_ca` (and optional `server_cn`)
     and confirm the device refuses a server presenting an untrusted cert
     (TLS handshake fails, logged `TLS connect ... failed`).

## 9. Measure CPU, heap and PSRAM watermarks during sustained streaming

- **Implements:** `health_monitor.c` 1 Hz snapshot of `free_heap`,
  `min_free_heap`, `free_psram`, `wifi_rssi`, surfaced in `status.statistics`.
  CPU is read via IDF's runtime stats.
- **Expected:** Stable watermarks (no leak), CPU headroom on both cores at
  128 kbps stereo.
- **Verify:**
  1. During a sustained stream, poll `get_status` and record `free_heap` /
     `min_free_heap` / `free_psram` over time — expect flat lines.
  2. For CPU, enable `CONFIG_FREERTOS_USE_TRACE_FACILITY` +
     `CONFIG_FREERTOS_GENERATE_RUN_TIME_STATS` and call `vTaskGetRunTimeStats`,
     or watch `uxTaskGetStackHighWaterMark` per task from a debug build.
  3. Pass = watermarks stable for the run; `min_free_heap` plateaus.

## 10. Verify all stream resources are released after stop/error/reconnect

- **Implements:** `audio_manager_stop_stream()` stops capture→encode→send in
  order and calls `free_storage()` (frees ring/queue arenas and both mutexes);
  each task's `*_stop()` disables/deletes its I2S channel / Opus encoder /
  socket. Control-loss grace stop path in `control_task.c`.
- **Expected:** After `stop_stream`, an error, or a control-loss grace stop,
  `is_streaming()` is false, the socket/I2S/encoder are gone, and heap/PSRAM
  return to baseline (ties to test 3).
- **Verify:**
  1. `start_stream` → `stop_stream`; confirm `stream_stopped` with final stats
     and `state=IDLE`.
  2. Repeat but instead drop the control connection mid-stream; confirm the
     device stops the stream after `CONTROL_LOSS_GRACE_MS` and frees resources.
  3. Compare `free_heap`/`free_psram` before first start and after release —
     equal within jitter. No dangling `rtp task` / `i2s_cap` / `opus` tasks in
     `idf.py monitor` task dumps.
