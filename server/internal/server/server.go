// Package server holds the end-to-end wiring of the audio server (spec §3,
// §14, §15). It ties config -> persistence -> registries -> control sessions ->
// stream lifecycle -> RTP receiver -> decoder -> PCM bus -> recorder + live.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"espmic/server/internal/api"
	"espmic/server/internal/audio"
	"espmic/server/internal/config"
	"espmic/server/internal/control"
	"espmic/server/internal/device"
	"espmic/server/internal/metrics"
	"espmic/server/internal/persistence"
	"espmic/server/internal/rtp"
	"espmic/server/internal/stream"
)

// Server is the top-level audio server (spec §3).
type Server struct {
	cfg     *config.Config
	db      *sql.DB
	repos   *persistence.Repos
	device  *device.Registry
	stream  *stream.Registry
	metrics *metrics.Metrics
	rtp     *rtp.Receiver
	bus     *audio.PCMBus
	ctrl    *control.SessionManager

	httpServer     *http.Server
	controlLn      net.Listener
	controlAddr    string
	controlLnReady chan struct{}
	streamsMu      sync.RWMutex
	recordings     map[string]*audio.Recorder // streamID -> Recorder
	recordingsMu   sync.Mutex
	recRepo        *persistence.RecordingRepo

	// metrics tracking for GAP-16
	metricsMu    sync.Mutex
	lastStats    map[string]rtp.Stats // streamID -> last Stats snapshot
	lastBitrate  map[string]int64     // streamID -> bytes received in last window
	lastBitrateT map[string]time.Time // streamID -> last bitrate calc time

	// device-final stats from stream_stopped (GAP-04/19)
	deviceStatsMu   sync.Mutex
	deviceStats     map[string]control.StreamStoppedStats
	deviceStatsRepo *persistence.DeviceStatsRepo

	ctx    context.Context
	cancel context.CancelFunc
}

// New builds a wired Server from a config, opening persistence and building
// registries (spec §3, §20).
func New(cfg *config.Config) (*Server, error) {
	ctx, cancel := context.WithCancel(context.Background())

	db, err := persistence.Open(cfg.DBPath)
	if err != nil {
		cancel()
		return nil, err
	}

	m := metrics.New()
	s := &Server{
		cfg:             cfg,
		db:              db,
		repos:           persistence.NewRepos(db),
		device:          device.NewRegistry(),
		stream:          stream.NewRegistry(),
		metrics:         m,
		rtp:             rtp.NewReceiver(m),
		bus:             audio.NewPCMBus(),
		ctrl:            control.NewSessionManager(),
		recordings:      make(map[string]*audio.Recorder),
		controlLnReady:  make(chan struct{}),
		lastStats:       make(map[string]rtp.Stats),
		lastBitrate:     make(map[string]int64),
		lastBitrateT:    make(map[string]time.Time),
		deviceStats:     make(map[string]control.StreamStoppedStats),
		recRepo:         persistence.NewRecordingRepo(db),
		deviceStatsRepo: persistence.NewDeviceStatsRepo(db),
		ctx:             ctx,
		cancel:          cancel,
	}
	return s, nil
}

