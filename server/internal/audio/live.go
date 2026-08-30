package audio

import (
	"context"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// upgrader configures the WebSocket upgrade (spec §14 live distribution).
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 16384,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// LiveOutput is a WebSocket PCM consumer subscribed to the PCM bus
// (spec §14). It is decoupled from RTP ingest: the bus fans decoded frames
// here. Per-client buffering is drop-safe — slow clients miss frames rather
// than stalling the pipeline.
type LiveOutput struct {
	mu     sync.Mutex
	ws     *websocket.Conn
	bus    *PCMBus
	sendCh chan *DecodedAudioFrame
	cancel context.CancelFunc
}

// NewLiveOutput upgrades the HTTP request to a WebSocket and subscribes to
// the bus (spec §14). The returned output is already running a write loop
// until ctx ends or the socket closes.
func NewLiveOutput(ctx context.Context, w http.ResponseWriter, r *http.Request, bus *PCMBus) (*LiveOutput, error) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	l := &LiveOutput{
		ws:     ws,
		bus:    bus,
		sendCh: make(chan *DecodedAudioFrame, 32),
		cancel: cancel,
	}
	bus.Subscribe(l)
	go l.writeLoop(ctx)
	return l, nil
}

// OnPCM enqueues a frame for the WebSocket client (spec §14). Drops if the
// client buffer is full (drop-safe, non-blocking).
func (l *LiveOutput) OnPCM(frame *DecodedAudioFrame) {
	select {
	case l.sendCh <- frame:
	default:
		// drop: slow client
	}
}

// writeLoop serialises PCM frames to the WebSocket as binary messages
// (interleaved int16 LE, spec §12/§14).
func (l *LiveOutput) writeLoop(ctx context.Context) {
	defer func() {
		l.bus.Unsubscribe(l)
		l.ws.Close()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-l.sendCh:
			if f == nil {
				return
			}
			if err := l.ws.WriteMessage(websocket.BinaryMessage, pcmBytes(f.PCM)); err != nil {
				return
			}
		}
	}
}

// pcmBytes converts interleaved int16 PCM to a little-endian byte slice.
func pcmBytes(pcm []int16) []byte {
	out := make([]byte, 2*len(pcm))
	for i, s := range pcm {
		out[2*i] = byte(s)
		out[2*i+1] = byte(s >> 8)
	}
	return out
}
