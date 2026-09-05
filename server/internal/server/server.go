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
	device  *device.Registry
	stream  *stream.Registry
	metrics *metrics.Metrics
	rtp     *rtp.Receiver
	bus     *audio.PCMBus
	ctrl    *control.SessionManager

	httpServer *http.Server
	controlLn  net.Listener
	streamsMu  sync.RWMutex

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
		cfg:     cfg,
		db:      db,
		device:  device.NewRegistry(),
		stream:  stream.NewRegistry(),
		metrics: m,
		rtp:     rtp.NewReceiver(m),
		bus:     audio.NewPCMBus(),
		ctrl:    control.NewSessionManager(),
		ctx:     ctx,
		cancel:  cancel,
	}
	return s, nil
}

// Start begins the control listener and returns when ctx is cancelled or an
// error occurs (spec §3). The HTTP API is owned by main.
//
// When cfg.TLSCertFile and cfg.TLSKeyFile are both set, the control listener
// is wrapped in TLS; otherwise it remains plain TCP (spec §19). This lets the
// same binary accept a real TLS handshake from an ESP32 device when certs are
// configured, and continue to serve plain TCP (LAN-mode) when they are not.
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
		// No credential hash stored for TOFU enrollment (credential is
		// validated against the shared secret if configured; otherwise open).
		s.device.Register(d, nil)
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
func (s *Server) StartStream(ctx context.Context, deviceID string, purpose string) (map[string]any, error) {
	// Verify device is connected
	if _, err := s.device.Get(deviceID); err != nil {
		return nil, fmt.Errorf("device not found: %w", err)
	}

	// Generate stream ID and SSRC
	streamID := newStreamID()
	ssrc := newSSRC()
	requestID := newRequestID()

	// Bind RTP port
	port, err := s.rtp.Bind(ctx, streamID, ssrc, 111) // PT 111 for Opus
	if err != nil {
		return nil, fmt.Errorf("bind RTP: %w", err)
	}

	// Create stream in CREATED state
	st := stream.New(streamID, deviceID, ssrc, time.Now())
	st.WithTimeoutConfig(stream.TimeoutConfig{
		RTPWait:      time.Duration(s.cfg.RTPWaitTimeoutS) * time.Second,
		RTPDisappear: 1 * time.Second,
	})
	s.stream.Add(st)

	// Transition to WAITING_FOR_DEVICE -> STARTING
	_ = st.Start(time.Now())
	_ = st.DeviceCommandSent()

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
	rtpCfg := control.RTPConfig{PayloadType: 111}

	startReq := control.NewStartStream(requestID, streamID, ssrc, dest, codec, rtpCfg)

	msg, err := s.ctrl.SendStartStream(ctx, deviceID, startReq)
	if err != nil {
		// Cleanup on error
		s.rtp.CloseStream(streamID)
		s.stream.Remove(streamID)
		return nil, fmt.Errorf("send start_stream: %w", err)
	}

	// Check reply
	switch r := msg.(type) {
	case *control.StreamStarted:
		// Device accepted, transition to RTP_WAIT
		_ = st.StreamStarted(time.Now())

		// Get the jitter buffer and start the audio worker
		binding, ok := s.rtp.GetStreamBinding(streamID)
		if !ok {
			s.rtp.CloseStream(streamID)
			s.stream.Remove(streamID)
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

		return map[string]any{
			"stream_id": streamID,
			"ssrc":      ssrc,
			"port":      port,
			"state":     string(st.State()),
		}, nil
	case *control.Error:
		// Device rejected
		_ = st.DeviceRejected(stream.FailureStartRejected)
		s.rtp.CloseStream(streamID)
		s.stream.Remove(streamID)
		return nil, fmt.Errorf("device rejected start_stream: %s", r.Message)
	default:
		// Unexpected reply
		_ = st.DeviceRejected(stream.FailureStartRejected)
		s.rtp.CloseStream(streamID)
		s.stream.Remove(streamID)
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
		return fmt.Errorf("send stop_stream: %w", err)
	}

	// Close RTP (this also cancels the worker via CloseStream)
	s.rtp.CloseStream(streamID)

	// Check reply
	switch r := msg.(type) {
	case *control.StreamStopped:
		_ = st.Stopped()
		return nil
	case *control.Error:
		_ = st.Stopped()
		return fmt.Errorf("device error on stop: %s", r.Message)
	default:
		_ = st.Stopped()
		return fmt.Errorf("unexpected reply type: %T", msg)
	}
}

// PCMBus returns the decoded-audio bus for live output (spec §14).
func (s *Server) PCMBus() *audio.PCMBus { return s.bus }

// newStreamID generates a random stream ID.
func newStreamID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("strm-%x", b[:])
}

// newSSRC generates a random SSRC.
func newSSRC() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// newRequestID generates a random request ID for correlation.
func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("req-%x", b[:])
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
