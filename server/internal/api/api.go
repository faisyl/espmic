package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"espmic/server/internal/audio"
	"espmic/server/internal/config"
	"espmic/server/internal/control"
	"espmic/server/internal/stream"
)

// streamTimeout bounds how long we wait for stream_started/stream_stopped
// before reporting a timeout (504).
const streamTimeout = 5 * time.Second

// Server is the dependency surface the API handlers need (spec §15-§16).
type Server interface {
	DeviceList() interface{}
	StreamList() interface{}
	MetricsSurface() interface{}
	PCMBus() *audio.PCMBus
	PushConfig(ctx context.Context, deviceID string, cfg control.SetConfig) (control.Message, error)
	StartStream(ctx context.Context, deviceID string, purpose string) (map[string]any, error)
	StopStream(ctx context.Context, streamID string) error
}

// Handlers holds the server reference and implements each §15 endpoint.
type Handlers struct {
	srv Server
}

// Build-time stamped values (set via -X ldflags in .goreleaser.yaml / Dockerfile).
// Defaults are dev/none/unknown.
var (
	serverVersion = "dev"
	serverCommit  = "none"
	serverDate    = "unknown"
)

// SetVersion records the build-time version for the /health endpoint.
func SetVersion(v string) { serverVersion = v }

// SetCommit records the build-time git commit for the /health endpoint.
func SetCommit(c string) { serverCommit = c }

// SetDate records the build-time date for the /health endpoint.
func SetDate(d string) { serverDate = d }

// NewHandlers returns handlers bound to the server.
func NewHandlers(srv Server) *Handlers { return &Handlers{srv: srv} }

// RegisterRoutes mounts all §15 endpoints + metrics on mux (spec §15, §18).
func RegisterRoutes(mux *http.ServeMux, cfg *config.Config, srv Server) {
	h := NewHandlers(srv)
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /api/devices", h.handleDevices)
	mux.HandleFunc("GET /api/devices/{id}", h.handleDevice)
	mux.HandleFunc("POST /api/devices/{id}/stream", h.handleStartStream)
	mux.HandleFunc("POST /api/devices/{id}/config", h.handleConfig)
	mux.HandleFunc("DELETE /api/streams/{id}", h.handleStopStream)
	mux.HandleFunc("GET /api/streams/{id}", h.handleStream)
	mux.HandleFunc("GET /api/streams", h.handleStreams)
	mux.HandleFunc("GET /api/streams/{id}/stats", h.handleStreamStats)
	mux.HandleFunc("GET /api/recordings/{id}", h.handleRecording)
	mux.HandleFunc("GET /api/recordings/{id}/download", h.handleRecordingDownload)
	mux.HandleFunc("GET /api/metrics", h.handleMetrics)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": serverVersion,
		"commit":  serverCommit,
		"date":    serverDate,
	})
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

	ctx, cancel := context.WithTimeout(r.Context(), streamTimeout)
	defer cancel()

	result, err := h.srv.StartStream(ctx, id, req.Purpose)
	if err != nil {
		switch {
		case errors.Is(err, stream.ErrStreamNotFound), errors.Is(err, stream.ErrIllegalTransition):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.Is(err, control.ErrNotConnected):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not connected"})
		case errors.Is(err, context.DeadlineExceeded):
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "device did not respond to start_stream"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// handleConfig pushes a set_config command to a connected device's live
// control session (spec §10). It validates the request (400), checks the
// device is connected (404), sends set_config and returns the device's echoed
// status (200), its rejection error (502), or a timeout (504).
func (h *Handlers) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing device id"})
		return
	}

	var req control.SetConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	req.Type = control.TypeSetConfig
	req.RequestID = newRequestID()

	if err := req.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), streamTimeout)
	defer cancel()

	msg, err := h.srv.PushConfig(ctx, id, req)
	switch {
	case errors.Is(err, control.ErrNotConnected):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not connected"})
		return
	case errors.Is(err, context.DeadlineExceeded):
		writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "device did not respond"})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// A correlated error reply means the device rejected the config (502).
	if _, rejected := msg.(*control.Error); rejected {
		writeJSON(w, http.StatusBadGateway, msg)
		return
	}
	// Success: the device echoed its new status (200).
	writeJSON(w, http.StatusOK, msg)
}

// newRequestID returns a random request id used to correlate a set_config
// command with the device's status/error reply.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("cfg-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("cfg-%x", b[:])
}

func (h *Handlers) handleStopStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), streamTimeout)
	defer cancel()

	err := h.srv.StopStream(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, stream.ErrStreamNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, stream.ErrIllegalTransition):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.Is(err, control.ErrNotConnected):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not connected"})
		case errors.Is(err, context.DeadlineExceeded):
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "device did not respond to stop_stream"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"stream_id": id, "state": "stopped"})
}

func (h *Handlers) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stream_id": id, "state": "unknown"})
}

func (h *Handlers) handleStreams(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.srv.StreamList())
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
