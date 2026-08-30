# Audio Server Component — Implementation Specification

**Target:** Device control + RTP/Opus ingest + decoding + recording + live distribution

## 1. Purpose

Define the server independently from the ESP32 firmware.

The server:

- manages devices over persistent control connections;
- authenticates devices;
- commands audio streams;
- allocates RTP destinations;
- receives standard RTP/Opus;
- handles loss/reordering/jitter;
- decodes Opus;
- records decoded audio;
- distributes decoded audio to live consumers;
- exposes HTTP APIs for device and stream management.

The server must not depend on private ESP32 audio packet formats.

## 2. Architectural principles

1. RTP/Opus is the media contract.
2. Control and media are separate connections.
3. Device management, RTP ingest, decoding, recording and live distribution are separate modules.
4. Server owns authoritative stream state.
5. RTP receiver tolerates loss, duplication and reordering.
6. Preserve RTP sequence numbers/timestamps/SSRC for diagnostics.
7. Internal PCM representation is independent of RTP.
8. Start with one deployable process if desired, but keep module interfaces clean.

## 3. Reference architecture

```text
                         +----------------------+
                         |       API / UI       |
                         +----------+-----------+
                                    |
                         +----------v-----------+
                         | Device Manager       |
                         | DB + command router  |
                         +----------+-----------+
                                    |
                               TLS control
                                    |
                  +-----------------v-----------------+
                  |              Device              |
                  +----------------------------------+
                                    |
                               RTP / UDP
                                    |
                         +----------v-----------+
                         | RTP Ingest           |
                         | parse + jitter       |
                         +----------+-----------+
                                    |
                              Opus payload
                                    |
                         +----------v-----------+
                         | Opus Decoder         |
                         +----------+-----------+
                                    |
                         +----------v-----------+
                         | PCM Distribution     |
                         +-----+------------+---+
                               |            |
                               v            v
                         Recorder       Live outputs
                               |
                               v
                         FLAC / WAV / object store
```

## 4. Recommended implementation stack

A practical first implementation is:

- Python
- FastAPI / asyncio
- TLS TCP control sessions
- UDP RTP receiver
- FFmpeg/libopus or a native Opus binding for decoding
- PostgreSQL or SQLite for metadata
- filesystem/object storage for recordings

Go or Rust are also suitable. The protocol is language-neutral.

## 5. Server modules

| Module | Responsibilities |
|---|---|
| `DeviceRegistry` | Device identity, credentials, metadata, online state |
| `ControlSession` | TLS connection, framing, command/response routing |
| `CommandService` | Validate stream requests and send device commands |
| `StreamRegistry` | Active stream state and destinations |
| `RtpReceiver` | UDP bind, RTP parsing, validation, statistics |
| `JitterBuffer` | Reorder packets and provide bounded playout delay |
| `OpusDecoder` | Decode Opus to stereo PCM |
| `PcmPipeline` | Fan PCM to recorder and live subscribers |
| `Recorder` | WAV/FLAC/archive output |
| `LiveOutput` | WebSocket/WebRTC/transcoder integration |
| `Metrics` | Packet loss, jitter, bitrate, decode and system metrics |

## 6. Device identity

Persist:

### Device

```text
device_id
display_name
credential reference/hash
firmware
capabilities
last_seen
status
```

### ControlSession

```text
session_id
device_id
connected_at
last_heartbeat
remote_ip
```

### Stream

```text
stream_id
device_id
SSRC
destination
codec config
started_at
state
```

### Recording

```text
recording_id
stream_id
start_time
end_time
file_uri
sample_rate
channels
codec
```

## 7. Control connection

Listen on a configurable TCP/TLS port.

Protocol framing:

```text
uint32_be payload_length
payload_length bytes UTF-8 JSON
```

Maximum accepted payload: 16 KiB.

Server flow:

1. Accept TCP/TLS.
2. Read `hello`.
3. Authenticate device.
4. Create `session_id`.
5. Send `hello_ack`.
6. Maintain heartbeat.
7. Route commands.
8. Receive status/events.
9. On disconnect, mark device offline and resolve active-stream policy.

