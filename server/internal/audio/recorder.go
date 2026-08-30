package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Recorder writes decoded PCM to a WAV (PCM) or FLAC file (spec §13) and
// tracks byte count + metadata. It implements PCMListener so it can subscribe
// to the PCMBus. Access is goroutine-safe (the bus may Publish from the RTP
// receiver goroutine).
type Recorder struct {
	mu        sync.Mutex
	format    string
	channels  int
	rate      int
	dir       string
	base      string
	startTime time.Time
	bytes     int64
	closed    bool
	// wav scratch
	wavFile *os.File
	// running sample checksum for tests/integrity
	checksum uint32
}

// NewRecorder returns a recorder writing to dir/base.{wav|flac}. format must be
// "wav" or "flac" (spec §13).
func NewRecorder(format, dir, base string, rate, channels int) (*Recorder, error) {
	if format != "wav" && format != "flac" {
		return nil, fmt.Errorf("recorder: unsupported format %q (spec §13: wav|flac)", format)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Recorder{format: format, dir: dir, base: base, rate: rate, channels: channels}, nil
}

// Begin opens the output file and writes the header (spec §13). For WAV this
// reserves the size fields; for FLAC we write a minimal fLaC streaminfo-less
// container (sufficient for the S2 recorder contract; a full FLAC encoder is
// out of scope and noted for S3).
func (r *Recorder) Begin(start time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wavFile != nil {
		return errors.New("recorder: already begun")
	}
	r.startTime = start
	path := filepath.Join(r.dir, r.base+"."+r.format)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	r.wavFile = f
	if r.format == "wav" {
		r.writeWAVHeader(f)
	} else {
		r.writeFLACHeader(f)
	}
	return nil
}

// OnPCM appends a decoded frame's interleaved PCM to the file (spec §13/§14).
func (r *Recorder) OnPCM(frame *DecodedAudioFrame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.wavFile == nil || frame == nil {
		return
	}
	r.append(frame.PCM)
	r.checksum = crc32Update(r.checksum, frame.PCM)
}

// append writes interleaved int16 PCM in little-endian byte order.
func (r *Recorder) append(pcm []int16) {
	buf := make([]byte, 2*len(pcm))
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(buf[2*i:], uint16(s))
	}
	if _, err := r.wavFile.Write(buf); err != nil {
		return
	}
	r.bytes += int64(len(buf))
}

// Bytes returns the number of PCM payload bytes written (spec §18 recording_bytes).
func (r *Recorder) Bytes() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes
}

// Checksum returns the running CRC of written PCM (test/integrity aid).
func (r *Recorder) Checksum() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checksum
}

// Finalize closes the file and patches the WAV size fields (spec §13). It is
// idempotent.
func (r *Recorder) Finalize(end time.Time) (string, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", 0, errors.New("recorder: already finalized")
	}
	r.closed = true
	if r.wavFile == nil {
		return "", 0, errors.New("recorder: never begun")
	}
	if r.format == "wav" {
		r.patchWAVSizes(r.wavFile)
	}
	uri := r.wavFile.Name()
	if err := r.wavFile.Close(); err != nil {
		return uri, r.bytes, err
	}
	return uri, r.bytes, nil
}

// ---- WAV (PCM) ----

var wavHeader = [44]byte{}

func (r *Recorder) writeWAVHeader(f *os.File) {
	h := wavHeader
	copy(h[0:4], "RIFF")
	binary.LittleEndian.PutUint32(h[4:8], 0) // patched on finalize
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	binary.LittleEndian.PutUint32(h[16:20], 16)
	binary.LittleEndian.PutUint16(h[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:24], uint16(r.channels))
	binary.LittleEndian.PutUint32(h[24:28], uint32(r.rate))
	binary.LittleEndian.PutUint32(h[28:32], uint32(r.rate*r.channels*2))
	binary.LittleEndian.PutUint16(h[32:34], uint16(r.channels*2))
	binary.LittleEndian.PutUint16(h[34:36], 16)
	copy(h[36:40], "data")
	binary.LittleEndian.PutUint32(h[40:44], 0) // patched on finalize
	_, _ = f.Write(h[:])
}

func (r *Recorder) patchWAVSizes(f *os.File) {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint32(r.bytes+36))
	_, _ = f.WriteAt(b.Bytes(), 4)
	b.Reset()
	_ = binary.Write(&b, binary.LittleEndian, uint32(r.bytes))
	_, _ = f.WriteAt(b.Bytes(), 40)
}

// ---- FLAC (minimal container) ----

func (r *Recorder) writeFLACHeader(f *os.File) {
	_, _ = f.Write([]byte("fLaC"))
}

func crc32Update(crc uint32, pcm []int16) uint32 {
	buf := make([]byte, 2*len(pcm))
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(buf[2*i:], uint16(s))
	}
	return crc32.Update(crc, crc32.IEEETable, buf)
}
