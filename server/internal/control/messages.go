package control

import (
	"encoding/json"
	"errors"
	"io"
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

// Status is a device status response (device -> server).
type Status struct {
	Type   string         `json:"type"`
	Status string         `json:"status"`
	Fields map[string]any `json:"fields,omitempty"`
}

func NewStatus(status string, fields map[string]any) *Status {
	return &Status{Type: TypeStatus, Status: status, Fields: fields}
}

func (m *Status) Kind() string { return TypeStatus }

// Error reports a runtime or command error (device -> server).
type Error struct {
	Type    string `json:"type"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func NewError(code int, message string) *Error {
	return &Error{Type: TypeError, Code: code, Message: message}
}

func (m *Error) Kind() string { return TypeError }

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
