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
curl localhost:8080/health          # -> {"status":"ok"}
curl localhost:8080/api/metrics     # -> {statistics snapshot}
curl localhost:8080/api/devices     # -> [device list]
```

Sends SIGTERM/SIGINT for graceful shutdown.

## API (spec §15)

| Endpoint | Purpose |
|---|---|
| `GET /health` | Health check |
| `GET /api/devices` | List devices |
| `GET /api/devices/{id}` | Device metadata |
| `POST /api/devices/{id}/stream` | Start managed stream (§16) |
| `DELETE /api/streams/{id}` | Stop stream |
| `GET /api/streams/{id}` | Stream state |
| `GET /api/streams/{id}/stats` | RTP/decoder statistics |
| `GET /api/recordings/{id}` | Recording metadata |
| `GET /api/recordings/{id}/download` | Retrieve recording |
| `GET /api/metrics` | Statistics (§18) |

## Dependencies (pinned in go.mod)

Pure-Go, no cgo:

- `github.com/pion/rtp` – RTP parse (spec §10)
- `github.com/pion/opus` – Opus decoder (primary)
- `modernc.org/sqlite` – SQLite via database/sql (spec §20)
- `github.com/gorilla/websocket` – live distribution (spec §14)
- `github.com/mewkiz/flac` – FLAC encoder (spec §13)

## Release (GoReleaser)

.config in `.goreleaser.yaml` (v2 schema): cross-compiles a static
(`CGO_ENABLED=0`) `espmic-server` for linux/amd64 + linux/arm64, produces
archives + checksums, and builds a `ghcr.io/faisyl/espmic-server` image.

```sh
go install github.com/goreleaser/goreleaser/v2@latest
goreleaser check             # validate .goreleaser.yaml
goreleaser release --snapshot --clean   # local build, outputs to dist/
```

## Docker / compose

The `Dockerfile` is multi-stage: `golang:1.26` builder → slim Alpine runtime,
non-root user, static binary, listens on `8080` (HTTP API) and `9000` (control
TLS). The SQLite DB lives at `$ESPMIC_DB_PATH` (`/data/espmic.db` in the
image) and recordings under `/data/recordings/`; `/data` is a volume.

```sh
docker compose up --build        # from this directory
curl localhost:8080/health       # -> {"status":"ok"}
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
