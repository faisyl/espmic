package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
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

func generateSelfSignedCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certOut, err := os.CreateTemp(dir, "cert*.pem")
	if err != nil {
		t.Fatal(err)
	}
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	certOut.Close()
	keyOut, err := os.CreateTemp(dir, "key*.pem")
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	keyOut.Close()
	return certOut.Name(), keyOut.Name()
}

func TestStartControlListenerTLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := generateSelfSignedCert(t, dir)
	cfg := config.Load()
	cfg.ControlAddr = "localhost:0"
	cfg.TLSCertFile = certFile
	cfg.TLSKeyFile = keyFile
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Start() }()
	addr := srv.ControlAddr()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	_ = conn.Close()
	srv.cancel()
}

func TestStartControlListenerPlainTCP(t *testing.T) {
	cfg := config.Load()
	cfg.ControlAddr = "localhost:0"
	cfg.TLSCertFile = ""
	cfg.TLSKeyFile = ""
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Start() }()
	addr := srv.ControlAddr()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	_ = conn.Close()
	srv.cancel()
}

func TestAuthenticateOpenEnrollment(t *testing.T) {
	cfg := config.Load()
	cfg.DeviceCredential = "" // open enrollment
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Unknown device with no credential configured -> accepted (TOFU)
	if err := srv.Authenticate(ctx, "esp32-new", ""); err != nil {
		t.Fatalf("Authenticate open TOFU: %v", err)
	}
	// Device should now be registered
	devs := srv.DeviceList().([]device.Device)
	if len(devs) != 1 || devs[0].DeviceID != "esp32-new" {
		t.Fatalf("expected device enrolled, got %v", devs)
	}

	// Same device again -> accepted
	if err := srv.Authenticate(ctx, "esp32-new", ""); err != nil {
		t.Fatalf("Authenticate existing: %v", err)
	}

	// Another new device -> accepted
	if err := srv.Authenticate(ctx, "esp32-other", "anything"); err != nil {
		t.Fatalf("Authenticate second TOFU: %v", err)
	}
	devs = srv.DeviceList().([]device.Device)
	if len(devs) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(devs))
	}
}

func TestAuthenticateWithCredential(t *testing.T) {
	cfg := config.Load()
	cfg.DeviceCredential = "shared-secret-123"
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Correct credential -> accepted + enrolled
	if err := srv.Authenticate(ctx, "esp32-cred", "shared-secret-123"); err != nil {
		t.Fatalf("Authenticate correct cred: %v", err)
	}
	devs := srv.DeviceList().([]device.Device)
	if len(devs) != 1 || devs[0].DeviceID != "esp32-cred" {
		t.Fatalf("expected device enrolled, got %v", devs)
	}

	// Wrong credential -> rejected
	if err := srv.Authenticate(ctx, "esp32-wrong", "wrong-secret"); err != device.ErrAuthFailed {
		t.Fatalf("expected ErrAuthFailed, got %v", err)
	}
	// Device not enrolled
	devs = srv.DeviceList().([]device.Device)
	if len(devs) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devs))
	}

	// Empty credential -> rejected (constant-time compare with empty string)
	if err := srv.Authenticate(ctx, "esp32-empty", ""); err != device.ErrAuthFailed {
		t.Fatalf("expected ErrAuthFailed for empty cred, got %v", err)
	}

	// Existing device with correct credential -> accepted
	if err := srv.Authenticate(ctx, "esp32-cred", "shared-secret-123"); err != nil {
		t.Fatalf("Authenticate existing with cred: %v", err)
	}
}

func TestAuthenticateConstantTimeCompare(t *testing.T) {
	// Verify constant-time compare behavior (no timing leak in test, just correctness)
	cfg := config.Load()
	cfg.DeviceCredential = "secret"
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Different length should fail (constant-time compare handles this)
	if err := srv.Authenticate(ctx, "dev1", "secret-extra"); err != device.ErrAuthFailed {
		t.Fatalf("expected ErrAuthFailed for longer string")
	}
	if err := srv.Authenticate(ctx, "dev2", "secr"); err != device.ErrAuthFailed {
		t.Fatalf("expected ErrAuthFailed for shorter string")
	}
}

