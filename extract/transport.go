package extract

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"slices"

	"github.com/asticode/go-astits"
)

// astits decodes individual packet envelopes and bounded complete PSI/header
// records. Its whole-PES packet pool is deliberately not used for video: zero
// length PES payloads are streamed without retaining their compressed bodies.
type transportReader struct {
	ctx            context.Context
	r              io.Reader
	size, position int
	raw            [192]byte
	packet         [188]byte
	index          uint64
	readErr        error
}

func (r *transportReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.readErr != nil {
		return 0, r.readErr
	}
	if r.position == 0 {
		n, err := io.ReadFull(&transportSource{r}, r.raw[:r.size])
		if err != nil {
			if n != 0 || err == io.ErrUnexpectedEOF {
				err = errors.Join(ErrInvalidBitstream, ErrIncomplete, err)
			}
			return 0, err
		}
		copy(r.packet[:], r.raw[r.size-188:r.size])
		r.index++
		if err := validateAdaptation(r.packet[:]); err != nil {
			return 0, err
		}
	}
	n := copy(p, r.packet[r.position:])
	r.position = (r.position + n) % 188
	return n, nil
}

type transportSource struct{ t *transportReader }

// Preflight declared control extents before astits reads the whole packet.
func validateAdaptation(packet []byte) error {
	control := packet[3] & 0x30
	if packet[0] != 0x47 || control == 0 {
		return fieldError("transport-header", ErrInvalidBitstream)
	}
	if control&0x20 == 0 {
		return nil
	}
	n := int(packet[4])
	if n > 183 || control == 0x20 && n != 183 || control == 0x30 && n > 182 {
		return fieldError("adaptation-field-length", ErrInvalidBitstream)
	}
	if n == 0 {
		return nil
	}
	b := bitReader{data: packet[5 : 5+n]}
	flags := b.read(8)
	if flags&0x18 == 0x08 {
		b.invalid() // OPCR requires PCR in the same packet.
	}
	for i, size := range []uint{48, 48, 8} {
		if flags&(0x10>>i) != 0 {
			if i < 2 {
				b.skip(33)
				b.expect(6, 63)
				if b.read(9) >= 300 {
					b.invalid()
				}
			} else {
				b.skip(size)
			}
		}
	}
	if flags&2 != 0 {
		b.skip(uint(b.read(8)) * 8)
	}
	if flags&1 != 0 {
		size := int(b.read(8))
		if size == 0 {
			b.invalid() // Every declared extension contains its flags byte.
		}
		start := b.pos / 8
		b.skip(uint(size) * 8)
		if b.err == nil {
			ext := bitReader{data: b.data[start : start+size]}
			extFlags := ext.read(8)
			if extFlags&15 != 15 {
				ext.invalid()
			}
			ltwValid := false
			if extFlags&0x80 != 0 {
				ltwValid = ext.flag()
				ext.skip(15)
			}
			if extFlags&0x40 != 0 {
				ext.expect(2, 3)
				if ext.read(22) == 0 && ltwValid {
					ext.invalid()
				}
			}
			if extFlags&0x20 != 0 {
				if flags&4 == 0 {
					ext.invalid()
				}
				// H.222.0 v9 gives conflicting non-H.262 splice_type values;
				// retain the bounded type without inferring HEVC splice continuity.
				ext.skip(4)
				for _, width := range []uint{3, 15, 15} {
					ext.skip(width)
					ext.expect(1, 1)
				}
			}
			if extFlags&0x10 == 0 {
				for ext.err == nil && ext.pos < len(ext.data)*8 {
					ext.read(8) // Descriptor tag; unknown bodies remain opaque.
					ext.skip(uint(ext.read(8)) * 8)
				}
			} else {
				for ext.err == nil && ext.pos < len(ext.data)*8 {
					ext.expect(8, 255)
				}
			}
			if err := syntaxError(&ext, "adaptation-extension"); err != nil {
				return err
			}
		}
	}
	for b.err == nil && b.pos < len(b.data)*8 {
		b.expect(8, 255)
	}
	return syntaxError(&b, "adaptation-field")
}

func (r *transportSource) Read(p []byte) (int, error) {
	if err := r.t.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.t.r.Read(p)
	if n == 0 && len(p) > 0 && err == nil {
		err = io.ErrNoProgress
	}
	if err != nil && err != io.EOF {
		r.t.readErr = err
	}
	return n, err
}

