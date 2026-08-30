// Package api exposes the HTTP management surface (spec §15-§16).
//
// Endpoints from spec §15 are registered here. S0 provides the health check
// and 501 stubs for the management endpoints; handlers land in S1-S3.
package api

import (
	"encoding/json"
	"net/http"

	"espmic/server/internal/config"
)

// RegisterRoutes mounts the HTTP API on mux, using cfg for runtime settings.
func RegisterRoutes(mux *http.ServeMux, cfg *config.Config) {
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /api/devices", notImplemented)
	mux.HandleFunc("GET /api/streams/{id}", notImplemented)
	mux.HandleFunc("DELETE /api/streams/{id}", notImplemented)
	mux.HandleFunc("GET /api/recordings/{id}", notImplemented)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not implemented"})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
