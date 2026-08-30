package rtp

import (
	"context"
	"net"
	"runtime"
	"strconv"
	"testing"
	"time"

	"espmic/server/internal/metrics"
)

// TestInteropReorderedDuplicate (§21 #3): inject reordered and duplicate
// packets and verify the jitter buffer emits in order without double-counting.
func TestInteropReorderedDuplicate(t *testing.T) {
	jb := New(60 * time.Millisecond)
	// Reordered arrival: 100, 102, 101, 102(dup), 103.
	pushes := []struct {
		seq uint16
		ts  uint32
	}{
		{100, 0},
		{102, 1920},
		{101, 960},
		{102, 1920}, // duplicate
		{103, 2880},
	}
	for i, p := range pushes {
		jb.Push(mkPacket(p.seq, p.ts), ms(i*20))
	}
	out := jb.Emit(ms(1000))
	if got, want := flatten(out), []uint16{100, 101, 102, 103}; !equal(got, want) {
		t.Fatalf("output = %v, want %v", got, want)
	}
	s := jb.Statistics()
	if s.Duplicate != 1 {
		t.Fatalf("duplicate = %d, want 1", s.Duplicate)
	}
	if s.Received != 5 {
		t.Fatalf("received = %d, want 5", s.Received)
	}
}

// TestInteropLossConcealmentStats (§21 #4): drop 1/5/10% of packets and verify
// the loss counter + PLC hook reflect the gaps. Drops are in the middle of the
// stream (not the tail) so a later present packet reveals the gap (spec §11).
func TestInteropLossConcealmentStats(t *testing.T) {
	cases := []struct {
		name     string
		dropAt   []int // indices (0-based) to drop
		wantLost int
	}{
		{"pct1", []int{50}, 1},
		{"pct5", []int{10, 30, 50, 70, 90}, 5},
		{"pct10", []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 95}, 10},
	}
	for _, tc := range cases {
		jb := New(60 * time.Millisecond)
		var lost []uint16
		jb2 := New(60*time.Millisecond, WithLossHook(func(seq uint16) { lost = append(lost, seq) }))
		dropM := make(map[int]bool)
		for _, d := range tc.dropAt {
			dropM[d] = true
		}
		var seq uint16
		sent := 0
		for i := 0; i < 100; i++ {
			if dropM[i] {
				// Skip this sequence number (drop).
				seq++
				continue
			}
			jb.Push(mkPacket(seq, uint32(seq)*960), ms(i*20))
			jb2.Push(mkPacket(seq, uint32(seq)*960), ms(i*20))
			seq++
		}
		jb.Emit(ms(10000))
		jb2.Emit(ms(10000))
		if s := jb.Statistics(); int(s.Lost) != tc.wantLost {
			t.Fatalf("%s: lost = %d, want %d (sent %d)", tc.name, s.Lost, tc.wantLost, sent)
		}
		if len(lost) != tc.wantLost {
			t.Fatalf("%s: PLC hook fired %d times, want %d", tc.name, len(lost), tc.wantLost)
		}
	}
}

// TestInteropNoLeak100Streams (§21 #5): start/stop >=100 streams with no
// socket or goroutine leak.
func TestInteropNoLeak100Streams(t *testing.T) {
	r := NewReceiver(metrics.New())
	before := runtime.NumGoroutine()
	seen := make(map[uint16]bool)
	for i := 0; i < 100; i++ {
		streamID := "stream-" + strconv.Itoa(i)
		port, err := r.Bind(context.Background(), streamID, uint32(i+1), DefaultPayloadType)
		if err != nil {
			t.Fatalf("bind %s: %v", streamID, err)
		}
		if port == 0 || seen[port] {
			t.Fatalf("duplicate/zero port %d", port)
		}
		seen[port] = true
	}
	if r.streamCount() != 100 {
		t.Fatalf("bound = %d, want 100", r.streamCount())
	}
	for i := 0; i < 100; i++ {
		r.CloseStream("stream-" + strconv.Itoa(i))
	}
	if r.streamCount() != 0 {
		t.Fatalf("after close bound = %d, want 0", r.streamCount())
	}
	// Give goroutines time to exit.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		after := runtime.NumGoroutine()
		if after <= before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}

// TestInteropTwoDevices (§21 #7): two devices simultaneously with independent
// SSRC/session state do not interfere.
func TestInteropTwoDevices(t *testing.T) {
	r := NewReceiver(metrics.New())
	_, err := r.Bind(context.Background(), "s1", 0xAAAA, DefaultPayloadType)
	if err != nil {
		t.Fatalf("bind s1: %v", err)
	}
	_, err = r.Bind(context.Background(), "s2", 0xBBBB, DefaultPayloadType)
	if err != nil {
		t.Fatalf("bind s2: %v", err)
	}
	defer r.CloseStream("s1")
	defer r.CloseStream("s2")

	jb1, _ := r.JitterBuffer("s1")
	jb2, _ := r.JitterBuffer("s2")

	// Push packets to each independently; interleaved seq numbers.
	jb1.Push(mkPacket(1, 0), ms(0))
	jb2.Push(mkPacket(1, 0), ms(0))
	jb1.Push(mkPacket(2, 960), ms(1))
	jb2.Push(mkPacket(2, 960), ms(1))

	if s1 := jb1.Statistics(); s1.Received != 2 {
		t.Fatalf("s1 received = %d, want 2", s1.Received)
	}
	if s2 := jb2.Statistics(); s2.Received != 2 {
		t.Fatalf("s2 received = %d, want 2", s2.Received)
	}
}

// TestInteropMalformedNoCrash (§21 #8): malformed RTP cannot crash or exhaust
// the receiver.
func TestInteropMalformedNoCrash(t *testing.T) {
	r := NewReceiver(metrics.New())
	port, err := r.Bind(context.Background(), "s1", 1, DefaultPayloadType)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer r.CloseStream("s1")

	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)}
	conn, _ := net.DialUDP("udp", nil, addr)
	defer conn.Close()

	malformed := [][]byte{
		{},                        // empty
		{0x00},                    // 1 byte
		{0x80, 0x6f},              // 2 bytes
		{0x80, 0x6f, 0x00, 0x01},  // 4 bytes
		make([]byte, 2048),        // all zeros, large
		{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, // valid-header empty payload
	}
	for _, m := range malformed {
		_, _ = conn.Write(m)
	}
	time.Sleep(100 * time.Millisecond)

	jb, _ := r.JitterBuffer("s1")
	if s := jb.Statistics(); s.Received != 0 {
		t.Fatalf("malformed produced %d received packets", s.Received)
	}
}

func equal(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
