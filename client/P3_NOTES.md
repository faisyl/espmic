# P3 — Wire-up, Hardening & Self-Review

Final phase: `main/app_main.c` orchestrates the full pipeline and drives the
spec §12 state machine; §14 failure-handling is completed; the four carried-over
review nits are addressed; `status` reports real numbers; §17 tests are mapped in
`ACCEPTANCE.md`. No P1 portable module logic (`rtp`, `control_protocol`,
`audio_buffer`, `state_machine`) was changed.

## Wired architecture

```
app_main (boot + SM owner)
  NVS -> nvs_config_load -> device_state(BOOT)
      -> audio_manager_init -> wifi_manager_init -> health_monitor_start(+WDT)
      -> BOOT_DONE -> wifi_manager_start
                         |
        wifi callbacks --+--> sm_dispatch(WIFI_CONNECTED / WIFI_LOST /
                         |                 PROVISIONED)  + force PROVISIONING
                         |
        on_got_ip -----> control_task_start (once)
                                 |
        control on_state_event --+--> sm_dispatch(CONTROL_CONNECT /
                                         CONTROL_CONNECTED / START_STREAM /
                                         STREAM_STARTED / STOP_STREAM /
                                         CONTROL_LOST)
                                 |
        start_stream ----------> audio_manager_start_stream
                                   -> rtp_sender + opus + i2s (real-time chain)
```

The device state is a single `sm_state_t` in app_main, mutated only via the pure
`sm_handle_event()` behind a mutex (`sm_dispatch`). All connectivity events from
the Wi-Fi event context and the control task funnel through it, so observed state
follows §12: `BOOT → [PROVISIONING] → WIFI_CONNECTING → WIFI_CONNECTED →
CONTROL_CONNECTING → IDLE → STREAM_STARTING → STREAMING → IDLE`, with the
any-state `WIFI_LOST / CONTROL_LOST / FATAL / RECOVER` overrides.

### Design choices / ambiguities resolved

- **PROVISIONING entry.** app_main dispatches `BOOT_DONE` before
  `wifi_manager_start()` so the normal (credentials-present) path visibly enters
  `WIFI_CONNECTING`. When the driver instead finds no credentials it raises the
  provisioning-started callback; because the SM has already left `BOOT`, we set
  `PROVISIONING` directly (`sm_force_provisioning()`) rather than route a second
  event through the pure SM. This keeps the SM pure and matches §12
  ("PROVISIONING is only entered when credentials are absent or reset"). An
  alternative (expose `wifi_manager_is_provisioned()` and branch before
  dispatch) was avoided to keep the P2 API surface unchanged.
- **Control task started once** on the first got-IP; later Wi-Fi reconnects are
  handled by the control task's own backoff loop, so it is not restarted.
- **status `state` field** reports the media-level `STREAMING`/`IDLE` from
  `audio_manager` (control_task has no reference to the app-level SM). The full
  §12 state is logged. Surfacing the exact SM label in `status` would couple
  control_task to app_main and was left as a minor TODO.
- **WDT feed strategy.** Critical loops self-subscribe to the Task WDT with
  `esp_task_wdt_add(NULL)` / `esp_task_wdt_reset()` / `esp_task_wdt_delete(NULL)`
  (a core `esp_system` API, so **no new component REQUIRES and no dependency
  cycle** — routing feeds through `health_monitor` would have created a cycle:
  `health_monitor → audio_manager → i2s_capture → health_monitor`). The control
  task subscribes only for its connected phase so the up-to-10 s blocking TLS
  handshake in `conn_open()` is not watched.

## Task / priority / stack table

`configMAX_PRIORITIES` is 25 by default on ESP-IDF FreeRTOS.

| Task        | Where started            | Priority                    | Stack | Core | WDT | Loop bound |
|-------------|--------------------------|-----------------------------|-------|------|-----|------------|
| `i2s_cap`   | `i2s_capture_start`      | `MAX-3` (=22), highest app  | 4096  | any  | yes | 200 ms DMA read |
| `opus`      | `opus_task_start`        | `MAX-4` (=21)               | 8192  | any  | yes | 5 ms poll / 50 ms lock |
| `rtp`       | `rtp_sender_start`       | `MAX-5` (=20)               | 4096  | any  | yes | 5 ms poll / 100 ms send |
| `control`   | `control_task_start`     | 5                           | 8192  | any  | yes*| 5 s read timeout |
| wifi/prov   | ESP-IDF system           | system                      | —     | —    | no  | event-driven |
| `health`    | `esp_timer` (snapshot)   | esp_timer task              | —     | —    | no  | 1 s period, 20 ms bounded stat locks |
| `main`      | app_main (returns)       | main task                   | 8192  | —    | no  | returns after wiring |

\* control subscribes to the WDT only while connected (in `run_session`).

Priority ordering keeps real-time capture above encode above send, so a slow
network can never starve the microphone (spec §4/§6).

## §14 failure-handling audit (all addressed)

