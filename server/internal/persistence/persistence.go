// Package persistence models durable state (spec §20).
//
// Stores registered devices, recording metadata and stream/recording
// metadata. Planned driver is modernc.org/sqlite (pure-Go) via database/sql.
package persistence
