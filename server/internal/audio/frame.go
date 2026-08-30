package audio

// DecodedAudioFrame is the internal decoded-audio representation, independent
// of RTP (spec §12, architectural principle 7). All downstream modules
// (recorder, live outputs, monitoring) consume exactly this contract.
//
// PCM holds interleaved stereo samples in signed 16-bit little-endian sample
// order (left, right) for sample_count_per_channel frames.
type DecodedAudioFrame struct {
	StreamID               string
	Timestamp48k           uint32
	SampleCountPerChannel  uint32
	SampleRate             int // always SampleRate (48000)
	Channels               int // always Channels (2)
	PCM                    []int16
	Discontinuity          bool
	SourceRTPSequenceStart uint16
	SourceRTPSequenceEnd   uint16
}

// NewFrame returns a DecodedAudioFrame with the fixed contract fields set
// (spec §12: sample_rate=48000, channels=2).
func NewFrame(streamID string, timestamp48k uint32) *DecodedAudioFrame {
	return &DecodedAudioFrame{
		StreamID:     streamID,
		Timestamp48k: timestamp48k,
		SampleRate:   SampleRate,
		Channels:     Channels,
	}
}

// Valid reports whether the frame satisfies the spec §12 contract: fixed
// sample rate and channels, PCM interleaving consistent with the sample count,
// and a non-empty stream id.
func (f *DecodedAudioFrame) Valid() bool {
	if f.StreamID == "" || f.SampleRate != SampleRate || f.Channels != Channels {
		return false
	}
	want := int(f.SampleCountPerChannel) * f.Channels
	return len(f.PCM) == want
}