// Restore loads persisted state from the DB into runtime registries (spec §20).
// Loads devices into the device registry and reconciles stale streams:
// previously ACTIVE/STARTING/RTP_WAIT streams are marked FAILED with
// FailureServerRestart. Safe to call once after New().
func (s *Server) Restore() error {
	// Load devices back into the registry so TOFU-enrolled devices are recognized.
	devices, err := s.repos.Devices.LoadAll()
	if err != nil {
		return fmt.Errorf("load devices: %w", err)
	}
	for _, rec := range devices {
		s.device.Register(rec.Device, rec.CredHash)
		slog.Info("restore: loaded device", "device_id", rec.Device.DeviceID, "status", rec.Device.Status)
	}

	// Load streams and reconcile: mark stale (live) streams as FAILED.
	streams, err := s.repos.Streams.LoadAll()
	if err != nil {
		return fmt.Errorf("load streams: %w", err)
	}
	for _, rec := range streams {
		switch stream.StreamState(rec.State) {
		case stream.StateActive, stream.StateStarting, stream.StateRTPWait:
			// Stale stream from before restart — mark FAILED.
			// Build the stream in CREATED, then use the dedicated restart
			// transition so the in-memory state agrees with what we persist.
			st := stream.New(rec.StreamID, rec.DeviceID, rec.SSRC, rec.Started)
			st.WithTimeoutConfig(stream.TimeoutConfig{
				RTPWait:      time.Duration(s.cfg.RTPWaitTimeoutS) * time.Second,
				RTPDisappear: 1 * time.Second,
			})
			if err := st.MarkServerRestartFailed(); err != nil {
				return fmt.Errorf("mark stream %s failed: %w", rec.StreamID, err)
			}
			s.stream.Add(st)
			// Persist the reconciled state.
			if err := s.repos.Streams.Save(rec.StreamID, rec.DeviceID, string(stream.StateFailed), string(stream.FailureServerRestart), rec.SSRC, rec.Started); err != nil {
				return fmt.Errorf("save stream %s: %w", rec.StreamID, err)
			}
			slog.Info("restore: marked stale stream failed", "stream_id", rec.StreamID, "device_id", rec.DeviceID, "was", rec.State)
		default:
			// COMPLETE/FAILED/CREATED/STOPPING — no runtime registration needed.
			slog.Info("restore: skipping non-live stream", "stream_id", rec.StreamID, "state", rec.State)
		}
	}
	return nil
}

// Start begins the control listener and returns when ctx is cancelled or an
// error occurs (spec §3). The HTTP API is owned by main.
//
// When cfg.TLSCertFile and cfg.TLSKeyFile are both set, the control listener
// is wrapped in TLS; otherwise it remains plain TCP (spec §19). This lets the
// same binary accept a real TLS handshake from an ESP32 device when certs are
// configured, and continue to serve plain TCP (LAN-mode) when they are not.
// ControlAddr returns the listener's bound address once the control
// listener is ready. It blocks until Start() has bound the listener,
// eliminating the test-only race of reading controlLn directly.
func (s *Server) ControlAddr() string {
	<-s.controlLnReady
	return s.controlAddr
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.ControlAddr)
	if err != nil {
		return err
	}
	s.controlLn = ln

	mode := "plain TCP"
	if s.cfg.TLSCertFile != "" && s.cfg.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		if err != nil {
			_ = ln.Close()
			return err
		}
		s.controlLn = tls.NewListener(ln, &tls.Config{
			Certificates: []tls.Certificate{cert},
		})
		mode = "TLS"
	}
	s.controlAddr = s.controlLn.Addr().String()
	close(s.controlLnReady)

	// Restore persisted state from a previous run (spec §20): load devices,
	// reconcile stale streams (mark FAILED with FailureServerRestart).
	if err := s.Restore(); err != nil {
		_ = ln.Close()
		return fmt.Errorf("restore: %w", err)
	}

	// Start the stream lifecycle monitor (GAP-13/14).
	// Polls every 500ms for streams that have timed out or disappeared.
	go s.streamMonitor()

	go func() {
		log.Printf("control listening on %s (%s)", s.cfg.ControlAddr, mode)
		s.controlLoop(s.controlLn)
	}()

	ctx, stop := signal.NotifyContext(s.ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	_ = ln.Close()
	s.cancel()
	return nil
}

