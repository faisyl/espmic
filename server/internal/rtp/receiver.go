package rtp

import (
	"context"
	"errors"
	"net"
	"strconv"
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

	onPacket     func(streamID string, first bool)                // per-accepted-packet liveness callback
	onCompressed func(streamID string, ts uint32, payload []byte) // per-accepted-packet compressed Opus payload
}

type streamBinding struct {
	streamID     string
	ssrc         uint32
	ssrcLearned  bool
	firstSeen    bool // true once the first valid packet has been accepted
	pt           uint16
	port         uint16
	pc           net.PacketConn
	jb           *JitterBuffer
	cancel       context.CancelFunc
	workerCancel context.CancelFunc // for stopping the audio worker
}

// JitterBuffer returns the jitter buffer for this stream binding.
func (b *streamBinding) JitterBuffer() *JitterBuffer {
	return b.jb
}

// Port returns the UDP port for this stream binding.
func (b *streamBinding) Port() uint16 {
	return b.port
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
// (spec §9: one UDP port per active stream). pt is uint8 (the RTP payload
// type field width). Both are validated on every packet (spec §19).
// jitterTarget sets the target playout delay for the jitter buffer; when zero
// it defaults to 60ms (spec §11). bindPort is the UDP port to bind; 0 =
// dynamic (current behavior).
func (r *Receiver) Bind(ctx context.Context, streamID string, pt uint8, jitterTarget time.Duration, bindPort int) (uint16, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.streams[streamID]; ok {
		return 0, errors.New("rtp: stream already bound")
	}
	addr := ":0"
	if bindPort > 0 {
		addr = net.JoinHostPort("", strconv.Itoa(bindPort))
	}
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return 0, err
	}
	// Bump SO_RCVBUF so the kernel doesn't drop packets under load while the
	// readLoop does per-packet work (push + callbacks).
	if udpConn, ok := pc.(*net.UDPConn); ok {
		_ = udpConn.SetReadBuffer(1024 * 1024)
	}
	port := uint16(pc.LocalAddr().(*net.UDPAddr).Port)
	if jitterTarget <= 0 {
		jitterTarget = 60 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(ctx)
	b := &streamBinding{
		streamID:    streamID,
		ssrc:        0,
		ssrcLearned: false,
		pt:          uint16(pt),
		port:        port,
		pc:          pc,
		jb:          New(jitterTarget),
		cancel:      cancel,
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

// StreamStats returns the per-stream RTP counters for streamID (received, lost, jitter).
func (r *Receiver) StreamStats(streamID string) (Stats, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.streams[streamID]
	if !ok {
		return Stats{}, false
	}
	return b.jb.Statistics(), true
}

// GetStreamBinding returns the streamBinding for streamID (for wiring worker to jitter buffer).
func (r *Receiver) GetStreamBinding(streamID string) (*streamBinding, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.streams[streamID]
	if !ok {
		return nil, false
	}
	return b, true
}

// StreamPort returns the UDP port for streamID.
func (r *Receiver) StreamPort(streamID string) (uint16, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.streams[streamID]
	if !ok {
		return 0, false
	}
	return b.Port(), true
}

// SetWorkerCancel sets the worker cancel function for a stream.
func (r *Receiver) SetWorkerCancel(streamID string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.streams[streamID]; ok {
		b.workerCancel = cancel
	}
}

// SetOnPacket sets the per-accepted-packet liveness callback. The callback
// fires for every SSRC/PT-validated packet pushed into the jitter buffer,
// independent of decode/playout. first=true for the first valid packet on the
// stream (triggers RTP_WAIT->ACTIVE), false thereafter (refreshes the RTP
// disappearance clock). This decouples stream liveness from the audio worker
// so a hanging/erroring decoder cannot stall liveness.
func (r *Receiver) SetOnPacket(cb func(streamID string, first bool)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onPacket = cb
}

// SetOnCompressed sets the per-accepted-packet compressed payload callback.
// The callback fires for every SSRC/PT-validated packet with the RTP timestamp
// and Opus payload. This feeds the Opus-to-disk recorder without any
// decode/playout dependency.
func (r *Receiver) SetOnCompressed(cb func(streamID string, ts uint32, payload []byte)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onCompressed = cb
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
	// Cancel worker first (if running)
	if b.workerCancel != nil {
		b.workerCancel()
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
// SSRC is learned from the first VALID packet (correct PT + parseable RTP)
// and then enforced for the stream lifetime (spec §8: device-chosen SSRC).
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

		// Learn SSRC from first VALID packet (correct PT + parseable RTP).
		// Guard with receiver lock to avoid data race on ssrc/ssrcLearned/firstSeen/onPacket.
		r.mu.Lock()
		if !b.ssrcLearned {
			b.ssrc = p.SSRC
			b.ssrcLearned = true
		}
		expectedSSRC := b.ssrc
		cb := r.onPacket
		compCb := r.onCompressed
		var first bool
		if cb != nil {
			first = !b.firstSeen
			b.firstSeen = true
		}
		r.mu.Unlock()

		if p.SSRC != expectedSSRC {
			// spec §19: ignore unsolicited / foreign SSRC
			continue
		}
		b.jb.Push(p, r.now())
		if r.metrics != nil {
			r.metrics.IncRTPPacketsReceived()
		}

		// Fire liveness callback for every accepted packet (spec §17:
		// RTP_WAIT->ACTIVE on first, then refresh the RTP-disappear clock).
		// This decouples liveness from the audio worker/decoder.
		if cb != nil {
			cb(b.streamID, first)
		}
		// Fire compressed payload callback (feeds Opus-to-disk recorder).
		if compCb != nil {
			compCb(b.streamID, p.Timestamp, p.Payload)
		}
	}
}
