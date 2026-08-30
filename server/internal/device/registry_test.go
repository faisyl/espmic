package device

import (
	"testing"
	"time"
)

func TestRegistryRegisterGet(t *testing.T) {
	r := NewRegistry()
	d := Device{DeviceID: "esp32-001", DisplayName: "Mic 1", Firmware: "1.2.3", Capabilities: []string{"opus"}}
	r.Register(d, []byte("hash"))

	got, err := r.Get("esp32-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DeviceID != d.DeviceID || got.DisplayName != d.DisplayName {
		t.Fatalf("got %+v, want %+v", got, d)
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	_, err := NewRegistry().Get("nope")
	if err != ErrDeviceNotFound {
		t.Fatalf("err = %v, want ErrDeviceNotFound", err)
	}
}

func TestRegistryAuthenticate(t *testing.T) {
	r := NewRegistry()
	r.Register(Device{DeviceID: "d1"}, []byte("secret-hash"))

	if _, err := r.Authenticate("d1", []byte("secret-hash")); err != nil {
		t.Fatalf("auth should succeed: %v", err)
	}
	if _, err := r.Authenticate("d1", []byte("wrong")); err != ErrAuthFailed {
		t.Fatalf("wrong cred: err = %v, want ErrAuthFailed", err)
	}
	if _, err := r.Authenticate("unknown", []byte("x")); err != ErrDeviceNotFound {
		t.Fatalf("unknown device: err = %v, want ErrDeviceNotFound", err)
	}
}

func TestRegistryOnlineOffline(t *testing.T) {
	r := NewRegistry()
	r.Register(Device{DeviceID: "d1"}, []byte("h"))
	now := time.Now()

	r.SetOnline("d1", now)
	if !r.Online("d1") {
		t.Fatal("expected online")
	}
	if ls, _ := r.LastSeen("d1"); !ls.Equal(now) {
		t.Fatalf("last seen = %v, want %v", ls, now)
	}

	r.SetOffline("d1")
	if r.Online("d1") {
		t.Fatal("expected offline")
	}
}
