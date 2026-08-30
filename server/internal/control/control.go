// Package control models the device control connection (spec §7-§9).
//
// Framing (spec §7): uint32_be payload_length then payload_length bytes of
// UTF-8 JSON. Maximum accepted payload is 16 KiB. All logic implementing
// hello/auth/heartbeat/command routing lands in S1.
package control

// MaxPayloadBytes is the maximum accepted control frame payload (spec §7).
const MaxPayloadBytes = 16 * 1024

// SessionState enumerates a control session lifecycle (spec §7 flow).
type SessionState string

const (
	SessionStateConnecting SessionState = "connecting"
	SessionStateActive     SessionState = "active"
	SessionStateClosed     SessionState = "closed"
)
