package control

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
)

// ErrNotConnected is returned when a device has no live control session to
// receive a command.
var ErrNotConnected = errors.New("control: device not connected")

// SessionManager tracks live control sessions by device id and correlates a
// command's reply back to its awaiting caller. It mirrors the CommandService
// Await/Deliver pattern but keys correlation by request_id (set_config echoes
// it in the device's status/error reply) and stream_id (start/stop stream).
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*Session     // deviceID -> live session
	pending  map[string]chan Message // request_id -> reply channel
	streamCh map[string]chan Message // stream_id -> reply channel (for start/stop)
}

// NewSessionManager returns an empty manager.
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
		pending:  make(map[string]chan Message),
		streamCh: make(map[string]chan Message),
	}
}

// Handler returns the inbound-message handler to pass to Session.SetOnMsg. It
// routes status/error replies to awaiting set_config callers; routes
// StreamStarted/StreamStopped/Error to awaiting stream callers; silently
// consumes Ping/Pong (keepalive); logs other unhandled types at debug.
func (m *SessionManager) Handler() func(Message) {
	return func(msg Message) {
		if m.deliverRequest(msg) {
			return
		}
		if m.deliverStream(msg) {
			return
		}
		// Keepalive: silently consume Ping/Pong
		if msg.Kind() == TypePing || msg.Kind() == TypePong {
			return
		}
		log.Printf("session_mgr: unhandled inbound %s", msg.Kind())
	}
}

// OnReady registers a session (by its device id) once it is authenticated. It
// is the callback passed to Session.SetOnReady, so a freshly-connected device
// is reachable by SendSetConfig immediately (not only after it first speaks).
func (m *SessionManager) OnReady(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s != nil && s.DeviceID() != "" {
		m.sessions[s.DeviceID()] = s
	}
}

// Unregister removes a device's live session (call on disconnect).
func (m *SessionManager) Unregister(deviceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, deviceID)
}

// SendSetConfig sends a set_config command to the device's live session and
// awaits the correlated status (success) or error (rejection), or returns
// ctx.Err() on timeout / cancellation. It mirrors CommandService.Await's
// channel registration and select-on-ctx pattern.
func (m *SessionManager) SendSetConfig(ctx context.Context, deviceID string, req *SetConfig) (Message, error) {
	if req == nil {
		return nil, errors.New("control: nil set_config")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.RequestID == "" {
		return nil, errors.New("control: set_config requires request_id")
	}
	s, ok := m.session(deviceID)
	if !ok {
		return nil, ErrNotConnected
	}

	m.mu.Lock()
	if _, dup := m.pending[req.RequestID]; dup {
		m.mu.Unlock()
		return nil, fmt.Errorf("control: duplicate await for %q", req.RequestID)
	}
	ch := make(chan Message, 1)
	m.pending[req.RequestID] = ch
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pending, req.RequestID)
		m.mu.Unlock()
	}()

	if err := s.Send(req); err != nil {
		return nil, err
	}

	select {
	case msg := <-ch:
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *SessionManager) session(deviceID string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[deviceID]
	return s, ok
}

// deliverRequest routes an inbound status/error to its awaiting caller by
// request_id (mirror CommandService.Deliver). Returns true if routed.
func (m *SessionManager) deliverRequest(msg Message) bool {
	rid := requestIDOf(msg)
	if rid == "" {
		return false
	}
	m.mu.Lock()
	ch, ok := m.pending[rid]
	m.mu.Unlock()
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

func requestIDOf(msg Message) string {
	switch m := msg.(type) {
	case *Status:
		return m.RequestID
	case *Error:
		return m.RequestID
	case *StreamStarted:
		return m.RequestID
	case *StreamStopped:
		return m.RequestID
	default:
		return ""
	}
}

// SendStartStream sends a start_stream command and awaits stream_started/error.
func (m *SessionManager) SendStartStream(ctx context.Context, deviceID string, req *StartStream) (Message, error) {
	if req == nil {
		return nil, errors.New("control: nil start_stream")
	}
	if req.RequestID == "" {
		return nil, errors.New("control: start_stream requires request_id")
	}
	if req.StreamID == "" {
		return nil, errors.New("control: start_stream requires stream_id")
	}
	s, ok := m.session(deviceID)
	if !ok {
		return nil, ErrNotConnected
	}

	m.mu.Lock()
	if _, dup := m.streamCh[req.RequestID]; dup {
		m.mu.Unlock()
		return nil, fmt.Errorf("control: duplicate await for request %q", req.RequestID)
	}
	ch := make(chan Message, 1)
	m.streamCh[req.RequestID] = ch
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.streamCh, req.RequestID)
		m.mu.Unlock()
	}()

	if err := s.Send(req); err != nil {
		return nil, err
	}

	select {
	case msg := <-ch:
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SendStopStream sends a stop_stream command and awaits stream_stopped/error.
func (m *SessionManager) SendStopStream(ctx context.Context, deviceID string, req *StopStream) (Message, error) {
	if req == nil {
		return nil, errors.New("control: nil stop_stream")
	}
	if req.RequestID == "" {
		return nil, errors.New("control: stop_stream requires request_id")
	}
	if req.StreamID == "" {
		return nil, errors.New("control: stop_stream requires stream_id")
	}
	s, ok := m.session(deviceID)
	if !ok {
		return nil, ErrNotConnected
	}

	m.mu.Lock()
	if _, dup := m.streamCh[req.RequestID]; dup {
		m.mu.Unlock()
		return nil, fmt.Errorf("control: duplicate await for request %q", req.RequestID)
	}
	ch := make(chan Message, 1)
	m.streamCh[req.RequestID] = ch
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.streamCh, req.RequestID)
		m.mu.Unlock()
	}()

	if err := s.Send(req); err != nil {
		return nil, err
	}

	select {
	case msg := <-ch:
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// deliverStream routes StreamStarted/StreamStopped/Error (with RequestID) to awaiting stream caller.
func (m *SessionManager) deliverStream(msg Message) bool {
	var rid string
	switch m := msg.(type) {
	case *StreamStarted:
		rid = m.RequestID
	case *StreamStopped:
		rid = m.RequestID
	case *Error:
		rid = m.RequestID
	default:
		return false
	}
	if rid == "" {
		return false
	}
	m.mu.Lock()
	ch, ok := m.streamCh[rid]
	m.mu.Unlock()
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
