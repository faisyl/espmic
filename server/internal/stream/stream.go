// Package stream models the stream registry and lifecycle (spec §17).
//
// The server owns authoritative stream state and drives the lifecycle
// CREATED -> ... -> COMPLETE with failure paths per spec §17.
package stream

// State enumerates the stream lifecycle (spec §17).
type State string

const (
	StateCreated         State = "CREATED"
	StateWaitingForDevice State = "WAITING_FOR_DEVICE"
	StateStarting        State = "STARTING"
	StateRTPWait         State = "RTP_WAIT"
	StateActive          State = "ACTIVE"
	StateStopping        State = "STOPPING"
	StateComplete        State = "COMPLETE"
	StateFailed          State = "FAILED"
)
