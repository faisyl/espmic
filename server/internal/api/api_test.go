package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"espmic/server/internal/config"
)

// fakeSrv is a minimal Server implementation for API tests (spec §15).
type fakeSrv struct{}

func (fakeSrv) DeviceList() interface{}     { return []string{"d1"} }
func (fakeSrv) MetricsSurface() interface{} { return map[string]int{} }

// TestHealth verifies the S0 health endpoint (spec §15).
func TestHealth(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, config.Load(), fakeSrv{})

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
	RegisterRoutes(mux, config.Load(), fakeSrv{})

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
	RegisterRoutes(mux, config.Load(), fakeSrv{})

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/metrics = %d, want %d", rec.Code, http.StatusOK)
	}
}