func (s *Server) controlLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		slog.Debug("control: accepted connection", "remote", conn.RemoteAddr())
		sess := control.NewSession(conn, s, time.Now, nil)
		sess.SetOnMsg(s.ctrl.Handler())
		sess.SetOnReady(s.ctrl.OnReady)
		sess.SetOnClose(func(sess *control.Session) {
			s.OnDeviceDisconnect(sess.DeviceID())
		})
		go func() {
			defer s.ctrl.Unregister(sess.DeviceID())
			if err := sess.Run(s.ctx); err != nil {
				// Swallowed-error fix: log non-clean disconnects so auth/decode
				// failures become visible (why the device peer-closed).
				if !errors.Is(err, io.EOF) &&
					!errors.Is(err, net.ErrClosed) &&
					!errors.Is(err, context.Canceled) {
					slog.Warn("control session ended", "remote", conn.RemoteAddr(), "err", err)
				}
			}
		}()
	}
}

// Authenticate implements control.Authenticator (spec §7, §19).
// Trust-on-first-use (TOFU) enrollment with optional shared credential:
//   - If cfg.DeviceCredential is set, the presented credential must match it
//     (constant-time compare). Mismatch => auth error.
//   - Then: if device exists, accept; if not, register it (TOFU) and accept.
//   - Log first-time enrollments.
func (s *Server) Authenticate(ctx context.Context, deviceID, credential string) error {
	if s.cfg.DeviceCredential != "" {
		if subtle.ConstantTimeCompare([]byte(credential), []byte(s.cfg.DeviceCredential)) != 1 {
			return device.ErrAuthFailed
		}
	}
	_, err := s.device.Get(deviceID)
	if err == device.ErrDeviceNotFound {
		d := device.Device{
			DeviceID:    deviceID,
			DisplayName: deviceID,
			Status:      "online",
		}
		// Persist TOFU enrollment (spec §20 GAP-15).
		s.device.Register(d, nil)
		_ = s.repos.Devices.Save(d, nil)
		log.Printf("control: enrolled new device %q (TOFU)", deviceID)
		return nil
	}
	return err
}

// MetricsSurface returns the metrics snapshot for the HTTP endpoint (§18).
func (s *Server) MetricsSurface() interface{} {
	return s.metrics.Snapshot()
}

// StreamInfo is the per-stream view returned by GET /api/streams (spec §15).
type StreamInfo struct {
	StreamID        string  `json:"StreamID"`
	DeviceID        string  `json:"DeviceID"`
	SSRC            uint32  `json:"SSRC"`
	State           string  `json:"State"`
	StartedAt       string  `json:"StartedAt"`
	PacketsReceived uint64  `json:"PacketsReceived"`
	PacketsLost     uint64  `json:"PacketsLost"`
	JitterMS        float64 `json:"JitterMS"`
}

// DeviceList returns registered devices (§15).
func (s *Server) DeviceList() interface{} {
	return s.device.List()
}

// StreamList returns all active streams with per-stream RTP stats (§15).
func (s *Server) StreamList() interface{} {
	s.streamsMu.RLock()
	defer s.streamsMu.RUnlock()
	out := make([]StreamInfo, 0, len(s.stream.List()))
	for _, st := range s.stream.List() {
		info := StreamInfo{
			StreamID:  st.StreamID,
			DeviceID:  st.DeviceID,
			SSRC:      st.SSRC,
			State:     string(st.State()),
			StartedAt: st.StartedAt.UTC().Format(time.RFC3339),
		}
		if stats, ok := s.rtp.StreamStats(st.StreamID); ok {
			info.PacketsReceived = stats.Received
			info.PacketsLost = stats.Lost
			info.JitterMS = stats.JitterMS
		}
		out = append(out, info)
	}
	return out
}

// PushConfig sends a set_config command to a device's live control session and
// awaits the correlated status/error reply (spec §10 set_config). It returns
// control.ErrNotConnected if the device is offline.
func (s *Server) PushConfig(ctx context.Context, deviceID string, cfg control.SetConfig) (control.Message, error) {
	return s.ctrl.SendSetConfig(ctx, deviceID, &cfg)
}

