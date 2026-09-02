package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Message types (spec §8). These are the JSON "type" discriminator values.
const (
	TypeHello         = "hello"
	TypeHelloAck      = "hello_ack"
	TypePing          = "ping"
	TypePong          = "pong"
	TypeStartStream   = "start_stream"
	TypeStreamStarted = "stream_started"
	TypeStopStream    = "stop_stream"
	TypeStreamStopped = "stream_stopped"
	TypeGetStatus     = "get_status"
	TypeStatus        = "status"
	TypeError         = "error"
	TypeSetConfig     = "set_config"
)

// ErrUnknownMessageType is returned by DecodePayload for an unrecognized type.
var ErrUnknownMessageType = errors.New("control: unknown message type")

// Message is any typed control message (spec §8).
type Message interface {
	Kind() string
}

// Hello introduces a device on connection (device -> server). Credential is
// the pre-shared secret/token the device presents; the server hashes it and
// compares against the stored hash (spec §19). The exact auth scheme (PSK,
// token, mTLS-derived) is pinned in S3; S2 uses a pre-shared credential.
type Hello struct {
	Type         string   `json:"type"`
	DeviceID     string   `json:"device_id"`
	Credential   string   `json:"credential,omitempty"`
	Firmware     string   `json:"firmware,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

func NewHello(deviceID, credential, firmware string, capabilities []string) *Hello {
	return &Hello{Type: TypeHello, DeviceID: deviceID, Credential: credential, Firmware: firmware, Capabilities: capabilities}
}

func (m *Hello) Kind() string { return TypeHello }

// HelloAck acknowledges authentication/session establishment (server -> device).
type HelloAck struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	DeviceID  string `json:"device_id"`
}

func NewHelloAck(sessionID, deviceID string) *HelloAck {
	return &HelloAck{Type: TypeHelloAck, SessionID: sessionID, DeviceID: deviceID}
}

func (m *HelloAck) Kind() string { return TypeHelloAck }

// Ping is a server heartbeat (server -> device). Seq correlates with Pong.
type Ping struct {
	Type string `json:"type"`
	Seq  uint32 `json:"seq"`
}

func NewPing(seq uint32) *Ping { return &Ping{Type: TypePing, Seq: seq} }

func (m *Ping) Kind() string { return TypePing }

// Pong is a device heartbeat response (device -> server).
type Pong struct {
	Type string `json:"type"`
	Seq  uint32 `json:"seq"`
}

func NewPong(seq uint32) *Pong { return &Pong{Type: TypePong, Seq: seq} }

func (m *Pong) Kind() string { return TypePong }

// StartStream requests a device to begin RTP (server -> device).
type StartStream struct {
	Type            string `json:"type"`
	StreamID        string `json:"stream_id"`
	SSRC            uint32 `json:"ssrc"`
	DestinationPort uint16 `json:"destination_port"`
	DestinationHost string `json:"destination_host,omitempty"`
}

func NewStartStream(streamID string, ssrc uint32, port uint16) *StartStream {
	return &StartStream{Type: TypeStartStream, StreamID: streamID, SSRC: ssrc, DestinationPort: port}
}

func (m *StartStream) Kind() string { return TypeStartStream }

// StreamStarted confirms a stream is running (device -> server).
type StreamStarted struct {
	Type     string `json:"type"`
	StreamID string `json:"stream_id"`
}

func NewStreamStarted(streamID string) *StreamStarted {
	return &StreamStarted{Type: TypeStreamStarted, StreamID: streamID}
}

func (m *StreamStarted) Kind() string { return TypeStreamStarted }

// StopStream requests a device to stop RTP (server -> device).
type StopStream struct {
	Type     string `json:"type"`
	StreamID string `json:"stream_id"`
}

func NewStopStream(streamID string) *StopStream {
	return &StopStream{Type: TypeStopStream, StreamID: streamID}
}

func (m *StopStream) Kind() string { return TypeStopStream }

// StreamStopped confirms a stream stopped, optionally with stats (device -> server).
type StreamStopped struct {
	Type     string         `json:"type"`
	StreamID string         `json:"stream_id"`
	Stats    map[string]any `json:"stats,omitempty"`
}

func NewStreamStopped(streamID string, stats map[string]any) *StreamStopped {
	return &StreamStopped{Type: TypeStreamStopped, StreamID: streamID, Stats: stats}
}

func (m *StreamStopped) Kind() string { return TypeStreamStopped }

// GetStatus requests device status (server -> device).
type GetStatus struct {
	Type string `json:"type"`
}

func NewGetStatus() *GetStatus { return &GetStatus{Type: TypeGetStatus} }

func (m *GetStatus) Kind() string { return TypeGetStatus }

// Status is a device status response (device -> server). RequestID echoes a
// request_id when the device replies to a correlated command (e.g. set_config,
// get_status); State captures the firmware's "state" field ("IDLE"/"STREAMING").
type Status struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id,omitempty"`
	Status    string         `json:"status"`
	State     string         `json:"state,omitempty"`
	Fields    map[string]any `json:"fields,omitempty"`
}

func NewStatus(status string, fields map[string]any) *Status {
	return &Status{Type: TypeStatus, Status: status, Fields: fields}
}

func (m *Status) Kind() string { return TypeStatus }

