package audio

import (
	"encoding/binary"
	"fmt"
	"os"
	"time"
)

// OpusRecorder muxes received RTP Opus payloads into an Ogg/Opus file
// (RFC 7845). It runs off the RTP receive path — no server-side PCM decode
// on the recording path. One .opus file per stream session.
//
// Ogg page layout (RFC 7845 §3):
//
//	OggS(4) | version(1) | headertype(1) | granulepos(8) | serialnum(4) |
//	seqnum(4) | crc(4) | nsegments(1) | segmenttable(n) | data
//
// Capture patterns: "OpusHead" on BOS page, "OpusTags" on the next page.
type OpusRecorder struct {
	serial   uint32
	seq      uint32
	preskip  uint16
	channels int
	rate     int
	startTS  uint32
	prevGP   int64

	f         *os.File
	startTime time.Time
	bytes     int64
	closed    bool
}

// NewOpusRecorder opens the file and writes OpusHead + OpusTags headers.
func NewOpusRecorder(dir, base string, rate, channels int, preskip uint16) (*OpusRecorder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(fmt.Sprintf("%s/%s.opus", dir, base))
	if err != nil {
		return nil, err
	}
	r := &OpusRecorder{
		serial:    uint32(time.Now().UnixNano()) & 0xFFFFFFFF,
		preskip:   preskip,
		channels:  channels,
		rate:      rate,
		f:         f,
		startTime: time.Now(),
	}
	r.writeOpusHead()
	r.writeOpusTags()
	return r, nil
}

// writePage appends an Ogg page with the given granulepos and data.
func (r *OpusRecorder) writePage(granulepos int64, data []byte) {
	const headLen = 27
	nseg := len(data) / 255
	if len(data)%255 != 0 || nseg == 0 {
		nseg++
	}
	hdr := [headLen]byte{}
	copy(hdr[0:4], "OggS")
	hdr[4] = 0 // version
	hdr[5] = 0 // header type (set by caller for BOS/EOS)
	binary.LittleEndian.PutUint64(hdr[6:14], uint64(granulepos))
	binary.LittleEndian.PutUint32(hdr[14:18], r.serial)
	binary.LittleEndian.PutUint32(hdr[18:22], r.seq)
	// crc slot reserved; computed after segment table
	hdr[26] = byte(nseg)

	segTable := make([]byte, nseg)
	filled := 0
	for i := 0; i < nseg; i++ {
		remaining := len(data) - filled
		if remaining >= 255 {
			segTable[i] = 255
			filled += 255
		} else {
			segTable[i] = byte(remaining)
			filled += remaining
		}
	}

	page := append(hdr[:], segTable...)
	page = append(page, data...)
	crc := oggCRC(page)
	binary.LittleEndian.PutUint32(page[22:26], crc)
	r.f.Write(page)
	r.seq++
	r.bytes += int64(len(page))
}

// writeOpusHead writes the OpusHead identification page (BOS).
func (r *OpusRecorder) writeOpusHead() {
	// OpusHead payload: "OpusHead"(8) + ver(1) + ch(1) + preskip(2 LE) +
	// rate(4 LE, always 48000) + gain(2 LE) + map_family(1)
	head := make([]byte, 19)
	copy(head[0:8], "OpusHead")
	head[8] = 1 // version
	head[9] = byte(r.channels)
	binary.LittleEndian.PutUint16(head[10:12], r.preskip)
	binary.LittleEndian.PutUint32(head[12:16], uint32(r.rate))
	binary.LittleEndian.PutUint16(head[16:18], 0) // output gain
	head[18] = 0                                  // channel mapping family (mono/stereo)
	// BOS flag set in header type byte
	page := r.buildBOSPage(0, head)
	r.f.Write(page)
	r.seq++
	r.bytes += int64(len(page))
}

func (r *OpusRecorder) buildBOSPage(granulepos int64, data []byte) []byte {
	const headLen = 27
	nseg := len(data) / 255
	if len(data)%255 != 0 || nseg == 0 {
		nseg++
	}
	hdr := [headLen]byte{}
	copy(hdr[0:4], "OggS")
	hdr[4] = 0
	hdr[5] = 0x02 // BOS
	binary.LittleEndian.PutUint64(hdr[6:14], uint64(granulepos))
	binary.LittleEndian.PutUint32(hdr[14:18], r.serial)
	binary.LittleEndian.PutUint32(hdr[18:22], r.seq)
	hdr[26] = byte(nseg)
	segTable := make([]byte, nseg)
	filled := 0
	for i := 0; i < nseg; i++ {
		remaining := len(data) - filled
		if remaining >= 255 {
			segTable[i] = 255
			filled += 255
		} else {
			segTable[i] = byte(remaining)
			filled += remaining
		}
	}
	page := append(hdr[:], segTable...)
	page = append(page, data...)
	crc := oggCRC(page)
	binary.LittleEndian.PutUint32(page[22:26], crc)
	return page
}

// writeOpusTags writes the OpusTags comment page.
func (r *OpusRecorder) writeOpusTags() {
	const vendor = "espmic"
	tags := make([]byte, 0, 32)
	tags = append(tags, "OpusTags"...)
	vendorLen := make([]byte, 4)
	binary.LittleEndian.PutUint32(vendorLen, uint32(len(vendor)))
	tags = append(tags, vendorLen...)
	tags = append(tags, []byte(vendor)...)
	commentLen := make([]byte, 4)
	binary.LittleEndian.PutUint32(commentLen, 0) // no user comments
	tags = append(tags, commentLen...)
	r.writePage(0, tags)
}

// WritePacket appends an Opus packet as a new Ogg page. granulepos is derived
// from the RTP timestamp at 48kHz, with preskip subtracted on the first page.
func (r *OpusRecorder) WritePacket(rtpTS uint32, payload []byte) {
	if r.closed {
		return
	}
	if r.startTS == 0 {
		r.startTS = rtpTS
	}
	// granulepos = (rtpTS - startTS) - preskip, in 48kHz units
	gp := int64(rtpTS) - int64(r.startTS) - int64(r.preskip)
	if gp < 0 {
		gp = 0
	}
	r.prevGP = gp
	r.writePage(gp, payload)
}

// Finalize closes the file with an EOS page and returns the URI + byte size.
func (r *OpusRecorder) Finalize(end time.Time) (string, int64, error) {
	if r.closed {
		return "", 0, fmt.Errorf("opus: already finalized")
	}
	// EOS page: empty data, granulepos = last gp
	r.writeEOSPage(r.prevGP)
	uri := r.f.Name()
	err := r.f.Close()
	r.closed = true
	return uri, r.bytes, err
}

func (r *OpusRecorder) writeEOSPage(granulepos int64) {
	const headLen = 27
	hdr := [headLen]byte{}
	copy(hdr[0:4], "OggS")
	hdr[4] = 0
	hdr[5] = 0x04 // EOS
	binary.LittleEndian.PutUint64(hdr[6:14], uint64(granulepos))
	binary.LittleEndian.PutUint32(hdr[14:18], r.serial)
	binary.LittleEndian.PutUint32(hdr[18:22], r.seq)
	hdr[26] = 0 // no segments
	page := hdr[:]
	crc := oggCRC(page)
	binary.LittleEndian.PutUint32(page[22:26], crc)
	r.f.Write(page)
	r.seq++
	r.bytes += int64(len(page))
}

// oggCRC computes the Ogg page CRC-32 (polynomial 0x04C11DB7).
func oggCRC(data []byte) uint32 {
	crc := uint32(0)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
