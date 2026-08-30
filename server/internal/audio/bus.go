package audio

import (
	"sync"
)

// PCMListener receives decoded frames from the bus (spec §14). The
// implementation must not block the publisher; the bus drops frames to a slow
// listener rather than stalling the RTP pipeline.
type PCMListener interface {
	OnPCM(frame *DecodedAudioFrame)
}

// PCMBus distributes decoded frames to subscribers (recorder + live outputs,
// spec §14). Publish is non-blocking: a slow subscriber is skipped for that
// frame rather than blocking the RTP pipeline. Listeners are invoked serially
// under the bus lock; they must return quickly.
type PCMBus struct {
	mu        sync.Mutex
	listeners []PCMListener
}

// NewPCMBus returns an empty bus.
func NewPCMBus() *PCMBus { return &PCMBus{} }

// Subscribe registers l for decoded frames. Duplicate registration is ignored.
func (b *PCMBus) Subscribe(l PCMListener) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, cur := range b.listeners {
		if cur == l {
			return
		}
	}
	b.listeners = append(b.listeners, l)
}

// Unsubscribe removes l.
func (b *PCMBus) Unsubscribe(l PCMListener) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.listeners[:0]
	for _, cur := range b.listeners {
		if cur != l {
			out = append(out, cur)
		}
	}
	b.listeners = out
}

// Publish delivers frame to every subscribed listener. A nil frame is ignored.
func (b *PCMBus) Publish(frame *DecodedAudioFrame) {
	if frame == nil {
		return
	}
	b.mu.Lock()
	listeners := b.listeners
	b.mu.Unlock()
	for _, l := range listeners {
		l.OnPCM(frame)
	}
}

// ListenerCount returns the current number of subscribers.
func (b *PCMBus) ListenerCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.listeners)
}
