package device

import (
	"crypto/subtle"
	"errors"
	"sync"
	"time"
)

// ErrDeviceNotFound is returned when a device_id is unknown to the registry.
var ErrDeviceNotFound = errors.New("device: unknown device")

// ErrAuthFailed is returned when credential validation fails.
var ErrAuthFailed = errors.New("device: authentication failed")

// Credential stores a device's authentication material. The secret is stored
// only as a precomputed hash (spec §6 "credential reference/hash"); the
// registry never retains the plaintext credential.
type Credential struct {
	DeviceID string
	Hash     []byte
}

// Registry holds device identity, credentials and online state (spec §6, §19).
// It is safe for concurrent use by the control session goroutines.
type Registry struct {
	mu      sync.RWMutex
	devices map[string]Device
	creds   map[string][]byte
	online  map[string]time.Time
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		devices: make(map[string]Device),
		creds:   make(map[string][]byte),
		online:  make(map[string]time.Time),
	}
}

// Register adds or updates a device and its credential hash.
func (r *Registry) Register(d Device, credHash []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.devices[d.DeviceID] = d
	r.creds[d.DeviceID] = credHash
}

// Get returns the device record for id.
func (r *Registry) Get(id string) (Device, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[id]
	if !ok {
		return Device{}, ErrDeviceNotFound
	}
	return d, nil
}

// Authenticate validates the presented credential hash against the stored one
// using constant-time comparison (spec §19: authenticate before accepting
// commands). Returns the device on success.
func (r *Registry) Authenticate(id string, credHash []byte) (Device, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[id]
	if !ok {
		return Device{}, ErrDeviceNotFound
	}
	want, ok := r.creds[id]
	if !ok {
		return Device{}, ErrAuthFailed
	}
	if subtle.ConstantTimeCompare(want, credHash) != 1 {
		return Device{}, ErrAuthFailed
	}
	return d, nil
}

// SetOnline marks a device online with the current time (last_seen, spec §6).
func (r *Registry) SetOnline(id string, t time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.online[id] = t
}

// SetOffline removes a device from the online set (on disconnect, spec §7).
func (r *Registry) SetOffline(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.online, id)
}

// Online reports whether id is currently online.
func (r *Registry) Online(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.online[id]
	return ok
}

// LastSeen returns the last online timestamp for id.
func (r *Registry) LastSeen(id string) (time.Time, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.online[id]
	return t, ok
}
