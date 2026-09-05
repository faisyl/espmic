package stream

import (
	"errors"
	"sync"
	"time"
)

// StreamState enumerates the stream lifecycle (spec §17).
type StreamState string

const (
	StateCreated          StreamState = "CREATED"
	StateWaitingForDevice StreamState = "WAITING_FOR_DEVICE"
	StateStarting         StreamState = "STARTING"
	StateRTPWait          StreamState = "RTP_WAIT"
	StateActive           StreamState = "ACTIVE"
	StateStopping         StreamState = "STOPPING"
	StateComplete         StreamState = "COMPLETE"
	StateFailed           StreamState = "FAILED"
)

// Lifecycle timeouts (spec §17). All configurable.
const (
	DefaultRTPWaitTimeout      = 5 * time.Second
	DefaultRTPDisappearTimeout = 1 * time.Second
)

// ErrStreamNotFound is returned when a stream_id is unknown.
var ErrStreamNotFound = errors.New("stream: unknown stream")

// ErrIllegalTransition is returned for a state/event combination the §17
// lifecycle does not permit.
var ErrIllegalTransition = errors.New("stream: illegal lifecycle transition")

// Failure reasons mirror spec §17 failure edges.
type FailureReason string

const (
	FailureNone           FailureReason = ""
	FailureStartRejected  FailureReason = "STARTING->FAILED"
	FailureRTPWaitTimeout FailureReason = "RTP_WAIT->TIMEOUT"
	FailureRTPTimeout     FailureReason = "ACTIVE->RTP_TIMEOUT"
	FailureDeviceDisc     FailureReason = "ACTIVE->DEVICE_DISCONNECTED"
	FailureDecodeError    FailureReason = "ACTIVE->DECODE_ERROR"
)

// Stream holds the authoritative lifecycle state for one stream (spec §6, §17).
// All transitions go through the §17 lifecycle; timestamps track the RTP_WAIT
// and ACTIVE timeouts. Access is guarded by the owning Registry; the Stream
// methods take an injected clock (now) for deterministic testing.
// Concurrency: all state mutations and reads are guarded by an internal RWMutex.
// State reads use State() accessor; internal methods hold Lock/RLock as needed.
type Stream struct {
	StreamID  string
	DeviceID  string
	SSRC      uint32
	state     StreamState // private; use State() accessor
	StartedAt time.Time
	Reason    FailureReason

	streamStartedAt time.Time // when device sent stream_started (RTP_WAIT clock)
	lastPacketAt    time.Time // last RTP packet in ACTIVE (RTP_TIMEOUT clock)

	cfg TimeoutConfig
	mu  sync.RWMutex
}

// TimeoutConfig holds per-stream lifecycle deadlines (spec §17 "all values
// configurable"). Zero values fall back to the package defaults.
type TimeoutConfig struct {
	RTPWait      time.Duration
	RTPDisappear time.Duration
}

func (c TimeoutConfig) wait() time.Duration {
	if c.RTPWait > 0 {
		return c.RTPWait
	}
	return DefaultRTPWaitTimeout
}

func (c TimeoutConfig) disappear() time.Duration {
	if c.RTPDisappear > 0 {
		return c.RTPDisappear
	}
	return DefaultRTPDisappearTimeout
}

// State returns the current stream state (thread-safe accessor).
func (s *Stream) State() StreamState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// New returns a Stream in CREATED.
func New(id, deviceID string, ssrc uint32, startedAt time.Time) *Stream {
	return &Stream{
		StreamID:  id,
		DeviceID:  deviceID,
		SSRC:      ssrc,
		state:     StateCreated,
		StartedAt: startedAt,
		cfg:       TimeoutConfig{},
	}
}

// WithTimeoutConfig sets the per-stream deadline overrides (spec §17).
func (s *Stream) WithTimeoutConfig(cfg TimeoutConfig) *Stream {
	s.cfg = cfg
	return s
}

// Start transitions CREATED -> WAITING_FOR_DEVICE (spec §17).
func (s *Stream) Start(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateCreated {
		return ErrIllegalTransition
	}
	s.state = StateWaitingForDevice
	s.StartedAt = now
	return nil
}

