package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// CommandService validates stream requests and builds/sends device commands
// (spec §9). It correlates responses via the stream registry. The actual
// transport is the Session; CommandService is the request/validation layer.
type CommandService struct {
	mu      sync.Mutex
	pending map[string]chan Message // stream_id -> response channel
}

// NewCommandService returns an empty service.
func NewCommandService() *CommandService {
	return &CommandService{pending: make(map[string]chan Message)}
}

// ValidateStart checks a start_stream request is well-formed (spec §9).
func (s *CommandService) ValidateStart(streamID, deviceID string, ssrc uint32) error {
	if streamID == "" {
		return errors.New("control: start_stream requires stream_id")
	}
	if deviceID == "" {
		return errors.New("control: start_stream requires device_id")
	}
	if ssrc == 0 {
		return errors.New("control: start_stream requires non-zero ssrc")
	}
	return nil
}

// BuildStartStream constructs a start_stream command (spec §9). The server
// allocates the destination port internally (spec §16: caller should not
// provide an arbitrary UDP destination).
func (s *CommandService) BuildStartStream(streamID string, ssrc uint32, destPort uint16) *StartStream {
	return NewStartStream(streamID, ssrc, destPort)
}

// BuildStopStream constructs a stop_stream command (spec §9).
func (s *CommandService) BuildStopStream(streamID string) *StopStream {
	return NewStopStream(streamID)
}

// BuildGetStatus constructs a get_status command (spec §8).
func (s *CommandService) BuildGetStatus() *GetStatus {
	return NewGetStatus()
}

// Await registers a correlation channel for stream_id and returns it. The
// caller reads the device's response (stream_started / stream_stopped / error)
// from the returned channel.
func (s *CommandService) Await(ctx context.Context, streamID string) (Message, error) {
	s.mu.Lock()
	if _, ok := s.pending[streamID]; ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("control: duplicate await for %q", streamID)
	}
	ch := make(chan Message, 1)
	s.pending[streamID] = ch
	s.mu.Unlock()

	select {
	case msg := <-ch:
		s.mu.Lock()
		delete(s.pending, streamID)
		s.mu.Unlock()
		return msg, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, streamID)
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Deliver routes an inbound message to its awaiting caller by stream_id
// (spec §9 correlation). Returns true if a caller was waiting.
func (s *CommandService) Deliver(msg Message) bool {
	if msg == nil {
		return false
	}
	var streamID string
	switch m := msg.(type) {
	case *StreamStarted:
		streamID = m.StreamID
	case *StreamStopped:
		streamID = m.StreamID
	case *Error:
		// errors are not correlated by stream id in this minimal scheme
		return false
	default:
		return false
	}
	s.mu.Lock()
	ch, ok := s.pending[streamID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- msg:
		return true
	default:
		return false
	}
}
