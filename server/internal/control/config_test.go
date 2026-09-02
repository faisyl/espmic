package control

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
)

// --- SetConfig encode/decode roundtrip ---

func TestSetConfigRoundtrip(t *testing.T) {
	pin := 12
	host := "audio.internal"
 bitrate := 128000
	req := NewSetConfig("req-01")
	req.DefaultBitrate = &bitrate
	req.ServerHost = &host
	req.I2SBclk = &pin

	payload, err := Encode(req)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	msg, err := DecodePayload(payload)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	got, ok := msg.(*SetConfig)
	if !ok {
		t.Fatalf("type = %T, want *SetConfig", msg)
	}
	if got.RequestID != "req-01" || got.Kind() != TypeSetConfig {
		t.Fatalf("decoded = %+v", got)
	}
	if got.I2SBclk == nil || *got.I2SBclk != 12 {
		t.Fatalf("i2s_bclk not preserved: %+v", got.I2SBclk)
	}
	if got.I2SWs != nil || got.I2SDin != nil {
		t.Fatalf("unset pin fields should be nil; got i2s_ws=%v i2s_din=%v", got.I2SWs, got.I2SDin)
	}
}

// TestSetConfigOmitNilFields verifies that nil pointer fields are omitted in
// the JSON so we only send what the caller intends to set.
func TestSetConfigOmitNilFields(t *testing.T) {
	req := NewSetConfig("r1")
	req.I2SWs = intPtr(33)

	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["i2s_ws"]; !ok {
		t.Fatal("i2s_ws should be present")
	}
	if _, ok := raw["i2s_bclk"]; ok {
		t.Fatal("i2s_bclk should be omitted when nil")
	}
}

// --- SetConfig.Validate ---

func TestSetConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		req  *SetConfig
		err  string // substring of expected error
	}{
		{"no fields", NewSetConfig("r1"), "at least one field"},
		{"bad bclk", &SetConfig{Type: TypeSetConfig, RequestID: "r1", I2SBclk: intPtr(48)}, "out of range"},
		{"bad ws", &SetConfig{Type: TypeSetConfig, RequestID: "r1", I2SWs: intPtr(-1)}, "out of range"},
		{"bad din", &SetConfig{Type: TypeSetConfig, RequestID: "r1", I2SDin: intPtr(48)}, "out of range"},
		{"bitrate negative", &SetConfig{Type: TypeSetConfig, RequestID: "r1", DefaultBitrate: intPtr(-1)}, ">= 0"},
		{"empty host", &SetConfig{Type: TypeSetConfig, RequestID: "r1", ServerHost: strPtr("")}, "not be empty"},
		{"valid single pin", &SetConfig{Type: TypeSetConfig, RequestID: "r1", I2SBclk: intPtr(12)}, ""},
		{"valid at boundary 0", &SetConfig{Type: TypeSetConfig, RequestID: "r1", I2SBclk: intPtr(0)}, ""},
		{"valid at boundary 47", &SetConfig{Type: TypeSetConfig, RequestID: "r1", I2SBclk: intPtr(47)}, ""},
		{"valid host", &SetConfig{Type: TypeSetConfig, RequestID: "r1", ServerHost: strPtr("a")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if tc.err == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !bytes.Contains([]byte(err.Error()), []byte(tc.err)) {
				t.Fatalf("error %q missing %q", err.Error(), tc.err)
			}
		})
	}
}

// --- ErrorCode string-tolerant decode ---

func TestErrorCodeStringTolerant(t *testing.T) {
	cases := []struct {
		name     string
		json     string
		wantCode ErrorCode
	}{
		{"string code", `{"type":"error","code":"invalid_config","message":"bad pin"}`, ErrorCode("invalid_config")},
		{"int code", `{"type":"error","code":7,"message":"boom"}`, ErrorCode("7")},
		{"int code 1", `{"type":"error","code":1,"message":"auth failed"}`, ErrorCode("1")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := DecodePayload([]byte(tc.json))
			if err != nil {
				t.Fatalf("DecodePayload: %v", err)
			}
			e, ok := msg.(*Error)
			if !ok {
				t.Fatalf("type = %T, want *Error", msg)
			}
			if e.Code != tc.wantCode {
				t.Fatalf("Code = %q, want %q", e.Code, tc.wantCode)
			}
		})
	}
}

// TestErrorMarshalAlwaysString verifies that MarshalJSON outputs a JSON string
// (the canonical form), which is what the firmware ignores but is consistent.
func TestErrorMarshalAlwaysString(t *testing.T) {
	e := NewError(1, "auth failed")
	payload, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	code, ok := raw["code"].(string)
	if !ok {
		t.Fatalf("code should marshal as string, got %T: %v", raw["code"], raw["code"])
	}
	if code != "1" {
		t.Fatalf("code = %q, want %q", code, "1")
	}
}

// TestStatusRequestID verifies the request_id field roundtrips on Status.
func TestStatusRequestID(t *testing.T) {
	s := &Status{Type: TypeStatus, RequestID: "cfg-42", State: "IDLE"}
	payload, err := Encode(s)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	msg, err := DecodePayload(payload)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	got, ok := msg.(*Status)
	if !ok {
		t.Fatalf("type = %T, want *Status", msg)
	}
	if got.RequestID != "cfg-42" {
		t.Fatalf("RequestID = %q, want %q", got.RequestID, "cfg-42")
	}
}