Reject a frame with a payload length over 16 KiB before allocating the payload.

## 8. Control messages

| Message | Direction | Meaning |
|---|---|---|
| `hello` | device → server | Introduce device |
| `hello_ack` | server → device | Authenticate/session establishment |
| `ping` | server → device | Heartbeat |
| `pong` | device → server | Heartbeat response |
| `start_stream` | server → device | Request RTP stream |
| `stream_started` | device → server | Confirm stream |
| `stop_stream` | server → device | Stop RTP |
| `stream_stopped` | device → server | Confirm + stats |
| `get_status` | server → device | Status request |
| `status` | device → server | Status response |
| `error` | device → server | Runtime/command error |

## 9. Starting a server-managed RTP stream

The server allocates and binds a UDP port before instructing the device.

```text
Server:
  allocate UDP port P
  bind RtpReceiver(P)
  create Stream(stream_id)
  send start_stream

Device:
  validate configuration
  start I2S
  start Opus
  start RTP
  send stream_started
  send RTP -> server_ip:P

Server:
  receive RTP
  validate SSRC/PT
  jitter-buffer
  decode
  publish PCM
  record if requested
```

The initial implementation should use one UDP port per active stream.

## 10. RTP/Opus ingest contract

| Property | Expected |
|---|---|
| RTP version | 2 |
| Payload type | Dynamic; default 111 |
| Codec | Opus |
| RTP clock | 48000 Hz |
| Channels | 2 |
| Typical packet duration | 20 ms |
| Typical timestamp increment | 960 |
| Transport | UDP |
| Payload | Exactly one Opus packet |

The receiver must not assume every packet has exactly 20 ms of audio. Parse RTP correctly and allow for Opus packet duration variations.

RTP sequence number detects loss/reordering.

RTP timestamp identifies media time.

SSRC identifies the synchronization source.

## 11. Jitter and packet loss

Start with a configurable jitter buffer, initially around 60 ms target playout delay.

Example:

```text
Received:
  seq 100, timestamp T
  seq 102, timestamp T+1920
  seq 101, timestamp T+960

Output:
  100
  101
  102
```

If a missing packet exceeds the playout deadline:

```text
declare loss
invoke Opus packet-loss concealment
continue playback
```

Maintain separate counters for:

- lost packets
- duplicates
- reordered packets
- late packets
- jitter
- decode errors

Do not add retransmission initially.

Future enhancement: Opus in-band FEC with larger jitter buffer.

## 12. Decoder/PCM contract

Use an internal decoded-audio representation independent of RTP:

```text
DecodedAudioFrame:
    stream_id
    timestamp_48k
    sample_count_per_channel
    sample_rate = 48000
    channels = 2
    pcm = interleaved stereo samples
    discontinuity = bool
    source_rtp_sequence_start
    source_rtp_sequence_end
```

The exact internal numeric representation may be 16-bit, 24-bit or float depending on the decoder, but all downstream modules must use an explicit contract.

## 13. Recording

Recommended formats:

| Format | Purpose |
|---|---|
| WAV PCM | Debugging and short recordings |
| FLAC | Preferred lossless archive |
| Original RTP/Opus | Optional exact network/session archive |

Important:

> Opus decoding cannot recreate the original ICS43434 24-bit samples exactly.

If exact lossless archival of microphone samples is required, the device must have a separate lossless capture/transport mode in addition to RTP/Opus.

## 14. Live audio distribution

Keep RTP ingest independent from client delivery:

```text
RTP
  -> jitter buffer
  -> Opus decoder
  -> PCM bus
       |
       +-> Recorder
       +-> WebSocket audio
       +-> WebRTC gateway
       +-> monitoring
       +-> analysis
       +-> transcoder
```

Do not couple browser playback directly to RTP ingest.

## 15. HTTP API

Suggested initial API:

| Endpoint | Purpose |
|---|---|
| `GET /api/devices` | List devices |
| `GET /api/devices/{id}` | Device metadata/status |
| `POST /api/devices/{id}/stream` | Start managed stream |
| `DELETE /api/streams/{id}` | Stop stream |
| `GET /api/streams/{id}` | Stream state |
| `GET /api/streams/{id}/stats` | RTP/decoder statistics |
| `GET /api/recordings/{id}` | Recording metadata |
| `GET /api/recordings/{id}/download` | Retrieve recording |

