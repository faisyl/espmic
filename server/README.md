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

## Build & test

Go 1.26 is required (no container needed for build/test).

```sh
go build ./...
go vet ./...
go test -race ./...
```

## Run

```sh
go run ./cmd/server
# defaults: HTTP :8080, control :9000; override via env vars
curl localhost:8080/health          # -> {"status":"ok","version":"dev"}
curl localhost:8080/api/metrics     # -> {statistics snapshot}
curl localhost:8080/api/devices     # -> [device list]
```

Sends SIGTERM/SIGINT for graceful shutdown.

## Version Stamping

Build metadata (`version`, `commit`, `date`) is stamped at build-time using Go linker flags (`-X main.version=... -X main.commit=... -X main.date=...`).
At startup, `espmic-server` logs these build variables, and the `GET /health` endpoint surfaces the version string:

```json
{
  "status": "ok",
  "version": "v0.4.6"
}
```

## API (spec §15)

| Endpoint | Purpose |
|---|---|
| `GET /health` | Health check |
| `GET /api/devices` | List devices |
| `GET /api/devices/{id}` | Device metadata |
| `POST /api/devices/{id}/stream` | Start managed stream (§16) |
| `POST /api/devices/{id}/config` | Push runtime config to a connected device (§10) |
| `DELETE /api/streams/{id}` | Stop stream |
| `GET /api/streams/{id}` | Stream state |
| `GET /api/streams/{id}/stats` | RTP/decoder statistics |
| `GET /api/recordings/{id}` | Recording metadata |
| `GET /api/recordings/{id}/download` | Retrieve recording |
| `GET /api/metrics` | Statistics (§18) |

### Push runtime config (POST /api/devices/{id}/config)

Push a `set_config` command to a device's live control session. The server
validates the request, correlates the device's `status`/`error` reply by
`request_id`, and returns the result.

**Request body** (JSON, at least one field required):

| Field | Type | Range | Description |
|---|---|---|---|
| `default_bitrate` | int | ≥ 0 | Apply bitrate immediately (spec §10) |
| `server_host` | string | non-empty | Persisted in NVS, applied on next boot |
| `i2s_bclk` | int | 0–47 | I2S bit-clock GPIO; persisted, applied on next boot |
| `i2s_ws` | int | 0–47 | I2S word-select GPIO |
| `i2s_din` | int | 0–47 | I2S serial data-in GPIO |

**Responses:**

| Status | Meaning |
|---|---|
| `200 OK` | Device echoed its new status |
| `400 Bad Request` | Invalid JSON or field out of range (server-side validation) |
| `404 Not Found` | Device is not currently connected |
| `502 Bad Gateway` | Device explicitly rejected the config (`error` reply) |
| `504 Gateway Timeout` | Device did not reply within the 5 s deadline |

**curl examples:**

```sh
# Set I2S pins for a connected device
curl -s -X POST http://localhost:8080/api/devices/esp32-001/config \
  -H 'Content-Type: application/json' \
  -d '{"i2s_bclk":5,"i2s_ws":6,"i2s_din":4}'
# -> {"type":"status","state":"IDLE","stream_id":"...",...}

# Change bitrate and server host
curl -s -X POST http://localhost:8080/api/devices/esp32-001/config \
  -H 'Content-Type: application/json' \
  -d '{"default_bitrate":256000,"server_host":"audio.new.local"}'

# Device not connected -> 404
# -> {"error":"device not connected"}

# Invalid pin -> 400
curl -s -X POST http://localhost:8080/api/devices/esp32-001/config \
  -H 'Content-Type: application/json' \
  -d '{"i2s_bclk":48}'
# -> {"error":"control: i2s_bclk out of range 0..47: 48"}
```

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

**RTP UDP ingest** uses one dynamic UDP port per managed stream (spec §17), so
it cannot be reached through the TCP `ports:` mapping above. The compose file
documents the two options — host networking or a mapped UDP range — with a
commented `network_mode: host` block. Until one is enabled, the HTTP API + TLS
control plane work but RTP receive does not.

All documented env vars (`ESPMIC_HTTP_ADDR`, `ESPMIC_CONTROL_ADDR`,
`ESPMIC_TLS_CERT`, `ESPMIC_TLS_KEY`, `ESPMIC_DB_PATH`,
`ESPMIC_JITTER_TARGET_MS`, `ESPMIC_RTP_WAIT_TIMEOUT_S`) are wired in
`docker-compose.yml`; source of truth is `internal/config/config.go`.

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
