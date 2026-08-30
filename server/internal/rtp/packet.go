package rtp

import (
	"errors"

	pionrtp "github.com/pion/rtp"
)

// Errors returned by packet parsing (spec §10).
var (
	ErrMalformedPacket  = errors.New("rtp: malformed or truncated RTP packet")
	ErrWrongVersion     = errors.New("rtp: RTP version must be 2")
	ErrWrongPayloadType = errors.New("rtp: unexpected payload type")
	ErrEmptyPayload     = errors.New("rtp: packet carries no Opus payload")
)

// Packet is the server's own representation of a validated RTP/Opus packet
// (spec §10). It preserves sequence, timestamp and SSRC for diagnostics
// (architectural principle 6).
type Packet struct {
	Version        uint8
	PayloadType    uint8
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	Payload        []byte
}

// Parse validates raw against the spec §10 contract using the default payload
// type (DefaultPayloadType).
func Parse(raw []byte) (Packet, error) {
	return ParseFor(raw, DefaultPayloadType)
}

// ParseFor validates raw against the spec §10 contract with an expected
// payload type. It rejects non-v2 packets, unexpected payload types and empty
// payloads. The whole RTP payload is treated as exactly one Opus packet
// (RFC 7587); multi-packet aggregation is not supported.
func ParseFor(raw []byte, expectedPT uint8) (Packet, error) {
	var p pionrtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		return Packet{}, ErrMalformedPacket
	}
	if p.Version != 2 {
		return Packet{}, ErrWrongVersion
	}
	if p.PayloadType != expectedPT {
		return Packet{}, ErrWrongPayloadType
	}
	if len(p.Payload) == 0 {
		return Packet{}, ErrEmptyPayload
	}
	return Packet{
		Version:        p.Version,
		PayloadType:    p.PayloadType,
		SequenceNumber: p.SequenceNumber,
		Timestamp:      p.Timestamp,
		SSRC:           p.SSRC,
		Payload:        append([]byte(nil), p.Payload...),
	}, nil
}
