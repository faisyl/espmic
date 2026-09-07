package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"espmic/server/internal/api"
	"espmic/server/internal/audio"
	"espmic/server/internal/config"
	"espmic/server/internal/control"
	"espmic/server/internal/device"
	"espmic/server/internal/persistence"
	"espmic/server/internal/rtp"
	"espmic/server/internal/stream"
)

// flowFakeSrv implements api.Server for the recording HTTP flow test.
// It has its own persistence, stream registry, and recorder map so the
// full StartStream → StopStream → GET recording → download lifecycle
// can be exercised through the HTTP handlers without a real device.
type flowFakeSrv struct {
	db        *sql.DB
	repos     *persistence.Repos
	recDir    string
	bus       *audio.PCMBus
	streams   map[string]*stream.Stream
	streamMu  sync.RWMutex
	recorders map[string]*audio.Recorder
	recMu     sync.Mutex
}

func newFlowFakeSrv(t *testing.T) *flowFakeSrv {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "recflow.db")
	recDir := filepath.Join(t.TempDir(), "recordings")
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := persistence.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	repos := persistence.NewRepos(db)
	bus := audio.NewPCMBus()
	return &flowFakeSrv{
		db:        db,
		repos:     repos,
		recDir:    recDir,
		bus:       bus,
		streams:   make(map[string]*stream.Stream),
		recorders: make(map[string]*audio.Recorder),
	}
}

func (s *flowFakeSrv) DeviceList() interface{}     { return []device.Device{} }
func (s *flowFakeSrv) StreamList() interface{}     { return []stream.Stream{} }
func (s *flowFakeSrv) MetricsSurface() interface{} { return map[string]int{} }
func (s *flowFakeSrv) PCMBus() *audio.PCMBus       { return s.bus }
func (s *flowFakeSrv) PushConfig(_ context.Context, _ string, _ control.SetConfig) (control.Message, error) {
	return nil, nil
}
func (s *flowFakeSrv) GetDeviceStatus(_ context.Context, _ string) (control.Message, error) {
	return nil, nil
}
func (s *flowFakeSrv) DeviceFinalStats(_ string) (*control.StreamStoppedStats, bool) {
	return nil, false
}
func (s *flowFakeSrv) RTPStreamStats(_ string) (rtp.Stats, bool) { return rtp.Stats{}, false }
func (s *flowFakeSrv) StreamPort(_ string) (uint16, bool)        { return 0, false }
func (s *flowFakeSrv) DeviceGet(_ string) (*device.Device, error) {
	return &device.Device{DeviceID: "d1", DisplayName: "d1", Status: "online"}, nil
}

func (s *flowFakeSrv) StartStream(_ context.Context, deviceID, purpose string, rec api.RecordingConfig) (map[string]any, error) {
	streamID := fmt.Sprintf("strm-%d", time.Now().UnixNano())
	st := stream.New(streamID, deviceID, 0, time.Now())
	_ = st.Start(time.Now())
	_ = st.DeviceCommandSent()
	_ = st.StreamStarted(time.Now())
	_ = st.FirstPacket(time.Now())
	s.streamMu.Lock()
	s.streams[streamID] = st
	s.streamMu.Unlock()

	if rec.Enabled {
		format := rec.Format
		if format == "" {
			format = "wav"
		}
		recorder, err := audio.NewRecorder(format, s.recDir, streamID, 48000, 2)
		if err != nil {
			return nil, err
		}
		if err := recorder.Begin(time.Now()); err != nil {
			return nil, err
		}
		s.bus.Subscribe(recorder)
		s.recMu.Lock()
		s.recorders[streamID] = recorder
		s.recMu.Unlock()
		recID := streamID + "-rec"
		_ = s.repos.Recordings.Create(recID, streamID, 48000, 2, "wav", time.Now())
	}
	return map[string]any{"stream_id": streamID, "state": string(st.State())}, nil
}

func (s *flowFakeSrv) StopStream(_ context.Context, streamID string) error {
	s.streamMu.RLock()
	st, ok := s.streams[streamID]
	s.streamMu.RUnlock()
	if !ok {
		return stream.ErrStreamNotFound
	}
	s.recMu.Lock()
	rec, hasRec := s.recorders[streamID]
	delete(s.recorders, streamID)
	s.recMu.Unlock()
	if hasRec {
		s.bus.Unsubscribe(rec)
		uri, n, err := rec.Finalize(time.Now())
		if err != nil {
			return err
		}
		recID := streamID + "-rec"
		_ = s.repos.Recordings.Finalize(recID, time.Now(), n, uri)
	}
	_ = st.Stopped()
	return nil
}

func (s *flowFakeSrv) GetStream(streamID string) (*stream.Stream, error) {
	s.streamMu.RLock()
	st, ok := s.streams[streamID]
	s.streamMu.RUnlock()
	if !ok {
		return nil, stream.ErrStreamNotFound
	}
	return st, nil
}