type transportPID struct {
	last             [188]byte
	known, duplicate bool
	cc               byte
	section          [1024]byte
	length, need     int
}
type transportFile struct {
	ctx        context.Context
	l          Limits
	memory     *accounting
	opts       Options
	reader     *transportReader
	states     map[uint16]*transportPID
	programs   map[uint16]uint16
	pmts       map[uint16][]byte
	candidates map[uint16]uint16
	early      [8192]bool
	stream     *Stream
	selected   uint16
	es         uint64
	pes        transportPES
	pat        bool
	indexBytes int64
}
type transportPES struct {
	header                              [264]byte
	length, need                        int
	remaining                           int
	active, decoded, unbounded, padding bool
	packet                              uint64
}

func (f *transportFile) index(n int64) error {
	if n > f.l.MaxContainerIndexBytes-f.indexBytes {
		return ErrResourceLimit
	}
	if _, err := f.memory.reserve(n); err != nil {
		return err
	}
	f.indexBytes += n
	return nil
}
func (f *transportFile) state(pid uint16) (*transportPID, error) {
	if state := f.states[pid]; state != nil {
		return state, nil
	}
	if err := f.index(2048); err != nil {
		return nil, err
	}
	state := &transportPID{}
	f.states[pid] = state
	return state, nil
}
func (f *transportFile) continuity(p *astits.Packet, state *transportPID) (bool, error) {
	if p.Header.TransportErrorIndicator {
		return false, fieldError("transport-error-indicator", errors.Join(ErrInvalidBitstream, ErrIncomplete))
	}
	if p.Header.TransportScramblingControl != 0 {
		return false, fieldError("scrambled-packet", ErrUnsupportedInput)
	}
	if p.AdaptationField != nil && p.AdaptationField.DiscontinuityIndicator {
		return false, fieldError("transport-discontinuity", ErrUnsupportedInput)
	}
	cc := p.Header.ContinuityCounter
	if state.known {
		if !p.Header.HasPayload {
			if cc != state.cc {
				return false, fieldError("adaptation-continuity", ErrInvalidBitstream)
			}
			state.duplicate = true // The preceding same-PID packet is no longer a payload.
			return true, nil
		}
		if cc == state.cc {
			packet := f.reader.packet[:]
			same := bytes.Equal(state.last[:], packet)
			if !same && packet[3]&0x20 != 0 && packet[4] >= 7 && packet[5]&0x10 != 0 {
				// Only PCR clock values may change; preflight validated their range.
				same = bytes.Equal(state.last[:6], packet[:6]) && state.last[10]&0x7e == packet[10]&0x7e && bytes.Equal(state.last[12:], packet[12:])
			}
			if state.duplicate || !same {
				return false, fieldError("conflicting-duplicate", ErrInvalidBitstream)
			}
			state.duplicate = true
			return true, nil
		}
		if cc != (state.cc+1)&15 {
			return false, fieldError("continuity-loss", errors.Join(ErrInvalidBitstream, ErrIncomplete))
		}
	}
	if p.Header.HasPayload {
		state.known = true
		state.cc = cc
		state.duplicate = false
		copy(state.last[:], f.reader.packet[:])
	}
	return !p.Header.HasPayload, nil
}

