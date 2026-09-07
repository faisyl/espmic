package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
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
	"espmic/server/internal/rtp"
	"espmic/server/internal/stream"
)

// TestRTPHoldStreamStaysActiveWithRealUDP verifies that a stream fed
// continuous real UDP RTP reaches ACTIVE and STAYS active (does not hit
// RTP disappeared) as long as packets flow, and produces a valid .opus
// file. This is a regression test for the "RTP disappeared while RTP
// flows continuously" bug.
func TestRTPHoldStreamStaysActiveWithRealUDP(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	cfg := config.Load()
	cfg.DBPath = dbPath
	cfg.RecordingsDir = filepath.Join(dir, "recordings")

	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamID := "test-stream"
	ssrc := uint32(0xDEADBEEF)

	// Bind RTP port
	port, err := srv.rtp.Bind(ctx, streamID, rtp.DefaultPayloadType, 60*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// Create stream in RTP_WAIT state (simulating StartStream flow)
	st := stream.New(streamID, "test-device", 0, time.Now())
	st.WithTimeoutConfig(stream.TimeoutConfig{
		RTPWait:      5 * time.Second,
		RTPDisappear: 1 * time.Second,
	})
	srv.stream.Add(st)
	_ = st.Start(time.Now())
	_ = st.DeviceCommandSent()
	_ = st.StreamStarted(time.Now())

	if st.State() != stream.StateRTPWait {
		t.Fatalf("expected RTP_WAIT, got %s", st.State())
	}

	// Wire liveness callback (mirrors server.go StartStream)
	srv.rtp.SetOnPacket(func(sid string, first bool) {
		st, err := srv.stream.Get(sid)
		if err != nil {
			return
		}
		if first {
			_ = st.FirstPacket(time.Now())
		} else {
			st.Packet(time.Now())
		}
	})

	// Create OpusRecorder (mirrors server.go StartStream)
	opusRec, err := audio.NewOpusRecorder(cfg.RecordingsDir, streamID, 48000, 2, 312)
	if err != nil {
		t.Fatalf("NewOpusRecorder: %v", err)
	}
	srv.rtp.SetOnCompressed(func(sid string, ts uint32, payload []byte) {
		opusRec.WritePacket(ts, payload)
	})

	// Blast 200 RTP packets at the UDP port
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	const numPackets = 200
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numPackets; i++ {
			raw := makeRawRTP(2, rtp.DefaultPayloadType, uint16(i+1), uint32(i*960), ssrc, []byte{0x42, 0x42})
			_, _ = conn.Write(raw)
			time.Sleep(20 * time.Millisecond) // ~50pps
		}
	}()

	// Wait for stream to reach ACTIVE
	deadline := time.After(5 * time.Second)
	for {
		if st.State() == stream.StateActive {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("stream did not reach ACTIVE within 5s, state=%s", st.State())
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Verify stream stays ACTIVE while packets flow
	time.Sleep(2 * time.Second)
	if st.State() != stream.StateActive {
		t.Fatalf("stream left ACTIVE during packet flow, state=%s", st.State())
	}

	// Wait for all packets to be sent
	wg.Wait()

	// Give the receive loop time to process
	time.Sleep(500 * time.Millisecond)

	// Verify stream is still ACTIVE
	if st.State() != stream.StateActive {
		t.Fatalf("stream not ACTIVE after packets sent, state=%s", st.State())
	}

	// Verify packets were received (not massively dropped)
	stats, ok := srv.rtp.StreamStats(streamID)
	if !ok {
		t.Fatal("stream stats not found")
	}
	if stats.Received < numPackets/2 {
		t.Fatalf("too many packets dropped: received=%d, sent=%d", stats.Received, numPackets)
	}
	t.Logf("received %d/%d packets", stats.Received, numPackets)

	// Finalize the OpusRecorder and verify the .opus file
	uri, bytes, err := opusRec.Finalize(time.Now())
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if uri == "" {
		t.Fatal("expected non-empty URI")
	}
	if bytes == 0 {
		t.Fatal("expected non-zero byte count")
	}

	// Verify the .opus file has valid Ogg structure
	data, err := os.ReadFile(uri)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 4 || string(data[0:4]) != "OggS" {
		t.Fatal("missing OggS sync pattern")
	}
	// Verify OpusHead capture pattern
	foundHead := false
	foundTags := false
	for i := 0; i <= len(data)-8; i++ {
		if string(data[i:i+8]) == "OpusHead" {
			foundHead = true
		}
		if string(data[i:i+8]) == "OpusTags" {
			foundTags = true
		}
	}
	if !foundHead {
		t.Fatal("missing OpusHead capture pattern")
	}
	if !foundTags {
		t.Fatal("missing OpusTags capture pattern")
	}
	t.Logf("valid .opus file: %s (%d bytes)", uri, bytes)

	// Stop the stream
	srv.rtp.CloseStream(streamID)
	srv.stream.Remove(streamID)
}

// TestStartStreamReplacesExistingStream verifies that starting a new stream
// for a device that already has an active stream tears down the old one.
func TestStartStreamReplacesExistingStream(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	cfg := config.Load()
	cfg.DBPath = dbPath
	cfg.RecordingsDir = filepath.Join(dir, "recordings")

	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	ctx := context.Background()

	// Register device
	if err := srv.Authenticate(ctx, "test-device", ""); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	// Manually create a stream for the device (simulating an existing active stream)
	streamID1 := "existing-stream"
	ssrc := uint32(0xDEADBEEF)
	st1 := stream.New(streamID1, "test-device", ssrc, time.Now())
	st1.WithTimeoutConfig(stream.TimeoutConfig{
		RTPWait:      5 * time.Second,
		RTPDisappear: 1 * time.Second,
	})
	srv.stream.Add(st1)
	_ = st1.Start(time.Now())
	_ = st1.DeviceCommandSent()
	_ = st1.StreamStarted(time.Now())
	_ = st1.FirstPacket(time.Now()) // transition to ACTIVE

	if st1.State() != stream.StateActive {
		t.Fatalf("expected ACTIVE, got %s", st1.State())
	}

	// Verify GetByDevice returns the existing stream
	existing, ok := srv.stream.GetByDevice("test-device")
	if !ok {
		t.Fatal("GetByDevice should return existing stream")
	}
	if existing.StreamID != streamID1 {
		t.Fatalf("GetByDevice returned wrong stream: %s", existing.StreamID)
	}

	// Now call StartStream for the same device — should replace the old stream
	// We need a control session for SendStartStream to work. Use a minimal approach.
	// Create a pipe and a goroutine that replies stream_started.
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

	// Complete hello handshake
	hello := control.NewHello("test-device", "", "1.0.0", nil)
	payload, _ := control.Encode(hello)
	_ = control.WriteFrame(clientConn, payload)
	_, _ = control.ReadFrame(clientConn) // hello_ack

	// Goroutine: read start_stream and reply streamStarted
	go func() {
		for {
			frame, err := control.ReadFrame(clientConn)
			if err != nil {
				return
			}
			msg, err := control.DecodePayload(frame)
			if err != nil {
				return
			}
			if p, ok := msg.(*control.Ping); ok {
				replyPayload, _ := control.Encode(control.NewPong(p.Seq))
				_ = control.WriteFrame(clientConn, replyPayload)
				continue
			}
			if _, ok := msg.(*control.StartStream); ok {
				// Read the full start_stream to get requestID and streamID
				// We need to parse it properly
				var startMsg struct {
					RequestID string `json:"request_id"`
					StreamID  string `json:"stream_id"`
				}
				_ = json.Unmarshal(frame, &startMsg)
				reply := control.NewStreamStarted(startMsg.RequestID, startMsg.StreamID)
				replyPayload, _ := control.Encode(reply)
				_ = control.WriteFrame(clientConn, replyPayload)
				return
			}
		}
	}()

	// Start a new stream for the same device
	result, err := srv.StartStream(ctx, "test-device", "test", api.RecordingConfig{})
	if err != nil {
		t.Fatalf("StartStream: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify first stream is now COMPLETE (replaced)
	if st1.State() != stream.StateComplete {
		t.Fatalf("first stream state = %s, want COMPLETE (replaced)", st1.State())
	}
	if st1.Reason != stream.FailureReplaced {
		t.Fatalf("first stream reason = %s, want REPLACED", st1.Reason)
	}

	// Verify first stream is no longer in registry
	if _, err := srv.stream.Get(streamID1); err == nil {
		t.Fatal("first stream should be removed from registry after replacement")
	}

	// Verify GetByDevice returns the new stream
	newStreamID := result["stream_id"].(string)
	if newStreamID == streamID1 {
		t.Fatal("new stream should have different ID")
	}
	if existing, ok := srv.stream.GetByDevice("test-device"); ok {
		if existing.StreamID != newStreamID {
			t.Fatalf("GetByDevice returned wrong stream: %s", existing.StreamID)
		}
	} else {
		t.Fatal("GetByDevice should return new stream")
	}
}

func makeRawRTP(version, pt byte, seq uint16, ts, ssrc uint32, payload []byte) []byte {
	raw := make([]byte, 12+len(payload))
	raw[0] = (version & 0x03) << 6
	raw[1] = pt
	binary.BigEndian.PutUint16(raw[2:4], seq)
	binary.BigEndian.PutUint32(raw[4:8], ts)
	binary.BigEndian.PutUint32(raw[8:12], ssrc)
	copy(raw[12:], payload)
	return raw
}