func (s *flowFakeSrv) GetRecording(recID string) (map[string]any, error) {
	row := s.db.QueryRow(`SELECT recording_id,stream_id,sample_rate,channels,codec,start_time,end_time,bytes_stored,uri FROM recordings WHERE recording_id=?`, recID)
	var rID, sID, codec string
	var sr, ch int
	var start, end sql.NullInt64
	var b int64
	var uri sql.NullString
	if err := row.Scan(&rID, &sID, &sr, &ch, &codec, &start, &end, &b, &uri); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("recording not found")
		}
		return nil, err
	}
	out := map[string]any{
		"recording_id": rID, "stream_id": sID, "sample_rate": sr,
		"channels": ch, "codec": codec, "bytes_stored": b,
	}
	if start.Valid {
		out["start_time"] = time.UnixMilli(start.Int64).UTC().Format(time.RFC3339)
	}
	if end.Valid {
		out["end_time"] = time.UnixMilli(end.Int64).UTC().Format(time.RFC3339)
	}
	if uri.Valid {
		out["file_uri"] = uri.String
	}
	return out, nil
}

func (s *flowFakeSrv) DownloadRecording(recID string) (string, error) {
	rec, err := s.GetRecording(recID)
	if err != nil {
		return "", err
	}
	uri, ok := rec["file_uri"].(string)
	if !ok || uri == "" {
		return "", fmt.Errorf("recording file not available")
	}
	return uri, nil
}

func (s *flowFakeSrv) ListRecordings() ([]map[string]any, error) {
	rows, err := s.db.Query(`SELECT recording_id,stream_id,sample_rate,channels,codec,start_time,end_time,bytes_stored,uri FROM recordings ORDER BY start_time DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []map[string]any
	for rows.Next() {
		var rID, sID, codec string
		var sr, ch int
		var start, end sql.NullInt64
		var b int64
		var uri sql.NullString
		if err := rows.Scan(&rID, &sID, &sr, &ch, &codec, &start, &end, &b, &uri); err != nil {
			return nil, err
		}
		results = append(results, map[string]any{"recording_id": rID, "stream_id": sID})
	}
	return results, nil
}

// TestRecordingFlowHTTP is the end-to-end HTTP test Jim requested for
// gap-recording-wireup: StartStream → StopStream → GET /api/recordings/{id} → /download.
func TestRecordingFlowHTTP(t *testing.T) {
	srv := newFlowFakeSrv(t)
	cfg := &config.Config{RecordingsDir: srv.recDir}
	mux := http.NewServeMux()
	api.RegisterRoutes(mux, cfg, srv)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 1. POST /api/devices/d1/stream with recording enabled.
	startBody := bytes.NewBufferString(`{"purpose":"test","recording":{"enabled":true,"format":"wav"}}`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ts.URL+"/api/devices/d1/stream", startBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("start stream = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var startResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	streamID, ok := startResp["stream_id"].(string)
	if !ok || streamID == "" {
		t.Fatalf("start response missing stream_id: %v", startResp)
	}

	// 2. DELETE /api/streams/{id} to stop.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, ts.URL+"/api/streams/"+streamID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stop stream = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// 3. GET /api/recordings/{id} — should return metadata with valid WAV file_uri.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ts.URL+"/api/recordings/"+streamID+"-rec", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /recordings = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var recBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &recBody); err != nil {
		t.Fatalf("decode recording metadata: %v", err)
	}
	for _, key := range []string{"recording_id", "stream_id", "codec", "sample_rate", "channels", "bytes_stored", "start_time", "end_time", "file_uri"} {
		if _, exists := recBody[key]; !exists {
			t.Fatalf("metadata missing key %q; got %v", key, recBody)
		}
	}
	if recBody["recording_id"] != streamID+"-rec" {
		t.Fatalf("recording_id = %v, want %v", recBody["recording_id"], streamID+"-rec")
	}
	if recBody["codec"] != "wav" {
		t.Fatalf("codec = %v, want wav", recBody["codec"])
	}
	uri, ok := recBody["file_uri"].(string)
	if !ok || uri == "" {
		t.Fatalf("missing file_uri in metadata: %v", recBody)
	}

	// 4. GET /api/recordings/{id}/download — return exact file bytes.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ts.URL+"/api/recordings/"+streamID+"-rec/download", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /download = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("Content-Type = %q, want audio/wav", ct)
	}
	bodyBytes, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("read download body: %v", err)
	}
	diskBytes, err := os.ReadFile(uri)
	if err != nil {
		t.Fatalf("read recording file: %v", err)
	}
	if !bytes.Equal(bodyBytes, diskBytes) {
		t.Fatalf("download bytes mismatch: got %d, want %d", len(bodyBytes), len(diskBytes))
	}

	// 5. Verify WAV header validity (RIFF....WAVE + fmt chunk + data chunk).
	if len(diskBytes) < 44 {
		t.Fatalf("recording file too small (%d bytes) to be a valid WAV", len(diskBytes))
	}
	if string(diskBytes[0:4]) != "RIFF" || string(diskBytes[8:12]) != "WAVE" || string(diskBytes[36:40]) != "data" {
		t.Fatalf("invalid WAV structure in %s", uri)
	}
}
