package device

import "time"

// Device is the persisted record of a client (spec §6).
type Device struct {
	DeviceID     string
	DisplayName  string
	Firmware     string
	Capabilities []string
	Status       string
	LastSeen     time.Time
}
