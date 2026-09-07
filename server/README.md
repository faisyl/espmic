# ESPMIC Audio Server (Go)

Server-side component for the ESPMIC audio system — the counterpart to the
ESP32 firmware in `../client`. It manages devices over persistent control
connections, ingests RTP/Opus, decodes to PCM, records and distributes live
audio. See `Audio_Server_Component_Specification.md` (this directory) for the
full spec.

Spec §4 permits Go; this implementation is pure Go (no cgo) for a clean
static binary.

## Status

S3 (FINAL): server is wired end-to-end and runnable. All §15 HTTP endpoints
implemented, WebSocket live output, metrics endpoint, WAV + FLAC recording,
and a self-contained interop harness covering §21.

## Layout

Go standard layout, mapping spec §5 modules onto `internal/` packages:

```
server/
  cmd/server/            main: full wiring + graceful shutdown
  internal/
    config/              settings with env overrides       (spec §4, §11, §17)
    control/             control framing/session/commands  (spec §7-§9)
    rtp/                 packet/receiver/jitter            (spec §10-§11)
    audio/               frame/bus/recorder/live           (spec §12-§14)
    device/              registry/models                   (spec §5-§6)
    stream/              registry/lifecycle                (spec §17)
    persistence/         db/repositories                   (spec §20)
    metrics/             statistics                        (spec §18)
    api/                 HTTP management surface           (spec §15-§16)
    server/              end-to-end wiring                 (spec §3)
```

## Build & Setup

### Prerequisites

- **Go 1.26+** (pure Go implementation, `CGO_ENABLED=0`, no C library or compiler dependencies required).

### Building & Testing

```sh
go build ./...
go vet ./...
go test -race ./...
```

### Configuration Environment Variables

Configuration is loaded from environment variables with sensible local defaults (source of truth: `internal/config/config.go`):

| Environment Variable | Default | Type | Description |
|---|---|---|---|
| `ESPMIC_HTTP_ADDR` | `:8080` | string | HTTP management API listen address |
| `ESPMIC_CONTROL_ADDR` | `:9000` | string | Control channel TCP/TLS listen address for client devices |
| `ESPMIC_TLS_CERT` | `""` | string | Path to PEM certificate for TLS control plane (empty = plain TCP) |
| `ESPMIC_TLS_KEY` | `""` | string | Path to PEM private key for TLS control plane (empty = plain TCP) |
| `ESPMIC_DEVICE_CREDENTIAL` | `""` | string | Shared secret for device enrollment. Empty = open enrollment (LAN default, TOFU). Non-empty = device must present this credential in hello message (constant-time compare). |
| `ESPMIC_LOG_LEVEL` | `info` | string | Log verbosity: `info` (default) or `debug`. `debug` enables verbose control-plane tracing (accept, hello, auth, disconnect). Case-insensitive. |
| `ESPMIC_JITTER_TARGET_MS` | `60` | int | Target playout delay for jitter buffer in milliseconds |
| `ESPMIC_RTP_WAIT_TIMEOUT_S` | `5` | int | Timeout in seconds waiting for RTP packets post stream start |
| `ESPMIC_DB_PATH` | `espmic.db` | string | Path to SQLite database file |
| `ESPMIC_RTP_BIND_PORT` | `0` | int | UDP port to bind for RTP ingest. `0` = dynamic port per stream (default). Set to a fixed port (e.g. `5004`) when running in Docker or behind NAT. |
| `ESPMIC_ADVERTISE_HOST` | `""` | string | Explicit host/IP advertised to devices for RTP destination. Empty = derive from control connection session IP (default). Set to Docker host LAN IP when containerized. |
| `ESPMIC_ADVERTISE_RTP_PORT` | `0` | int | Explicit RTP destination port advertised to devices. `0` = advertise the actually-bound port (default). Set when external host port differs from container bind port. |

### Listening Ports & Protocols

- **Port `8080` (TCP - HTTP):** Serves the management REST API (`/health`, `/api/devices`, `/api/streams`, `/api/metrics`) and WebSocket endpoints for live audio monitoring.
- **Port `9000` (TCP / TLS):** Persistent control connection listener for ESP32 clients. If `ESPMIC_TLS_CERT` and `ESPMIC_TLS_KEY` environment variables are configured, TLS encryption is enabled; otherwise, it operates over plain TCP.
- **Port `5004` (UDP - RTP Ingest):** RTP audio ingest receiver (when `ESPMIC_RTP_BIND_PORT` is configured; binds dynamically to `:0` when `0`).
- **Port `5004` (UDP - RTP Ingest):** RTP audio ingest receiver (when `ESPMIC_RTP_BIND_PORT` is configured; binds dynamically to `:0` when `0`).