// StartStream creates a new stream, binds RTP port, registers it, sends
// start_stream to the device, and awaits stream_started. On success, marks
// stream ACTIVE and returns stream info. On failure, cleans up and returns error.
func (s *Server) StartStream(ctx context.Context, deviceID string, purpose string, rec api.RecordingConfig) (map[string]any, error) {
	// Verify device is connected
	if _, err := s.device.Get(deviceID); err != nil {
		return nil, fmt.Errorf("device not found: %w", err)
	}

	// Generate stream ID (SSRC is device-chosen, learned by the RTP receiver per spec §8)
	streamID := newStreamID()
	requestID := newRequestID()

	// Bind RTP port
	port, err := s.rtp.Bind(ctx, streamID, rtp.DefaultPayloadType, time.Duration(s.cfg.JitterTargetMS)*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("bind RTP: %w", err)
	}

	// Create stream in CREATED state
	// SSRC is 0 at creation; the device chooses it and the receiver learns it from the first RTP packet (spec §8).
	st := stream.New(streamID, deviceID, 0, time.Now())
	st.WithTimeoutConfig(stream.TimeoutConfig{
		RTPWait:      time.Duration(s.cfg.RTPWaitTimeoutS) * time.Second,
		RTPDisappear: 1 * time.Second,
	})
	s.stream.Add(st)

	// Transition to WAITING_FOR_DEVICE -> STARTING
	_ = st.Start(time.Now())
	_ = st.DeviceCommandSent()
	_ = s.repos.Streams.Save(streamID, deviceID, string(stream.StateStarting), "", 0, st.StartedAt)

	// Determine the server IP the device connected to (from the control session)
	// We don't have direct access to the session here, so use a best-effort:
	// if ControlAddr is a specific IP, use that; otherwise use localhost fallback
	serverIP := "127.0.0.1"
	if s.cfg.ControlAddr != "" {
		host, _, err := net.SplitHostPort(s.cfg.ControlAddr)
		if err == nil && host != "" && host != "0.0.0.0" && host != "::" {
			serverIP = host
		}
	}

	// Send start_stream to device with full spec §11 schema
	dest := control.Destination{IP: serverIP, Port: port}
	codec := control.Codec{
		Name:       "opus",
		SampleRate: 48000,
		Channels:   2,
		FrameMS:    20,
		Bitrate:    128000,
		VBR:        true,
		FEC:        false,
		DTX:        false,
	}
	rtpCfg := control.RTPConfig{PayloadType: rtp.DefaultPayloadType}

	startReq := control.NewStartStream(requestID, streamID, dest, codec, rtpCfg)

	msg, err := s.ctrl.SendStartStream(ctx, deviceID, startReq)
	if err != nil {
		// Cleanup on error
		s.rtp.CloseStream(streamID)
		s.stream.Remove(streamID)
		_ = s.repos.Streams.Save(streamID, deviceID, string(stream.StateFailed), string(stream.FailureStartRejected), 0, time.Now())
		return nil, fmt.Errorf("send start_stream: %w", err)
	}

	// Check reply
	switch r := msg.(type) {
	case *control.StreamStarted:
		// Device accepted, transition to RTP_WAIT
		_ = st.StreamStarted(time.Now())
		_ = s.repos.Streams.Save(streamID, deviceID, string(stream.StateRTPWait), "", 0, st.StartedAt)

		// Get the jitter buffer and start the audio worker
		binding, ok := s.rtp.GetStreamBinding(streamID)
		if !ok {
			s.rtp.CloseStream(streamID)
			s.stream.Remove(streamID)
			_ = s.repos.Streams.Save(streamID, deviceID, string(stream.StateFailed), string(stream.FailureStartRejected), 0, time.Now())
			return nil, fmt.Errorf("stream binding not found after start")
		}
		jb := binding.JitterBuffer()

		// Create and start the audio worker
		dec := audio.NewPionDecoder()
		dec.Reset()

		workerCtx, workerCancel := context.WithCancel(context.Background())
		s.rtp.SetWorkerCancel(streamID, workerCancel)

		worker := audio.NewWorker(streamID, jb, dec, s.bus, s.metrics, func(first bool) {
			// Called for each packet dequeued from jitter buffer
			// (not gated on successful decode) — spec §17: RTP_WAIT->ACTIVE on first packet
			if first {
				_ = st.FirstPacket(time.Now())
			} else {
				st.Packet(time.Now())
			}
		})
		go worker.Start(workerCtx)

		// If recording enabled, create and start recorder (subscribed to PCM bus)
		if rec.Enabled {
			format := rec.Format
			if format == "" {
				format = "wav"
			}
			recorder, err := audio.NewRecorder(format, s.cfg.RecordingsDir, streamID, 48000, 2)
			if err != nil {
				slog.Error("recorder: failed to create", "stream_id", streamID, "err", err)
			} else {
				if err := recorder.Begin(time.Now()); err != nil {
					slog.Error("recorder: failed to begin", "stream_id", streamID, "err", err)
				} else {
					s.bus.Subscribe(recorder)
					s.recordingsMu.Lock()
					s.recordings[streamID] = recorder
					s.recordingsMu.Unlock()
					// Persist recording metadata
					recID := streamID + "-rec"
					_ = s.recRepo.Create(recID, streamID, 48000, 2, "opus", time.Now())
				}
			}
		}

		return map[string]any{
			"stream_id": streamID,
			"port":      port,
			"state":     string(st.State()),
		}, nil
	case *control.Error:
		// Device rejected
		_ = st.DeviceRejected(stream.FailureStartRejected)
		_ = s.repos.Streams.Save(streamID, deviceID, string(stream.StateFailed), string(stream.FailureStartRejected), 0, time.Now())
		s.rtp.CloseStream(streamID)
		s.stream.Remove(streamID)
		return nil, fmt.Errorf("device rejected start_stream: %s", r.Message)
	default:
		// Unexpected reply
		_ = st.DeviceRejected(stream.FailureStartRejected)
		s.rtp.CloseStream(streamID)
		s.stream.Remove(streamID)
		_ = s.repos.Streams.Save(streamID, deviceID, string(stream.StateFailed), string(stream.FailureStartRejected), 0, time.Now())
		return nil, fmt.Errorf("unexpected reply type: %T", msg)
	}
}

