// Package control models the device control connection (spec §7-§9).
//
// Framing (spec §7): uint32_be payload_length then payload_length bytes of
// UTF-8 JSON, see frame.go. Control message structs and type-dispatch live in
// messages.go. Session/routing logic (hello/auth/heartbeat/commands) lands in
// S2.
package control

// SessionState enumerates a control session lifecycle (spec §7 flow).
type SessionState string

const (
	SessionStateConnecting SessionState = "connecting"
	SessionStateActive     SessionState = "active"
	SessionStateClosed     SessionState = "closed"
)
