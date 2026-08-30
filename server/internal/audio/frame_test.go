package audio

import "testing"

func TestNewFrameDefaults(t *testing.T) {
	f := NewFrame("stream-1", 96000)
	if f.SampleRate != SampleRate {
		t.Errorf("sample rate = %d, want %d", f.SampleRate, SampleRate)
	}
	if f.Channels != Channels {
		t.Errorf("channels = %d, want %d", f.Channels, Channels)
	}
	if f.SampleRate != 48000 || f.Channels != 2 {
		t.Errorf("contract mismatch: rate=%d ch=%d", f.SampleRate, f.Channels)
	}
}

func TestValidFrame(t *testing.T) {
	f := NewFrame("stream-1", 96000)
	f.SampleCountPerChannel = 20
	f.PCM = make([]int16, 20*2) // interleaved stereo
	f.SourceRTPSequenceStart = 100
	f.SourceRTPSequenceEnd = 100
	if !f.Valid() {
		t.Fatal("expected valid frame")
	}
}

func TestInvalidFrame(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*DecodedAudioFrame)
	}{
		{"empty stream id", func(f *DecodedAudioFrame) { f.StreamID = "" }},
		{"wrong sample rate", func(f *DecodedAudioFrame) { f.SampleRate = 44100 }},
		{"wrong channels", func(f *DecodedAudioFrame) { f.Channels = 1 }},
		{"pcm too short", func(f *DecodedAudioFrame) { f.PCM = make([]int16, 20) }},
		{"pcm too long", func(f *DecodedAudioFrame) { f.PCM = make([]int16, 20*2+2) }},
		{"zero sample count with empty pcm is valid", func(f *DecodedAudioFrame) { f.SampleCountPerChannel = 0; f.PCM = nil }},
	}
	// The last case is intentionally valid; assert the rest are invalid.
	for _, tc := range tests {
		f := NewFrame("stream-1", 96000)
		f.SampleCountPerChannel = 20
		f.PCM = make([]int16, 20*2)
		tc.mod(f)
		if tc.name == "zero sample count with empty pcm is valid" {
			if !f.Valid() {
				t.Fatalf("%s: expected valid", tc.name)
			}
			continue
		}
		if f.Valid() {
			t.Fatalf("%s: expected invalid", tc.name)
		}
	}
}

func TestContinuityFlags(t *testing.T) {
	f := NewFrame("stream-1", 96000)
	if f.Discontinuity {
		t.Fatal("new frame should not be a discontinuity")
	}
	f.Discontinuity = true
	f.SampleCountPerChannel = 0
	f.PCM = nil
	if !f.Valid() {
		t.Fatal("discontinuity frame with zero samples should still be valid")
	}
}