// Error reports a runtime or command error (device -> server). When
// stream_id is set, the error correlates to an awaiting start/stop caller
// (Jim S2 minor: fail the caller immediately rather than waiting on timeout).
// RequestID echoes a request_id when the device rejects a correlated command.
// Code is string-tolerant: the server sends legacy integer codes (see
// NewError) while the firmware sends string codes (e.g. "invalid_config").
type Error struct {
	Type      string    `json:"type"`
	RequestID string    `json:"request_id,omitempty"`
	StreamID  string    `json:"stream_id,omitempty"`
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
}

func NewError(code int, message string) *Error {
	return &Error{Type: TypeError, Code: ErrorCode(strconv.Itoa(code)), Message: message}
}

func NewStreamError(streamID string, code int, message string) *Error {
	return &Error{Type: TypeError, StreamID: streamID, Code: ErrorCode(strconv.Itoa(code)), Message: message}
}

func (m *Error) Kind() string { return TypeError }

// ErrorCode is a code that accepts both the server's legacy integer codes and
// the firmware's string codes (e.g. "invalid_config"). It is stored as the
// canonical string form and (un)marshals transparently in both directions so
// the existing outbound int path keeps working while inbound string codes
// decode cleanly.
type ErrorCode string

// MarshalJSON emits a JSON string (canonical form for both representations).
func (c ErrorCode) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(c))
}

// UnmarshalJSON accepts either a JSON string or a JSON number.
func (c *ErrorCode) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*c = ErrorCode(s)
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*c = ErrorCode(strconv.Itoa(n))
	return nil
}

// SetConfig pushes runtime config to a device (server -> device). It matches
// the firmware's set_config wire contract: only fields the caller sets are
// sent (pointer fields with omitempty), and RequestID correlates the device's
// status/error reply back to this request.
type SetConfig struct {
	Type           string  `json:"type"`
	RequestID      string  `json:"request_id"`
	DefaultBitrate *int    `json:"default_bitrate,omitempty"`
	ServerHost     *string `json:"server_host,omitempty"`
	I2SBclk        *int    `json:"i2s_bclk,omitempty"`
	I2SWs          *int    `json:"i2s_ws,omitempty"`
	I2SDin         *int    `json:"i2s_din,omitempty"`
}

// NewSetConfig returns an empty set_config command with the given request id.
func NewSetConfig(requestID string) *SetConfig {
	return &SetConfig{Type: TypeSetConfig, RequestID: requestID}
}

func (m *SetConfig) Kind() string { return TypeSetConfig }

// Validate checks a set_config request is well-formed before sending: at least
// one field must be set, and any provided I2S pin must be in 0..47 (the range
// the firmware accepts for ESP32 & ESP32-S3). Mirrors firmware-side validation.
func (s *SetConfig) Validate() error {
	if s.DefaultBitrate == nil && s.ServerHost == nil &&
		s.I2SBclk == nil && s.I2SWs == nil && s.I2SDin == nil {
		return errors.New("control: set_config requires at least one field")
	}
	if s.DefaultBitrate != nil && *s.DefaultBitrate < 0 {
		return fmt.Errorf("control: default_bitrate must be >= 0: %d", *s.DefaultBitrate)
	}
	if s.ServerHost != nil && *s.ServerHost == "" {
		return errors.New("control: server_host must not be empty")
	}
	for name, v := range map[string]*int{"i2s_bclk": s.I2SBclk, "i2s_ws": s.I2SWs, "i2s_din": s.I2SDin} {
		if v != nil && (*v < 0 || *v > 47) {
			return fmt.Errorf("control: %s out of range 0..47: %d", name, *v)
		}
	}
	return nil
}

// Encode marshals a typed message to its JSON payload (no length prefix).
func Encode(msg Message) ([]byte, error) {
	return json.Marshal(msg)
}

// DecodePayload unmarshals an already-extracted JSON payload into a typed
// message using the "type" discriminator (spec §8 type-dispatch).
func DecodePayload(payload []byte) (Message, error) {
	var hdr struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &hdr); err != nil {
		return nil, err
	}
	switch hdr.Type {
	case TypeHello:
		return decodeAs[*Hello](payload)
	case TypeHelloAck:
		return decodeAs[*HelloAck](payload)
	case TypePing:
		return decodeAs[*Ping](payload)
	case TypePong:
		return decodeAs[*Pong](payload)
	case TypeStartStream:
		return decodeAs[*StartStream](payload)
	case TypeStreamStarted:
		return decodeAs[*StreamStarted](payload)
	case TypeStopStream:
		return decodeAs[*StopStream](payload)
	case TypeStreamStopped:
		return decodeAs[*StreamStopped](payload)
	case TypeGetStatus:
		return decodeAs[*GetStatus](payload)
	case TypeStatus:
		return decodeAs[*Status](payload)
	case TypeError:
		return decodeAs[*Error](payload)
	case TypeSetConfig:
		return decodeAs[*SetConfig](payload)
	default:
		return nil, ErrUnknownMessageType
	}
}

// WriteMessage marshals msg and writes it to w as a framed message (spec §7-§8).
func WriteMessage(w io.Writer, msg Message) error {
	payload, err := Encode(msg)
	if err != nil {
		return err
	}
	return WriteFrame(w, payload)
}

func decodeAs[T any](payload []byte) (Message, error) {
	v := new(T)
	if err := json.Unmarshal(payload, v); err != nil {
		return nil, err
	}
	return any(*v).(Message), nil
}
