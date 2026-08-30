# ESPMIC Audio Server (Go)

Server-side component for the ESPMIC audio system — the counterpart to the
ESP32 firmware in `../client`. It manages devices over persistent control
connections, ingests RTP/Opus, decodes to PCM, records and distributes live
audio. See `Audio_Server_Component_Specification.md` (this directory) for the
full spec.

Spec §4 permits Go; this implementation is pure Go (no cgo) for a clean
static binary.

## Status

S0 scaffold: module layout + interface stubs with doc comments citing the spec
section. No real control/RTP/jitter/opus/db logic yet — that lands S1-S3.

## Layout

Go standard layout, mapping spec §5 modules onto `internal/` packages:

```
server/
  cmd/server/            main: net/http server + graceful shutdown
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
```

## Build & test

Go 1.26 is required (no container needed for S0).

```sh
go build ./...
go vet ./...
go test ./...
```

## Run

```sh
go run ./cmd/server
# defaults to :8080; override with env vars, e.g. ESPMIC_HTTP_ADDR=:9090
curl localhost:8080/health   # -> {"status":"ok"}
```

Sends SIGTERM/SIGINT for graceful shutdown.

## Dependencies (pinned in go.mod for S1+)

Pure-Go, no cgo:

- `github.com/pion/rtp` – RTP parse (spec §10)
- `github.com/pion/opus` – Opus decoder (primary; validate fidelity in S2)
- `modernc.org/sqlite` – SQLite via database/sql (spec §20)
- `github.com/gorilla/websocket` – live distribution (spec §14, S3)

TODO (S2): if `pion/opus` fidelity is insufficient, consider cgo
`github.com/hraban/opus` (libopus) as the decoder fallback.