// Provide one bounded complete control record to the dependency, avoiding its
// EOF fallback that deliberately logs and discards malformed partial records.
func decodeTransportControl(ctx context.Context, payload []byte, psi bool) (*astits.DemuxerData, error) {
	pid := uint16(0x1000)
	if psi {
		pid = 0
		payload = append([]byte{0}, payload...)
	}
	var packets []byte
	for pos, cc := 0, byte(0); pos < len(payload); cc++ {
		var packet [188]byte
		for i := range packet {
			packet[i] = 0xff
		}
		packet[0] = 0x47
		packet[1] = byte(pid >> 8)
		packet[2] = byte(pid)
		packet[3] = 0x10 | cc&15
		if pos == 0 {
			packet[1] |= 0x40
		}
		n := copy(packet[4:], payload[pos:])
		pos += n
		packets = append(packets, packet[:]...)
	}
	d := astits.NewDemuxer(ctx, bytes.NewReader(packets), astits.DemuxerOptPacketSize(188))
	data, err := d.NextData()
	if err != nil {
		return nil, fieldError("control-record", errors.Join(ErrInvalidBitstream, err))
	}
	if data == nil {
		return nil, ErrInvalidBitstream
	}
	return data, nil
}
func (f *transportFile) section(pid uint16, data []byte) error {
	if len(data) < 12 || data[1]&0xf0 != 0xb0 {
		return fieldError("PSI-syntax", ErrInvalidBitstream)
	}
	if data[6] != 0 || data[7] != 0 {
		return fieldError("multi-section-table", ErrUnsupportedInput)
	}
	if pid == 0 && data[0] != 0 || pid != 0 && data[0] != 2 {
		return ErrInvalidBitstream
	}
	decoded, err := decodeTransportControl(f.ctx, data, true)
	if err != nil {
		return err
	}
	if data[5]&1 == 0 {
		return nil
	}
	if pid == 0 {
		if decoded.PAT == nil {
			return ErrInvalidBitstream
		}
		programs := make(map[uint16]uint16)
		for _, p := range decoded.PAT.Programs {
			if p.ProgramNumber == 0 {
				continue
			}
			if p.ProgramMapID < 0x10 || p.ProgramMapID >= 0x1fff {
				return ErrInvalidBitstream
			}
			if _, exists := programs[p.ProgramMapID]; exists {
				return ErrInvalidBitstream
			}
			programs[p.ProgramMapID] = p.ProgramNumber
		}
		if len(programs) == 0 {
			return ErrUnsupportedInput
		}
		if f.pat {
			if len(programs) != len(f.programs) {
				return fieldError("PAT-program-change", ErrUnsupportedInput)
			}
			for key, value := range programs {
				if f.programs[key] != value {
					return fieldError("PAT-program-change", ErrUnsupportedInput)
				}
			}
		} else {
			if err := f.index(int64(len(programs)) * 128); err != nil {
				return err
			}
			f.programs = programs
			f.pat = true
		}
		return nil
	}
	if decoded.PMT == nil || decoded.PMT.ProgramNumber != f.programs[pid] {
		return ErrInvalidBitstream
	}
	// A version increment with unchanged stream mapping is harmless. Other
	// changes cannot prove that a single physical stream remains continuous.
	normal := slices.Clone(data[:len(data)-4])
	normal[5] &^= 0x3e
	if old := f.pmts[pid]; old != nil {
		if !bytes.Equal(old, normal) {
			return fieldError("PMT-stream-change", ErrUnsupportedInput)
		}
		return nil
	}
	if err := f.index(int64(len(normal)) + 512); err != nil {
		return err
	}
	f.pmts[pid] = normal
	for _, entry := range decoded.PMT.ElementaryStreams {
		if entry.StreamType != astits.StreamTypeH265Video {
			continue
		}
		id := entry.ElementaryPID
		if id < 0x10 || id >= 0x1fff || f.programs[id] != 0 {
			return ErrInvalidBitstream
		}
		if program := f.candidates[id]; program != 0 && program != decoded.PMT.ProgramNumber {
			return ErrAmbiguousTrack
		}
		if f.candidates[id] == 0 {
			if err := f.index(128); err != nil {
				return err
			}
			f.candidates[id] = decoded.PMT.ProgramNumber
		}
		if f.opts.TrackID != 0 && f.opts.TrackID != uint64(id) {
			continue
		}
		if f.stream != nil && f.selected != id {
			return ErrAmbiguousTrack
		}
		if f.stream == nil {
			if f.early[id] {
				return fieldError("video-before-program-map", ErrUnsupportedInput)
			}
			f.selected = id
			s, err := newStream(StreamOptions{Key: StreamKey{TrackID: uint64(id)}, Limits: f.l, Budget: f.opts.Budget}, f.l, f.memory)
			if err != nil {
				return err
			}
			f.stream = s
		}
	}
	return nil
}
func (f *transportFile) psiBytes(pid uint16, state *transportPID, data []byte) error {
	for len(data) > 0 {
		if state.length == 0 && data[0] == 0xff {
			for _, b := range data {
				if b != 0xff {
					return ErrInvalidBitstream
				}
			}
			return nil
		}
		need := 3
		if state.need != 0 {
			need = state.need
		}
		amount := min(len(data), need-state.length)
		copy(state.section[state.length:], data[:amount])
		state.length += amount
		data = data[amount:]
		if state.length == 3 && state.need == 0 {
			n := int(binary.BigEndian.Uint16(state.section[1:3])&0xfff) + 3
			if n < 12 || n > 1024 {
				return fieldError("PSI-section-length", ErrInvalidBitstream)
			}
			state.need = n
		}
		if state.need != 0 && state.length == state.need {
			if err := f.section(pid, state.section[:state.length]); err != nil {
				return err
			}
			state.length = 0
			state.need = 0
		}
	}
	return nil
}
func (f *transportFile) psi(p *astits.Packet, state *transportPID) error {
	data := p.Payload
	if p.Header.PayloadUnitStartIndicator {
		if len(data) == 0 {
			return ErrInvalidBitstream
		}
		pointer := int(data[0])
		data = data[1:]
		if pointer > len(data) {
			return ErrInvalidBitstream
		}
		if state.length != 0 {
			if err := f.psiBytes(p.Header.PID, state, data[:pointer]); err != nil {
				return err
			}
			if state.length != 0 {
				return fieldError("partial-PSI-section", errors.Join(ErrInvalidBitstream, ErrIncomplete))
			}
		}
		return f.psiBytes(p.Header.PID, state, data[pointer:])
	}
	if state.length == 0 {
		return fieldError("PSI-without-start", ErrIncomplete)
	}
	return f.psiBytes(p.Header.PID, state, data)
}
func validPESTimestamp(b []byte, prefix byte) bool {
	return len(b) == 5 && b[0]>>4 == prefix && b[0]&1 == 1 && b[2]&1 == 1 && b[4]&1 == 1
}