// DeviceCommandSent transitions CREATED/WAITING_FOR_DEVICE -> STARTING (the
// server has issued start_stream to the device, spec §9). Accepts either
// state so a stream can be started directly or via the WAITING_FOR_DEVICE
// intermediate.
func (s *Stream) DeviceCommandSent() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateCreated && s.state != StateWaitingForDevice {
		return ErrIllegalTransition
	}
	s.state = StateStarting
	return nil
}

// DeviceRejected transitions STARTING -> FAILED (device declined/nack).
func (s *Stream) DeviceRejected(reason FailureReason) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateStarting {
		return ErrIllegalTransition
	}
	if reason == FailureNone {
		reason = FailureStartRejected
	}
	s.state = StateFailed
	s.Reason = reason
	return nil
}

// StreamStarted transitions STARTING -> RTP_WAIT and starts the 5s deadline
// (spec §17: TIMEOUT edge RTP_WAIT->TIMEOUT@5s after stream_started).
func (s *Stream) StreamStarted(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateStarting {
		return ErrIllegalTransition
	}
	s.state = StateRTPWait
	s.streamStartedAt = now
	return nil
}

// FirstPacket transitions RTP_WAIT -> ACTIVE on the first RTP packet (spec §17).
func (s *Stream) FirstPacket(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRTPWait {
		return ErrIllegalTransition
	}
	s.state = StateActive
	s.lastPacketAt = now
	return nil
}

// Packet refreshes the RTP disappearance clock in ACTIVE (spec §17: ~1s without
// a packet => RTP_TIMEOUT).
func (s *Stream) Packet(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateActive {
		return
	}
	s.lastPacketAt = now
}

// RTPWaitTimedOut reports whether the RTP_WAIT->TIMEOUT deadline has elapsed
// (spec §17). Only meaningful in RTP_WAIT.
func (s *Stream) RTPWaitTimedOut(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state != StateRTPWait {
		return false
	}
	return now.Sub(s.streamStartedAt) >= s.cfg.wait()
}

// RTPDisappeared reports whether the ACTIVE->RTP_TIMEOUT deadline has elapsed
// (~1s without packets, spec §17). Only meaningful in ACTIVE.
func (s *Stream) RTPDisappeared(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state != StateActive {
		return false
	}
	return now.Sub(s.lastPacketAt) >= s.cfg.disappear()
}

// StopRequested transitions ACTIVE -> STOPPING (server issued stop_stream).
func (s *Stream) StopRequested() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateActive {
		return ErrIllegalTransition
	}
	s.state = StateStopping
	return nil
}

// Stopped transitions STOPPING -> COMPLETE (device sent stream_stopped).
func (s *Stream) Stopped() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateStopping {
		return ErrIllegalTransition
	}
	s.state = StateComplete
	return nil
}

// DeviceDisconnected transitions ACTIVE -> DEVICE_DISCONNECTED (spec §17).
func (s *Stream) DeviceDisconnected() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateActive {
		return ErrIllegalTransition
	}
	s.state = StateFailed
	s.Reason = FailureDeviceDisc
	return nil
}

// DecodeError transitions ACTIVE -> DECODE_ERROR (spec §17).
func (s *Stream) DecodeError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateActive {
		return ErrIllegalTransition
	}
	s.state = StateFailed
	s.Reason = FailureDecodeError
	return nil
}

// Registry holds active streams (spec §6). It is concurrency-safe for the
// control session and RTP receiver goroutines that drive the lifecycle.
type Registry struct {
	mu      sync.RWMutex
	streams map[string]*Stream
}

// NewRegistry returns an empty stream registry.
func NewRegistry() *Registry { return &Registry{streams: make(map[string]*Stream)} }

// Add registers a stream (typically CREATED).
func (r *Registry) Add(s *Stream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streams[s.StreamID] = s
}

// Get returns the stream for id.
func (r *Registry) Get(id string) (*Stream, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.streams[id]
	if !ok {
		return nil, ErrStreamNotFound
	}
	return s, nil
}

// List returns all streams.
func (r *Registry) List() []*Stream {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Stream, 0, len(r.streams))
	for _, s := range r.streams {
		out = append(out, s)
	}
	return out
}

// Remove drops a stream from the registry.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.streams, id)
}

// ForEach invokes fn for every stream under a read lock (spec §20 cleanup).
func (r *Registry) ForEach(fn func(*Stream)) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.streams {
		fn(s)
	}
}

// Count returns the number of tracked streams.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.streams)
}
