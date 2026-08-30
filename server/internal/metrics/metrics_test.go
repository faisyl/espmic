package metrics

import (
	"sync"
	"testing"
)

func TestCounters(t *testing.T) {
	m := New()
	m.IncRTPPacketsReceived()
	m.AddRTPPacketsLost(3)
	m.IncRTPPacketsDuplicate()
	m.IncRTPPacketsReordered()
	m.IncRTPPacketsLate()
	m.IncOpusDecodeErrors()
	m.IncPCMFramesDecoded()
	m.IncStreamDiscontinuity()
	m.AddRecordingBytes(1024)
	m.IncControlReconnects()

	s := m.Snapshot()
	want := map[string]int64{
		LabelRTPPacketsReceived:  1,
		LabelRTPPacketsLost:      3,
		LabelRTPPacketsDuplicate: 1,
		LabelRTPPacketsReordered: 1,
		LabelRTPPacketsLate:      1,
		LabelOpusDecodeErrors:    1,
		LabelPCMFramesDecoded:    1,
		LabelStreamDiscontinuity: 1,
		LabelRecordingBytes:      1024,
		LabelControlReconnects:   1,
	}
	for k, v := range want {
		if s[k] != v {
			t.Errorf("%s = %d, want %d", k, s[k], v)
		}
	}
}

func TestGauges(t *testing.T) {
	m := New()
	m.SetRTPJitterMS(5.0)
	m.SetRTPBitrateBPS(128000)
	s := m.Snapshot()
	if s[LabelRTPJitterMS] != 5 {
		t.Errorf("jitter = %d, want 5", s[LabelRTPJitterMS])
	}
	if s[LabelRTPBitrateBPS] != 128000 {
		t.Errorf("bitrate = %d, want 128000", s[LabelRTPBitrateBPS])
	}
}

func TestSnapshotEmpty(t *testing.T) {
	s := New().Snapshot()
	for _, k := range []string{
		LabelRTPPacketsReceived, LabelRTPPacketsLost, LabelRTPPacketsDuplicate,
		LabelRTPPacketsReordered, LabelRTPPacketsLate, LabelRTPJitterMS,
		LabelRTPBitrateBPS, LabelOpusDecodeErrors, LabelPCMFramesDecoded,
		LabelStreamDiscontinuity, LabelRecordingBytes, LabelControlReconnects,
	} {
		if s[k] != 0 {
			t.Errorf("%s = %d, want 0", k, s[k])
		}
	}
}

func TestConcurrentSafety(t *testing.T) {
	// Best checked with `go test -race`.
	m := New()
	const goroutines, per = 50, 1000
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				m.IncRTPPacketsReceived()
				m.SetRTPJitterMS(1.0)
			}
		}()
	}
	wg.Wait()
	if got := m.rtpPacketsReceived.Load(); got != goroutines*per {
		t.Fatalf("received = %d, want %d", got, goroutines*per)
	}
}