// StopStream sends stop_stream to the device, closes RTP, and marks stream COMPLETE.
func (s *Server) StopStream(ctx context.Context, streamID string) error {
	st, err := s.stream.Get(streamID)
	if err != nil {
		return err
	}

	// Must be ACTIVE to stop
	if st.State() != stream.StateActive {
		return fmt.Errorf("stream %s not active (state=%s)", streamID, st.State())
	}

	_ = st.StopRequested()

	// Send stop_stream to device
	requestID := newRequestID()
	stopReq := control.NewStopStream(requestID, streamID)
	msg, err := s.ctrl.SendStopStream(ctx, st.DeviceID, stopReq)
	if err != nil {
		// Still close RTP and mark stopped
		s.rtp.CloseStream(streamID)
		_ = st.Stopped()
		s.finalizeRecorder(streamID)
		_ = s.repos.Streams.Save(streamID, st.DeviceID, string(stream.StateComplete), "", 0, time.Now())
		return fmt.Errorf("send stop_stream: %w", err)
	}

	// Close RTP (this also cancels the worker via CloseStream)
	s.rtp.CloseStream(streamID)

	// Finalize recorder (if any) before marking stream stopped
	s.finalizeRecorder(streamID)

	// Check reply
	switch r := msg.(type) {
	case *control.StreamStopped:
		if r.Stats != nil {
			s.deviceStatsMu.Lock()
			s.deviceStats[streamID] = *r.Stats
			s.deviceStatsMu.Unlock()
			var extraBytes []byte
			if len(r.Stats.Extra) > 0 {
				if b, err := json.Marshal(r.Stats.Extra); err == nil {
					extraBytes = b
				}
			}
			_ = s.deviceStatsRepo.Save(streamID, st.DeviceID, r.Stats.PacketsSent, r.Stats.BytesSent, r.Stats.DurationMS, r.Stats.EncoderErrors, extraBytes)
		}
		_ = st.Stopped()
		_ = s.repos.Streams.Save(streamID, st.DeviceID, string(stream.StateComplete), "", 0, time.Now())
		return nil
	case *control.Error:
		_ = st.Stopped()
		_ = s.repos.Streams.Save(streamID, st.DeviceID, string(stream.StateFailed), string(stream.FailureStartRejected), 0, time.Now())
		return fmt.Errorf("device error on stop: %s", r.Message)
	default:
		_ = st.Stopped()
		return fmt.Errorf("unexpected reply type: %T", msg)
	}
}

