package rtp

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"espmic/server/internal/metrics"
)

// ErrUnsolicitedRTP is returned (logged, not fatal) when a packet arrives on a
// port that has no active stream, or with an unexpected SSRC/PT (spec §19:
// ignore unsolicited RTP).
var ErrUnsolicitedRTP = errors.New("rtp: unsolicited packet (no active stream / wrong ssrc/pt)")

// Receiver binds one UDP socket per active stream and feeds parsed packets
// into the S1 jitter buffer (spec §9, §10, §19). It ignores packets that do
// not belong to an active stream (wrong SSRC/PT or no stream on that port).
type Receiver struct {
	mu      sync.Mutex
	streams map[string]*streamBinding // stream_id -> binding
	metrics *metrics.Metrics
	now     func() time.Time
}

type streamBinding struct {
	streamID string
	ssrc     uint32
	pt       uint16
	port     uint16
	pc       net.PacketConn
	jb       *JitterBuffer
	cancel   context.CancelFunc
}

// NewReceiver returns a receiver wired to the shared metrics surface.
func NewReceiver(m *metrics.Metrics) *Receiver {
	return &Receiver{
		streams: make(map[string]*streamBinding),
		metrics: m,
		now:     time.Now,
	}
}

// Bind allocates a UDP port for streamID and starts a read goroutine
// (spec §9: one UDP port per active stream). ssrc is a uint32 (the RTP SSRC
// field width); pt is uint8 (the RTP payload type field width). Both are
// validated on every packet (spec §19).
func (r *Receiver) Bind(ctx context.Context, streamID string, ssrc uint32, pt uint8) (uint16, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.streams[streamID]; ok {
		return 0, errors.New("rtp: stream already bound")
	}
	pc, err := net.ListenPacket("udp", ":0")
	if err != nil {
		return 0, err
	}
	port := uint16(pc.LocalAddr().(*net.UDPAddr).Port)
	ctx, cancel := context.WithCancel(ctx)
	b := &streamBinding{
		streamID: streamID,
		ssrc:     ssrc,
		pt:       uint16(pt),
		port:     port,
		pc:       pc,
		jb:       New(60*time.Millisecond),
		cancel:   cancel,
	}
	r.streams[streamID] = b
	go r.readLoop(ctx, b)
	return port, nil
}

// JitterBuffer returns the jitter buffer for streamID (for tests / S2 wiring).
func (r *Receiver) JitterBuffer(streamID string) (*JitterBuffer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.streams[streamID]
	if !ok {
		return nil, false
	}
	return b.jb, true
}

// CloseStream tears down the UDP socket and goroutine for streamID.
func (r *Receiver) CloseStream(streamID string) {
	r.mu.Lock()
	b, ok := r.streams[streamID]
	delete(r.streams, streamID)
	r.mu.Unlock()
	if !ok {
		return
	}
	b.cancel()
	_ = b.pc.Close()
}

// streamCount returns the number of bound streams (for tests/leak checks).
func (r *Receiver) streamCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.streams)
}

// readLoop reads packets, validates SSRC/PT, and pushes into the jitter
// buffer. Unsolicited packets are dropped (spec §19).
func (r *Receiver) readLoop(ctx context.Context, b *streamBinding) {
	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_ = b.pc.SetReadDeadline(r.now().Add(100 * time.Millisecond))
		n, _, err := b.pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		p, err := ParseFor(buf[:n], uint8(b.pt))
		if err != nil {
			if r.metrics != nil {
				r.metrics.IncOpusDecodeErrors()
			}
			continue
		}
		if p.SSRC != b.ssrc {
			// spec §19: ignore unsolicited
			continue
		}
		b.jb.Push(p, r.now())
		if r.metrics != nil {
			r.metrics.IncRTPPacketsReceived()
		}
	}
}
