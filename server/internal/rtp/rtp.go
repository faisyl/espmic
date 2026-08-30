// Package rtp models the RTP/Opus ingest path (spec §10-§11).
//
// Uses pion/rtp for parse and pion/opus (pure-Go) for decode. Interfaces for
// the receiver and jitter buffer are declared here; S1 implements them.
package rtp

// ClockRate is the RTP clock for Opus (spec §10).
const ClockRate = 48000

// DefaultPayloadType is the default dynamic payload type for Opus (spec §10).
const DefaultPayloadType = 111
