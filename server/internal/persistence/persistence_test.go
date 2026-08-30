package persistence

import (
	"testing"
	"time"

	"espmic/server/internal/device"
)

func TestDeviceRepoRoundtrip(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	r := NewRepos(db)
	d := device.Device{
		DeviceID:     "d1",
		DisplayName:  "Mic 1",
		Firmware:     "1.2.3",
		Capabilities: []string{"opus", "stereo"},
	}
	if err := r.Devices.Save(d, []byte("cred-hash")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, hash, err := r.Devices.Load("d1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.DeviceID != d.DeviceID || got.DisplayName != d.DisplayName || got.Firmware != d.Firmware {
		t.Fatalf("Load got %+v", got)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "opus" {
		t.Fatalf("capabilities = %v", got.Capabilities)
	}
	if string(hash) != "cred-hash" {
		t.Fatalf("hash = %q", hash)
	}
}

func TestDeviceRepoCredHash(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	r := NewRepos(db)
	if err := r.Devices.Save(device.Device{DeviceID: "d1"}, []byte("h1")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	hash, err := r.Devices.CredHash("d1")
	if err != nil || string(hash) != "h1" {
		t.Fatalf("CredHash: hash=%q err=%v", hash, err)
	}
	if _, err := r.Devices.CredHash("missing"); err == nil {
		t.Fatal("expected error for missing device")
	}
}

func TestStreamRepoSave(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	r := NewRepos(db)
	now := time.Unix(1_000_000, 0)
	if err := r.Streams.Save("s1", "d1", "ACTIVE", "", 12345, now); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestRecordingRepoCreateFinalize(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	r := NewRepos(db)
	now := time.Unix(1_000_000, 0)
	if err := r.Streams.Save("s1", "d1", "ACTIVE", "", 1, now); err != nil {
		t.Fatalf("Save stream: %v", err)
	}
	if err := r.Recordings.Create("r1", "s1", 48000, 2, "flac", now); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := r.Recordings.Finalize("r1", now.Add(5*time.Second), 1024, "/tmp/r1.flac"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}
