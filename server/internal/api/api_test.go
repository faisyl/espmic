package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"espmic/server/internal/audio"
	"espmic/server/internal/config"
	"espmic/server/internal/control"
	"espmic/server/internal/device"
	"espmic/server/internal/rtp"
	"espmic/server/internal/stream"
)

// fakeSrv is a minimal Server implementation for API tests (spec §15). Its
// PushConfig behavior is programmable so handler tests can cover success,
// rejection, offline, and timeout.
type fakeSrv struct {
	pushMsg       control.Message
	pushErr       error
	pushCfg       control.SetConfig
	pushDev       string
	pushCall      bool
	streams       interface{}
	startMsg      map[string]any
	startErr      error
	startCall     bool
	stopErr       error
	stopCall      bool
	devFinalStats *control.StreamStoppedStats
}

func (f *fakeSrv) DeviceList() interface{}     { return []string{"d1"} }
func (f *fakeSrv) StreamList() interface{}     { return f.streams }
func (f *fakeSrv) MetricsSurface() interface{} { return map[string]int{} }
func (f *fakeSrv) PCMBus() *audio.PCMBus       { return audio.NewPCMBus() }
func (f *fakeSrv) PushConfig(_ context.Context, deviceID string, cfg control.SetConfig) (control.Message, error) {
	f.pushDev = deviceID
	f.pushCfg = cfg
	f.pushCall = true
	return f.pushMsg, f.pushErr
}
func (f *fakeSrv) StartStream(_ context.Context, deviceID, purpose string, rec RecordingConfig) (map[string]any, error) {
	f.startCall = true
	return f.startMsg, f.startErr
}
func (f *fakeSrv) StopStream(_ context.Context, streamID string) error {
	f.stopCall = true
	return f.stopErr
}
func (f *fakeSrv) GetStream(streamID string) (*stream.Stream, error) {
	s := stream.New(streamID, "test-device", 0, time.Time{})
	_ = s.Start(time.Time{})
	_ = s.DeviceCommandSent()
	_ = s.StreamStarted(time.Time{})
	_ = s.FirstPacket(time.Time{})
	return s, nil
}
func (f *fakeSrv) RTPStreamStats(streamID string) (rtp.Stats, bool) {
	return rtp.Stats{Received: 100, Lost: 2, JitterMS: 0.5}, true
}
func (f *fakeSrv) StreamPort(streamID string) (uint16, bool) {
	return 5004, true
}
func (f *fakeSrv) DeviceGet(deviceID string) (*device.Device, error) {
	return &device.Device{DeviceID: deviceID, DisplayName: deviceID, Status: "online"}, nil
}
func (f *fakeSrv) GetRecording(recordingID string) (map[string]any, error) {
	return map[string]any{"recording_id": recordingID}, nil
}
func (f *fakeSrv) DownloadRecording(recordingID string) (string, error) {
	return "", nil
}
func (f *fakeSrv) ListRecordings() ([]map[string]any, error) {
	return nil, nil
}
func (f *fakeSrv) GetDeviceStatus(_ context.Context, deviceID string) (control.Message, error) {
	return &control.Status{Type: control.TypeStatus, RequestID: "req-test", Status: "ok", State: "IDLE"}, nil
}
func (f *fakeSrv) DeviceFinalStats(streamID string) (*control.StreamStoppedStats, bool) {
	if f.devFinalStats != nil {
		return f.devFinalStats, true
	}
	return nil, false
}

func (f *fakeSrv) SetDeviceDisplayName(deviceID, name string) error {
	return nil
}

// TestHealth verifies the S0 health endpoint (spec §15).
func TestHealth(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, config.Load(), &fakeSrv{})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("GET /health status = %q, want %q", body["status"], "ok")
	}
	if body["version"] != "dev" {
		t.Fatalf("GET /health version = %q, want %q (default before SetVersion)", body["version"], "dev")
	}
	if body["commit"] != "none" {
		t.Fatalf("GET /health commit = %q, want %q (default before SetCommit)", body["commit"], "none")
	}
	if body["date"] != "unknown" {
		t.Fatalf("GET /health date = %q, want %q (default before SetDate)", body["date"], "unknown")
	}

	// SetVersion/SetCommit/SetDate are reflected on /health.
	SetVersion("v9.9.9-test")
	SetCommit("abc1234")
	SetDate("2026-09-05T12:34:56Z")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/health", nil))
	if err := json.Unmarshal(rec2.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["version"] != "v9.9.9-test" {
		t.Fatalf("after SetVersion, /health version = %q, want v9.9.9-test", body["version"])
	}
	if body["commit"] != "abc1234" {
		t.Fatalf("after SetCommit, /health commit = %q, want abc1234", body["commit"])
	}
	if body["date"] != "2026-09-05T12:34:56Z" {
		t.Fatalf("after SetDate, /health date = %q, want 2026-09-05T12:34:56Z", body["date"])
	}
}

