package rtp

import (
	"reflect"
	"testing"
	"time"
)

var base = time.Unix(0, 0).UTC()

func ms(o int) time.Time { return base.Add(time.Duration(o) * time.Millisecond) }

func mkPacket(seq uint16, ts uint32) Packet {
	return Packet{Version: 2, PayloadType: DefaultPayloadType,
		SequenceNumber: seq, Timestamp: ts, SSRC: 1}
}

func flatten(ps []Packet) []uint16 {
	out := make([]uint16, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.SequenceNumber)
	}
	return out
}

func TestReorder(t *testing.T) {
	jb := New(60 * time.Millisecond)
	jb.Push(mkPacket(100, 0), ms(0))
	jb.Push(mkPacket(102, 1920), ms(1))
	jb.Push(mkPacket(101, 960), ms(2))

	out := jb.Emit(ms(1000)) // flush past all deadlines
	if got, want := flatten(out), []uint16{100, 101, 102}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %v, want %v", got, want)
	}
	s := jb.Statistics()
	if s.Reordered != 1 {
		t.Errorf("reordered = %d, want 1", s.Reordered)
	}
	if s.Lost != 0 || s.Duplicate != 0 || s.Late != 0 {
		t.Errorf("unexpected loss counters: %+v", s)
	}
}

func TestInOrder(t *testing.T) {
	jb := New(60 * time.Millisecond)
	jb.Push(mkPacket(100, 0), ms(0))
	jb.Push(mkPacket(101, 960), ms(1))
	jb.Push(mkPacket(102, 1920), ms(2))

	out := jb.Emit(ms(1000))
	if got, want := flatten(out), []uint16{100, 101, 102}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %v, want %v", got, want)
	}
}

func TestLossGapDeclaresAndEmitsAfter(t *testing.T) {
	jb := New(60 * time.Millisecond)
	jb.Push(mkPacket(100, 0), ms(0))    // present
	jb.Push(mkPacket(102, 1920), ms(1)) // 101 missing
	jb.Push(mkPacket(103, 2880), ms(2))

	out := jb.Emit(ms(1000))
	if got, want := flatten(out), []uint16{100, 102, 103}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %v, want %v", got, want)
	}
	s := jb.Statistics()
	if s.Lost != 1 {
		t.Errorf("lost = %d, want 1", s.Lost)
	}
}

func TestLossHookReceivesMissingSeqs(t *testing.T) {
	var lost []uint16
	jb := New(60*time.Millisecond, WithLossHook(func(seq uint16) { lost = append(lost, seq) }))
	jb.Push(mkPacket(100, 0), ms(0))
	jb.Push(mkPacket(105, 4800), ms(1))

	jb.Emit(ms(1000))
	want := []uint16{101, 102, 103, 104}
	if !reflect.DeepEqual(lost, want) {
		t.Fatalf("loss hook seqs = %v, want %v", lost, want)
	}
}

func TestDuplicateWithinBuffer(t *testing.T) {
	jb := New(60 * time.Millisecond)
	jb.Push(mkPacket(100, 0), ms(0))
	jb.Push(mkPacket(100, 0), ms(1)) // duplicate of still-buffered packet
	jb.Push(mkPacket(101, 960), ms(2))

	out := jb.Emit(ms(1000))
	if got, want := flatten(out), []uint16{100, 101}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %v, want %v", got, want)
	}
	s := jb.Statistics()
	if s.Duplicate != 1 || s.Received != 3 {
		t.Errorf("duplicate = %d, received = %d; want 1, 3", s.Duplicate, s.Received)
	}
}

func TestLate(t *testing.T) {
	jb := New(60 * time.Millisecond)
	jb.Push(mkPacket(100, 0), ms(0))
	jb.Push(mkPacket(101, 960), ms(1))
	jb.Emit(ms(1000)) // expected now past 101

	jb.Push(mkPacket(100, 0), ms(1100)) // arrives after its slot was played

	out := jb.Emit(ms(2000))
	if len(out) != 0 {
		t.Fatalf("expected no output, got %v", flatten(out))
	}
	s := jb.Statistics()
	if s.Late != 1 {
		t.Errorf("late = %d, want 1", s.Late)
	}
}

func TestMultipleLossGap(t *testing.T) {
	jb := New(60 * time.Millisecond)
	jb.Push(mkPacket(100, 0), ms(0))
	jb.Push(mkPacket(105, 4800), ms(1))

	out := jb.Emit(ms(1000))
	if got, want := flatten(out), []uint16{100, 105}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %v, want %v", got, want)
	}
	if s := jb.Statistics(); s.Lost != 4 {
		t.Errorf("lost = %d, want 4", s.Lost)
	}
}

func TestStartArbitrarySeq(t *testing.T) {
	jb := New(60 * time.Millisecond)
	jb.Push(mkPacket(500, 0), ms(0))
	jb.Push(mkPacket(501, 960), ms(1))

	out := jb.Emit(ms(1000))
	if got, want := flatten(out), []uint16{500, 501}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %v, want %v", got, want)
	}
}

