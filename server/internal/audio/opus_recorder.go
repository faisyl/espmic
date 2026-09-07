package audio

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"time"
)

// opusPacket is a received RTP Opus payload queued for disk write.
type opusPacket struct {
	ts      uint32
	payload []byte
}

// OpusRecorder muxes received RTP Opus payloads into an Ogg/Opus file
// (RFC 7845). It runs off the RTP receive path — no server-side PCM decode
// on the recording path. One .opus file per stream session.
//
// WritePacket is non-blocking: it enqueues the packet to a buffered channel
// drained by a separate writer goroutine, so the RTP receive loop never
// blocks on disk I/O.
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

	mu        sync.Mutex
	f         *os.File
	startTime time.Time
	bytes     int64
	closed    bool

	// Buffered channel decouples the RTP receive loop from disk I/O.
	ch chan *opusPacket
	// done closed when the writer goroutine has flushed and exited.
	done chan struct{}
	// writerErr holds the first error from the writer goroutine.
	writerErr error
}

// NewOpusRecorder opens the file, writes OpusHead + OpusTags headers, and
// starts the background writer goroutine.
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
		ch:        make(chan *opusPacket, 256),
		done:      make(chan struct{}),
	}
	r.writeOpusHead()
	r.writeOpusTags()
	go r.writer()
	return r, nil
}

// writer drains the packet channel and writes Ogg pages to disk.
func (r *OpusRecorder) writer() {
	defer close(r.done)
	for pkt := range r.ch {
		r.writePageForPacket(pkt)
	}
}

// writePageForPacket writes a single Ogg page for the given Opus packet.
func (r *OpusRecorder) writePageForPacket(pkt *opusPacket) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if r.startTS == 0 {
		r.startTS = pkt.ts
	}
	gp := int64(pkt.ts) - int64(r.startTS) - int64(r.preskip)
	if gp < 0 {
		gp = 0
	}
	r.prevGP = gp
	r.writePage(gp, pkt.payload)
}

// WritePacket enqueues an Opus packet for non-blocking disk write. If the
// channel is full (writer falling behind), the packet is dropped so the RTP
// receive loop never stalls.
func (r *OpusRecorder) WritePacket(rtpTS uint32, payload []byte) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	select {
	case r.ch <- &opusPacket{ts: rtpTS, payload: payload}:
	default:
		// Channel full; drop to keep the receive loop moving.
	}
}

// Finalize closes the channel, waits for the writer to flush, writes the EOS
// page, and closes the file.
func (r *OpusRecorder) Finalize(end time.Time) (string, int64, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return "", 0, fmt.Errorf("opus: already finalized")
	}
	r.closed = true
	r.mu.Unlock()

	close(r.ch)
	<-r.done

	uri := r.f.Name()
	r.writeEOSPage(r.prevGP)
	err := r.f.Close()
	return uri, r.bytes, err
}

// writePage appends an Ogg page with the given granulepos and data.
func (r *OpusRecorder) writePage(granulepos int64, data []byte) {
	const headLen = 27
	nseg := len(data)/255 + 1
	hdr := [headLen]byte{}
	copy(hdr[0:4], "OggS")
	hdr[4] = 0
	hdr[5] = 0
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
	r.f.Write(page)
	r.seq++
	r.bytes += int64(len(page))
}

// writeOpusHead writes the OpusHead identification page (BOS).
func (r *OpusRecorder) writeOpusHead() {
	head := make([]byte, 19)
	copy(head[0:8], "OpusHead")
	head[8] = 1
	head[9] = byte(r.channels)
	binary.LittleEndian.PutUint16(head[10:12], r.preskip)
	binary.LittleEndian.PutUint32(head[12:16], uint32(r.rate))
	binary.LittleEndian.PutUint16(head[16:18], 0)
	head[18] = 0
	page := r.buildBOSPage(0, head)
	r.f.Write(page)
	r.seq++
	r.bytes += int64(len(page))
}

func (r *OpusRecorder) buildBOSPage(granulepos int64, data []byte) []byte {
	const headLen = 27
	nseg := len(data)/255 + 1
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
	binary.LittleEndian.PutUint32(commentLen, 0)
	tags = append(tags, commentLen...)
	r.writePage(0, tags)
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
	hdr[26] = 0
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
