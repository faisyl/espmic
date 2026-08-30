# ESP32 Network Audio Device — Implementation Specification

**Target:** ESP32-S3 + dual ICS43434 + 24-bit capture + Opus/RTP + server control

## 1. Purpose

Define an implementation-ready firmware architecture for an ESP32-S3 device containing two ICS43434 digital microphones configured as left/right channels.

The device:

- captures stereo 24-bit PCM at 48 kHz;
- encodes audio as Opus;
- packetizes each Opus packet as standards-compliant RTP;
- streams RTP/Opus to a server over UDP;
- maintains a separate persistent control connection to the server;
- supports Wi-Fi onboarding and reconfiguration;
- never blocks microphone capture on network I/O.

The device does **not** implement RTSP. RTP/Opus is the media interface. RTSP, WebRTC, browser playback, recording APIs, etc. belong on the server or consuming client.

## 2. Requirements

- Target platform: ESP32-S3, preferably a module/board with PSRAM.
- Two ICS43434 microphones share BCLK, WS/LRCLK and SD.
- One microphone is configured LEFT and the other RIGHT.
- Capture stereo PCM at 48 kHz.
- Preserve microphone 24-bit samples in 32-bit DMA containers.
- Encode stereo audio using Opus.
- Initial Opus configuration:
  - 48 kHz
  - stereo
  - 20 ms frames
  - 96–128 kbps target
  - VBR enabled
  - general audio application
- Packetize each Opus packet into one RTP packet.
- Use RFC 3550 RTP and RFC 7587 Opus RTP payload semantics.
- Maintain a persistent TLS/TCP control connection.
- Automatically reconnect Wi-Fi and the control connection.
- Expose diagnostics and stream statistics.
- Do not let network stalls cause an unbounded I²S backlog.

## 3. Architecture

```text
                    +-----------------------+
                    |      Wi-Fi Manager    |
                    +-----------+-----------+
                                |
                    +-----------v-----------+
                    |    Control Task       |
                    | TLS/TCP + JSON frames |
                    +-----------+-----------+
                                |
                       commands/status
                                |
              +-----------------v-----------------+
              |            Audio Manager          |
              +-----------------+-----------------+
                                |
                    +-----------v-----------+
                    |      I2S/DMA Task      |
                    |  24-bit L/R @ 48 kHz   |
                    +-----------+-----------+
                                |
                         PCM ring buffer
                                |
                    +-----------v-----------+
                    |      Opus Task         |
                    | 960 stereo samples     |
                    +-----------+-----------+
                                |
                         RTP packet buffer
                                |
                    +-----------v-----------+
                    |       RTP Task         |
                    | UDP send + statistics  |
                    +------------------------+
```

## 4. FreeRTOS task responsibilities

| Component | Responsibility | Must not do |
|---|---|---|
| `wifi_manager` | Provisioning, station connection/reconnection, IP state | Audio encoding |
| `control_task` | TLS connection, framing, commands, acknowledgements, heartbeat | Block capture |
| `i2s_capture_task` | Read DMA and push samples to PCM ring | Network operations |
| `opus_task` | Consume Opus frames and encode | Wait indefinitely for network |
| `rtp_task` | Build RTP headers, send UDP, update stats | Read I²S directly |
| `audio_manager` | Stream lifecycle and configuration | Long blocking operations |
| `health/watchdog` | Detect stalled tasks and report health | Restart healthy tasks |

## 5. I²S and microphone interface

Use the ESP32-S3 I²S peripheral in standard I²S/Philips mode.

Both microphones share:

- BCLK
- WS/LRCLK
- SD

The L/R configuration selects which microphone drives the left or right slot.

Logical frame:

```text
slot 0 = LEFT
slot 1 = RIGHT
```

DMA representation:

```c
int32_t left;
int32_t right;
```

Do not pack samples into 3-byte values in the real-time capture path. Keep 32-bit containers for DMA/alignment and convert at the encoder boundary.

Initial configuration:

| Parameter | Value |
|---|---|
| Sample rate | 48000 Hz |
| Channels | 2 |
| Microphone resolution | 24 bits |
| DMA storage | 32-bit signed containers |
| Opus frame | 20 ms |
| Samples/channel/frame | 960 |
| Frames/sec | 50 |

GPIO assignments are board-specific configuration, not protocol assumptions.

## 6. Audio buffering

Pipeline:

```text
I2S DMA
  -> PCM ring
  -> Opus
  -> encoded queue
  -> RTP/UDP
```

Recommended starting sizes:

- PCM ring: 100–250 ms
- encoded queue: 20–100 RTP packets
- expose high-water/low-water statistics
- count every PCM overflow

The capture task must remain real-time. If the network stalls long enough to exhaust buffers, the implementation should drop audio rather than deadlock the capture pipeline. Report the resulting discontinuity/overrun.

## 7. Opus configuration

Initial configuration:

| Setting | Initial value |
|---|---|
| Input rate | 48000 Hz |
| Channels | 2 |
| Application | `OPUS_APPLICATION_AUDIO` |
| Frame duration | 20 ms |
| Bitrate | 128000 bps |
| VBR | Enabled |
| FEC | Disabled initially |
| DTX | Disabled |
| Complexity | Benchmark 5–8 |
| Signal | General audio / auto |

Benchmark 96, 128, 160 and 192 kbps and choose based on audio quality and CPU/network requirements.

The capture path preserves the 24-bit microphone samples until the codec boundary. Opus is lossy and must not be described as preserving the original 24-bit PCM bit-for-bit.

## 8. RTP media contract

RTP is the only required media protocol between device and server.

The payload is exactly one Opus packet.