func validatePESOptional(data []byte) error {
	b := bitReader{data: data[9:]}
	flags := data[7]
	if flags>>6 == 2 {
		b.skip(40)
	} else if flags>>6 == 3 {
		b.skip(80)
	}
	if flags&0x20 != 0 {
		b.expect(2, 3)
		for _, width := range []uint{3, 15, 15} {
			b.skip(width)
			b.expect(1, 1)
		}
		if b.read(9) >= 300 {
			b.invalid()
		}
		b.expect(1, 1)
	}
	if flags&0x10 != 0 {
		b.expect(1, 1)
		if b.read(22) == 0 {
			b.invalid()
		}
		b.expect(1, 1)
	}
	if flags&8 != 0 {
		b.skip(8)
		if b.err == nil {
			return fieldError("DSM-trick-mode", ErrUnsupportedInput)
		}
	}
	if flags&4 != 0 {
		b.expect(1, 1)
		b.skip(7)
	}
	if flags&2 != 0 {
		b.skip(16) // A preceding-packet CRC remains a bounded diagnostic field.
	}
	if flags&1 != 0 {
		ext := b.read(8)
		if ext&14 != 14 {
			b.invalid()
		}
		if ext&0x80 != 0 {
			b.skip(128)
		}
		if ext&0x40 != 0 {
			size := int(b.read(8))
			start := b.pos / 8
			b.skip(uint(size) * 8)
			if b.err == nil {
				pack := bitReader{data: b.data[start : start+size]}
				pack.expect(32, 0x000001ba)
				if pack.err == nil && len(pack.data) >= 12 && pack.data[4]>>4 == 2 {
					return fieldError("MPEG1-pack-header", ErrUnsupportedInput)
				}
				pack.expect(2, 1)
				for _, width := range []uint{3, 15, 15} {
					pack.skip(width)
					pack.expect(1, 1)
				}
				if pack.read(9) >= 300 {
					pack.invalid()
				}
				pack.expect(1, 1)
				if pack.read(22) == 0 {
					pack.invalid()
				}
				pack.expect(2, 3)
				pack.expect(5, 31)
				stuffing := pack.read(3)
				for range stuffing {
					pack.expect(8, 255)
				}
				if pack.err == nil && pack.pos < len(pack.data)*8 {
					// Embedded system headers have independent program constraints.
					if len(pack.data)-pack.pos/8 >= 4 && binary.BigEndian.Uint32(pack.data[pack.pos/8:]) == 0x000001bb {
						return fieldError("pack-system-header", ErrUnsupportedInput)
					}
					pack.invalid()
				}
				if err := syntaxError(&pack, "PES-pack-header"); err != nil {
					return err
				}
			}
		}
		if ext&0x20 != 0 {
			for range 2 {
				b.expect(1, 1)
				b.skip(7)
			}
		}
		if ext&0x10 != 0 {
			b.expect(2, 1)
			b.skip(14)
		}
		if ext&1 != 0 {
			b.expect(1, 1)
			size := int(b.read(7))
			start := b.pos / 8
			b.skip(uint(size) * 8)
			if b.err == nil {
				extra := bitReader{data: b.data[start : start+size]}
				if extra.flag() {
					extra.expect(6, 63)
					if !extra.flag() {
						extra.expect(4, 15)
						for _, width := range []uint{3, 15, 15} {
							extra.skip(width)
							extra.expect(1, 1)
						}
					}
				} else {
					extra.skip(7)
					if data[3] != 0xfd {
						extra.invalid()
					}
				}
				for extra.err == nil && extra.pos < len(extra.data)*8 {
					extra.expect(8, 255)
				}
				if err := syntaxError(&extra, "PES-extension2"); err != nil {
					return err
				}
			}
		}
	}
	if len(b.data)-b.pos/8 > 32 {
		b.invalid()
	}
	for b.err == nil && b.pos < len(b.data)*8 {
		b.expect(8, 255)
	}
	return syntaxError(&b, "PES-optional-header")
}