## Running the Server

### Option 1: Direct Execution (Local Binary)

```sh
# Build static server binary
go build -o espmic-server ./cmd/server

# Run binary with default settings
./espmic-server

# Override settings via environment variables
ESPMIC_HTTP_ADDR=:8080 ESPMIC_CONTROL_ADDR=:9000 ./espmic-server
```

### Option 2: Development Run

```sh
go run ./cmd/server
# defaults: HTTP :8080, control :9000; override via env vars
curl localhost:8080/health          # -> {"status":"ok","version":"dev","commit":"none","date":"unknown"}
curl localhost:8080/api/metrics     # -> {statistics snapshot}
curl localhost:8080/api/devices     # -> [device list]
```

### Option 3: Docker Compose

```sh
# Run containerized server stack from the server/ directory
docker compose up --build
```

### Server Health Verification

Verify server operation by querying the health endpoint:

```sh
curl http://localhost:8080/health
# Response: {"status":"ok","version":"dev"}
```

Sends SIGTERM or SIGINT (`Ctrl+C`) for graceful shutdown.

## Version Stamping

Build metadata (`version`, `commit`, `date`) is stamped at build-time using Go linker flags (`-X main.version=... -X main.commit=... -X main.date=...`).
At startup, `espmic-server` logs these build variables, and the `GET /health` endpoint surfaces the version string:

```json
{
  "status": "ok",
  "version": "v0.4.6"
}
```

## Version stamping (build identity)

The binary exposes its build identity via `GET /health`:

```json
{
  "status": "ok",
  "version": "v0.4.6",
  "commit": "a1b2c3d",
  "date": "2026-09-05T12:34:56Z"
}
```

