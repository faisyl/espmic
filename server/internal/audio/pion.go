package audio

import (
	"errors"

	pionopus "github.com/pion/opus"
)

// PionDecoder wraps the pure-Go pion/opus decoder (no cgo) behind our Decoder
// interface (spec §12, §4 "pure-Go" preference). It decodes one Opus packet
// into interleaved int16 PCM at 48 kHz.
type PionDecoder struct {
	decoder pionopus.Decoder
}

// NewPionDecoder returns an initialised pion/opus decoder outputting 48 kHz /
// stereo (spec §10, §12). The decoder is created empty and lazily initialised
// on first Reset.
func NewPionDecoder() *PionDecoder {
	return &PionDecoder{}
}

// Reset (re)initialises the underlying pion/opus decoder for 48 kHz stereo.
func (d *PionDecoder) Reset() {
	var err error
	d.decoder, err = pionopus.NewDecoderWithOutput(SampleRate, Channels)
	if err != nil {
		// 48k/stereo is always supported by pion/opus; ignore.
		panic(err)
	}
}

// Decode decodes one Opus packet into out and returns samples per channel.
// pion/opus's DecodeToInt16 already returns the per-channel sample count.
func (d *PionDecoder) Decode(opus []byte, out []int16) (int, error) {
	n, err := d.decoder.DecodeToInt16(opus, out)
	if err != nil {
		return 0, errors.Join(ErrDecode, err)
	}
	return n, nil
}
