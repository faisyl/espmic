package audio

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

// FlacRecorder writes decoded PCM to a valid FLAC stream (STREAMINFO + frames)
// so flac/ffmpeg can decode it (spec §13). It implements PCMListener.
type FlacRecorder struct {
	mu        sync.Mutex
	channels  int
	rate      int
	blockSize int
	buf       []int16
	enc       *flac.Encoder
	out       *os.File
	startTime time.Time
	bytes     int64
	closed    bool
	nBlock    uint64
}

// NewFlacRecorder returns a FLAC recorder writing to path, at the given sample
// rate and channels (spec §13: 48000 Hz, stereo).
func NewFlacRecorder(path string, rate, channels, blockSize int) (*FlacRecorder, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	info := &meta.StreamInfo{
		BlockSizeMin:  uint16(blockSize),
		BlockSizeMax:  uint16(blockSize),
		NChannels:     uint8(channels),
		BitsPerSample: 16,
		SampleRate:    uint32(rate),
	}
	enc, err := flac.NewEncoder(f, info)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &FlacRecorder{
		channels:  channels,
		rate:      rate,
		blockSize: blockSize,
		enc:       enc,
		out:       f,
	}, nil
}

// Begin marks the recording start (spec §13).
func (r *FlacRecorder) Begin(start time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startTime = start
	return nil
}

// OnPCM appends decoded frames and encodes complete blocks (spec §13/§14).
func (r *FlacRecorder) OnPCM(f *DecodedAudioFrame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || f == nil {
		return
	}
	r.buf = append(r.buf, f.PCM...)
	for len(r.buf) >= r.blockSize*r.channels {
		r.writeBlock(r.buf[:r.blockSize*r.channels])
		r.buf = r.buf[r.blockSize*r.channels:]
	}
}

func (r *FlacRecorder) writeBlock(pcm []int16) {
	// Deinterleave into per-channel int32 slices.
	chData := make([][]int32, r.channels)
	for c := 0; c < r.channels; c++ {
		chData[c] = make([]int32, r.blockSize)
	}
	for i := 0; i < r.blockSize; i++ {
		for c := 0; c < r.channels; c++ {
			chData[c][i] = int32(pcm[i*r.channels+c])
		}
	}
	subframes := make([]*frame.Subframe, r.channels)
	for c := 0; c < r.channels; c++ {
		subframes[c] = &frame.Subframe{
			SubHeader: frame.SubHeader{
				Pred: frame.PredVerbatim,
			},
			Samples:  chData[c],
			NSamples: r.blockSize,
		}
	}
	r.nBlock++
	fr := &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true,
			BlockSize:         uint16(r.blockSize),
			SampleRate:        uint32(r.rate),
			Channels:          frame.Channels(r.channels - 1),
			BitsPerSample:     16,
			Num:               r.nBlock,
		},
		Subframes: subframes,
	}
	if err := r.enc.WriteFrame(fr); err == nil {
		r.bytes += int64(r.blockSize * r.channels * 2)
	}
}

// Bytes returns the number of encoded payload bytes (spec §18).
func (r *FlacRecorder) Bytes() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes
}

// Finalize flushes the encoder and closes the file (spec §13).
func (r *FlacRecorder) Finalize(end time.Time) (string, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", 0, errors.New("flac: already finalized")
	}
	r.closed = true
	if len(r.buf) > 0 {
		r.writeBlock(r.buf)
		r.buf = nil
	}
	// enc.Close() flushes remaining frames AND closes the underlying file, so
	// closing r.out again here would return "file already closed" (Jim S3 review).
	if err := r.enc.Close(); err != nil {
		return r.out.Name(), r.bytes, err
	}
	return r.out.Name(), r.bytes, nil
}
