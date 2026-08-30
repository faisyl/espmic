package rtp

import (
	"sync"
	"time"
)

// Stats holds the counters/loss estimators maintained by the jitter buffer
// (spec §11, §18). Values are updated by buffer operations.
type Stats struct {
	Received  uint64
	Lost      uint64
	Duplicate uint64
	Reordered uint64
	Late      uint64
	JitterMS  float64
}

// Option configures a JitterBuffer.
type Option func(*JitterBuffer)

// WithLossHook registers a function invoked once per packet declared lost,
// passing the missing sequence number. This is the Opus PLC hook (spec §11);
// no concealment is performed here.
func WithLossHook(fn func(seq uint16)) Option {
	return func(jb *JitterBuffer) { jb.onLoss = fn }
}

// withLossHookCalls caps how many per-sequence PLC hook invocations a single
// Emit may perform for one gap, so an adversarial far-ahead packet can't make
// one Emit walk the whole sequence range. The lost counter still accounts for
// the full gap arithmetically.
const maxLossHookCalls = 1024

// JitterBuffer reorders RTP packets by sequence number and provides a bounded
// playout delay (spec §11). Packets are emitted in sequence order once their
// playout deadline has elapsed; a missing packet that has exceeded its
// deadline is declared lost and its PLC hook invoked.
type JitterBuffer struct {
	mu       sync.RWMutex
	target   time.Duration
	onLoss   func(seq uint16)
	buf      map[uint16]buffered
	expected uint16
	haveSeq  bool
	maxSeen  uint16
	stats    Stats

	// RFC 3550 jitter estimator state, in RTP clock units (48 kHz).
	prevTransit int64
	havePrev    bool
	jitter      float64
}

type buffered struct {
	p   Packet
	due time.Time
}

// New returns an empty jitter buffer with the given target playout delay
// (spec §11 suggests ~60 ms).
func New(target time.Duration, opts ...Option) *JitterBuffer {
	jb := &JitterBuffer{target: target, buf: make(map[uint16]buffered)}
	for _, o := range opts {
		o(jb)
	}
	return jb
}

// Push submits a received packet with its receipt time. It does not emit; use
// Emit to advance playback with an injected clock.
func (jb *JitterBuffer) Push(p Packet, recv time.Time) {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	jb.stats.Received++
	if !jb.haveSeq {
		jb.expected = p.SequenceNumber
		jb.maxSeen = p.SequenceNumber
		jb.haveSeq = true
		jb.updateJitter(p, recv)
		jb.buf[p.SequenceNumber] = buffered{p, recv.Add(jb.target)}
		return
	}
	if _, ok := jb.buf[p.SequenceNumber]; ok {
		jb.stats.Duplicate++
		return
	}
	if seqBefore(p.SequenceNumber, jb.expected) {
		// A duplicate of an already-played slot is counted as Late (it can no
		// longer be played in order), rather than Duplicate, which is reserved
		// for a sequence still buffered awaiting playout.
		jb.stats.Late++
		return
	}
	if seqBefore(p.SequenceNumber, jb.maxSeen) {
		jb.stats.Reordered++
	} else {
		jb.maxSeen = p.SequenceNumber
	}
	jb.updateJitter(p, recv)
	jb.buf[p.SequenceNumber] = buffered{p, recv.Add(jb.target)}
}

// Emit returns the packets whose playout deadline has passed at now, in
// sequence order, declaring and reporting any intervening gaps as lost
// (spec §11). now is injected so behaviour is deterministic in tests.
func (jb *JitterBuffer) Emit(now time.Time) []Packet {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	var out []Packet
	for {
		cur := jb.expected
		if pkt, ok := jb.buf[cur]; ok {
			if pkt.due.After(now) {
				break
			}
			delete(jb.buf, cur)
			out = append(out, pkt.p)
			jb.expected = seqNext(cur)
			continue
		}
		next, ok := jb.nextPresentDue(now)
		if !ok {
			break
		}
		// The gap [cur, next) is missing. Count it arithmetically (bounded work
		// even for a far-ahead packet) and invoke the PLC hook per sequence,
		// capped so one Emit can't walk the entire sequence range.
		missing := seqDistance(cur, next.p.SequenceNumber)
		jb.stats.Lost += uint64(missing)
		calls := missing
		if calls > maxLossHookCalls {
			calls = maxLossHookCalls
		}
		if jb.onLoss != nil {
			for i := 0; i < calls; i++ {
				jb.onLoss(cur)
				cur = seqNext(cur)
			}
		}
		jb.expected = next.p.SequenceNumber
	}
	return out
}

// Statistics returns a snapshot of the current counters.
func (jb *JitterBuffer) Statistics() Stats {
	jb.mu.RLock()
	defer jb.mu.RUnlock()
	return jb.stats
}

// Len reports how many packets are currently buffered awaiting playout.
func (jb *JitterBuffer) Len() int {
	jb.mu.RLock()
	defer jb.mu.RUnlock()
	return len(jb.buf)
}

// nextPresentDue finds the smallest sequence number present in the buffer whose
// playout deadline has passed, revealing that intervening in-order slots are
// missing.
func (jb *JitterBuffer) nextPresentDue(now time.Time) (buffered, bool) {
	var best *buffered
	for _, b := range jb.buf {
		if b.due.After(now) {
			continue
		}
		if best == nil || seqBefore(b.p.SequenceNumber, best.p.SequenceNumber) {
			bb := b
			best = &bb
		}
	}
	if best == nil {
		return buffered{}, false
	}
	return *best, true
}

// updateJitter applies the RFC 3550 inter-arrival jitter estimate.
//
// recv is converted to RTP clock units (48 kHz): a real second equals
// ClockRate ticks, so ticks = ns * ClockRate / 1e9.
func (jb *JitterBuffer) updateJitter(p Packet, recv time.Time) {
	arrival := int64(float64(recv.UnixNano()) * float64(ClockRate) / 1e9)
	transit := arrival - int64(p.Timestamp)
	if jb.havePrev {
		d := transit - jb.prevTransit
		if d < 0 {
			d = -d
		}
		jb.jitter += (float64(d) - jb.jitter) / 16
		jb.stats.JitterMS = jb.jitter / float64(ClockRate) * 1000
	}
	jb.prevTransit = transit
	jb.havePrev = true
}

// seqBefore reports whether a precedes b in RTP sequence order using RFC 3550
// half-range serial arithmetic (reliable within a window of < 32768). This
// handles the 65535->0 wrap for in-order, late and reorder classification.
func seqBefore(a, b uint16) bool {
	return a != b && int16(a-b) < 0
}

// seqDistance returns the forward cyclic distance from a to b, i.e. how many
// incremental steps take a to b (0 for a == b).
func seqDistance(a, b uint16) int {
	return (int(b) - int(a) + 65536) % 65536
}

func seqNext(s uint16) uint16 {
	return s + 1
}
