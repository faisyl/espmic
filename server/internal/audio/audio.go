// Package audio models decoded-PCM distribution (spec §12-§14).
//
// The internal decoded-audio representation is independent of RTP (spec §2,
// §12). The PCM bus fans a decoded frame to recorder and live outputs.
package audio

// SampleRate is the nominal decoded sample rate (spec §12).
const SampleRate = 48000

// Channels is the nominal decoded channel count (spec §12).
const Channels = 2
