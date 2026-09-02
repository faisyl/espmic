package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"espmic/server/internal/config"
	"espmic/server/internal/control"
)

// fakeSrv is a minimal Server implementation for API tests (spec §15). Its
// PushConfig behavior is programmable so handler tests can cover success,
// rejection, offline, and timeout.
type fakeSrv struct {
	pushMsg  control.Message
	pushErr  error
	pushCfg  control.SetConfig
	pushDev  string
	pushCall bool
}

func (f *fakeSrv) DeviceList() interface{}     { return []string{"d1"} }
func (f *fakeSrv) MetricsSurface() interface{} { return map[string]int{} }
func (f *fakeSrv) PushConfig(_ context.Context, deviceID string, cfg control.SetConfig) (control.Message, error) {
	f.pushDev = deviceID
	f.pushCfg = cfg
	f.pushCall = true
	return f.pushMsg, f.pushErr
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

	// SetVersion is reflected on /health.
	SetVersion("v9.9.9-test")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/health", nil))
	if err := json.Unmarshal(rec2.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["version"] != "v9.9.9-test" {
		t.Fatalf("after SetVersion, /health version = %q, want v9.9.9-test", body["version"])
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