// TestAudioPipelineEndToEnd verifies the decode pipeline wiring:
// RTP -> jitter buffer -> decoder -> PCM bus + stream state RTP_WAIT -> ACTIVE.
// Uses a StubDecoder to avoid Opus encoding complexity in-test (per god refinement #2).
func TestAudioPipelineEndToEnd(t *testing.T) {
	cfg := config.Load()
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Bind RTP directly (bypass control handshake per god refinement #2)
	streamID := "test-stream"
	ssrc := uint32(0x12345678)
	_, err = srv.rtp.Bind(ctx, streamID, 111, 60*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("bind RTP: %v", err)
	}
	defer srv.rtp.CloseStream(streamID)

	// Get the jitter buffer
	binding, ok := srv.rtp.GetStreamBinding(streamID)
	if !ok {
		t.Fatal("stream binding not found")
	}
	jb := binding.JitterBuffer()

	// Create stream and add to registry
	st := stream.New(streamID, "test-device", ssrc, time.Now())
	st.WithTimeoutConfig(stream.TimeoutConfig{
		RTPWait:      5 * time.Second,
		RTPDisappear: 1 * time.Second,
	})
	srv.stream.Add(st)
	defer srv.stream.Remove(streamID)

	// Start -> WAITING_FOR_DEVICE -> STARTING -> RTP_WAIT
	_ = st.Start(time.Now())
	_ = st.DeviceCommandSent()
	_ = st.StreamStarted(time.Now())
	if st.State() != stream.StateRTPWait {
		t.Fatalf("expected RTP_WAIT, got %s", st.State())
	}

	// Use a StubDecoder to avoid Opus encoding complexity (per god refinement #2)
	dec := audio.NewStubDecoder(960, 2) // 960 samples per channel at 48kHz/20ms

	// Collect PCM frames from the bus
	var framesMu sync.Mutex
	var frames []*audio.DecodedAudioFrame
	srv.bus.Subscribe(&audioTestListener{onPCM: func(f *audio.DecodedAudioFrame) {
		framesMu.Lock()
		frames = append(frames, f)
		framesMu.Unlock()
	}})
	defer srv.bus.Unsubscribe(&audioTestListener{})

	// Create and start worker
	workerCtx, workerCancel := context.WithCancel(ctx)
	srv.rtp.SetWorkerCancel(streamID, workerCancel)

	worker := audio.NewWorker(streamID, binding.JitterBuffer(), dec, srv.bus, srv.metrics, func(first bool) {
		if first {
			_ = st.FirstPacket(time.Now())
		} else {
			st.Packet(time.Now())
		}
	})
	go worker.Start(workerCtx)
	defer workerCancel()

	// Push a packet directly to the jitter buffer (bypassing UDP for test simplicity)
	p := rtp.Packet{
		Version:        2,
		PayloadType:    111,
		SequenceNumber: 100,
		Timestamp:      0,
		SSRC:           ssrc,
		Payload:        []byte{0x00, 0x00}, // minimal Opus packet (stub decodes to silence)
	}
	jb.Push(p, time.Now())

	// Wait for worker to process
	time.Sleep(100 * time.Millisecond)

	// Check stream state reached ACTIVE
	if st.State() != stream.StateActive {
		t.Fatalf("expected ACTIVE, got %s", st.State())
	}

	// Check PCM frames received on bus
	framesMu.Lock()
	frameCount := len(frames)
	framesMu.Unlock()
	if frameCount == 0 {
		t.Fatal("expected PCM frames on bus, got 0")
	}
	t.Logf("Received %d PCM frames on bus", frameCount)
}

// audioTestListener implements audio.PCMListener for testing.
type audioTestListener struct {
	onPCM func(*audio.DecodedAudioFrame)
}

func (l *audioTestListener) OnPCM(f *audio.DecodedAudioFrame) {
	if l.onPCM != nil {
		l.onPCM(f)
	}
}

// TestStreamMonitorRTPWaitTimeout verifies the streamMonitor transitions
// RTP_WAIT -> FAILED (RTP_WAIT->TIMEOUT) when no RTP packets arrive within
// the configured RTPWait timeout.
func TestStreamMonitorRTPWaitTimeout(t *testing.T) {
	cfg := config.Load()
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the server (starts streamMonitor)
	go srv.streamMonitor() // monitor only; avoids fixed-port bind races across tests
	defer srv.cancel()

	// Create stream with short RTPWait timeout (100ms)
	streamID := "test-stream-rtpwait"
	st := stream.New(streamID, "test-device", 0, time.Now())
	st.WithTimeoutConfig(stream.TimeoutConfig{
		RTPWait:      100 * time.Millisecond,
		RTPDisappear: 1 * time.Second,
	})
	srv.stream.Add(st)

	// Start the stream to RTP_WAIT state
	_ = st.Start(time.Now())
	_ = st.DeviceCommandSent()
	_ = st.StreamStarted(time.Now())
	if st.State() != stream.StateRTPWait {
		t.Fatalf("expected RTP_WAIT, got %s", st.State())
	}

	// Wait for monitor to trigger timeout (poll every 500ms, so wait ~1s)
	time.Sleep(1500 * time.Millisecond)

	// Stream should be transitioned to FAILED and cleaned up
	if st.State() != stream.StateFailed {
		t.Fatalf("expected FAILED after RTP wait timeout, got %s", st.State())
	}
	if st.Reason != stream.FailureRTPWaitTimeout {
		t.Fatalf("expected FailureRTPWaitTimeout, got %s", st.Reason)
	}
	// Stream should be removed from registry
	if _, err := srv.stream.Get(streamID); err != stream.ErrStreamNotFound {
		t.Fatalf("expected stream removed from registry")
	}
}

// TestStreamMonitorRTPDisappeared verifies the streamMonitor transitions
// ACTIVE -> FAILED (ACTIVE->RTP_TIMEOUT) when RTP packets stop arriving
// for the configured RTPDisappear timeout.
func TestStreamMonitorRTPDisappeared(t *testing.T) {
	cfg := config.Load()
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the server (starts streamMonitor)
	go srv.streamMonitor() // monitor only; avoids fixed-port bind races across tests
	defer srv.cancel()

	// Create stream with short RTPDisappear timeout (50ms)
	streamID := "test-stream-rtptimeout"
	st := stream.New(streamID, "test-device", 0, time.Now())
	st.WithTimeoutConfig(stream.TimeoutConfig{
		RTPWait:      5 * time.Second,
		RTPDisappear: 50 * time.Millisecond,
	})
	srv.stream.Add(st)

	// Start the stream to ACTIVE state
	_ = st.Start(time.Now())
	_ = st.DeviceCommandSent()
	_ = st.StreamStarted(time.Now())
	_ = st.FirstPacket(time.Now()) // transition to ACTIVE
	if st.State() != stream.StateActive {
		t.Fatalf("expected ACTIVE, got %s", st.State())
	}

	// Wait for monitor to trigger disappearance timeout (poll every 500ms, wait ~1s)
	time.Sleep(1500 * time.Millisecond)

	// Stream should be transitioned to FAILED and cleaned up
	if st.State() != stream.StateFailed {
		t.Fatalf("expected FAILED after RTP disappear timeout, got %s", st.State())
	}
	if st.Reason != stream.FailureRTPTimeout {
		t.Fatalf("expected FailureRTPTimeout, got %s", st.Reason)
	}
	// Stream should be removed from registry
	if _, err := srv.stream.Get(streamID); err != stream.ErrStreamNotFound {
		t.Fatalf("expected stream removed from registry")
	}
}

// TestOnDeviceDisconnectFailsActiveStreams verifies that when a device
// disconnects, its active streams are failed with DEVICE_DISCONNECTED.
func TestOnDeviceDisconnectFailsActiveStreams(t *testing.T) {
	cfg := config.Load()
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Create active stream for device
	streamID := "test-stream-disconnect"
	st := stream.New(streamID, "test-device", 0, time.Now())
	srv.stream.Add(st)

	_ = st.Start(time.Now())
	_ = st.DeviceCommandSent()
	_ = st.StreamStarted(time.Now())
	_ = st.FirstPacket(time.Now())
	if st.State() != stream.StateActive {
		t.Fatalf("expected ACTIVE, got %s", st.State())
	}

	// Simulate device disconnect via OnDeviceDisconnect
	srv.OnDeviceDisconnect("test-device")

	// Stream should be FAILED with DEVICE_DISCONNECTED
	if st.State() != stream.StateFailed {
		t.Fatalf("expected FAILED after device disconnect, got %s", st.State())
	}
	if st.Reason != stream.FailureDeviceDisc {
		t.Fatalf("expected FailureDeviceDisc, got %s", st.Reason)
	}
	// Stream should be removed from registry
	if _, err := srv.stream.Get(streamID); err != stream.ErrStreamNotFound {
		t.Fatalf("expected stream removed from registry")
	}
}

// TestCleanupStreamByIDIdempotent verifies cleanupStreamByID is idempotent
// and safe to call twice (e.g., monitor + disconnect race).
func TestCleanupStreamByIDIdempotent(t *testing.T) {
	cfg := config.Load()
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Create and register a stream
	streamID := "test-stream-idempotent"
	st := stream.New(streamID, "test-device", 0, time.Now())
	srv.stream.Add(st)

	// First cleanup
	srv.cleanupStreamByID(streamID)

	// Stream should be removed
	if _, err := srv.stream.Get(streamID); err != stream.ErrStreamNotFound {
		t.Fatalf("expected stream removed after first cleanup")
	}

	// Second cleanup should not panic
	srv.cleanupStreamByID(streamID)

	// Third cleanup also safe
	srv.cleanupStreamByID(streamID)
}

func TestStopStreamConsumesAndPersistsStats(t *testing.T) {
	cfg := config.Load()
	cfg.DBPath = ":memory:"
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// Connect fake device session via net.Pipe
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	sess := control.NewSession(serverConn, srv, time.Now, nil)
	sess.SetOnMsg(srv.ctrl.Handler())
	sess.SetOnReady(srv.ctrl.OnReady)
	go func() {
		_ = sess.Run(context.Background())
	}()

	// Device sends hello
	hello := control.NewHello("test-device", "", "v1.0.0", nil)
	if err := control.WriteMessage(clientConn, hello); err != nil {
		t.Fatal(err)
	}
	// Read hello_ack
	frame, err := control.ReadFrame(clientConn)
	if err != nil {
		t.Fatalf("read hello_ack: %v", err)
	}
	ackMsg, err := control.DecodePayload(frame)
	if err != nil || ackMsg.Kind() != control.TypeHelloAck {
		t.Fatalf("expected hello_ack, got %v (%v)", ackMsg, err)
	}

	// Wait for the session to be registered as connected (avoids
	// the flaky race where OnReady fires after StopStream runs).
	deadline := time.After(1 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if srv.ctrl.IsConnected("test-device") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for test-device to connect")
		case <-ticker.C:
		}
	}

	// Register stream
	streamID := "test-stream-stopstats"
	st := stream.New(streamID, "test-device", 1234, time.Now())
	_ = st.Start(time.Now())
	_ = st.DeviceCommandSent()
	_ = st.StreamStarted(time.Now())
	_ = st.FirstPacket(time.Now())
	srv.stream.Add(st)

	// Bind RTP dummy port
	_, _ = srv.rtp.Bind(context.Background(), streamID, 111, 60*time.Millisecond, 0)

	// Device routine to handle stop_stream and reply with StreamStopped carrying stats
	stopHandled := make(chan struct{})
	go func() {
		defer close(stopHandled)
		payload, err := control.ReadFrame(clientConn)
		if err != nil {
			return
		}
		msg, err := control.DecodePayload(payload)
		if err != nil {
			return
		}
		stopMsg, ok := msg.(*control.StopStream)
		if !ok {
			return
		}
		stats := &control.StreamStoppedStats{
			PacketsSent:   150,
			BytesSent:     30000,
			DurationMS:    10000,
			EncoderErrors: 0,
		}
		reply := control.NewStreamStopped(stopMsg.RequestID, stopMsg.StreamID, stats)
		_ = control.WriteMessage(clientConn, reply)
	}()

	// Call StopStream
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.StopStream(ctx, streamID); err != nil {
		t.Fatalf("StopStream failed: %v", err)
	}
	<-stopHandled

	// Verify DeviceFinalStats returns the stats
	devStats, found := srv.DeviceFinalStats(streamID)
	if !found || devStats == nil {
		t.Fatal("expected device final stats to be found")
	}
	if devStats.PacketsSent != 150 || devStats.BytesSent != 30000 || devStats.DurationMS != 10000 {
		t.Fatalf("unexpected devStats: %+v", devStats)
	}

	// Verify persistence: clear in-memory and reload
	srv.deviceStatsMu.Lock()
	delete(srv.deviceStats, streamID)
	srv.deviceStatsMu.Unlock()

	loadedStats, found2 := srv.DeviceFinalStats(streamID)
	if !found2 || loadedStats == nil {
		t.Fatal("expected device final stats to load from persistence")
	}
	if loadedStats.PacketsSent != 150 || loadedStats.BytesSent != 30000 {
		t.Fatalf("unexpected loadedStats: %+v", loadedStats)
	}
}

