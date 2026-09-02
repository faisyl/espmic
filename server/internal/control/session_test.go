package control

import (
	"bytes"
	"context"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"
)

// chanConn is a deterministic net.Conn (spec S2 test boundary: no real
// net/tls). Writes accumulate into a guarded byte buffer consumed by a
// streaming frame reader; reads pull framed messages from a channel. This
// coalesces multi-Write frames and is race-free.
type chanConn struct {
	readCh       chan []byte // test -> session: framed messages to deliver
	mu           sync.Mutex
	writeBuf     []byte
	writeOffset  int    // bytes already pushed to fr (avoids duplicate frame reads)
	writeSig     chan struct{}
	closed       chan struct{}
	fr           *FrameReader
}

func newChanConn() *chanConn {
	return &chanConn{
		readCh:   make(chan []byte, 16),
		writeSig: make(chan struct{}, 16),
		closed:   make(chan struct{}),
		fr:       &FrameReader{},
	}
}

func (c *chanConn) Read(p []byte) (int, error) {
	select {
	case b := <-c.readCh:
		return copy(p, b), nil
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *chanConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writeBuf = append(c.writeBuf, p...)
	c.mu.Unlock()
	select {
	case c.writeSig <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (c *chanConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func (c *chanConn) LocalAddr() net.Addr  { return &net.TCPAddr{} }
func (c *chanConn) RemoteAddr() net.Addr { return &net.TCPAddr{} }
func (c *chanConn) SetDeadline(time.Time) error {
	return nil
}
func (c *chanConn) SetReadDeadline(time.Time) error {
	return nil
}
func (c *chanConn) SetWriteDeadline(time.Time) error {
	return nil
}

// deliver pushes a framed message for the session to read.
func (c *chanConn) deliver(t *testing.T, msg Message) {
	t.Helper()
	b, _ := Encode(msg)
	var buf bytes.Buffer
	WriteFrame(&buf, b)
	c.readCh <- buf.Bytes()
}

// nextWrite returns the next framed message the session wrote, or times out.
// It keeps feeding the streaming frame reader until a complete frame emerges.
func (c *chanConn) nextWrite(t *testing.T, timeout time.Duration) Message {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		c.mu.Lock()
		newBytes := c.writeBuf[c.writeOffset:]
		frames, needMore, err := c.fr.Push(newBytes)
		c.writeOffset = len(c.writeBuf) // all current bytes have been pushed
		c.mu.Unlock()
		if err != nil {
			t.Fatalf("frame reader: %v", err)
		}
		if len(frames) > 0 {
			msg, err := DecodePayload(frames[0])
			if err != nil {
				t.Fatalf("DecodePayload: %v", err)
			}
			return msg
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for complete frame")
			return nil
		}
		if needMore {
			select {
			case <-c.writeSig:
			case <-time.After(time.Until(deadline)):
			}
		} else {
			time.Sleep(time.Millisecond)
		}
	}
}

type fakeAuth struct {
	ok bool
}

func (a *fakeAuth) Authenticate(_ context.Context, _, _ string) error {
	if !a.ok {
		return errors.New("auth failed")
	}
	return nil
}

func TestSessionHelloAck(t *testing.T) {
	conn := newChanConn()
	s := NewSession(conn, &fakeAuth{ok: true}, func() time.Time { return time.Now() }, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Send hello; expect hello_ack.
	conn.deliver(t, NewHello("d1", "token", "1.0", nil))
	ack := conn.nextWrite(t, time.Second)
	if _, ok := ack.(*HelloAck); !ok {
		t.Fatalf("expected *HelloAck, got %T", ack)
	}
	if s.DeviceID() != "d1" {
		t.Fatalf("device = %q, want d1", s.DeviceID())
	}
	if s.ID() == "" {
		t.Fatal("expected session id")
	}
	cancel()
	<-done
}

func TestSessionAuthFailure(t *testing.T) {
	conn := newChanConn()
	s := NewSession(conn, &fakeAuth{ok: false}, func() time.Time { return time.Now() }, nil)

	conn.deliver(t, NewHello("d1", "bad", "1.0", nil))
	err := s.Run(context.Background())
	if err == nil {
		t.Fatal("expected auth error")
	}
	// Should have written an error frame.
	msg := conn.nextWrite(t, time.Second)
	if _, ok := msg.(*Error); !ok {
		t.Fatalf("expected *Error, got %T", msg)
	}
}

func TestSessionReceivesStatus(t *testing.T) {
	conn := newChanConn()
	var mu sync.Mutex
	var got []Message
	s := NewSession(conn, &fakeAuth{ok: true}, func() time.Time { return time.Now() }, func(m Message) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Hello -> hello_ack.
	conn.deliver(t, NewHello("d1", "token", "1.0", nil))
	conn.nextWrite(t, time.Second) // consume hello_ack

	// Send status; expect onMsg to fire.
	conn.deliver(t, NewStatus("ok", map[string]any{"battery": 88}))

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Kind() != TypeStatus {
		t.Fatalf("got %q, want status", got[0].Kind())
	}
}

func TestCommandServiceValidate(t *testing.T) {
	cs := NewCommandService()
	if err := cs.ValidateStart("", "d1", 1); err == nil {
		t.Fatal("empty stream_id should fail")
	}
	if err := cs.ValidateStart("s1", "", 1); err == nil {
		t.Fatal("empty device_id should fail")
	}
	if err := cs.ValidateStart("s1", "d1", 0); err == nil {
		t.Fatal("zero ssrc should fail")
	}
	if err := cs.ValidateStart("s1", "d1", 42); err != nil {
		t.Fatalf("valid start: %v", err)
	}
}

func TestCommandServiceBuildCommands(t *testing.T) {
	cs := NewCommandService()
	ss := cs.BuildStartStream("s1", 42, 5004)
	if ss.StreamID != "s1" || ss.SSRC != 42 || ss.DestinationPort != 5004 {
		t.Fatalf("start_stream: %+v", ss)
	}
	st := cs.BuildStopStream("s1")
	if st.StreamID != "s1" {
		t.Fatalf("stop_stream: %+v", st)
	}
	gs := cs.BuildGetStatus()
	if gs.Kind() != TypeGetStatus {
		t.Fatalf("get_status: %+v", gs)
	}
}

func TestCommandServiceAwaitDeliver(t *testing.T) {
	cs := NewCommandService()
	ctx := context.Background()

	awaitErr := make(chan error, 1)
	var got Message
	go func() {
		m, err := cs.Await(ctx, "s1")
		got = m
		awaitErr <- err
	}()

	// Give Await time to register.
	time.Sleep(10 * time.Millisecond)
	if !cs.Deliver(NewStreamStarted("s1")) {
		t.Fatal("Deliver should find the awaiting caller")
	}

	if err := <-awaitErr; err != nil {
		t.Fatalf("Await err: %v", err)
	}
	if got.Kind() != TypeStreamStarted {
		t.Fatalf("got %q, want stream_started", got.Kind())
	}
}

func TestCommandServiceDeliverUnsolicited(t *testing.T) {
	cs := NewCommandService()
	if cs.Deliver(NewStreamStarted("nobody")) {
		t.Fatal("unsolicited deliver should return false")
	}
}

func TestStreamStoppedStatsField(t *testing.T) {
	st := NewStreamStopped("s1", map[string]any{"packets": 100})
	if st.Stats["packets"] != 100 {
		t.Fatalf("stats = %+v", st.Stats)
	}
	b, _ := Encode(st)
	msg, err := DecodePayload(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	ss, ok := msg.(*StreamStopped)
	if !ok {
		t.Fatalf("type = %T", msg)
	}
	if !reflect.DeepEqual(ss.Stats["packets"], float64(100)) {
		t.Fatalf("roundtrip stats = %+v", ss.Stats)
	}
}
