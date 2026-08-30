package audio

import (
	"bytes"
	"testing"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

// TestFlacRecorderOutput verifies the mewkiz/flac encoder produces a valid
// STREAMINFO block (spec §13). The mewkiz library is a well-tested encoder;
// we verify the wire format is well-formed: fLaC magic + STREAMINFO header.
func TestFlacRecorderOutput(t *testing.T) {
	buf := &bytes.Buffer{}
	info := &meta.StreamInfo{
		BlockSizeMin: 4096,
		BlockSizeMax: 4096,
		NChannels:    2,
		BitsPerSample: 16,
		SampleRate:   48000,
	}
	enc, err := flac.NewEncoder(buf, info)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	pcm := make([]int16, 4096*2)
	for i := range pcm {
		pcm[i] = int16((i % 32767) - 16383)
	}
	chData := make([][]int32, 2)
	for c := 0; c < 2; c++ {
		chData[c] = make([]int32, 4096)
		for i := 0; i < 4096; i++ {
			chData[c][i] = int32(pcm[i*2+c])
		}
	}
	if err := enc.WriteFrame(makeFlacFrame(chData)); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data := buf.Bytes()
	if len(data) < 50 {
		t.Fatalf("output too short: %d", len(data))
	}
	if string(data[0:4]) != "fLaC" {
		t.Fatalf("missing fLaC magic: %q", data[0:4])
	}
	// Metadata block header: 1 bit IsLast=1, 7 bits Type=0 (STREAMINFO).
	if data[4] != 0x80 {
		t.Fatalf("expected last-metadata-block flag, got %#x", data[4])
	}
	// STREAMINFO is 34 bytes = 0x22.
	if data[5] != 0x00 || data[6] != 0x00 || data[7] != 0x22 {
		t.Fatalf("expected STREAMINFO length 34, got %#x %#x %#x", data[5], data[6], data[7])
	}
}

func makeFlacFrame(chData [][]int32) *frame.Frame {
	subframes := make([]*frame.Subframe, len(chData))
	for c, samples := range chData {
		subframes[c] = &frame.Subframe{
			SubHeader: frame.SubHeader{
				Pred: frame.PredVerbatim,
			},
			Samples:  samples,
			NSamples: len(samples),
		}
	}
	return &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true,
			BlockSize:         uint16(len(chData[0])),
			SampleRate:        48000,
			Channels:          frame.Channels(len(chData) - 1),
			BitsPerSample:     16,
			Num:               1,
		},
		Subframes: subframes,
	}
}
