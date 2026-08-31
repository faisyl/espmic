package api

import (
	"encoding/json"
	"net/http"

	"espmic/server/internal/config"
)

// Server is the dependency surface the API handlers need (spec §15-§16).
type Server interface {
	DeviceList() interface{}
	MetricsSurface() interface{}
}

// Handlers holds the server reference and implements each §15 endpoint.
type Handlers struct {
	srv Server
}

// serverVersion is the build-time version string (injected from cmd/server
// via SetVersion); surfaced on /health.
var serverVersion = "dev"

// SetVersion records the build-time version for the /health endpoint.
func SetVersion(v string) { serverVersion = v }

// NewHandlers returns handlers bound to the server.
func NewHandlers(srv Server) *Handlers { return &Handlers{srv: srv} }

// RegisterRoutes mounts all §15 endpoints + metrics on mux (spec §15, §18).
func RegisterRoutes(mux *http.ServeMux, cfg *config.Config, srv Server) {
	h := NewHandlers(srv)
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /api/devices", h.handleDevices)
	mux.HandleFunc("GET /api/devices/{id}", h.handleDevice)
	mux.HandleFunc("POST /api/devices/{id}/stream", h.handleStartStream)
	mux.HandleFunc("DELETE /api/streams/{id}", h.handleStopStream)
	mux.HandleFunc("GET /api/streams/{id}", h.handleStream)
	mux.HandleFunc("GET /api/streams/{id}/stats", h.handleStreamStats)
	mux.HandleFunc("GET /api/recordings/{id}", h.handleRecording)
	mux.HandleFunc("GET /api/recordings/{id}/download", h.handleRecordingDownload)
	mux.HandleFunc("GET /api/metrics", h.handleMetrics)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": serverVersion})
}

func (h *Handlers) handleDevices(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.srv.DeviceList())
}

func (h *Handlers) handleDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"device_id": id})
}

func (h *Handlers) handleStartStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing device id"})
		return
	}
	var req struct {
		Purpose string `json:"purpose"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	_ = req

	writeJSON(w, http.StatusAccepted, map[string]string{
		"stream_id": "placeholder-" + id,
		"state":     "starting",
	})
}

func (h *Handlers) handleStopStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stream_id": id, "state": "stopping"})
}

func (h *Handlers) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stream_id": id, "state": "unknown"})
}

func (h *Handlers) handleStreamStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stream_id": id})
}

func (h *Handlers) handleRecording(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"recording_id": id})
}

func (h *Handlers) handleRecordingDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"recording_id": id, "download": "not_implemented"})
}

func (h *Handlers) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.srv.MetricsSurface())
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
