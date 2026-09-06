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
		{"hello", NewHello("esp32-001", "secret", "1.2.3", &Capabilities{Codecs: []string{"opus"}})},
		{"hello_ack", NewHelloAck("sess-1", "esp32-001", 1700000000000)},
		{"ping", NewPing(5)},
		{"pong", NewPong(5)},
		{"start_stream", NewStartStream("req-1", "uuid",
			Destination{IP: "192.168.1.100", Port: 5004},
			Codec{Name: "opus", SampleRate: 48000, Channels: 2, FrameMS: 20, Bitrate: 128000, VBR: true, FEC: false, DTX: false},
			RTPConfig{PayloadType: 111})},
		{"stream_started", NewStreamStarted("req-1", "uuid")},
		{"stop_stream", NewStopStream("req-1", "uuid")},
		{"stream_stopped", NewStreamStopped("req-1", "uuid", nil)},
		{"get_status", NewGetStatus("req-test")},
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
	orig := NewStartStream("req-1", "uuid",
		Destination{IP: "192.168.1.100", Port: 5004},
		Codec{Name: "opus", SampleRate: 48000, Channels: 2, FrameMS: 20, Bitrate: 128000, VBR: true, FEC: false, DTX: false},
		RTPConfig{PayloadType: 111})
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
	if ss.Destination.Port != 5004 || ss.StreamID != "uuid" || ss.RequestID != "req-1" {
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

func TestDecodeHelloWithCapabilitiesObject(t *testing.T) {
	// Exact client hello JSON from spec (client/ESP32_Audio_Device_Specification.md line 237)
	clientHello := `{"type":"hello","device_id":"esp32-001","credential":"token123","firmware":"v1.2.3","capabilities":{"sample_rates":[48000],"channels":2,"codecs":["opus"],"psram":true}}`

	msg, err := DecodePayload([]byte(clientHello))
	if err != nil {
		t.Fatalf("DecodePayload failed on real client hello: %v", err)
	}
	hello, ok := msg.(*Hello)
	if !ok {
		t.Fatalf("decoded type = %T, want *Hello", msg)
	}
	if hello.DeviceID != "esp32-001" {
		t.Fatalf("device_id = %q, want esp32-001", hello.DeviceID)
	}
	if hello.Credential != "token123" {
		t.Fatalf("credential = %q, want token123", hello.Credential)
	}
	if hello.Firmware != "v1.2.3" {
		t.Fatalf("firmware = %q, want v1.2.3", hello.Firmware)
	}
	if hello.Capabilities == nil {
		t.Fatal("capabilities should not be nil")
	}
	if len(hello.Capabilities.SampleRates) != 1 || hello.Capabilities.SampleRates[0] != 48000 {
		t.Fatalf("sample_rates = %v, want [48000]", hello.Capabilities.SampleRates)
	}
	if hello.Capabilities.Channels != 2 {
		t.Fatalf("channels = %d, want 2", hello.Capabilities.Channels)
	}
	if len(hello.Capabilities.Codecs) != 1 || hello.Capabilities.Codecs[0] != "opus" {
		t.Fatalf("codecs = %v, want [opus]", hello.Capabilities.Codecs)
	}
	if !hello.Capabilities.PSRAM {
		t.Fatalf("psram = %v, want true", hello.Capabilities.PSRAM)
	}

	// Verify round-trip encode/decode
	payload, err := Encode(hello)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	msg2, err := DecodePayload(payload)
	if err != nil {
		t.Fatalf("DecodePayload roundtrip: %v", err)
	}
	hello2, ok := msg2.(*Hello)
	if !ok {
		t.Fatalf("roundtrip type = %T, want *Hello", msg2)
	}
	if hello2.DeviceID != hello.DeviceID || hello2.Capabilities == nil {
		t.Fatalf("roundtrip lost data")
	}
}

func TestDecodeStreamStoppedStats(t *testing.T) {
	jsonPayload := `{"type":"stream_stopped","request_id":"req-99","stream_id":"strm-123","stats":{"packets_sent":100,"bytes_sent":2000,"duration_ms":5000,"encoder_errors":2}}`
	msg, err := DecodePayload([]byte(jsonPayload))
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	ss, ok := msg.(*StreamStopped)
	if !ok {
		t.Fatalf("type = %T, want *StreamStopped", msg)
	}
	if ss.StreamID != "strm-123" || ss.RequestID != "req-99" {
		t.Fatalf("unexpected stream_id or request_id: %+v", ss)
	}
	if ss.Stats == nil {
		t.Fatal("stats should not be nil")
	}
	if ss.Stats.PacketsSent != 100 || ss.Stats.BytesSent != 2000 || ss.Stats.DurationMS != 5000 || ss.Stats.EncoderErrors != 2 {
		t.Fatalf("unexpected stats: %+v", ss.Stats)
	}
}

func TestHelloAckServerTimeMsRoundtrip(t *testing.T) {
	ack := NewHelloAck("sess-1", "esp32-001", 1700000000000)
	payload, err := Encode(ack)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodePayload(payload)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	ack2, ok := got.(*HelloAck)
	if !ok {
		t.Fatalf("type = %T, want *HelloAck", got)
	}
	if ack2.ServerTimeMs != 1700000000000 {
		t.Fatalf("server_time_ms = %d, want 1700000000000", ack2.ServerTimeMs)
	}
	if ack2.SessionID != "sess-1" || ack2.DeviceID != "esp32-001" {
		t.Fatalf("lost session/device id: %+v", ack2)
	}
}

func TestHelloProtocolRoundtrip(t *testing.T) {
	hello := NewHello("esp32-001", "secret", "1.2.3", nil)
	hello.Protocol = 1
	payload, err := Encode(hello)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodePayload(payload)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	hello2, ok := got.(*Hello)
	if !ok {
		t.Fatalf("type = %T, want *Hello", got)
	}
	if hello2.Protocol != 1 {
		t.Fatalf("protocol = %d, want 1", hello2.Protocol)
	}
}

func TestErrorCodeStringDecode(t *testing.T) {
	// Device sends string error codes (e.g. "invalid_config") — verify the
	// ErrorCode custom type decodes them (spec §10).
	jsonPayload := `{"type":"error","code":"invalid_config","message":"bad config"}`
	msg, err := DecodePayload([]byte(jsonPayload))
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	e, ok := msg.(*Error)
	if !ok {
		t.Fatalf("type = %T, want *Error", msg)
	}
	if e.Code != "invalid_config" {
		t.Fatalf("code = %q, want invalid_config", e.Code)
	}
	if e.Message != "bad config" {
		t.Fatalf("message = %q, want bad config", e.Message)
	}
}

func TestErrorCodeIntDecode(t *testing.T) {
	// Legacy server integer codes still decode (spec §10).
	jsonPayload := `{"type":"error","code":7,"message":"boom"}`
	msg, err := DecodePayload([]byte(jsonPayload))
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	e, ok := msg.(*Error)
	if !ok {
		t.Fatalf("type = %T, want *Error", msg)
	}
	if e.Code != "7" {
		t.Fatalf("code = %q, want 7", e.Code)
	}
}
