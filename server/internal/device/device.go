// Package device models the device registry (spec §5-§6).
//
// Holds device identity, credentials/credential hashes, firmware,
// capabilities, last-seen and online status. Persistence-backed logic lands
// in S1-S3.
package device

// Device is the persisted record of a client (spec §6).
type Device struct {
	DeviceID   string
	DisplayName string
	Firmware   string
	Capabilities []string
	Status     string
}
