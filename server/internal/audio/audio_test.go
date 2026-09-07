package audio

import (
	"os"
	"sync"
	"testing"
	"time"
)

func TestStubDecoderSilence(t *testing.T) {
	d := NewStubDecoder(960, 2)
	out := make([]int16, 960*2)
	n, err := d.Decode([]byte{0x01, 0x02}, out)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if n != 960 {
		t.Fatalf("samples = %d, want 960", n)
	}
	for i, s := range out {
		if s != 0 {
			t.Fatalf("sample[%d] = %d, want 0 (silence)", i, s)
		}
	}
	if d.Calls() != 1 {
		t.Fatalf("calls = %d, want 1", d.Calls())
	}
	d.Reset()
	if d.Calls() != 0 {
		t.Fatalf("after reset calls = %d", d.Calls())
	}
}

func TestStubDecoderOutputTooSmall(t *testing.T) {
	d := NewStubDecoder(960, 2)
	out := make([]int16, 100) // too small
	if _, err := d.Decode([]byte{0x01}, out); err == nil {
		t.Fatal("expected ErrDecode for too-small output")
	}
}

func TestPCMBusPublishSubscribe(t *testing.T) {
	bus := NewPCMBus()
	var mu sync.Mutex
	var got []*DecodedAudioFrame
	l := &collectListener{mu: &mu, frames: &got}
	bus.Subscribe(l)

	f := NewFrame("s1", 0)
	f.SampleCountPerChannel = 20
	f.PCM = make([]int16, 40)
	bus.Publish(f)

	if bus.ListenerCount() != 1 {
		t.Fatalf("listeners = %d, want 1", bus.ListenerCount())
	}

	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("frames delivered = %d, want 1", n)
	}
}

func TestPCMBusUnsubscribe(t *testing.T) {
	bus := NewPCMBus()
	l := &collectListener{}
	bus.Subscribe(l)
	bus.Subscribe(l) // duplicate ignored
	if bus.ListenerCount() != 1 {
		t.Fatalf("listeners = %d, want 1", bus.ListenerCount())
	}
	bus.Unsubscribe(l)
	if bus.ListenerCount() != 0 {
		t.Fatalf("listeners = %d, want 0", bus.ListenerCount())
	}
}

func TestPCMBusNilFrameIgnored(t *testing.T) {
	bus := NewPCMBus()
	l := &collectListener{}
	bus.Subscribe(l)
	bus.Publish(nil) // should not panic or deliver
	if bus.ListenerCount() != 1 {
		t.Fatal("bus should remain subscribed")
	}
}

type collectListener struct {
	mu     *sync.Mutex
	frames *[]*DecodedAudioFrame
}

func (l *collectListener) OnPCM(f *DecodedAudioFrame) {
	if l.mu != nil {
		l.mu.Lock()
		defer l.mu.Unlock()
	}
	if l.frames != nil {
		*l.frames = append(*l.frames, f)
	}
}

func TestRecorderWAVOutput(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder("wav", dir, "test", 48000, 2)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if err := rec.Begin(t0()); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	f := NewFrame("s1", 0)
	f.SampleCountPerChannel = 100
	f.PCM = make([]int16, 200)
	for i := range f.PCM {
		f.PCM[i] = int16(i)
	}
	rec.OnPCM(f)
	rec.OnPCM(f)

	uri, n, err := rec.Finalize(t0().Add(timeSecond))
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if n != 800 {
		t.Fatalf("bytes = %d, want 800 (2 frames * 200 samples * 2 bytes)", n)
	}
	if _, err := os.Stat(uri); err != nil {
		t.Fatalf("stat %s: %v", uri, err)
	}
}

func TestRecorderFLACRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := NewRecorder("flac", dir, "flac-test", 48000, 2)
	if err == nil {
		t.Fatal("expected error for FLAC format")
	}
	// Verify error message is clear
	if err.Error() != "recorder: unsupported format \"flac\" (only WAV is supported; FLAC not implemented)" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRecorderInvalidFormat(t *testing.T) {
	if _, err := NewRecorder("mp3", t.TempDir(), "x", 48000, 2); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}

func TestRecorderIdempotentFinalize(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder("wav", dir, "dup", 48000, 2)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if err := rec.Begin(t0()); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, _, err = rec.Finalize(t0().Add(timeSecond)); err != nil {
		t.Fatalf("first finalize: %v", err)
	}
	if _, _, err = rec.Finalize(t0().Add(2 * timeSecond)); err == nil {
		t.Fatal("second finalize should fail")
	}
}

func TestOpusRecorderWritesValidOgg(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewOpusRecorder(dir, "stream1", 48000, 2, 312)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a few RTP Opus packets with sequential timestamps
	for i := 0; i < 5; i++ {
		rec.WritePacket(uint32(i*960), []byte{0x42, 0x42})
	}
	uri, bytes, err := rec.Finalize(t0().Add(timeSecond))
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if uri == "" {
		t.Fatal("expected non-empty URI")
	}
	if bytes == 0 {
		t.Fatal("expected non-zero byte count")
	}
	data, err := os.ReadFile(uri)
	if err != nil {
		t.Fatal(err)
	}
	// Print hex for debugging
	t.Logf("file size=%d hex=%x", len(data), data[:min(80, len(data))])
	// Verify OggS sync pattern at start of file
	if len(data) < 4 || string(data[0:4]) != "OggS" {
		t.Fatal("missing OggS sync pattern — not a valid Ogg file")
	}
	// Verify OpusHead capture pattern exists anywhere in the file
	if !containsOpusHead(data) {
		t.Fatalf("missing OpusHead capture pattern — invalid Ogg/Opus file (size=%d hex=%x)", len(data), data[:min(80, len(data))])
	}
	// Verify OpusTags capture pattern exists
	if !containsOpusTags(data) {
		t.Fatal("missing OpusTags capture pattern — invalid Ogg/Opus file")
	}
}

func containsOpusHead(data []byte) bool {
	for i := 0; i <= len(data)-8; i++ {
		if string(data[i:i+8]) == "OpusHead" {
			return true
		}
	}
	return false
}

func containsOpusTags(data []byte) bool {
	// Scan for "OpusTags" capture pattern anywhere in the file
	for i := 0; i <= len(data)-8; i++ {
		if string(data[i:i+8]) == "OpusTags" {
			return true
		}
	}
	return false
}

func t0() time.Time { return time.Unix(1_000_000, 0).UTC() }

const timeSecond = 1000000000
