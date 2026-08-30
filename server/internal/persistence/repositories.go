package persistence

import (
	"database/sql"
	"strings"
	"time"

	"espmic/server/internal/device"
)

// DeviceRepo persists device identity + credential hash (spec §6, §20).
type DeviceRepo struct {
	db *sql.DB
}

// Save inserts or replaces a device record with its credential hash.
func (r *DeviceRepo) Save(d device.Device, credHash []byte) error {
	caps := strings.Join(d.Capabilities, ",")
	var ls sql.NullInt64
	if !d.LastSeen.IsZero() {
		ls = sql.NullInt64{Valid: true, Int64: d.LastSeen.UnixMilli()}
	}
	_, err := r.db.Exec(
		`INSERT INTO devices(device_id,display_name,firmware,capabilities,credential_hash,status,last_seen)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(device_id) DO UPDATE SET
		   display_name=excluded.display_name,
		   firmware=excluded.firmware,
		   capabilities=excluded.capabilities,
		   credential_hash=excluded.credential_hash,
		   status=excluded.status,
		   last_seen=excluded.last_seen`,
		d.DeviceID, d.DisplayName, d.Firmware, caps, credHash, d.Status, ls)
	return err
}

// Load returns the device for id with its credential hash.
func (r *DeviceRepo) Load(id string) (device.Device, []byte, error) {
	row := r.db.QueryRow(
		`SELECT device_id,display_name,firmware,capabilities,credential_hash,status,last_seen
		   FROM devices WHERE device_id=?`, id)
	var d device.Device
	var caps string
	var hash []byte
	var ls sql.NullInt64
	if err := row.Scan(&d.DeviceID, &d.DisplayName, &d.Firmware, &caps, &hash, &d.Status, &ls); err != nil {
		return device.Device{}, nil, err
	}
	if caps != "" {
		d.Capabilities = strings.Split(caps, ",")
	}
	if ls.Valid {
		d.LastSeen = time.UnixMilli(ls.Int64)
	}
	return d, hash, nil
}

// CredHash returns the stored credential hash for id (used on hello auth,
// spec §19).
func (r *DeviceRepo) CredHash(id string) ([]byte, error) {
	row := r.db.QueryRow(`SELECT credential_hash FROM devices WHERE device_id=?`, id)
	var hash []byte
	if err := row.Scan(&hash); err != nil {
		return nil, err
	}
	return hash, nil
}

// StreamRepo persists stream metadata (spec §6, §20).
type StreamRepo struct {
	db *sql.DB
}

func (r *StreamRepo) Save(id, deviceID, state, reason string, ssrc uint32, started time.Time) error {
	var st sql.NullInt64
	if !started.IsZero() {
		st = sql.NullInt64{Valid: true, Int64: started.UnixMilli()}
	}
	_, err := r.db.Exec(
		`INSERT INTO streams(stream_id,device_id,ssrc,state,started_at,reason)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(stream_id) DO UPDATE SET
		   state=excluded.state, reason=excluded.reason`,
		id, deviceID, int64(ssrc)&0xffffffff, state, st, reason)
	return err
}

// RecordingRepo persists recording metadata (spec §6, §20).
type RecordingRepo struct {
	db *sql.DB
}

func (r *RecordingRepo) Create(recID, streamID string, sampleRate, channels int, codec string, start time.Time) error {
	_, err := r.db.Exec(
		`INSERT INTO recordings(recording_id,stream_id,sample_rate,channels,codec,start_time,bytes_stored)
		 VALUES(?,?,?,?,?,?,0)`, recID, streamID, sampleRate, channels, codec, start.UnixMilli())
	return err
}

// Finalize marks a recording complete with end time, byte count and file uri.

func (r *RecordingRepo) Finalize(recID string, end time.Time, bytes int64, uri string) error {
	_, err := r.db.Exec(
		`UPDATE recordings SET end_time=?, bytes_stored=?, uri=? WHERE recording_id=?`,
		end.UnixMilli(), bytes, uri, recID)
	return err
}
