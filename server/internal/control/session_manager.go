package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrNotConnected is returned when a device has no live control session to
// receive a command.
var ErrNotConnected = errors.New("control: device not connected")

// SessionManager tracks live control sessions by device id and correlates a
// command's reply back to its awaiting caller. It mirrors the CommandService
// Await/Deliver pattern but keys correlation by request_id (set_config echoes
// it in the device's status/error reply) instead of stream_id.
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*Session   // deviceID -> live session
	pending  map[string]chan Message // request_id -> reply channel
}

// NewSessionManager returns an empty manager.
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
		pending:  make(map[string]chan Message),
	}
}

// Handler returns the inbound-message handler to pass to Session.SetOnMsg. It
// routes status/error replies to awaiting set_config callers.
func (m *SessionManager) Handler() func(Message) {
	return func(msg Message) {
		m.deliverRequest(msg)
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

func (m *SessionManager) handleInbound(s *Session, msg Message) {
	if s != nil && s.DeviceID() != "" {
		m.mu.Lock()
		m.sessions[s.DeviceID()] = s
		m.mu.Unlock()
	}
	m.deliverRequest(msg)
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
	default:
		return ""
	}
}
