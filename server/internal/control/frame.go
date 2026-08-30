package control

import (
	"encoding/binary"
	"errors"
	"io"
)

// MaxPayloadBytes is the maximum accepted control frame payload (spec §7).
const MaxPayloadBytes = 16 * 1024

// ErrFrameTooLarge is returned when a frame's declared length exceeds
// MaxPayloadBytes. It is checked from the header BEFORE the payload is
// read or allocated (spec §7).
var ErrFrameTooLarge = errors.New("control: payload length exceeds 16 KiB limit")

// ReadFrame reads one length-prefixed payload from r (spec §7).
//
// Wire format: uint32_be payload_length followed by payload_length bytes of
// UTF-8 JSON. A declared length over MaxPayloadBytes is rejected before any
// payload is read. Clean end-of-stream is reported as io.EOF.
func ReadFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxPayloadBytes {
		return nil, ErrFrameTooLarge
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// WriteFrame writes payload to w as a single length-prefixed frame (spec §7).
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxPayloadBytes {
		return ErrFrameTooLarge
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// FrameReader incrementally assembles length-prefixed frames from pushed wire
// bytes. It distinguishes a complete frame from the expectation of more bytes,
// and rejects an oversized length from the header without allocating the
// payload (spec §7).
type FrameReader struct {
	buf []byte
}

// Push appends raw wire bytes and returns every complete frame decoded from
// them. needMore is true when a partial frame is still being accumulated.
// err, when non-nil, is a fatal framing error (ErrFrameTooLarge).
func (fr *FrameReader) Push(data []byte) (frames [][]byte, needMore bool, err error) {
	fr.buf = append(fr.buf, data...)
	for {
		payload, status, ferr := fr.next()
		if ferr != nil {
			return frames, false, ferr
		}
		if status != frameComplete {
			break
		}
		frames = append(frames, payload)
	}
	return frames, len(fr.buf) > 0, nil
}

type frameStatus int

const (
	frameComplete frameStatus = iota
	frameNeedMore
	frameError
)

// next inspects the buffered bytes and attempts to extract a single frame.
func (fr *FrameReader) next() ([]byte, frameStatus, error) {
	if len(fr.buf) < 4 {
		return nil, frameNeedMore, nil
	}
	n := binary.BigEndian.Uint32(fr.buf[:4])
	if n > MaxPayloadBytes {
		return nil, frameError, ErrFrameTooLarge
	}
	need := 4 + int(n)
	if len(fr.buf) < need {
		return nil, frameNeedMore, nil
	}
	payload := append([]byte(nil), fr.buf[4:need]...)
	fr.buf = fr.buf[need:]
	return payload, frameComplete, nil
}
