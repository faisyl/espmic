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

func TestRecorderFLACMinimal(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder("flac", dir, "flac-test", 48000, 2)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if err := rec.Begin(t0()); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, _, err = rec.Finalize(t0().Add(timeSecond))
	if err != nil {
		t.Fatalf("Finalize: %v", err)
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

func t0() time.Time { return time.Unix(1_000_000, 0).UTC() }

const timeSecond = 1000000000
