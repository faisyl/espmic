package audio

import (
	"context"
	"sync"
	"time"

	"espmic/server/internal/metrics"
	"espmic/server/internal/rtp"
)

// Worker drains a jitter buffer, decodes each RTP/Opus packet, and publishes
// DecodedAudioFrame to the PCM bus (spec §11→§12→§14). It runs one goroutine
// per active stream and is driven by ctx for teardown.
type Worker struct {
	streamID       string
	jb             *rtp.JitterBuffer
	decoder        Decoder
	bus            *PCMBus
	metrics        *metrics.Metrics
	now            func() time.Time
	onPacket       func(first bool) // callback for stream state (FirstPacket/Packet)
	firstPacketSent bool             // track if FirstPacket has been fired

	mu     sync.Mutex
	cancel context.CancelFunc
}

// NewWorker returns a worker bound to the given jitter buffer, decoder and
// bus. Ownership of the decoder stays with the Worker (one decoder per
// stream, Decoder is not concurrency-safe, spec §12). onPacket is called
// for each packet dequeued from the jitter buffer; first=true on the first
// packet (triggers RTP_WAIT->ACTIVE), false thereafter (refreshes ACTIVE clock).
func NewWorker(streamID string, jb *rtp.JitterBuffer, dec Decoder, bus *PCMBus, m *metrics.Metrics, onPacket func(first bool)) *Worker {
	return &Worker{
		streamID:  streamID,
		jb:        jb,
		decoder:   dec,
		bus:       bus,
		metrics:   m,
		now:       time.Now,
		onPacket:  onPacket,
	}
}

// Start begins draining the jitter buffer until ctx ends. It emits frames in
// playout order (spec §11).
func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	ctx, w.cancel = context.WithCancel(ctx)
	w.mu.Unlock()

	outBuf := make([]int16, MaxFrameSamples*Channels)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		packets := w.jb.Emit(w.now())
		for _, p := range packets {
			// Fire stream state callback for EVERY packet dequeued from jitter buffer
			// (not gated on successful decode) — spec §17: RTP_WAIT->ACTIVE on first packet
			if w.onPacket != nil {
				first := !w.firstPacketSent
				w.firstPacketSent = true
				w.onPacket(first)
			}

			spc, err := w.decoder.Decode(p.Payload, outBuf)
			if err != nil {
				if w.metrics != nil {
					w.metrics.IncOpusDecodeErrors()
				}
				continue
			}
			frame := NewFrame(w.streamID, p.Timestamp)
			frame.SampleCountPerChannel = uint32(spc)
			frame.PCM = append([]int16(nil), outBuf[:spc*Channels]...)
			frame.SourceRTPSequenceStart = p.SequenceNumber
			frame.SourceRTPSequenceEnd = p.SequenceNumber
			if w.metrics != nil {
				w.metrics.IncPCMFramesDecoded()
			}
			w.bus.Publish(frame)
		}
	}
}

// Stop cancels the worker's context (spec §14 teardown).
func (w *Worker) Stop() {
	w.mu.Lock()
	if w.cancel != nil {
		w.cancel()
	}
	w.mu.Unlock()
}