// TestRequestIDOfDispatcher verifies the dispatcher finds request_id on both
// Status and Error messages.
func TestRequestIDOfDispatcher(t *testing.T) {
	if got := requestIDOf(&Status{RequestID: "s1"}); got != "s1" {
		t.Fatalf("status request_id = %q", got)
	}
	if got := requestIDOf(&Error{RequestID: "e1"}); got != "e1" {
		t.Fatalf("error request_id = %q", got)
	}
	if got := requestIDOf(&StartStream{}); got != "" {
		t.Fatalf("non-status/error should return empty; got %q", got)
	}
}

// --- SessionManager send/receive ---

func TestSessionManagerSendSetConfigSuccess(t *testing.T) {
	mgr := NewSessionManager()
	conn := newChanConn()
	s := NewSession(conn, &fakeAuth{ok: true}, func() time.Time { return time.Now() }, nil)
	s.SetOnMsg(mgr.Handler())
	s.SetOnReady(mgr.OnReady)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Hello -> hello_ack + onReady registers the session.
	conn.deliver(t, NewHello("d1", "token", "1.0", nil))
	conn.nextWrite(t, time.Second)

	// Now send_set_config through the manager and reply with status.
	req := NewSetConfig("cfg-01")
	pin := 12
	req.I2SBclk = &pin

	replyCh := make(chan Message, 1)
	go func() {
		msg, err := mgr.SendSetConfig(context.Background(), "d1", req)
		if err != nil {
			t.Errorf("SendSetConfig: %v", err)
			return
		}
		replyCh <- msg
	}()

	// The session should have written the set_config frame; read it and reply.
	got := conn.nextWrite(t, time.Second)
	sc, ok := got.(*SetConfig)
	if !ok {
		t.Fatalf("device received %T, want *SetConfig", got)
	}
	if sc.RequestID != "cfg-01" {
		t.Fatalf("request_id = %q, want cfg-01", sc.RequestID)
	}
	if sc.I2SBclk == nil || *sc.I2SBclk != 12 {
		t.Fatalf("i2s_bclk not forwarded: %+v", sc.I2SBclk)
	}

	// Device replies with status echoing the request_id.
	conn.deliver(t, &Status{Type: TypeStatus, RequestID: "cfg-01", State: "IDLE"})

	select {
	case msg := <-replyCh:
		st, ok := msg.(*Status)
		if !ok {
			t.Fatalf("reply type = %T, want *Status", msg)
		}
		if st.RequestID != "cfg-01" || st.State != "IDLE" {
			t.Fatalf("unexpected status: %+v", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reply")
	}
	cancel()
	<-done
}

func TestSessionManagerSendSetConfigRejected(t *testing.T) {
	mgr := NewSessionManager()
	conn := newChanConn()
	s := NewSession(conn, &fakeAuth{ok: true}, func() time.Time { return time.Now() }, nil)
	s.SetOnMsg(mgr.Handler())
	s.SetOnReady(mgr.OnReady)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	conn.deliver(t, NewHello("d1", "token", "1.0", nil))
	conn.nextWrite(t, time.Second)

	req := NewSetConfig("cfg-02")
	req.I2SBclk = intPtr(12) // valid pin; device rejects for other reasons in this test

	replyCh := make(chan Message, 1)
	go func() {
		msg, err := mgr.SendSetConfig(context.Background(), "d1", req)
		if err != nil {
			t.Errorf("SendSetConfig: %v", err)
			return
		}
		replyCh <- msg
	}()

	// Consume the set_config the session wrote (device would validate and reply).
	conn.nextWrite(t, time.Second)

	// Device replies with an error (string code, matching firmware).
	conn.deliver(t, &Error{Type: TypeError, RequestID: "cfg-02", Code: ErrorCode("invalid_config"), Message: "bad pin"})

	select {
	case msg := <-replyCh:
		e, ok := msg.(*Error)
		if !ok {
			t.Fatalf("reply type = %T, want *Error", msg)
		}
		if e.Code != "invalid_config" {
			t.Fatalf("code = %q, want %q", e.Code, "invalid_config")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reply")
	}
	cancel()
	<-done
}

func TestSessionManagerSendSetConfigNotConnected(t *testing.T) {
	mgr := NewSessionManager()
	req := NewSetConfig("cfg-x")
	pin := 12
	req.I2SBclk = &pin
	_, err := mgr.SendSetConfig(context.Background(), "offline-device", req)
	if err == nil || err.Error() != "control: device not connected" {
		t.Fatalf("expected ErrNotConnected, got %v", err)
	}
}

func TestSessionManagerSendSetConfigTimeout(t *testing.T) {
	mgr := NewSessionManager()
	conn := newChanConn()
	s := NewSession(conn, &fakeAuth{ok: true}, func() time.Time { return time.Now() }, nil)
	s.SetOnMsg(mgr.Handler())
	s.SetOnReady(mgr.OnReady)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	conn.deliver(t, NewHello("d1", "token", "1.0", nil))
	conn.nextWrite(t, time.Second)

	req := NewSetConfig("cfg-to")
	pin := 12
	req.I2SBclk = &pin

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer shortCancel()
	_, err := mgr.SendSetConfig(shortCtx, "d1", req)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	cancel()
	<-done
}

// --- helpers ---

func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }
