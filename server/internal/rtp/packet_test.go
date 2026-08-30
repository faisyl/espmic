package rtp

import (
	"encoding/binary"
	"errors"
	"testing"
)

func buildRaw(t *testing.T, version, pt byte, seq uint16, ts, ssrc uint32, payload []byte) []byte {
	t.Helper()
	raw := make([]byte, 12+len(payload))
	b0 := (version & 0x03) << 6
	raw[0] = b0
	raw[1] = pt
	binary.BigEndian.PutUint16(raw[2:4], seq)
	binary.BigEndian.PutUint32(raw[4:8], ts)
	binary.BigEndian.PutUint32(raw[8:12], ssrc)
	copy(raw[12:], payload)
	return raw
}

func TestParseValid(t *testing.T) {
	payload := []byte{0xf8, 0xff, 0xfe, 0x01, 0x02}
	raw := buildRaw(t, 2, DefaultPayloadType, 100, 96000, 0xDEADBEEF, payload)

	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Version != 2 {
		t.Errorf("version = %d, want 2", p.Version)
	}
	if p.PayloadType != DefaultPayloadType {
		t.Errorf("pt = %d, want %d", p.PayloadType, DefaultPayloadType)
	}
	if p.SequenceNumber != 100 {
		t.Errorf("seq = %d, want 100", p.SequenceNumber)
	}
	if p.Timestamp != 96000 {
		t.Errorf("ts = %d, want 96000", p.Timestamp)
	}
	if p.SSRC != 0xDEADBEEF {
		t.Errorf("ssrc = %x, want deadbeef", p.SSRC)
	}
	if len(p.Payload) != len(payload) {
		t.Errorf("payload len = %d, want %d", len(p.Payload), len(payload))
	}
}

func TestParseWrongVersion(t *testing.T) {
	raw := buildRaw(t, 0, DefaultPayloadType, 1, 0, 1, []byte{0x01})
	_, err := Parse(raw)
	if !errors.Is(err, ErrWrongVersion) {
		t.Fatalf("err = %v, want ErrWrongVersion", err)
	}
}

func TestParseWrongPayloadType(t *testing.T) {
	raw := buildRaw(t, 2, DefaultPayloadType+1, 1, 0, 1, []byte{0x01})
	_, err := Parse(raw)
	if !errors.Is(err, ErrWrongPayloadType) {
		t.Fatalf("err = %v, want ErrWrongPayloadType", err)
	}
}

func TestParseForCustomPT(t *testing.T) {
	raw := buildRaw(t, 2, 120, 1, 0, 1, []byte{0x01})
	p, err := ParseFor(raw, 120)
	if err != nil {
		t.Fatalf("ParseFor(120): %v", err)
	}
	if p.PayloadType != 120 {
		t.Errorf("pt = %d, want 120", p.PayloadType)
	}
}

func TestParseEmptyPayload(t *testing.T) {
	raw := buildRaw(t, 2, DefaultPayloadType, 1, 0, 1, nil)
	_, err := Parse(raw)
	if !errors.Is(err, ErrEmptyPayload) {
		t.Fatalf("err = %v, want ErrEmptyPayload", err)
	}
}

func TestParseTruncated(t *testing.T) {
	_, err := Parse([]byte{0x80, 0x6f, 0x00})
	if !errors.Is(err, ErrMalformedPacket) {
		t.Fatalf("err = %v, want ErrMalformedPacket", err)
	}
}

func TestParseDoNotAssume20ms(t *testing.T) {
	// Packets must parse correctly regardless of opus duration; verify two
	// different payload lengths (timestamp increments differ) both parse.
	cases := []struct {
		ts      uint32
		payload []byte
	}{
		{960, make([]byte, 60)},   // a ~20ms small packet
		{1440, make([]byte, 160)}, // a longer packet
		{480, make([]byte, 20)},   // a short packet
	}
	for _, c := range cases {
		raw := buildRaw(t, 2, DefaultPayloadType, 1, c.ts, 1, c.payload)
		if _, err := Parse(raw); err != nil {
			t.Fatalf("Parse(ts=%d): %v", c.ts, err)
		}
	}
}