// Validate the complete original bounded PES header before either producer
// discards it. Marker headers are borrowed and never retained by the collector.
func validatePESHeader(data []byte) error {
	if len(data) < 9 || len(data) != 9+int(data[8]) || !bytes.Equal(data[:3], []byte{0, 0, 1}) || data[6]&0xc0 != 0x80 {
		return fieldError("PES-fixed-header", ErrInvalidBitstream)
	}
	if (data[3] < 0xe0 || data[3] > 0xef) && data[3] != 0xfd || data[6]&0x30 != 0 {
		return fieldError("PES-stream-ID/scrambling", ErrUnsupportedInput)
	}
	flags := data[7] >> 6
	if flags == 1 || flags == 2 && (len(data) < 14 || !validPESTimestamp(data[9:14], 2)) || flags == 3 && (len(data) < 19 || !validPESTimestamp(data[9:14], 3) || !validPESTimestamp(data[14:19], 1)) {
		return fieldError("PES-timestamp", ErrInvalidBitstream)
	}
	length := int(binary.BigEndian.Uint16(data[4:6]))
	if length != 0 && length < len(data)-6 {
		return fieldError("PES-declared-length", ErrInvalidBitstream)
	}
	return validatePESOptional(data)
}

func (f *transportFile) pesHeader() error {
	p := &f.pes
	data := p.header[:p.need]
	if data[3] == 0xbe {
		p.padding = true
		p.remaining = int(binary.BigEndian.Uint16(data[4:6]))
		p.decoded = true
		return nil
	}
	if err := validatePESHeader(data); err != nil {
		return err
	}
	length := int(binary.BigEndian.Uint16(data[4:6]))
	p.unbounded = length == 0
	p.remaining = length - (p.need - 6)
	// Decode only the optional PES header through the dependency. An artificial
	// one-byte body and length make this bounded record immediately complete.
	control := append(slices.Clone(data), 0xff)
	binary.BigEndian.PutUint16(control[4:6], uint16(len(control)-6))
	decoded, err := decodeTransportControl(f.ctx, control, false)
	if err != nil {
		return err
	}
	if decoded.PES == nil || decoded.PES.Header == nil || decoded.PES.Header.OptionalHeader == nil {
		return ErrInvalidBitstream
	}
	h := decoded.PES.Header.OptionalHeader
	marker := Marker{ESOffset: f.es, Kind: PESStart, Modulo33: true, PacketIndex: p.packet, SourceOffset: p.packet * uint64(f.reader.size), HasSourcePosition: true}
	if h.PTS != nil {
		marker.PTS = Timestamp{Value: h.PTS.Base, Timescale: 90000, Valid: true}
	}
	if h.DTS != nil {
		marker.DTS = Timestamp{Value: h.DTS.Base, Timescale: 90000, Valid: true}
	}
	if err := f.stream.Push(f.ctx, Chunk{ESOffset: f.es, Markers: []Marker{marker}}); err != nil {
		return err
	}
	p.decoded = true
	return nil
}
func (f *transportFile) video(p *astits.Packet) error {
	state := &f.pes
	data := p.Payload
	if p.Header.PayloadUnitStartIndicator {
		if state.active && (!state.decoded || !state.unbounded && state.remaining != 0) {
			return fieldError("unfinished-PES", errors.Join(ErrInvalidBitstream, ErrIncomplete))
		}
		*state = transportPES{active: true, need: 6, packet: f.reader.index - 1}
	}
	if !state.active {
		for _, b := range data {
			if b != 0xff {
				return fieldError("PES-without-start", ErrIncomplete)
			}
		}
		return nil
	}
	for len(data) > 0 {
		if !state.decoded {
			amount := min(len(data), state.need-state.length)
			copy(state.header[state.length:], data[:amount])
			state.length += amount
			data = data[amount:]
			if state.length != state.need {
				continue
			}
			if state.need == 6 {
				if !bytes.Equal(state.header[:3], []byte{0, 0, 1}) {
					return fieldError("PES-prefix", ErrInvalidBitstream)
				}
				if state.header[3] != 0xbe {
					state.need = 9
					continue
				}
			} else if state.need == 9 && state.header[8] != 0 {
				state.need = 9 + int(state.header[8])
				continue
			}
			if err := f.pesHeader(); err != nil {
				return err
			}
		}
		amount := len(data)
		if !state.unbounded {
			amount = min(amount, state.remaining)
		}
		if amount > 0 && !state.padding {
			if uint64(amount) > ^uint64(0)-f.es {
				return ErrResourceLimit
			}
			if err := f.stream.Push(f.ctx, Chunk{ESOffset: f.es, Data: data[:amount]}); err != nil {
				return err
			}
			f.es += uint64(amount)
		}
		data = data[amount:]
		if !state.unbounded {
			state.remaining -= amount
			if state.remaining == 0 {
				state.active = false
				for _, b := range data {
					if b != 0xff {
						return fieldError("PES-padding", ErrInvalidBitstream)
					}
				}
				return nil
			}
		}
	}
	return nil
}
func extractTransport(ctx context.Context, r io.ReadSeeker, opts Options, l Limits) (result *Extraction, err error) {
	if opts.TrackID >= 0x1fff {
		return nil, fieldError("transport-PID", ErrUnsupportedInput)
	}
	memory := &accounting{max: l.MaxRetainedBytes, shared: opts.Budget}
	defer memory.release()
	if _, err := memory.reserve(32768); err != nil {
		return nil, err
	}
	size := 188
	if opts.Format == M2TS {
		size = 192
	}
	reader := &transportReader{ctx: ctx, r: r, size: size}
	f := &transportFile{ctx: ctx, l: l, memory: memory, opts: opts, reader: reader, states: make(map[uint16]*transportPID), programs: make(map[uint16]uint16), pmts: make(map[uint16][]byte), candidates: make(map[uint16]uint16)}
	defer func() {
		if err != nil {
			err = &Error{Container: map[bool]string{true: "M2TS", false: "TS"}[size == 192], Stream: StreamKey{TrackID: uint64(f.selected)}, Offset: (reader.index - 1) * uint64(size), OffsetKnown: reader.index != 0, Field: "transport", Err: err}
		}
	}()
	d := astits.NewDemuxer(ctx, reader, astits.DemuxerOptPacketSize(188))
	for {
		packet, readErr := d.NextPacket()
		if reader.readErr != nil {
			return nil, reader.readErr
		}
		if errors.Is(readErr, astits.ErrNoMorePackets) {
			break
		}
		if readErr != nil {
			return nil, fieldError("packet", errors.Join(ErrInvalidBitstream, readErr))
		}
		pid := packet.Header.PID
		if pid != 0 && f.programs[pid] == 0 && (f.stream == nil || pid != f.selected) {
			if packet.Header.HasPayload && packet.Header.PayloadUnitStartIndicator {
				f.early[pid] = true
			}
			continue
		}
		if packet.Header.HasAdaptationField && packet.AdaptationField == nil || !packet.Header.HasPayload && !packet.Header.HasAdaptationField {
			return nil, ErrInvalidBitstream
		}
		state, e := f.state(pid)
		if e != nil {
			return nil, e
		}
		skip, e := f.continuity(packet, state)
		if e != nil {
			return nil, e
		}
		if skip {
			continue
		}
		if pid == 0 || f.programs[pid] != 0 {
			e = f.psi(packet, state)
		} else {
			e = f.video(packet)
		}
		if e != nil {
			return nil, e
		}
	}
	for _, state := range f.states {
		if state.length != 0 {
			return nil, fieldError("final-PSI-section", errors.Join(ErrInvalidBitstream, ErrIncomplete))
		}
	}
	if f.stream == nil || !f.pat || len(f.pmts) != len(f.programs) {
		return nil, fieldError("program-map/HEVC-selection", ErrUnsupportedInput)
	}
	if f.pes.active && (!f.pes.decoded || !f.pes.unbounded && f.pes.remaining != 0) {
		return nil, fieldError("final-PES", errors.Join(ErrInvalidBitstream, ErrIncomplete))
	}
	return finishFile(ctx, f.stream, l, opts.Budget)
}