// TestRestoreReconcilesStaleStreams verifies the full restart
// reconciliation path (spec §20):
//  1. Seed a DB with a device + streams in STARTING, RTP_WAIT, ACTIVE, COMPLETE.
//  2. Build a new Server over the same DB, call Restore().
//  3. All three live streams are FAILED with FailureServerRestart in BOTH
//     the registry (GetStream) and the DB (StreamRepo.LoadAll).
//  4. COMPLETE stream is untouched and not re-registered.
//  5. Registry state == DB state for every reconciled stream.
func TestRestoreReconcilesStaleStreams(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := persistence.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repos := persistence.NewRepos(db)
	now := time.Now()

	// Seed device (TOFU)
	dev := device.Device{DeviceID: "esp32-test", DisplayName: "esp32-test", Status: "online"}
	if err := repos.Devices.Save(dev, []byte{}); err != nil {
		t.Fatal("seed device:", err)
	}

	// Seed four streams: STARTING, RTP_WAIT, ACTIVE, COMPLETE
	cases := []struct {
		id    string
		state string
	}{
		{"strm-starting", string(stream.StateStarting)},
		{"strm-rtpwait", string(stream.StateRTPWait)},
		{"strm-active", string(stream.StateActive)},
		{"strm-complete", string(stream.StateComplete)},
	}
	for _, c := range cases {
		if err := repos.Streams.Save(c.id, dev.DeviceID, c.state, "", 1, now); err != nil {
			t.Fatalf("seed stream %s: %v", c.id, err)
		}
	}

	// Build a new Server over the same DB and Restore
	cfg := config.Load()
	cfg.DBPath = dbPath
	cfg.ControlAddr = "localhost:0"
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if err := srv.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Device is present in registry (recognized)
	if _, err := srv.device.Get(dev.DeviceID); err != nil {
		t.Fatalf("device not in registry: %v", err)
	}

	// Live streams: registry = FAILED with FailureServerRestart
	for _, c := range cases[:3] {
		st, err := srv.stream.Get(c.id)
		if err != nil {
			t.Fatalf("%s: registry Get: %v", c.id, err)
		}
		if st.State() != stream.StateFailed {
			t.Fatalf("%s: registry state = %s, want FAILED", c.id, st.State())
		}
		if st.Reason != stream.FailureServerRestart {
			t.Fatalf("%s: registry reason = %s, want FailureServerRestart", c.id, st.Reason)
		}
	}

	// COMPLETE stream: not registered in memory (reconciliation skipped it)
	if _, err := srv.stream.Get(cases[3].id); err != stream.ErrStreamNotFound {
		t.Fatalf("COMPLETE stream should not be registered, got %v", err)
	}

	// DB state must match registry for every stream
	loaded, err := repos.Streams.LoadAll()
	if err != nil {
		t.Fatal("LoadAll:", err)
	}
	byID := make(map[string]persistence.StreamRecord)
	for _, r := range loaded {
		byID[r.StreamID] = r
	}
	for _, c := range cases[:3] {
		rec := byID[c.id]
		if rec.State != string(stream.StateFailed) {
			t.Fatalf("%s: DB state = %s, want FAILED", c.id, rec.State)
		}
		if rec.Reason != string(stream.FailureServerRestart) {
			t.Fatalf("%s: DB reason = %s, want FailureServerRestart", c.id, rec.Reason)
		}
	}
	// COMPLETE untouched in DB
	if byID[cases[3].id].State != string(stream.StateComplete) {
		t.Fatalf("COMPLETE: DB state = %s, want COMPLETE", byID[cases[3].id].State)
	}
}

