package control

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Authenticator validates a device's hello (spec §7, §19).
type Authenticator interface {
	Authenticate(ctx context.Context, deviceID, credential string) error
}

// Session implements the server side of a device control connection
// (spec §7). It runs over any net.Conn so tests can use a fake. The server
// flow (spec §7) is driven by Run:
//
//	accept -> read hello -> authenticate -> create session_id -> hello_ack
//	-> heartbeat ping/pong -> route commands -> receive status/events
//	-> on disconnect mark offline.
type Session struct {
	id    string
	conn  net.Conn
	auth  Authenticator
	now   func() time.Time
	onMsg func(Message)

	outboundMu sync.Mutex
	buf        *FrameReader
	readBuf    [1024]byte // reusable read buffer (Jim S2 minor: avoid per-call alloc)
	deviceID   string
	registered bool

	onReady func(*Session)
}

// NewSession returns a session bound to conn. auth validates the hello. now
// injects the clock for deterministic testing. onMsg, if set, is invoked for
// each decoded inbound message after hello_ack.
func NewSession(conn net.Conn, auth Authenticator, now func() time.Time, onMsg func(Message)) *Session {
	if now == nil {
		now = time.Now
	}
	return &Session{
		conn:  conn,
		auth:  auth,
		now:   now,
		onMsg: onMsg,
		buf:   &FrameReader{},
	}
}

// ID returns the created session id after hello_ack (empty before).
func (s *Session) ID() string { return s.id }

// DeviceID returns the authenticated device id (empty before).
func (s *Session) DeviceID() string { return s.deviceID }

// SetOnMsg sets the inbound-message handler invoked for each decoded message
// after hello_ack. It must be called before Run; it lets the caller wire a
// handler that needs a reference to the session itself.
func (s *Session) SetOnMsg(h func(Message)) { s.onMsg = h }

// SetOnReady sets a callback invoked once the session is authenticated (after
// hello_ack, when DeviceID is known). It must be called before Run; used to
// register the session with a SessionManager.
func (s *Session) SetOnReady(h func(*Session)) { s.onReady = h }

// Run drives the session until ctx is cancelled or the connection closes.
func (s *Session) Run(ctx context.Context) error {
	defer s.conn.Close()

	// Read hello with a bounded deadline (spec §7 step 2).
	s.conn.SetReadDeadline(s.now().Add(10 * time.Second))
	payload, err := s.readFrame()
	if err != nil {
		return fmt.Errorf("control: read hello: %w", err)
	}
	s.conn.SetReadDeadline(time.Time{})

	msg, err := DecodePayload(payload)
	if err != nil {
		return fmt.Errorf("control: decode hello: %w", err)
	}
	hello, ok := msg.(*Hello)
	if !ok {
		return errors.New("control: expected hello")
	}

	// Authenticate (spec §19).
	if s.auth != nil {
		if err := s.auth.Authenticate(ctx, hello.DeviceID, hello.Credential); err != nil {
			_ = s.writeMsg(NewError(1, "auth failed"))
			return fmt.Errorf("control: authenticate: %w", err)
		}
	}

	s.id = newSessionID()
	s.deviceID = hello.DeviceID
	s.registered = true

	if err := s.writeMsg(NewHelloAck(s.id, s.deviceID)); err != nil {
		return fmt.Errorf("control: write hello_ack: %w", err)
	}

	if s.onReady != nil {
		s.onReady(s)
	}

	hb := time.NewTicker(30 * time.Second)
	defer hb.Stop()

	errCh := make(chan error, 1)
	go s.readLoop(ctx, errCh)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		case <-hb.C:
			if err := s.writeMsg(NewPing(0)); err != nil {
				return fmt.Errorf("control: heartbeat ping: %w", err)
			}
		}
	}
}

// readLoop consumes framed messages until ctx is cancelled or EOF.
func (s *Session) readLoop(ctx context.Context, errCh chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		payload, err := s.readFrame()
		if err != nil {
			errCh <- err
			return
		}
		msg, err := DecodePayload(payload)
		if err != nil {
			continue
		}
		if s.onMsg != nil {
			s.onMsg(msg)
		}
	}
}

// readFrame uses the streaming reader with bounded reads (spec §7).
func (s *Session) readFrame() ([]byte, error) {
	for {
		n, err := s.conn.Read(s.readBuf[:])
		if err != nil {
			return nil, err
		}
		frames, needMore, ferr := s.buf.Push(s.readBuf[:n])
		if ferr != nil {
			return nil, ferr
		}
		if len(frames) > 0 {
			return frames[0], nil
		}
		if !needMore {
			return nil, ErrFrameTooLarge
		}
	}
}

// Send writes a command message to the device over the control connection
// (spec §9). It serialises concurrent heartbeats and command writes. Safe to
// call from any goroutine after the session is authenticated.
func (s *Session) Send(msg Message) error {
	return s.writeMsg(msg)
}

// writeMsg sends a framed message (spec §7). Serialises concurrent heartbeats
// and command writes.
func (s *Session) writeMsg(msg Message) error {
	s.outboundMu.Lock()
	defer s.outboundMu.Unlock()
	s.conn.SetWriteDeadline(s.now().Add(5 * time.Second))
	defer s.conn.SetWriteDeadline(time.Time{})
	return WriteMessage(s.conn, msg)
}

func newSessionID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("sess-%x", b[:])
}