## 16. Start-stream API

Request:

```http
POST /api/devices/esp32-001/stream
Content-Type: application/json
```

```json
{
  "purpose": "record",
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
  "recording": {
    "enabled": true,
    "format": "flac"
  }
}
```

Response:

```json
{
  "stream_id": "uuid",
  "state": "starting"
}
```

The server chooses the RTP destination internally; the caller should not normally provide an arbitrary UDP destination.

## 17. Stream lifecycle

```text
CREATED
  -> WAITING_FOR_DEVICE
  -> STARTING
  -> RTP_WAIT
  -> ACTIVE
  -> STOPPING
  -> COMPLETE
```

Failure paths:

```text
STARTING -> FAILED
RTP_WAIT -> TIMEOUT
ACTIVE -> DEVICE_DISCONNECTED
ACTIVE -> RTP_TIMEOUT
ACTIVE -> DECODE_ERROR
```

Initial timing:

- RTP wait timeout: 5 seconds after `stream_started`
- RTP disappearance timeout: approximately 1 second without packets
- all values configurable

The implementation should distinguish temporary packet loss from total stream disappearance.

## 18. Statistics

Track at least:

| Metric | Meaning |
|---|---|
| `rtp_packets_received` | Total packets |
| `rtp_packets_lost` | Sequence gaps |
| `rtp_packets_duplicate` | Duplicate packets |
| `rtp_packets_reordered` | Reordered packets |
| `rtp_packets_late` | Late packets |
| `rtp_jitter_ms` | Estimated jitter |
| `rtp_bitrate_bps` | Measured bitrate |
| `opus_decode_errors` | Decoder failures |
| `pcm_frames_decoded` | Decoded frames |
| `stream_discontinuities` | Audio discontinuities |
| `recording_bytes` | Bytes stored |
| `control_reconnects` | Control reconnects |

## 19. Security

Production requirements:

- TLS control connections.
- Authenticate device before accepting commands.
- Associate RTP packets with an active stream and expected SSRC/payload type.
- Ignore unsolicited RTP packets.
- Rate-limit malformed RTP/control traffic.
- Plain RTP is suitable for controlled LAN/private-network deployments.
- Use SRTP or a secure tunnel across untrusted networks.

## 20. Restart behavior

Persist:

- registered devices;
- recording metadata;
- stream/recording metadata.

Do not treat persisted RTP sequence numbers as authoritative after a server restart.

After restart:

1. mark previous control sessions offline;
2. mark stale streams failed;
3. wait for devices to reconnect;
4. optionally implement automatic stream re-establishment later.

## 21. Interoperability tests

1. Consume a captured RTP/Opus stream using ffmpeg/ffplay or GStreamer.
2. Feed an independent standards-compliant RTP/Opus source into the server and verify that no ESP32-specific code is required.
3. Inject reordered and duplicate packets.
4. Drop 1%, 5% and 10% of packets and verify concealment/statistics.
5. Start/stop at least 100 streams without leaking sockets/tasks.
6. Disconnect the device control connection during RTP streaming and verify cleanup.
7. Run two devices simultaneously and verify independent SSRC/session state.
8. Verify malformed RTP cannot crash or exhaust the receiver.

## 22. Suggested repository structure

```text
server/
  app/
    api/
      devices.py
      streams.py
      recordings.py
    control/
      protocol.py
      session.py
      commands.py
    rtp/
      packet.py
      receiver.py
      jitter.py
      opus.py
    audio/
      frame.py
      bus.py
      recorder.py
      live.py
    devices/
      registry.py
      models.py
    persistence/
      db.py
      repositories.py
    metrics/
      metrics.py
    config.py
    main.py
  tests/
    test_control_protocol.py
    test_rtp.py
    test_jitter.py
    test_opus.py
    test_stream_lifecycle.py
```

## 23. References

- RTP: RFC 3550
- Opus codec: RFC 6716
- Opus over RTP: RFC 7587