// finalizeRecorder finalizes and removes the recorder for a stream (idempotent).
func (s *Server) finalizeRecorder(streamID string) {
	s.recordingsMu.Lock()
	rec, ok := s.recordings[streamID]
	if !ok {
		s.recordingsMu.Unlock()
		return
	}
	delete(s.recordings, streamID)
	s.recordingsMu.Unlock()

	s.bus.Unsubscribe(rec)
	uri, bytes, err := rec.Finalize(time.Now())
	if err != nil {
		slog.Error("recorder: finalize failed", "stream_id", streamID, "err", err)
		return
	}
	recID := streamID + "-rec"
	_ = s.recRepo.Finalize(recID, time.Now(), bytes, uri)
}

// PCMBus returns the decoded-audio bus for live output (spec §14).
func (s *Server) PCMBus() *audio.PCMBus { return s.bus }

// GetStream returns the stream for the given ID.
func (s *Server) GetStream(streamID string) (*stream.Stream, error) {
	return s.stream.Get(streamID)
}

// DeviceGet returns the device for the given ID.
func (s *Server) DeviceGet(deviceID string) (*device.Device, error) {
	d, err := s.device.Get(deviceID)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// RTPStreamStats returns the RTP statistics for a stream.
func (s *Server) RTPStreamStats(streamID string) (rtp.Stats, bool) {
	return s.rtp.StreamStats(streamID)
}

// DeviceFinalStats returns the device-final stats from stream_stopped for a stream.
func (s *Server) DeviceFinalStats(streamID string) (*control.StreamStoppedStats, bool) {
	s.deviceStatsMu.Lock()
	stats, ok := s.deviceStats[streamID]
	s.deviceStatsMu.Unlock()
	if ok {
		return &stats, true
	}

	// Try loading from persistence
	if s.deviceStatsRepo != nil {
		_, packetsSent, bytesSent, durationMS, encoderErrors, extraJSON, err := s.deviceStatsRepo.Load(streamID)
		if err == nil {
			loaded := control.StreamStoppedStats{
				PacketsSent:   packetsSent,
				BytesSent:     bytesSent,
				DurationMS:    durationMS,
				EncoderErrors: encoderErrors,
			}
			if len(extraJSON) > 0 {
				var extra map[string]any
				if err := json.Unmarshal(extraJSON, &extra); err == nil {
					loaded.Extra = extra
				}
			}
			s.deviceStatsMu.Lock()
			s.deviceStats[streamID] = loaded
			s.deviceStatsMu.Unlock()
			return &loaded, true
		}
	}
	return nil, false
}

// StreamPort returns the UDP port for a stream.
func (s *Server) StreamPort(streamID string) (uint16, bool) {
	return s.rtp.StreamPort(streamID)
}

// GetDeviceStatus sends a get_status command to the device's live control
// session and awaits the correlated status (success) or error (rejection).
func (s *Server) GetDeviceStatus(ctx context.Context, deviceID string) (control.Message, error) {
	// Verify device is connected
	if _, err := s.device.Get(deviceID); err != nil {
		return nil, fmt.Errorf("device not found: %w", err)
	}

	requestID := newRequestID()
	req := control.NewGetStatus(requestID)

	return s.ctrl.SendGetStatus(ctx, deviceID, req)
}

func newStreamID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("strm-%x", b[:])
}

