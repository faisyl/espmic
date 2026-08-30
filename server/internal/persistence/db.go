// Package persistence stores durable state using SQLite via modernc.org/sqlite
// and database/sql (spec §20). It manages registered devices, stream metadata
// and recording metadata. Repositories expose CRUD backed by the shared *sql.DB.
package persistence

import (
	"database/sql"
	_ "modernc.org/sqlite"
)

// Open initialises (or opens) the SQLite database at path and applies the
// startup schema (spec §6, §20). The driver is the pure-Go modernc.org/sqlite
// build tag import above.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// migrate applies the schema for §6/§20 tables if not present.
func migrate(db *sql.DB) error {
	stmt := `
	CREATE TABLE IF NOT EXISTS devices (
		device_id TEXT PRIMARY KEY,
		display_name TEXT,
		firmware TEXT,
		capabilities TEXT,
		credential_hash BLOB NOT NULL,
		status TEXT,
		last_seen INTEGER
	);
	CREATE TABLE IF NOT EXISTS streams (
		stream_id TEXT PRIMARY KEY,
		device_id TEXT NOT NULL,
		ssrc INTEGER NOT NULL,
		state TEXT NOT NULL,
		started_at INTEGER,
		reason TEXT,
		FOREIGN KEY(device_id) REFERENCES devices(device_id)
	);
	CREATE TABLE IF NOT EXISTS recordings (
		recording_id TEXT PRIMARY KEY,
		stream_id TEXT NOT NULL,
		sample_rate INTEGER NOT NULL,
		channels INTEGER NOT NULL,
		codec TEXT NOT NULL,
		start_time INTEGER,
		end_time INTEGER,
		bytes_stored INTEGER NOT NULL DEFAULT 0,
		uri TEXT,
		FOREIGN KEY(stream_id) REFERENCES streams(stream_id)
	);
	`
	_, err := db.Exec(stmt)
	return err
}

// Repos provides handles to the per-entity repositories (spec §20).
type Repos struct {
	Devices    *DeviceRepo
	Streams    *StreamRepo
	Recordings *RecordingRepo
}

// NewRepos builds repositories over an open db.
func NewRepos(db *sql.DB) *Repos {
	return &Repos{
		Devices:    &DeviceRepo{db: db},
		Streams:    &StreamRepo{db: db},
		Recordings: &RecordingRepo{db: db},
	}
}