// mockTCPConn wraps a net.Conn and overrides LocalAddr to return a
// *net.TCPAddr — used to prove StartStream derives Destination.IP from
// the control session's LocalAddr (not the 127.0.0.1 fallback).
type mockTCPConn struct {
	net.Conn
	localAddr *net.TCPAddr
}

func (c *mockTCPConn) LocalAddr() net.Addr {
	return c.localAddr
}

// TestStartStreamDestinationIPFromSessionLocalAddr verifies the emitted
// start_stream Destination.IP equals the control session's LocalAddr IP
// (not 127.0.0.1), per the fix-rtp-dest-ip dispatch.
func TestStartStreamDestinationIPFromSessionLocalAddr(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	cfg := config.Load()
	cfg.DBPath = dbPath
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ctx := context.Background()

	// Register the device so StartStream passes the device.Get check
	if err := srv.Authenticate(ctx, "test-device", ""); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	// Create a pipe; wrap the server side to report a concrete LocalAddr.
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	mockConn := &mockTCPConn{
		Conn: serverConn,
		localAddr: &net.TCPAddr{
			IP:   net.ParseIP("192.168.1.50"),
			Port: 12345,
		},
	}

	// Create and run a control session for the device
	session := control.NewSession(mockConn, nil, time.Now, nil)
	session.SetOnReady(srv.ctrl.OnReady)
	session.SetOnClose(func(s *control.Session) { srv.ctrl.Unregister(s.DeviceID()) })
	session.SetOnMsg(srv.ctrl.Handler())
	sessCtx, sessCancel := context.WithCancel(ctx)
	defer sessCancel()
	go session.Run(sessCtx)

	// Complete the hello handshake so the session registers
	hello := control.NewHello("test-device", "", "1.0.0", nil)
	payload, err := control.Encode(hello)
	if err != nil {
		t.Fatalf("Encode hello: %v", err)
	}
	if err := control.WriteFrame(clientConn, payload); err != nil {
		t.Fatalf("WriteFrame hello: %v", err)
	}
	_, err = control.ReadFrame(clientConn) // hello_ack
	if err != nil {
		t.Fatalf("ReadFrame hello_ack: %v", err)
	}

	// StartStream sends start_stream and awaits stream_started. Run it in a
	// goroutine so we can read the emitted message and reply.
	type result struct {
		res map[string]any
		err error
	}
	ch := make(chan result, 1)
	go func() {
		res, err := srv.StartStream(ctx, "test-device", "test", api.RecordingConfig{})
		ch <- result{res, err}
	}()

	// Read the emitted start_stream and assert Destination.IP
	frame, err := control.ReadFrame(clientConn)
	if err != nil {
		t.Fatalf("ReadFrame start_stream: %v", err)
	}
	msg, err := control.DecodePayload(frame)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	startReq, ok := msg.(*control.StartStream)
	if !ok {
		t.Fatalf("type = %T, want *control.StartStream", msg)
	}
	if startReq.Destination.IP != "192.168.1.50" {
		t.Fatalf("Destination.IP = %q, want 192.168.1.50 (from session LocalAddr)", startReq.Destination.IP)
	}

	// Reply stream_started so StartStream completes
	reply := control.NewStreamStarted(startReq.RequestID, startReq.StreamID)
	replyPayload, err := control.Encode(reply)
	if err != nil {
		t.Fatalf("Encode reply: %v", err)
	}
	if err := control.WriteFrame(clientConn, replyPayload); err != nil {
		t.Fatalf("WriteFrame reply: %v", err)
	}

	// Verify StartStream succeeded
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("StartStream: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartStream did not complete")
	}
}

