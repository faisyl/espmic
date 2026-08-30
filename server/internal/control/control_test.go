package control

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func rawFrame(t *testing.T, payload []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := WriteFrame(&b, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	return b.Bytes()
}

func TestReadFrameRoundtrip(t *testing.T) {
	payload := []byte(`{"type":"hello","device_id":"esp32-001"}`)
	data := rawFrame(t, payload)

	got, err := ReadFrame(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
}

func TestReadFrameRejectsOversizeBeforePayload(t *testing.T) {
	// Header declaring MaxPayloadBytes+1 but with NO actual payload following.
	var b bytes.Buffer
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, MaxPayloadBytes+1)
	b.Write(hdr)

	_, err := ReadFrame(bytes.NewReader(b.Bytes()))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestReadFrameTruncatedPayload(t *testing.T) {
	// Header says length 10 but only 3 bytes delivered.
	var b bytes.Buffer
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, 10)
	b.Write(hdr)
	b.Write([]byte("abc"))

	_, err := ReadFrame(bytes.NewReader(b.Bytes()))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestReadFrameCleanEOF(t *testing.T) {
	_, err := ReadFrame(bytes.NewReader(nil))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestWriteFrameRejectsOversize(t *testing.T) {
	var b bytes.Buffer
	payload := make([]byte, MaxPayloadBytes+1)
	if err := WriteFrame(&b, payload); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestFrameReaderIncremental(t *testing.T) {
	fr := &FrameReader{}
	p1 := []byte(`{"type":"ping","seq":1}`)
	p2 := []byte(`{"type":"pong","seq":1}`)
	frame1 := rawFrame(t, p1)
	frame2 := rawFrame(t, p2)
	data := append(append([]byte(nil), frame1...), frame2...)

	// No complete frame from a single byte: need more.
	frames, needMore, err := fr.Push(data[:1])
	if err != nil || !needMore || len(frames) != 0 {
		t.Fatalf("after 1 byte: frames=%d needMore=%v err=%v; want 0 frames + needMore", len(frames), needMore, err)
	}

	// Feed the rest of frame1: one complete frame leaves nothing pending.
	frames, needMore, err = fr.Push(data[1:len(frame1)])
	if err != nil || len(frames) != 1 {
		t.Fatalf("mid: frames=%d err=%v; want 1 frame", len(frames), err)
	}
	if got := string(frames[0]); got != string(p1) {
		t.Fatalf("frame 1 = %q, want %q", got, p1)
	}

	// Feed frame2 entirely: second complete frame, nothing pending.
	frames, needMore, err = fr.Push(data[len(frame1):])
	if err != nil || len(frames) != 1 {
		t.Fatalf("tail: frames=%d err=%v; want 1 frame", len(frames), err)
	}
	if needMore {
		t.Fatal("tail: expected no needMore after all frames consumed")
	}
	if got := string(frames[0]); got != string(p2) {
		t.Fatalf("frame 2 = %q, want %q", got, p2)
	}
}

func TestFrameReaderTwoFramesOnePush(t *testing.T) {
	fr := &FrameReader{}
	p1 := []byte(`{"type":"status","status":"ok"}`)
	p2 := []byte(`{"type":"error","code":7,"message":"x"}`)
	data := append(rawFrame(t, p1), rawFrame(t, p2)...)

	frames, needMore, err := fr.Push(data)
	if err != nil || needMore || len(frames) != 2 {
		t.Fatalf("frames=%d needMore=%v err=%v; want 2 frames", len(frames), needMore, err)
	}
}

func TestFrameReaderRejectsOversize(t *testing.T) {
	fr := &FrameReader{}
	// 4-byte header only, declaring an oversized length.
	var b bytes.Buffer
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, MaxPayloadBytes+1)
	b.Write(hdr)

	_, _, err := fr.Push(b.Bytes())
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestMessagesRoundtrip(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
	}{
		{"hello", NewHello("esp32-001", "1.2.3", []string{"opus"})},
		{"hello_ack", NewHelloAck("sess-1", "esp32-001")},
		{"ping", NewPing(5)},
		{"pong", NewPong(5)},
		{"start_stream", NewStartStream("uuid", 1234567, 5004)},
		{"stream_started", NewStreamStarted("uuid")},
		{"stop_stream", NewStopStream("uuid")},
		{"stream_stopped", NewStreamStopped("uuid")},
		{"get_status", NewGetStatus()},
		{"status", NewStatus("ok", map[string]any{"battery": 88})},
		{"error", NewError(7, "boom")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := Encode(tc.msg)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, err := DecodePayload(payload)
			if err != nil {
				t.Fatalf("DecodePayload: %v", err)
			}
			if got.Kind() != tc.msg.Kind() {
				t.Fatalf("kind = %q, want %q", got.Kind(), tc.msg.Kind())
			}
		})
	}
}

func TestMessageKindConstants(t *testing.T) {
	want := []string{
		TypeHello, TypeHelloAck, TypePing, TypePong, TypeStartStream,
		TypeStreamStarted, TypeStopStream, TypeStreamStopped, TypeGetStatus,
		TypeStatus, TypeError,
	}
	for _, k := range want {
		if k == "" {
			t.Fatalf("empty message type constant")
		}
	}
}

func TestWriteMessageReadFrameRoundtrip(t *testing.T) {
	var b bytes.Buffer
	orig := NewStartStream("uuid", 42, 5004)
	if err := WriteMessage(&b, orig); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	payload, err := ReadFrame(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	got, err := DecodePayload(payload)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	ss, ok := got.(*StartStream)
	if !ok {
		t.Fatalf("type = %T, want *StartStream", got)
	}
	if ss.SSRC != 42 || ss.DestinationPort != 5004 || ss.StreamID != "uuid" {
		t.Fatalf("unexpected start_stream fields: %+v", ss)
	}
}

func TestDecodePayloadUnknownType(t *testing.T) {
	_, err := DecodePayload([]byte(`{"type":"bogus"}`))
	if !errors.Is(err, ErrUnknownMessageType) {
		t.Fatalf("err = %v, want ErrUnknownMessageType", err)
	}
}

func TestDecodePayloadMalformedJSON(t *testing.T) {
	if _, err := DecodePayload([]byte(`{not json`)); err == nil {
		t.Fatalf("expected error for malformed JSON")
	}
}