// TestDevices verifies the devices endpoint (spec §15).
func TestDevices(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, config.Load(), &fakeSrv{})

	req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/devices = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestMetrics verifies the metrics endpoint (spec §18).
func TestMetrics(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, config.Load(), &fakeSrv{})

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/metrics = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestStreams verifies GET /api/streams returns a JSON array of active stream
// objects with the exact field names the dashboard reads (spec §15).
func TestStreams(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, config.Load(), &fakeSrv{
		streams: []map[string]any{
			{"StreamID": "s1", "DeviceID": "d1", "State": "ACTIVE", "SSRC": 12345, "StartedAt": "2026-09-04T00:00:00Z", "PacketsReceived": uint64(100), "PacketsLost": uint64(3), "JitterMS": 0.31},
		},
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/streams", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/streams = %d, want %d", rec.Code, http.StatusOK)
	}
	var arr []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &arr); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(arr) != 1 {
		t.Fatalf("len(arr) = %d, want 1", len(arr))
	}
	for _, key := range []string{"StreamID", "DeviceID", "State", "SSRC", "StartedAt", "PacketsReceived", "PacketsLost", "JitterMS"} {
		if _, ok := arr[0][key]; !ok {
			t.Fatalf("array element missing key %q; got %v", key, arr[0])
		}
	}
}

// TestConfigEndpoint exercises POST /api/devices/{id}/config (spec §10
// set_config via the operator API). Table-driven across validation, offline,
// rejection, timeout, and success.
func TestConfigEndpoint(t *testing.T) {
	pin := 12

	cases := []struct {
		name     string
		deviceID string
		body     string
		pushMsg  control.Message
		pushErr  error
		wantCode int
		wantBody string // substring expected in the JSON body
	}{
		{
			name:     "success returns echoed status",
			deviceID: "d1",
			body:     `{"i2s_bclk":12,"i2s_ws":13,"i2s_din":14}`,
			pushMsg:  &control.Status{Type: control.TypeStatus, RequestID: "cfg-1", State: "IDLE"},
			wantCode: http.StatusOK,
			wantBody: `"state":"IDLE"`,
		},
		{
			name:     "device rejection maps to 502",
			deviceID: "d1",
			body:     `{"i2s_bclk":12}`,
			pushMsg:  &control.Error{Type: control.TypeError, RequestID: "cfg-1", Code: "invalid_config", Message: "i2s_bclk must be in range 0..47"},
			wantCode: http.StatusBadGateway,
			wantBody: `"code":"invalid_config"`,
		},
		{
			name:     "device offline maps to 404",
			deviceID: "d1",
			body:     `{"i2s_bclk":12}`,
			pushErr:  control.ErrNotConnected,
			wantCode: http.StatusNotFound,
			wantBody: `device not connected`,
		},
		{
			name:     "device timeout maps to 504",
			deviceID: "d1",
			body:     `{"i2s_bclk":12}`,
			pushErr:  context.DeadlineExceeded,
			wantCode: http.StatusGatewayTimeout,
			wantBody: `device did not respond`,
		},
		{
			name:     "invalid JSON maps to 400",
			deviceID: "d1",
			body:     `{not json`,
			wantCode: http.StatusBadRequest,
			wantBody: `invalid JSON body`,
		},
		{
			name:     "pin out of range maps to 400",
			deviceID: "d1",
			body:     `{"i2s_bclk":48}`,
			wantCode: http.StatusBadRequest,
			wantBody: `i2s_bclk out of range`,
		},
		{
			name:     "no fields maps to 400",
			deviceID: "d1",
			body:     `{}`,
			wantCode: http.StatusBadRequest,
			wantBody: `at least one field`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &fakeSrv{pushMsg: tc.pushMsg, pushErr: tc.pushErr}
			mux := http.NewServeMux()
			RegisterRoutes(mux, config.Load(), srv)

			req := httptest.NewRequest(http.MethodPost, "/api/devices/"+tc.deviceID+"/config", bytes.NewBufferString(tc.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantBody != "" {
				if !bytes.Contains(rec.Body.Bytes(), []byte(tc.wantBody)) {
					t.Fatalf("body %q missing %q", rec.Body.String(), tc.wantBody)
				}
			}
			// A valid request that reaches PushConfig must carry a set_config
			// type and a non-empty request_id, and must not be mutated by us.
			if tc.wantCode == http.StatusOK || tc.wantCode == http.StatusBadGateway {
				if !srv.pushCall {
					t.Fatal("expected PushConfig to be called")
				}
				if srv.pushCfg.Type != control.TypeSetConfig {
					t.Fatalf("type = %q, want %q", srv.pushCfg.Type, control.TypeSetConfig)
				}
				if srv.pushCfg.RequestID == "" {
					t.Fatal("expected a non-empty request_id")
				}
				if srv.pushCfg.I2SBclk == nil || *srv.pushCfg.I2SBclk != pin {
					t.Fatalf("i2s_bclk not forwarded: %+v", srv.pushCfg.I2SBclk)
				}
			}
		})
	}
}

// TestConfigEndpointPartialFields verifies only provided fields are sent
// (pointers/omitempty), including server_host and a single pin.
func TestConfigEndpointPartialFields(t *testing.T) {
	wantHost := "audio.internal"
	srv := &fakeSrv{pushMsg: &control.Status{Type: control.TypeStatus, State: "IDLE"}}
	mux := http.NewServeMux()
	RegisterRoutes(mux, config.Load(), srv)

	body := `{"server_host":"audio.internal","i2s_din":14}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/d1/config", bytes.NewBufferString(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !srv.pushCall {
		t.Fatal("expected PushConfig to be called")
	}
	if srv.pushCfg.ServerHost == nil || *srv.pushCfg.ServerHost != wantHost {
		t.Fatalf("server_host not forwarded: %+v", srv.pushCfg.ServerHost)
	}
	if srv.pushCfg.I2SBclk != nil || srv.pushCfg.I2SWs != nil {
		t.Fatalf("unset fields should be nil; got %+v", srv.pushCfg)
	}
	if srv.pushCfg.I2SDin == nil || *srv.pushCfg.I2SDin != 14 {
		t.Fatalf("i2s_din not forwarded: %+v", srv.pushCfg.I2SDin)
	}
}

// TestConfigEndpointOpusParams verifies Opus encoder params pass through
// handleConfig to PushConfig (spec §10 set_config via operator API).
func TestConfigEndpointOpusParams(t *testing.T) {
	srv := &fakeSrv{pushMsg: &control.Status{Type: control.TypeStatus, State: "IDLE"}}
	mux := http.NewServeMux()
	RegisterRoutes(mux, config.Load(), srv)

	body := `{"fec":true,"dtx":false,"complexity":5,"bitrate":128000,"vbr":true,"frame_ms":20}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/d1/config", bytes.NewBufferString(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !srv.pushCall {
		t.Fatal("expected PushConfig to be called")
	}
	if srv.pushCfg.Fec == nil || *srv.pushCfg.Fec != true {
		t.Fatalf("fec not forwarded: %+v", srv.pushCfg.Fec)
	}
	if srv.pushCfg.Dtx == nil || *srv.pushCfg.Dtx != false {
		t.Fatalf("dtx not forwarded: %+v", srv.pushCfg.Dtx)
	}
	if srv.pushCfg.Complexity == nil || *srv.pushCfg.Complexity != 5 {
		t.Fatalf("complexity not forwarded: %+v", srv.pushCfg.Complexity)
	}
	if srv.pushCfg.Bitrate == nil || *srv.pushCfg.Bitrate != 128000 {
		t.Fatalf("bitrate not forwarded: %+v", srv.pushCfg.Bitrate)
	}
	if srv.pushCfg.Vbr == nil || *srv.pushCfg.Vbr != true {
		t.Fatalf("vbr not forwarded: %+v", srv.pushCfg.Vbr)
	}
	if srv.pushCfg.FrameMS == nil || *srv.pushCfg.FrameMS != 20 {
		t.Fatalf("frame_ms not forwarded: %+v", srv.pushCfg.FrameMS)
	}
}