| RTP field | Device behavior |
|---|---|
| Version | 2 |
| Payload type | Dynamic; default 111 |
| Sequence | Random initial 16-bit value; increment by 1 |
| Timestamp | Random initial 32-bit value; increment by 960 |
| SSRC | Random per stream; stable for stream lifetime |
| Marker | 0 normally; may be 1 on first packet |
| Payload | Raw Opus packet |
| Transport | UDP unicast |

Example:

```text
RTP header:
  PT = 111
  sequence = N
  timestamp = T
  SSRC = S

RTP payload:
  [Opus packet bytes]

Next:
  sequence = N + 1
  timestamp = T + 960
  SSRC = S
```

RTP timestamp rate is 48 kHz. A 20 ms packet advances the timestamp by 960.

Do not add a private application header to the RTP payload.

## 9. Server control protocol

Control uses persistent TLS/TCP.

The protocol is a length-prefixed JSON message protocol.

```text
uint32_be payload_length
uint8[payload_length] UTF-8 JSON
```

Maximum accepted JSON payload: 16 KiB.

Example `hello`:

```json
{
  "type": "hello",
  "protocol": 1,
  "device_id": "esp32-001",
  "firmware": "1.0.0",
  "capabilities": {
    "sample_rates": [48000],
    "channels": 2,
    "codecs": ["opus"],
    "psram": true
  }
}
```

Server:

```json
{
  "type": "hello_ack",
  "session_id": "uuid",
  "server_time_ms": 1760000000000
}
```

## 10. Control messages

| Message | Direction | Purpose |
|---|---|---|
| `hello` | device → server | Introduce device and capabilities |
| `hello_ack` | server → device | Establish authenticated session |
| `ping` | server → device | Heartbeat |
| `pong` | device → server | Heartbeat response |
| `start_stream` | server → device | Start RTP stream |
| `stream_started` | device → server | Confirm stream |
| `stop_stream` | server → device | Stop stream |
| `stream_stopped` | device → server | Confirm stop + statistics |
| `get_status` | server → device | Request status |
| `status` | device → server | Return state/statistics |
| `set_config` | server → device | Change supported persistent configuration |
| `error` | device → server | Report error |

## 11. Start-stream command

```json
{
  "type": "start_stream",
  "request_id": "req-42",
  "stream_id": "recording-abc",
  "destination": {
    "ip": "192.168.1.20",
    "port": 5004
  },
  "codec": {
    "name": "opus",
    "sample_rate": 48000,
    "channels": 2,
    "frame_ms": 20,
    "bitrate": 128000,
    "vbr": true,
    "fec": false,
    "dtx": false
  },
  "rtp": {
    "payload_type": 111
  }
}
```

The device must validate the complete configuration before starting. If validation fails, return an error and remain in `IDLE`.

## 12. Device state machine

```text
BOOT
  -> PROVISIONING
  -> WIFI_CONNECTING
  -> WIFI_CONNECTED
  -> CONTROL_CONNECTING
  -> IDLE
  -> STREAM_STARTING
  -> STREAMING
  -> IDLE

Any state:
  Wi-Fi loss -> WIFI_CONNECTING
  control loss -> CONTROL_CONNECTING
  fatal internal error -> ERROR -> recovery/reboot
```

`PROVISIONING` is only entered when credentials are absent or an explicit reset is requested.

## 13. Wi-Fi onboarding

Use ESP-IDF provisioning facilities.

Requirements:

- persist Wi-Fi credentials in NVS;
- attempt saved credentials on boot;
- provide a physical/button or software mechanism to erase credentials;
- enter provisioning mode after credential reset;
- stop provisioning service after successful setup;
- reconnect as a normal station.

Provisioning must not expose the server authentication secret.

## 14. Failure handling

- Wi-Fi loss: reconnect automatically.
- Control connection loss: reconnect automatically.
- Active stream + control loss: stop the stream after a short grace period unless autonomous streaming is explicitly added later.
- RTP destination failure: bounded retry behavior; never block I²S indefinitely.
- PCM overflow: increment counter and continue from newest available audio.
- Opus deadline misses: increment encoder-late/underrun counter.
- No task may wait indefinitely on a queue or socket.
- Watchdog must remain enabled.

## 15. Security

Production requirements:

- TLS for control.
- Unique device identity.
- Device authentication before commands are accepted.
- Validate all message lengths and fields.
- Do not accept arbitrary RTP destinations from unauthenticated control clients.
- Plain RTP is acceptable for controlled LAN deployments.
- Use SRTP or a secure tunnel for hostile/untrusted networks.

## 16. Persistent NVS configuration

| Key | Example |
|---|---|
| `device_id` | `esp32-001` |
| Wi-Fi credentials | managed by provisioning |
| `server_host` | `audio.example.local` |
| `server_port` | `4433` |
| `control_tls_enabled` | `true` |
| `default_bitrate` | `128000` |
| I²S GPIO configuration | board-specific |

## 17. Acceptance tests

1. Verify left/right microphone separation and correct channel order.
2. Run 30 minutes at 48 kHz stereo with zero PCM overrun.
3. Start/stop at least 100 times without memory growth.
4. Decode RTP/Opus using ffmpeg/GStreamer and verify 48 kHz stereo playback.
5. Verify RTP sequence increments by one and timestamp increments by 960 for each 20 ms frame.
6. Disconnect/reconnect Wi-Fi during streaming and verify recovery without watchdog reset.
7. Send malformed/oversized control frames and verify safe rejection.
8. Verify unauthenticated clients cannot start streams.
9. Measure CPU, heap and PSRAM watermarks during sustained streaming.
10. Verify all stream resources are released after stop/error/reconnect.

## 18. References

- RTP: RFC 3550
- Opus codec: RFC 6716
- Opus over RTP: RFC 7587
- ESP-IDF documentation and the selected ESP32-S3 Opus implementation
