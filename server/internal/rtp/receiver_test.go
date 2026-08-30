package rtp

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"espmic/server/internal/metrics"
)

// makeRawRTP builds a minimal RTP packet (v2, no CSRCs, no extensions).
func makeRawRTP(t *testing.T, version, pt byte, seq uint16, ts, ssrc uint32, payload []byte) []byte {
	t.Helper()
	raw := make([]byte, 12+len(payload))
	raw[0] = (version & 0x03) << 6
	raw[1] = pt
	binary.BigEndian.PutUint16(raw[2:4], seq)
	binary.BigEndian.PutUint32(raw[4:8], ts)
	binary.BigEndian.PutUint32(raw[8:12], ssrc)
	copy(raw[12:], payload)
	return raw
}

// sendPacket sends a raw RTP packet to a local UDP addr.
func sendPacket(t *testing.T, addr *net.UDPAddr, raw []byte) {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(raw); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestReceiverBindCreatesPort(t *testing.T) {
	r := NewReceiver(metrics.New())
	port, err := r.Bind(context.Background(), "s1", 111, DefaultPayloadType)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if port == 0 {
		t.Fatal("expected non-zero port")
	}
	defer r.CloseStream("s1")

	if _, ok := r.JitterBuffer("s1"); !ok {
		t.Fatal("expected jitter buffer for s1")
	}
}

func TestReceiverDuplicateBind(t *testing.T) {
	r := NewReceiver(metrics.New())
	if _, err := r.Bind(context.Background(), "s1", 111, DefaultPayloadType); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	defer r.CloseStream("s1")
	if _, err := r.Bind(context.Background(), "s1", 222, DefaultPayloadType); err == nil {
		t.Fatal("expected error on duplicate bind")
	}
}

func TestReceiverAcceptsValidPacket(t *testing.T) {
	r := NewReceiver(metrics.New())
	port, err := r.Bind(context.Background(), "s1", 0x1234, DefaultPayloadType)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	defer r.CloseStream("s1")

	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)}
	raw := makeRawRTP(t, 2, DefaultPayloadType, 100, 96000, 0x1234, []byte{0x42})
	sendPacket(t, addr, raw)

	// Give the read loop time to process.
	time.Sleep(50 * time.Millisecond)
	jb, _ := r.JitterBuffer("s1")
	if s := jb.Statistics(); s.Received != 1 {
		t.Fatalf("received = %d, want 1", s.Received)
	}
	if r.metrics.Snapshot()[metrics.LabelRTPPacketsReceived] != 1 {
		t.Fatal("metrics not incremented")
	}
}

func TestReceiverIgnoresWrongSSRC(t *testing.T) {
	r := NewReceiver(metrics.New())
	port, err := r.Bind(context.Background(), "s1", 0x1234, DefaultPayloadType)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	defer r.CloseStream("s1")

	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)}
	raw := makeRawRTP(t, 2, DefaultPayloadType, 1, 96000, 0x9999, []byte{0x42}) // wrong ssrc
	sendPacket(t, addr, raw)

	time.Sleep(50 * time.Millisecond)
	jb, _ := r.JitterBuffer("s1")
	if s := jb.Statistics(); s.Received != 0 {
		t.Fatalf("received = %d, want 0 (wrong ssrc)", s.Received)
	}
}

func TestReceiverIgnoresWrongPT(t *testing.T) {
	r := NewReceiver(metrics.New())
	port, err := r.Bind(context.Background(), "s1", 0x1234, DefaultPayloadType)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	defer r.CloseStream("s1")

	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)}
	raw := makeRawRTP(t, 2, DefaultPayloadType+1, 1, 96000, 0x1234, []byte{0x42}) // wrong PT
	sendPacket(t, addr, raw)

	time.Sleep(50 * time.Millisecond)
	jb, _ := r.JitterBuffer("s1")
	if s := jb.Statistics(); s.Received != 0 {
		t.Fatalf("received = %d, want 0 (wrong pt)", s.Received)
	}
}

func TestReceiverIgnoresMalformed(t *testing.T) {
	r := NewReceiver(metrics.New())
	port, err := r.Bind(context.Background(), "s1", 0x1234, DefaultPayloadType)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	defer r.CloseStream("s1")

	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)}
	conn, _ := net.DialUDP("udp", nil, addr)
	defer conn.Close()
	_, _ = conn.Write([]byte{0x00, 0x00}) // too short

	time.Sleep(50 * time.Millisecond)
	jb, _ := r.JitterBuffer("s1")
	if s := jb.Statistics(); s.Received != 0 {
		t.Fatalf("received = %d, want 0 (malformed)", s.Received)
	}
}

func TestReceiverCloseStream(t *testing.T) {
	r := NewReceiver(metrics.New())
	if _, err := r.Bind(context.Background(), "s1", 1, DefaultPayloadType); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	r.CloseStream("s1")
	if _, ok := r.JitterBuffer("s1"); ok {
		t.Fatal("stream should be closed")
	}
}
