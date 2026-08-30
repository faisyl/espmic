// Package audio holds the decoded-PCM contract, Opus decoder (real + stub),
// the PCM distribution bus, and the recorder (spec §12-§14).
package audio

import (
	"errors"
	"sync"
	"time"
)

const (
	// FrameDuration is the typical decode frame duration (20ms, spec §10).
	FrameDuration = 20 * time.Millisecond
)

// ErrDecode is returned when the decoder cannot produce PCM for a packet.
var ErrDecode = errors.New("audio: opus decode failed")

// Decoder decodes a single Opus packet into interleaved signed-16-bit PCM at
// 48 kHz stereo (spec §12). Implementations are NOT safe for concurrent use;
// the bus owns one decoder per stream.
type Decoder interface {
	// Decode decodes opus (one Opus packet) into out, returning the number of
	// samples per channel written. out must be large enough for the largest
	// expected frame; use MaxFrameSamples to size it.
	Decode(opus []byte, out []int16) (samplesPerChannel int, err error)

	// Reset returns the decoder to a fresh state (e.g. on stream start).
	Reset()
}

// MaxFrameSamples is the maximum per-channel samples in any 12ms Opus frame
// at 48 kHz (RFC 6716: 5760 samples) rounded up to the decoder's internal
// ceiling.
const MaxFrameSamples = 5760

// StubDecoder is a deterministic test decoder (spec S2 test boundary: no real
// opus decode in tests). It produces silence and reports the expected sample
// count for the configured duration, exercising the bus/recorder paths
// without a codec.
type StubDecoder struct {
	mu       sync.Mutex
	samples  int
	channels int
	calls    int
}

// NewStubDecoder returns a stub producing samplesPerChannel per decode at the
// given channel count.
func NewStubDecoder(samplesPerChannel, channels int) *StubDecoder {
	return &StubDecoder{samples: samplesPerChannel, channels: channels}
}

// Decode returns silence (zeros) for the configured sample count.
func (d *StubDecoder) Decode(_ []byte, out []int16) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	want := d.samples * d.channels
	if len(out) < want {
		return 0, ErrDecode
	}
	for i := 0; i < want; i++ {
		out[i] = 0
	}
	return d.samples, nil
}

// Reset clears the decode call counter.
func (d *StubDecoder) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = 0
}

// Calls returns how many Decode calls have been made.
func (d *StubDecoder) Calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}