These come from three package-level variables in `cmd/server/main.go`:
`version`, `commit`, `date` — set at link time via `-X` ldflags (matching
GoReleaser's `.goreleaser.yaml`):

```
-X main.version=... -X main.commit=... -X main.date=...
```

### Local binary build (stamped)

```sh
make build
# or manually:
CGO_ENABLED=0 go build -trimpath \
  -ldflags="-X main.version=dev -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o espmic-server ./cmd/server
```

### Docker compose build (stamped)

The `Dockerfile` accepts `BUILD_VERSION`, `GIT_COMMIT`, `BUILD_DATE` build args
and passes them via `-ldflags`. The `docker-compose.yml` forwards them from the
environment.

```sh
# One-liner (stamps current git commit + UTC timestamp):
make docker-build-dev

# Or explicitly:
make docker-build GIT_COMMIT=$(git rev-parse --short HEAD) BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# Then run:
docker compose up
curl localhost:8080/health   # -> {"status":"ok","version":"dev","commit":"a1b2c3d","date":"2026-09-05T12:34:56Z"}
```

The published `ghcr.io/faisyl/espmic-server` images are stamped by GoReleaser at
release time (see `.goreleaser.yaml`).

## HTTP API Reference (spec §15, §16, §18)

Base URL: `http://<server>:8080`. All responses are JSON.

### GET /health

Health check + build identity.

**Response 200:**
```json
{"status":"ok","version":"dev","commit":"none","date":"unknown"}
```

---

### Devices

**GET /api/devices** — List all enrolled devices.
Response: array of device objects (id, state, last_seen).

**GET /api/devices/{id}** — Device metadata by id.
Path param: `id` — device ID.
**Response 200:** device object. **404:** device not found.

**GET /api/devices/{id}/status** — Live device status (spec §10 get_status).
Path param: `id` — device ID. Sends `get_status` over the live control session and returns the device's reply.
**Response 200:** `*control.Status` (see below). **404:** device not connected. **504:** device timeout.

**POST /api/devices/{id}/config** — Push runtime config to a connected device (set_config).
Path param: `id` — device ID.

**Request body** (JSON, at least one field required):
```json
{
  "default_bitrate": 64000,
  "server_host": "audio.local",
  "i2s_bclk": 5,
  "i2s_ws": 6,
  "i2s_din": 4,
  "fec": true,
  "dtx": false,
  "complexity": 8,
  "bitrate": 64000,
  "vbr": true,
  "frame_ms": 20
}
```
Field constraints (server validates): I2S pins 0–47, complexity 0–10, frame_ms ∈ {10,20,40,60}, bitrate ≥ 0. Unknown fields silently ignored (Go default).

**Response 200:** device echoed `*control.Status` (new state after apply). **400:** validation error. **404:** device not connected. **502:** device rejected config. **504:** device timeout.

*Status response shape (from `control.Status`):*
```json
{"type":"status","request_id":"...","status":"ok","state":"IDLE","fields":{"i2s_bclk":5}}
```

---

### Streams

**POST /api/devices/{id}/stream** — Start a managed stream (spec §16).
Path param: `id` — device ID.

**Request body:**
```json
{"purpose":"listen","recording":{"enabled":true,"format":"wav"}}
```

**Response 200:** stream object with `stream_id`, `device_id`, `state`, `port`, `started_at`. **404:** device not connected. **408/504:** device timeout. **409:** illegal transition.

**GET /api/streams** — List all active streams.
Response: array of stream objects.

**GET /api/streams/{id}** — Stream state by id.
Response 200:
```json
{"stream_id":"...","device_id":"...","state":"STREAMING","port":60000,"started_at":"2026-09-06T12:00:00Z"}
```
Omits SSRC per spec (device-learned, 0/unknown on stream object).

**GET /api/streams/{id}/stats** — RTP + decoder statistics for a stream.
Response 200 — three top-level groups for backward-compat + namespaced objects:
```json
{
  "stream_id": "...",
  "packets_received": 1200,
  "packets_lost": 3,
  "packets_duplicate": 1,
  "packets_reordered": 2,
  "packets_late": 5,
  "jitter_ms": 1.2,
  "rtp": {
    "packets_received": 1200,
    "packets_lost": 3,
    "packets_duplicate": 1,
    "packets_reordered": 2,
    "packets_late": 5,
    "jitter_ms": 1.2
  },
  "device_final": {
    "packets_sent": 1198,
    "bytes_sent": 1480000,
    "duration_ms": 12000,
    "encoder_errors": 0
  }
}
```
- Top-level `packets_*`/`jitter_ms` — backward-compat (same keys as legacy `rtp`).
- `rtp` — namespaced RTP counters (`rtp.Received/Lost/Duplicate/Reordered/Late/JitterMS`).
- `device_final` — present only while the stream is active; omitted after stream stop (GAP-04/19).

**DELETE /api/streams/{id}** — Stop a stream.
Response 200: `{"stream_id":"...","state":"stopped"}`. **404:** stream not found. **409:** illegal transition. **504:** device timeout.

---

### Recordings

**GET /api/recordings/{id}** — Recording metadata by id.
Response 200: recording object. **404:** not found.

**GET /api/recordings/{id}/download** — Download a recording file.
Response: file binary (WAV). **404:** not found.

---

### Metrics

**GET /api/metrics** — Server-wide statistics snapshot (spec §18).
Response 200: metrics object (counters/gauges).

## Dependencies (pinned in go.mod)

Pure-Go, no cgo:

- `github.com/pion/rtp` – RTP parse (spec §10)
- `github.com/pion/opus` – Opus decoder (primary)
- `modernc.org/sqlite` – SQLite via database/sql (spec §20)
- `github.com/gorilla/websocket` – live distribution (spec §14)
- `github.com/mewkiz/flac` – FLAC encoder (spec §13)

## Release (GoReleaser)

Configuration in `.goreleaser.yaml` (v2 schema): cross-compiles a static
(`CGO_ENABLED=0`) `espmic-server` binary for `linux/amd64` and `linux/arm64`.
It stamps `main.version`, `main.commit`, and `main.date` via `-X` ldflags, produces
`tar.gz` archives and `checksums.txt`, and builds the Docker image `ghcr.io/faisyl/espmic-server`
via `Dockerfile.release` (re-using the prebuilt binary).

```sh
go install github.com/goreleaser/goreleaser/v2@latest
goreleaser check             # validate .goreleaser.yaml
goreleaser release --snapshot --clean   # local snapshot build, outputs to dist/
```

## Docker / compose

The standalone `Dockerfile` provides a multi-stage build: `golang:1.26` builder → slim `alpine:3.20` runtime with non-root user `espmic`, static binary (`CGO_ENABLED=0`), exposing ports `8080` (HTTP API) and `9000` (control TLS/TCP). The SQLite DB lives at `$ESPMIC_DB_PATH` (`/data/espmic.db` in the image) and recordings under `/data/recordings/`; `/data` is mounted as a volume.

`Dockerfile.release` is used by GoReleaser to package the prebuilt static binary into the same minimal Alpine runtime image.

```sh
docker compose up --build        # from this directory
curl localhost:8080/health       # -> {"status":"ok","version":"dev"}
```

### TLS auto-provisioning (compose)

The compose stack sets `ESPMIC_TLS_CERT=/data/certs/cert.pem` and
`ESPMIC_TLS_KEY=/data/certs/key.pem` on the persistent `espmic-data` volume.
On first start, if either file is missing, the container entrypoint generates
a self-signed pair (RSA 2048, 10-year, CN `espmic.local`) before exec'ing the
server — so the control plane comes up with working TLS out of the box. On
restart the same pair is reused (no regeneration).

To supply your own certificate, mount real `cert.pem`/`key.pem` at those paths
(bind mount or pre-populated volume); the entrypoint skips generation when both
files already exist. The client connects with verification skipped (LAN mode).

Override the CN via `ESPMIC_TLS_CN` (default `espmic.local`).

### Device enrollment (control plane)

Devices authenticate via a `hello` message on the control connection (spec §7).
The server supports two enrollment modes via `ESPMIC_DEVICE_CREDENTIAL`:

| Mode | `ESPMIC_DEVICE_CREDENTIAL` | Behavior |
|---|---|---|
| **Open (TOFU, default)** | empty (default) | First hello from an unknown device is accepted and the device is enrolled automatically (trust-on-first-use). Subsequent hellos from the same device ID are accepted. No credential required. Suitable for trusted LANs. |
| **Credential-gated** | non-empty string | Device must present the exact credential in its `hello.credential` field. Comparison uses `crypto/subtle.ConstantTimeCompare`. Unknown devices with correct credential are enrolled (TOFU). Wrong/empty credential → connection closed with `auth failed`. |

After enrollment, the device appears in `GET /api/devices` and the dashboard DEVICES panel.

**Example (open LAN):**
```sh
# Default — no credential needed
ESPMIC_TLS_CERT=/data/certs/cert.pem ESPMIC_TLS_KEY=/data/certs/key.pem ./espmic-server
```

**Example (credential-gated):**
```sh
ESPMIC_DEVICE_CREDENTIAL="super-secret-token" \
ESPMIC_TLS_CERT=/data/certs/cert.pem ESPMIC_TLS_KEY=/data/certs/key.pem \
./espmic-server
```
Device hello must include `{"type":"hello","device_id":"esp32-001","credential":"super-secret-token"}`.

### Docker / NAT RTP

When the server runs inside a Docker container using bridge networking, the control connection `LocalAddr` is the container-internal IP (e.g. `172.21.0.2`). By default, the server instructs the ESP32 to send RTP audio packets to this address, which is unreachable from the physical LAN. Additionally, dynamic UDP port binding (`:0`) cannot be published cleanly across Docker.

To route RTP through Docker/NAT:
1. Set `ESPMIC_RTP_BIND_PORT=5004` to bind a predictable UDP port in the container.
2. Publish that port in Docker (e.g. `-p 5004:5004/udp` or `ports:` in compose).
3. Set `ESPMIC_ADVERTISE_HOST` to the Docker host's physical LAN IP (e.g. `192.168.1.100`) so the server instructs devices to stream to the host machine.
4. (Optional) If your external host port differs from the container port (e.g. `50004:5004/udp`), set `ESPMIC_ADVERTISE_RTP_PORT=50004`.

**Worked example (docker run):**
```sh
docker run -d \
  --name espmic-server \
  -p 8080:8080 \
  -p 4433:4433 \
  -p 5004:5004/udp \
  -e ESPMIC_RTP_BIND_PORT=5004 \
  -e ESPMIC_ADVERTISE_HOST=192.168.1.100 \
  -v espmic-data:/data \
  ghcr.io/faisyl/espmic-server:latest
```

**Worked example (docker compose):**
In `docker-compose.yml`, uncomment and configure `ESPMIC_ADVERTISE_HOST`:
```yaml
environment:
  ESPMIC_RTP_BIND_PORT: "5004"
  ESPMIC_ADVERTISE_HOST: "192.168.1.100"
ports:
  - "8080:8080"
  - "4433:4433"
  - "5004:5004/udp"
```

All documented env vars (`ESPMIC_HTTP_ADDR`, `ESPMIC_CONTROL_ADDR`,
`ESPMIC_TLS_CERT`, `ESPMIC_TLS_KEY`, `ESPMIC_DB_PATH`,
`ESPMIC_JITTER_TARGET_MS`, `ESPMIC_RTP_WAIT_TIMEOUT_S`,
`ESPMIC_RTP_BIND_PORT`, `ESPMIC_ADVERTISE_HOST`, `ESPMIC_ADVERTISE_RTP_PORT`)
are wired in `docker-compose.yml`; source of truth is `internal/config/config.go`.

---

## Browsing recordings on the host

Recordings land in `/data/recordings/` inside the container. To make them
directly browsable on the host without `sudo`, run the container with a UID/GID
that matches your host user and bind-mount a host directory you own.

### Quick start

```sh
# 1. Copy the example env file
cp .env.example .env

# 2. Edit .env with your host UID/GID (run `id -u` and `id -g` to find them)
#    The defaults (1000:1000) work for the first user on most Linux systems.
#    ESPMIC_DATA_DIR=./data   # host directory that will be bind-mounted

# 2. Pre-create the data directory and set ownership to your UID:GID
mkdir -p "$ESPMIC_DATA_DIR"
sudo chown -R <your-uid>:<your-gid> "$ESPMIC_DATA_DIR"
# Example:
# mkdir -p ./data
# sudo chown -R 1000:1000 ./data

# 3. Start the stack
docker compose up --build
```

Recordings will appear at `$ESPMIC_DATA_DIR/recordings/*.opus` on the host,
browsable directly in your file manager or CLI — no `docker cp`, no `sudo`.

### Environment variables (from `.env.example`)

| Variable | Default | Purpose |
|----------|---------|---------|
| `ESPMIC_UID` | `1000` | Host UID the container runs as. Must match the owner of `ESPMIC_DATA_DIR`. |
| `ESPMIC_GID` | `1000` | Host GID the container runs as. Must match the group of `ESPMIC_DATA_DIR`. |
| `ESPMIC_DATA_DIR` | `./data` | Host directory bind-mounted to `/data` in the container. Must exist and be owned by `ESPMIC_UID:ESPMIC_GID` before starting. |

### Notes

- The `docker-compose.yml` uses `user: "${ESPMIC_UID:-1000}:${ESPMIC_GID:-1000}"` and `volumes: ${ESPMIC_DATA_DIR:-./data}:/data`.
- The `ESPMIC_RECORDINGS_DIR` config defaults to `/data/recordings`, which lives on the bind-mount.
- If `ESPMIC_DATA_DIR` is a relative path (e.g. `./data`), Docker Compose resolves it relative to the compose file directory.
- **Important**: The host directory must be pre-created and `chown`ed to the correct UID:GID *before* `docker compose up`, because the container runs as a non-root user and cannot `chown` a root-owned bind mount.

---

## Opus fidelity validation (spec §21 #1/#2)

`libopus`/`ffmpeg` are NOT on the host. To validate pion/opus fidelity
against a reference stream:

```sh
# Build the server (pure-Go, no container needed for normal operation)
go build -o espmic-server ./cmd/server
```

To validate opus decode fidelity, build a containerized reference encoder
(requires ffmpeg/libopus in the image), e.g. via an Earthly target:

```dockerfile
# Earthfile target example (opus-fidelity):
FROM ubuntu:22.04
RUN apt-get update && apt-get install -y ffmpeg opus-tools libopus-dev
# Generate a known PCM tone, encode to Opus, packetize as RTP, feed to the
# server, decode via pion/opus, and compare decoded PCM to source.
```

The pion/opus conformance tests (CELT, silk, rangecoding, resample) all
pass — see `go test ./...` in the pion/opus module directory. The final
fidelity gate requires the containerized reference encoder; if pion/opus
decodes incorrectly, switch to cgo `github.com/hraban/opus` (libopus).