func TestNoPrematureLossWithinWindow(t *testing.T) {
	jb := New(60 * time.Millisecond)
	jb.Push(mkPacket(100, 0), ms(0))
	jb.Push(mkPacket(102, 1920), ms(1)) // reordered; 101 not yet arrived

	// Before 101's deadline, do not declare it lost and emit nothing due.
	if out := jb.Emit(ms(50)); len(out) != 0 {
		t.Fatalf("emitted too early: %v", flatten(out))
	}
	if s := jb.Statistics(); s.Lost != 0 {
		t.Errorf("lost prematurely = %d, want 0", s.Lost)
	}

	jb.Push(mkPacket(101, 960), ms(2)) // arrives within the window
	out := jb.Emit(ms(1000))
	if got, want := flatten(out), []uint16{100, 101, 102}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %v, want %v", got, want)
	}
	if s := jb.Statistics(); s.Lost != 0 {
		t.Errorf("lost = %d, want 0", s.Lost)
	}
}

func TestJitterEstimateNonNegative(t *testing.T) {
	jb := New(60 * time.Millisecond)
	jb.Push(mkPacket(100, 0), ms(0))
	jb.Push(mkPacket(101, 960), ms(1))
	jb.Push(mkPacket(102, 1920), ms(3))
	jb.Push(mkPacket(103, 2880), ms(4))
	if s := jb.Statistics(); s.JitterMS < 0 {
		t.Errorf("jitter = %v, want >= 0", s.JitterMS)
	}
}

// TestJitterEstimateInCorrectRange catches the RFC3550 unit-scaling bug where
// arrival was converted to RTP ticks ~1000x too large. A steady 20ms cadence
// with 960-tick timestamps must yield ~0 jitter; a deterministic 5ms late
// arrival must yield a bounded ~0.3ms estimate. (The buggy code reported
// ~1000x these values.)
func TestJitterEstimateInCorrectRange(t *testing.T) {
	steady := New(60 * time.Millisecond)
	for i := 0; i < 5; i++ {
		steady.Push(mkPacket(uint16(i), uint32(i*960)), ms(i*20))
	}
	if s := steady.Statistics(); s.JitterMS >= 1.0 {
		t.Fatalf("steady-cadence jitter = %.3f ms, want < 1 ms", s.JitterMS)
	}

	pert := New(60 * time.Millisecond)
	for i := 0; i < 3; i++ {
		pert.Push(mkPacket(uint16(i), uint32(i*960)), ms(i*20)) // perfect
	}
	pert.Push(mkPacket(3, 2880), ms(65)) // should arrive at 60ms, i.e. 5ms late
	if s := pert.Statistics(); s.JitterMS < 0.1 || s.JitterMS > 5.0 {
		t.Fatalf("perturbation jitter = %.3f ms, want in (0.1, 5.0)", s.JitterMS)
	}
}

func TestSeqWrapInOrder(t *testing.T) {
	jb := New(60 * time.Millisecond)
	seqs := []uint16{65534, 65535, 0, 1}
	for i, s := range seqs {
		jb.Push(mkPacket(s, uint32(i)*960), ms(i*20))
	}
	out := jb.Emit(ms(1000))
	if got, want := flatten(out), []uint16{65534, 65535, 0, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %v, want %v", got, want)
	}
	if s := jb.Statistics(); s.Reordered != 0 || s.Lost != 0 {
		t.Fatalf("wrap in-order miscounted: reordered=%d lost=%d", s.Reordered, s.Lost)
	}
}

func TestSeqWrapReorder(t *testing.T) {
	jb := New(60 * time.Millisecond)
	// 65535 arrives after 0 (which was assumed missing) => it is reordered.
	jb.Push(mkPacket(65534, 0), ms(0))
	jb.Push(mkPacket(0, 1920), ms(1))
	jb.Push(mkPacket(65535, 960), ms(2))

	out := jb.Emit(ms(1000))
	if got, want := flatten(out), []uint16{65534, 65535, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %v, want %v", got, want)
	}
	if s := jb.Statistics(); s.Reordered != 1 {
		t.Fatalf("reordered = %d, want 1 (wrap reorder not detected)", s.Reordered)
	}
}

func TestLargeGapLossWalkBounded(t *testing.T) {
	calls := 0
	jb := New(60*time.Millisecond, WithLossHook(func(seq uint16) { calls++ }))
	jb.Push(mkPacket(0, 0), ms(0))
	jb.Push(mkPacket(30000, 30000*960), ms(1)) // far ahead

	out := jb.Emit(ms(1000))
	if got, want := flatten(out), []uint16{0, 30000}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %v, want %v", got, want)
	}
	if s := jb.Statistics(); s.Lost != 29999 {
		t.Fatalf("lost = %d, want 29999", s.Lost)
	}
	if calls != maxLossHookCalls {
		t.Fatalf("onLoss calls = %d, want cap %d", calls, maxLossHookCalls)
	}
}