// newRequestID generates a random request ID for correlation.
func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("req-%x", b[:])
}

// streamMonitor polls for stream lifecycle timeouts (GAP-13/14) and
// pushes per-stream RTP metrics to the global metrics surface (GAP-16).
// Runs every 500ms and checks:
//   - RTP_WAIT streams that have exceeded their wait timeout -> FAILED (RTP_WAIT->TIMEOUT)
//   - ACTIVE streams that have disappeared (no RTP packets) -> FAILED (ACTIVE->RTP_TIMEOUT)
//   - For ACTIVE streams: collect Stats from jitter buffer, compute deltas, push to Metrics
func (s *Server) streamMonitor() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			// Collect active stream IDs under registry lock (avoid nested locks)
			var activeIDs []string
			s.stream.ForEach(func(st *stream.Stream) {
				if st.State() == stream.StateActive {
					activeIDs = append(activeIDs, st.StreamID)
				}
			})

			// Push metrics for active streams (no registry lock held)
			for _, id := range activeIDs {
				if stats, ok := s.rtp.StreamStats(id); ok {
					s.pushStreamMetrics(id, stats, now)
				}
			}

			// Stream lifecycle timeout checks
			var toCleanup []string
			s.stream.ForEach(func(st *stream.Stream) {
				if st.RTPWaitTimedOut(now) {
					slog.Debug("stream: RTP wait timeout", "stream_id", st.StreamID, "device_id", st.DeviceID)
					_ = st.RTPWaitTimeout(now)
					toCleanup = append(toCleanup, st.StreamID)
				} else if st.RTPDisappeared(now) {
					slog.Debug("stream: RTP disappeared", "stream_id", st.StreamID, "device_id", st.DeviceID)
					_ = st.RTPTimeout(now)
					toCleanup = append(toCleanup, st.StreamID)
				}
			})
			for _, id := range toCleanup {
				s.cleanupStreamByID(id)
			}
		}
	}
}

// pushStreamMetrics computes deltas from jitter buffer Stats and pushes
// them to the global Metrics. Called from streamMonitor (no locks held).
func (s *Server) pushStreamMetrics(streamID string, stats rtp.Stats, now time.Time) {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()

	last := s.lastStats[streamID]

	// Lost
	if delta := stats.Lost - last.Lost; delta > 0 {
		s.metrics.AddRTPPacketsLost(int(delta))
	}
	// Duplicate
	if delta := stats.Duplicate - last.Duplicate; delta > 0 {
		for i := uint64(0); i < delta; i++ {
			s.metrics.IncRTPPacketsDuplicate()
		}
	}
	// Reordered
	if delta := stats.Reordered - last.Reordered; delta > 0 {
		for i := uint64(0); i < delta; i++ {
			s.metrics.IncRTPPacketsReordered()
		}
	}
	// Late
	if delta := stats.Late - last.Late; delta > 0 {
		for i := uint64(0); i < delta; i++ {
			s.metrics.IncRTPPacketsLate()
		}
	}
	// Jitter (gauge)
	s.metrics.SetRTPJitterMS(stats.JitterMS)

	// Bitrate: sliding window over ~5s
	lastTime := s.lastBitrateT[streamID]
	// Estimate bytes from received packets * typical Opus frame size (~300 bytes avg)
	// Since we don't track exact bytes, approximate: 1 packet ≈ 300 bytes
	receivedDelta := stats.Received - last.Received
	if receivedDelta > 0 {
		estBytes := int64(receivedDelta) * 300
		s.lastBitrate[streamID] = estBytes
		s.lastBitrateT[streamID] = now
	}
	if !lastTime.IsZero() {
		window := now.Sub(lastTime).Seconds()
		if window > 0 {
			bitrate := int64(float64(s.lastBitrate[streamID]) * 8 / window)
			s.metrics.SetRTPBitrateBPS(bitrate)
		}
	}

	// Update last stats
	s.lastStats[streamID] = stats
}