// TestStartStreamDestinationOverride verifies AdvertiseHost + AdvertiseRTPPort
// override the emitted start_stream Destination.IP/Port.
func TestStartStreamDestinationOverride(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	cfg := config.Load()
	cfg.DBPath = dbPath
	cfg.AdvertiseHost = "10.0.0.1"
	cfg.AdvertiseRTPPort = 20000
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ctx := context.Background()

	if err := srv.Authenticate(ctx, "test-device", ""); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	mockConn := &mockTCPConn{
		Conn: serverConn,
		localAddr: &net.TCPAddr{
			IP:   net.ParseIP("192.168.1.50"),
			Port: 12345,
		},
	}

	session := control.NewSession(mockConn, nil, time.Now, nil)
	session.SetOnReady(srv.ctrl.OnReady)
	session.SetOnClose(func(s *control.Session) { srv.ctrl.Unregister(s.DeviceID()) })
	session.SetOnMsg(srv.ctrl.Handler())
	sessCtx, sessCancel := context.WithCancel(ctx)
	defer sessCancel()
	go session.Run(sessCtx)

	hello := control.NewHello("test-device", "", "1.0.0", nil)
	payload, err := control.Encode(hello)
	if err != nil {
		t.Fatalf("Encode hello: %v", err)
	}
	if err := control.WriteFrame(clientConn, payload); err != nil {
		t.Fatalf("WriteFrame hello: %v", err)
	}
	_, err = control.ReadFrame(clientConn)
	if err != nil {
		t.Fatalf("ReadFrame hello_ack: %v", err)
	}

	type result struct {
		res map[string]any
		err error
	}
	ch := make(chan result, 1)
	go func() {
		res, err := srv.StartStream(ctx, "test-device", "test", api.RecordingConfig{})
		ch <- result{res, err}
	}()

	frame, err := control.ReadFrame(clientConn)
	if err != nil {
		t.Fatalf("ReadFrame start_stream: %v", err)
	}
	msg, err := control.DecodePayload(frame)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	startReq, ok := msg.(*control.StartStream)
	if !ok {
		t.Fatalf("type = %T, want *control.StartStream", msg)
	}
	if startReq.Destination.IP != "10.0.0.1" {
		t.Fatalf("Destination.IP = %q, want 10.0.0.1 (from AdvertiseHost)", startReq.Destination.IP)
	}
	if startReq.Destination.Port != 20000 {
		t.Fatalf("Destination.Port = %d, want 20000 (from AdvertiseRTPPort)", startReq.Destination.Port)
	}

	reply := control.NewStreamStarted(startReq.RequestID, startReq.StreamID)
	replyPayload, err := control.Encode(reply)
	if err != nil {
		t.Fatalf("Encode reply: %v", err)
	}
	if err := control.WriteFrame(clientConn, replyPayload); err != nil {
		t.Fatalf("WriteFrame reply: %v", err)
	}

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("StartStream: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartStream did not complete")
	}
}
