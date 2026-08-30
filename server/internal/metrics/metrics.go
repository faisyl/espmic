package metrics

import "sync/atomic"

// Label constants mirror the spec §18 statistics table. Counter metrics
// monotonically increase; gauge metrics hold an instantaneous value.
const (
	LabelRTPPacketsReceived  = "rtp_packets_received"
	LabelRTPPacketsLost      = "rtp_packets_lost"
	LabelRTPPacketsDuplicate = "rtp_packets_duplicate"
	LabelRTPPacketsReordered = "rtp_packets_reordered"
	LabelRTPPacketsLate      = "rtp_packets_late"
	LabelRTPJitterMS         = "rtp_jitter_ms"
	LabelRTPBitrateBPS       = "rtp_bitrate_bps"
	LabelOpusDecodeErrors    = "opus_decode_errors"
	LabelPCMFramesDecoded    = "pcm_frames_decoded"
	LabelStreamDiscontinuity = "stream_discontinuities"
	LabelRecordingBytes      = "recording_bytes"
	LabelControlReconnects   = "control_reconnects"
)

// Metrics is a concurrency-safe container for every statistic in spec §18.
// All fields use atomic types because S2 goroutines (control session, RTP
// receiver, decoder) may update them concurrently.
type Metrics struct {
	rtpPacketsReceived  atomic.Int64
	rtpPacketsLost      atomic.Int64
	rtpPacketsDuplicate atomic.Int64
	rtpPacketsReordered atomic.Int64
	rtpPacketsLate      atomic.Int64
	rtpJitterMS         atomic.Int64
	rtpBitrateBPS       atomic.Int64
	opusDecodeErrors    atomic.Int64
	pcmFramesDecoded    atomic.Int64
	streamDiscontinuity atomic.Int64
	recordingBytes      atomic.Int64
	controlReconnects   atomic.Int64
}

// New returns a zeroed Metrics.
func New() *Metrics { return &Metrics{} }

// Counter increments.
func (m *Metrics) IncRTPPacketsReceived()  { m.rtpPacketsReceived.Add(1) }
func (m *Metrics) AddRTPPacketsLost(n int) { m.rtpPacketsLost.Add(int64(n)) }
func (m *Metrics) IncRTPPacketsDuplicate() { m.rtpPacketsDuplicate.Add(1) }
func (m *Metrics) IncRTPPacketsReordered() { m.rtpPacketsReordered.Add(1) }
func (m *Metrics) IncRTPPacketsLate()      { m.rtpPacketsLate.Add(1) }
func (m *Metrics) IncOpusDecodeErrors()    { m.opusDecodeErrors.Add(1) }
func (m *Metrics) IncPCMFramesDecoded()    { m.pcmFramesDecoded.Add(1) }
func (m *Metrics) IncStreamDiscontinuity() { m.streamDiscontinuity.Add(1) }
func (m *Metrics) AddRecordingBytes(n int) { m.recordingBytes.Add(int64(n)) }
func (m *Metrics) IncControlReconnects()   { m.controlReconnects.Add(1) }

// Gauge stores.
func (m *Metrics) SetRTPJitterMS(v float64) { m.rtpJitterMS.Store(int64(v * 1000)) }
func (m *Metrics) SetRTPBitrateBPS(v int64) { m.rtpBitrateBPS.Store(v) }

// Snapshot returns the current values keyed by spec §18 label.
func (m *Metrics) Snapshot() map[string]int64 {
	return map[string]int64{
		LabelRTPPacketsReceived:  m.rtpPacketsReceived.Load(),
		LabelRTPPacketsLost:      m.rtpPacketsLost.Load(),
		LabelRTPPacketsDuplicate: m.rtpPacketsDuplicate.Load(),
		LabelRTPPacketsReordered: m.rtpPacketsReordered.Load(),
		LabelRTPPacketsLate:      m.rtpPacketsLate.Load(),
		LabelRTPJitterMS:         m.rtpJitterMS.Load() / 1000,
		LabelRTPBitrateBPS:       m.rtpBitrateBPS.Load(),
		LabelOpusDecodeErrors:    m.opusDecodeErrors.Load(),
		LabelPCMFramesDecoded:    m.pcmFramesDecoded.Load(),
		LabelStreamDiscontinuity: m.streamDiscontinuity.Load(),
		LabelRecordingBytes:      m.recordingBytes.Load(),
		LabelControlReconnects:   m.controlReconnects.Load(),
	}
}