// OnDeviceDisconnect is called when a control session ends (after hello_ack).
// Fails all ACTIVE streams for the disconnected device (spec §17: ACTIVE->DEVICE_DISCONNECTED).
func (s *Server) OnDeviceDisconnect(deviceID string) {
	if deviceID == "" {
		return
	}
	var toCleanup []string
	s.stream.ForEach(func(st *stream.Stream) {
		if st.DeviceID == deviceID && st.State() == stream.StateActive {
			slog.Debug("stream: device disconnected, failing stream", "stream_id", st.StreamID, "device_id", deviceID)
			_ = st.DeviceDisconnected()
			toCleanup = append(toCleanup, st.StreamID)
		}
	})
	for _, id := range toCleanup {
		s.cleanupStreamByID(id)
	}
}

// cleanupStreamByID closes RTP resources and removes the stream from the registry by ID.
// Idempotent — safe if stream already gone (e.g., double teardown from monitor + disconnect race).
// Also finalizes any associated recorder and cleans up metrics tracking.
func (s *Server) cleanupStreamByID(streamID string) {
	s.rtp.CloseStream(streamID)
	s.stream.Remove(streamID)
	s.finalizeRecorder(streamID)

	// Clean up metrics tracking for this stream (prevents leak/stale deltas)
	s.metricsMu.Lock()
	delete(s.lastStats, streamID)
	delete(s.lastBitrate, streamID)
	delete(s.lastBitrateT, streamID)
	s.metricsMu.Unlock()
}

// GetRecording returns recording metadata by recording ID.
func (s *Server) GetRecording(recordingID string) (map[string]any, error) {
	// Query the recording from the database
	row := s.db.QueryRow(
		`SELECT recording_id,stream_id,sample_rate,channels,codec,start_time,end_time,bytes_stored,uri
		 FROM recordings WHERE recording_id=?`, recordingID)
	var recID, streamID, codec string
	var sampleRate, channels int
	var startTime, endTime sql.NullInt64
	var bytesStored int64
	var uri sql.NullString
	if err := row.Scan(&recID, &streamID, &sampleRate, &channels, &codec, &startTime, &endTime, &bytesStored, &uri); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("recording not found")
		}
		return nil, err
	}
	result := map[string]any{
		"recording_id": recID,
		"stream_id":    streamID,
		"sample_rate":  sampleRate,
		"channels":     channels,
		"codec":        codec,
		"bytes_stored": bytesStored,
	}
	if startTime.Valid {
		result["start_time"] = time.UnixMilli(startTime.Int64).UTC().Format(time.RFC3339)
	}
	if endTime.Valid {
		result["end_time"] = time.UnixMilli(endTime.Int64).UTC().Format(time.RFC3339)
	}
	if uri.Valid {
		result["uri"] = uri.String
	}
	return result, nil
}

// DownloadRecording returns the file path for a recording.
func (s *Server) DownloadRecording(recordingID string) (string, error) {
	rec, err := s.GetRecording(recordingID)
	if err != nil {
		return "", err
	}
	uri, ok := rec["uri"].(string)
	if !ok || uri == "" {
		return "", fmt.Errorf("recording file not available")
	}
	return uri, nil
}

func (s *Server) Close() error {
	s.cancel()
	if s.httpServer != nil {
		_ = s.httpServer.Close()
	}
	if s.controlLn != nil {
		_ = s.controlLn.Close()
	}
	return s.db.Close()
}
