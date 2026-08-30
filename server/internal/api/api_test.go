package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"espmic/server/internal/config"
)

// TestHealth verifies the S0 health endpoint (spec §15).
func TestHealth(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, config.Load())

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
		t.Fatalf("status = %q, want %q", body["status"], "ok")
	}
}