- **No unbounded I2S backlog** — 200 ms drop-oldest PCM ring; `pcm_overflow`
  counted and reported.
- **Bounded RTP retry** — connected UDP socket with 100 ms `SO_SNDTIMEO`; send
  failures counted (`rtp_send_errors`), never fatal, never block I2S.
- **PCM overflow / drops surfaced** — `pcm_overflow`, `capture_late`,
  `encoder_late`, `encoded_drops/rejects` in `status`.
- **No indefinite waits** — every pipeline lock now uses a bounded timeout
  (i2s/opus/rtp 50 ms; stats reader 20 ms); reads/sends have socket timeouts;
  no `portMAX_DELAY` remains in any task/reader loop.
- **Watchdog enabled and fed** — `health_monitor_start` (re)configures the Task
  WDT at 8 s (< the 10 s sdkconfig panic) and each critical loop feeds it.
- **Control loss with active stream** — grace stop after `CONTROL_LOSS_GRACE_MS`.
- **Wi-Fi loss** — off-event-loop exponential reconnect (1→16 s).

## Carried-over review nits — resolution

- **Jim P2 #1 — `wifi_manager.c` slept in the event handler.** Replaced the
  `vTaskDelay` in `WIFI_EVENT_STA_DISCONNECTED` with a one-shot `esp_timer`
  (`schedule_reconnect()` / `reconnect_cb`) doing exponential 1→16 s backoff off
  the event-loop task; backoff resets on got-IP.
- **Jim P2 #2 — `i2s_capture.c` / `opus_encoder_task.c` used `portMAX_DELAY`
  on buffer mutexes.** Now bounded to `pdMS_TO_TICKS(50)`; on timeout the chunk /
  frame / packet is dropped and the late/drop counter is bumped. (Also applied to
  `rtp_sender.c` for consistency.)
- **Jim P2 #3 — `control_task.c` LAN-mode `skip_common_name`.** Added an optional
  cert/CA-pinning path: `load_pinning_from_nvs()` reads a PEM CA bundle (NVS key
  `server_ca`) and an optional expected CN/SAN (`server_cn`); when present the
  chain is verified (and CN pinned). LAN mode (no CA) remains the documented
  default. **Sizing:** the `server_ca` PEM buffer (`s_ca_buf`) is capped at 3072
  bytes — enough for a device/intermediate CA chain, not a full public root
  store; operators pinning a large bundle must trim to the intermediate(s), or
  raise the cap if a broader trust store is required. (Jim P3 review, obs #2.)
- **Jim P1 #3 — `low_water == (size_t)-1` sentinel.** `pcm_low_water` is now
  carried through `audio_stats_t` and guarded in the `status` reporter
  (`add_stats_block`) — reports `0` until the first non-empty observation.

## Diagnostics surfaced in `status` (real numbers)

`uptime_ms`, `free_heap`, `min_free_heap`, `free_psram`, `wifi_rssi`,
`pcm_overflow`, `pcm_written`, `pcm_read`, `pcm_high_water`, `pcm_low_water`
(guarded), `encoded_drops/rejects/pushed/popped/high_water`, `encoder_late`,
`capture_late`, `rtp_packets_sent`, `rtp_bytes_sent`, `rtp_send_errors`,
`rtp_ssrc`, plus `state` and `stream_id`.

## Files changed in P3

- `main/app_main.c` (rewrite), `main/CMakeLists.txt` (REQUIRES).
- `components/wifi_manager/{wifi_manager.c,CMakeLists.txt}` (nit #1 + esp_timer).
- `components/i2s_capture/i2s_capture.c` (nit #2 + WDT).
- `components/opus_encoder/opus_encoder_task.c` (nit #2 + WDT).
- `components/rtp_sender/rtp_sender.c` (bounded lock + WDT).
- `components/control_task/{control_task.c,CMakeLists.txt}` (nit #3 + WDT +
  richer status + nvs_flash REQUIRES).
- `components/audio_manager/{include/audio_manager.h,audio_manager.c}`
  (`pcm_low_water` + bounded stat locks).
- `components/health_monitor/{include/health_monitor.h,health_monitor.c,
  CMakeLists.txt}` (`wifi_rssi` + esp_wifi REQUIRES).
- `client/ACCEPTANCE.md`, `client/P3_NOTES.md` (new).

## Residual TODOs (non-blocking)

1. Report the exact §12 SM label in `status` (needs an app→control state getter).
2. Physical GPIO button to trigger `wifi_manager_reset_credentials()` (spec §13
   allows a software mechanism; the API exists, no button wired).
3. Optionally persist and use `server_cn` from provisioning UX; document the ops
   flow for writing `server_ca`/`server_cn` blobs into NVS.
4. SRTP / secure tunnel for hostile networks (spec §15 lists it as beyond the LAN
   default); only plain RTP + optional TLS-pinned control are implemented.
5. `set_config` currently applies `default_bitrate` + `server_host`; extend to
   the remaining §16 keys if the server needs to rewrite them at runtime.