// TestConfigEndpointOpusParamsRejected verifies out-of-range Opus params
// are rejected with 400 before reaching PushConfig.
func TestConfigEndpointOpusParamsRejected(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"complexity too high", `{"complexity":11}`},
		{"complexity negative", `{"complexity":-1}`},
		{"frame_ms invalid", `{"frame_ms":30}`},
		{"bitrate negative", `{"bitrate":-1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &fakeSrv{}
			mux := http.NewServeMux()
			RegisterRoutes(mux, config.Load(), srv)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/d1/config", bytes.NewBufferString(tc.body)))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if srv.pushCall {
				t.Fatal("PushConfig should NOT have been called for invalid params")
			}
		})
	}
}

// TestStartStreamEndpoint exercises POST /api/devices/{id}/stream.
func TestStartStreamEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		deviceID string
		body     string
		startMsg map[string]any
		startErr error
		wantCode int
		wantBody string
	}{
		{
			name:     "success returns stream info",
			deviceID: "d1",
			body:     `{"purpose":"test"}`,
			startMsg: map[string]any{"stream_id": "strm-abc", "ssrc": 12345, "port": 5004, "state": "RTP_WAIT"},
			wantCode: http.StatusOK,
			wantBody: `"stream_id":"strm-abc"`,
		},
		{
			name:     "device not connected maps to 404",
			deviceID: "d1",
			body:     `{"purpose":"test"}`,
			startErr: control.ErrNotConnected,
			wantCode: http.StatusNotFound,
			wantBody: `device not connected`,
		},
		{
			name:     "device timeout maps to 504",
			deviceID: "d1",
			body:     `{"purpose":"test"}`,
			startErr: context.DeadlineExceeded,
			wantCode: http.StatusGatewayTimeout,
			wantBody: `device did not respond`,
		},
		{
			name:     "conflict maps to 409",
			deviceID: "d1",
			body:     `{"purpose":"test"}`,
			startErr: stream.ErrIllegalTransition,
			wantCode: http.StatusConflict,
			wantBody: `illegal lifecycle transition`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &fakeSrv{startMsg: tc.startMsg, startErr: tc.startErr}
			mux := http.NewServeMux()
			RegisterRoutes(mux, config.Load(), srv)

			req := httptest.NewRequest(http.MethodPost, "/api/devices/"+tc.deviceID+"/stream", bytes.NewBufferString(tc.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantBody != "" {
				if !bytes.Contains(rec.Body.Bytes(), []byte(tc.wantBody)) {
					t.Fatalf("body %q missing %q", rec.Body.String(), tc.wantBody)
				}
			}
			if tc.wantCode == http.StatusOK {
				if !srv.startCall {
					t.Fatal("expected StartStream to be called")
				}
			}
		})
	}
}

// TestStopStreamEndpoint exercises DELETE /api/streams/{id}.
func TestStopStreamEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		streamID string
		stopErr  error
		wantCode int
		wantBody string
	}{
		{
			name:     "success returns stopped",
			streamID: "strm-1",
			wantCode: http.StatusOK,
			wantBody: `"state":"stopped"`,
		},
		{
			name:     "stream not found maps to 404",
			streamID: "strm-1",
			stopErr:  stream.ErrStreamNotFound,
			wantCode: http.StatusNotFound,
			wantBody: `unknown stream`,
		},
		{
			name:     "illegal transition maps to 409",
			streamID: "strm-1",
			stopErr:  stream.ErrIllegalTransition,
			wantCode: http.StatusConflict,
			wantBody: `illegal lifecycle transition`,
		},
		{
			name:     "device timeout maps to 504",
			streamID: "strm-1",
			stopErr:  context.DeadlineExceeded,
			wantCode: http.StatusGatewayTimeout,
			wantBody: `device did not respond`,
		},
		{
			name:     "device not connected maps to 404",
			streamID: "strm-1",
			stopErr:  control.ErrNotConnected,
			wantCode: http.StatusNotFound,
			wantBody: `device not connected`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &fakeSrv{stopErr: tc.stopErr}
			mux := http.NewServeMux()
			RegisterRoutes(mux, config.Load(), srv)

			req := httptest.NewRequest(http.MethodDelete, "/api/streams/"+tc.streamID, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantBody != "" {
				if !bytes.Contains(rec.Body.Bytes(), []byte(tc.wantBody)) {
					t.Fatalf("body %q missing %q", rec.Body.String(), tc.wantBody)
				}
			}
			if tc.wantCode == http.StatusOK {
				if !srv.stopCall {
					t.Fatal("expected StopStream to be called")
				}
			}
		})
	}
}

// TestStreamStatsEndpoint exercises GET /api/streams/{id}/stats.
func TestStreamStatsEndpoint(t *testing.T) {
	t.Run("rtp only - no device_final", func(t *testing.T) {
		srv := &fakeSrv{}
		mux := http.NewServeMux()
		RegisterRoutes(mux, config.Load(), srv)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/streams/strm-1/stats", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		for _, key := range []string{"stream_id", "packets_received", "packets_lost", "jitter_ms", "rtp"} {
			if _, ok := body[key]; !ok {
				t.Fatalf("body missing key %q; got %v", key, body)
			}
		}
		if _, ok := body["device_final"]; ok {
			t.Fatal("device_final should be absent when no device stats")
		}
	})

	t.Run("with device_final stats", func(t *testing.T) {
		srv := &fakeSrv{
			devFinalStats: &control.StreamStoppedStats{
				PacketsSent: 42,
				BytesSent:   8400,
				DurationMS:  5000,
			},
		}
		mux := http.NewServeMux()
		RegisterRoutes(mux, config.Load(), srv)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/streams/strm-1/stats", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte(`"device_final"`)) {
			t.Fatalf("body missing device_final; got %s", rec.Body.String())
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte(`"rtp"`)) {
			t.Fatalf("body missing rtp namespace; got %s", rec.Body.String())
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte(`"packets_received"`)) {
			t.Fatalf("body missing backward-compat packets_received; got %s", rec.Body.String())
		}
	})
}
