package stream

import (
	"testing"
	"time"
)

var t0 = time.Unix(1_000_000, 0).UTC()

func TestLifecycleHappyPath(t *testing.T) {
	s := New("s1", "d1", 42, t0)
	if s.State() != StateCreated {
		t.Fatalf("initial state = %s, want CREATED", s.State())
	}
	if err := s.Start(t0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.DeviceCommandSent(); err != nil {
		t.Fatalf("DeviceCommandSent: %v", err)
	}
	if err := s.StreamStarted(t0); err != nil {
		t.Fatalf("StreamStarted: %v", err)
	}
	if err := s.FirstPacket(t0.Add(time.Second)); err != nil {
		t.Fatalf("FirstPacket: %v", err)
	}
	if err := s.StopRequested(); err != nil {
		t.Fatalf("StopRequested: %v", err)
	}
	if err := s.Stopped(); err != nil {
		t.Fatalf("Stopped: %v", err)
	}
	if s.State() != StateComplete {
		t.Fatalf("final state = %s, want COMPLETE", s.State())
	}
}

func TestIllegalTransitions(t *testing.T) {
	s := New("s1", "d1", 1, t0)
	if err := s.Stopped(); err != ErrIllegalTransition {
		t.Fatalf("Stopped from CREATED: err = %v, want ErrIllegalTransition", err)
	}
	if err := s.DeviceCommandSent(); err != nil {
		t.Fatalf("DeviceCommandSent: %v", err)
	}
	if err := s.FirstPacket(t0); err != ErrIllegalTransition {
		t.Fatalf("FirstPacket from STARTING: err = %v, want ErrIllegalTransition", err)
	}
}

func TestRTPWaitTimeout(t *testing.T) {
	s := New("s1", "d1", 1, t0)
	s.Start(t0)
	s.DeviceCommandSent()
	s.StreamStarted(t0)
	if s.RTPWaitTimedOut(t0.Add(4 * time.Second)) {
		t.Fatal("should not time out before 5s")
	}
	if !s.RTPWaitTimedOut(t0.Add(6 * time.Second)) {
		t.Fatal("should time out after 5s")
	}
}

func TestRTPWaitTimeoutConfigurable(t *testing.T) {
	s := New("s1", "d1", 1, t0)
	s.WithTimeoutConfig(TimeoutConfig{RTPWait: 500 * time.Millisecond})
	s.Start(t0)
	s.DeviceCommandSent()
	s.StreamStarted(t0)
	if !s.RTPWaitTimedOut(t0.Add(time.Second)) {
		t.Fatal("custom 500ms timeout should fire at 1s")
	}
}

func TestRTPDisappeared(t *testing.T) {
	s := New("s1", "d1", 1, t0)
	s.Start(t0)
	s.DeviceCommandSent()
	s.StreamStarted(t0)
	s.FirstPacket(t0)
	if s.RTPDisappeared(t0.Add(500 * time.Millisecond)) {
		t.Fatal("should not disappear before 1s")
	}
	if !s.RTPDisappeared(t0.Add(2 * time.Second)) {
		t.Fatal("should disappear after 1s without packets")
	}
	// Packet refreshes the clock.
	s.Packet(t0.Add(3 * time.Second))
	if s.RTPDisappeared(t0.Add(3500 * time.Millisecond)) {
		t.Fatal("packet should have refreshed disappearance clock")
	}
}

func TestFailureEdges(t *testing.T) {
	cases := []struct {
		name string
		run  func(s *Stream)
		want StreamState
	}{
		{"device rejected", func(s *Stream) {
			s.Start(t0)
			s.DeviceCommandSent()
			s.DeviceRejected(FailureStartRejected)
		}, StateFailed},
		{"device disconnected", func(s *Stream) {
			s.Start(t0)
			s.DeviceCommandSent()
			s.StreamStarted(t0)
			s.FirstPacket(t0)
			s.DeviceDisconnected()
		}, StateFailed},
		{"decode error", func(s *Stream) {
			s.Start(t0)
			s.DeviceCommandSent()
			s.StreamStarted(t0)
			s.FirstPacket(t0)
			s.DecodeError()
		}, StateFailed},
	}
	for _, tc := range cases {
		s := New("s1", "d1", 1, t0)
		tc.run(s)
		if s.State() != tc.want {
			t.Fatalf("%s: state = %s, want %s", tc.name, s.State(), tc.want)
		}
		if s.Reason == FailureNone {
			t.Fatalf("%s: expected a failure reason", tc.name)
		}
	}
}

func TestRegistryAddGetRemove(t *testing.T) {
	r := NewRegistry()
	r.Add(New("s1", "d1", 1, t0))
	r.Add(New("s2", "d1", 2, t0))
	if r.Count() != 2 {
		t.Fatalf("count = %d, want 2", r.Count())
	}
	s, err := r.Get("s1")
	if err != nil || s.StreamID != "s1" {
		t.Fatalf("Get: s=%v err=%v", s, err)
	}
	r.Remove("s1")
	if _, err := r.Get("s1"); err != ErrStreamNotFound {
		t.Fatalf("after remove: err = %v, want ErrStreamNotFound", err)
	}
}

func TestForEach(t *testing.T) {
	r := NewRegistry()
	r.Add(New("s1", "d1", 1, t0))
	r.Add(New("s2", "d1", 2, t0))
	var ids []string
	r.ForEach(func(s *Stream) { ids = append(ids, s.StreamID) })
	if len(ids) != 2 {
		t.Fatalf("ForEach visited %d, want 2", len(ids))
	}
}
