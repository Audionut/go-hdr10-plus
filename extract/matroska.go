package extract

// Matroska/EBML envelope and timestamps follow RFC 8794 and RFC 9559.
// ebml-go supplies block and all four lacing decoders; traversal stays synchronous.
import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math"
	"math/big"

	ebml "github.com/at-wat/ebml-go"
)

const (
	mkvHeader  = 0x1a45dfa3
	mkvSegment = 0x18538067
	mkvInfo    = 0x1549a966
	mkvTracks  = 0x1654ae6b
	mkvCluster = 0x1f43b675
)

type mkvElement struct {
	id               uint64
	start, body, end int64
	unknown          bool
	priorCRC         uint32
}
type mkvScope struct {
	crc, want       uint32
	checksum, child bool
}
type mkvCursor struct {
	ctx                   context.Context
	r                     io.ReadSeeker
	pos, size             int64
	limits                Limits
	memory                *accounting
	scopes                []mkvScope
	scopeCharge           *charge
	pending               *mkvElement
	discovery, excludeCRC bool
	readErr               error
	buffer                []byte
	readBuffer            []byte
	readStart             int64
	readLength            int
	bufferErr             error
	discoveryEnd          int64
}

func (c *mkvCursor) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if c.readErr != nil {
		return 0, c.readErr
	}
	if len(p) > 32768 {
		p = p[:32768]
	}
	if len(p) == 0 {
		return 0, nil
	}
	if c.pos < c.readStart || c.pos >= c.readStart+int64(c.readLength) {
		c.readStart = c.pos
		length := len(c.readBuffer)
		if c.discovery {
			// Only known metadata bodies are safe to read ahead. Element
			// headers can precede a video body that discovery must not read.
			length = len(p)
			if c.pos < c.discoveryEnd {
				length = int(min(int64(len(c.readBuffer)), c.discoveryEnd-c.pos))
			}
		}
		c.readLength, c.bufferErr = c.r.Read(c.readBuffer[:length])
		if c.readLength == 0 && c.bufferErr == nil {
			c.bufferErr = io.ErrNoProgress
		}
		if c.bufferErr != nil && c.bufferErr != io.EOF {
			// A read error must survive even if read-ahead supplied all bytes
			// requested by io.ReadFull or the parser subsequently skips them.
			c.readErr = c.bufferErr
		}
	}
	n := copy(p, c.readBuffer[int(c.pos-c.readStart):c.readLength])
	err := c.readErr
	if err == nil && c.pos+int64(n) == c.readStart+int64(c.readLength) {
		err = c.bufferErr
	}
	c.pos += int64(n)
	if n > 0 && !c.discovery {
		for i := range c.scopes {
			if c.scopes[i].checksum && !(c.excludeCRC && i == len(c.scopes)-1) {
				c.scopes[i].crc = crc32.Update(c.scopes[i].crc, crc32.IEEETable, p[:n])
			}
		}
	}
	return n, err
}

func (c *mkvCursor) seek(position int64) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if c.readErr != nil {
		return c.readErr
	}
	if position >= c.readStart && position <= c.readStart+int64(c.readLength) {
		c.pos = position
		return nil
	}
	pos, err := c.r.Seek(position, io.SeekStart)
	if err != nil {
		return err
	}
	if pos != position {
		return io.ErrUnexpectedEOF
	}
	c.pos, c.readStart, c.readLength, c.bufferErr = pos, pos, 0, nil
	return nil
}
func (c *mkvCursor) vint(id bool) (uint64, int, bool, error) {
	var b [8]byte
	if _, err := io.ReadFull(c, b[:1]); err != nil {
		return 0, 0, false, err
	}
	n := 1
	marker := byte(0x80)
	for marker != 0 && b[0]&marker == 0 {
		marker >>= 1
		n++
	}
	if marker == 0 || id && n > 4 {
		return 0, 0, false, ErrInvalidBitstream
	}
	if _, err := io.ReadFull(c, b[1:n]); err != nil {
		return 0, 0, false, err
	}
	v := uint64(b[0] &^ marker)
	if id {
		v = uint64(b[0])
	}
	for _, x := range b[1:n] {
		v = v<<8 | uint64(x)
	}
	unknown := !id && v == (uint64(1)<<(7*n))-1
	return v, n, unknown, nil
}
func (c *mkvCursor) next(end int64) (mkvElement, error) {
	for {
		if err := c.ctx.Err(); err != nil {
			return mkvElement{}, err
		}
		if c.readErr != nil {
			return mkvElement{}, c.readErr
		}
		if c.pending != nil {
			e := *c.pending
			c.pending = nil
			if len(c.scopes) > 0 {
				c.scopes[len(c.scopes)-1].child = true
			}
			return e, nil
		}
		if c.pos == end {
			return mkvElement{}, io.EOF
		}
		if c.pos > end {
			return mkvElement{}, ErrInvalidBitstream
		}
		e := mkvElement{start: c.pos}
		if len(c.scopes) > 0 {
			e.priorCRC = c.scopes[len(c.scopes)-1].crc
		}
		id, _, _, err := c.vint(true)
		if err != nil {
			return e, containerReadError("EBML-ID", err)
		}
		e.id = id
		size, _, unknown, err := c.vint(false)
		if err != nil {
			return e, containerReadError("EBML-size", err)
		}
		e.body = c.pos
		e.unknown = unknown
		if unknown {
			if id != mkvSegment && id != mkvCluster {
				return e, fieldError("unknown-element-size", ErrInvalidBitstream)
			}
			e.end = end
		} else {
			if c.pos > end || size > uint64(end-c.pos) {
				return e, fieldError("EBML-extent", errors.Join(ErrInvalidBitstream, ErrIncomplete))
			}
			e.end = c.pos + int64(size)
		}
		if id == 0xbf {
			if len(c.scopes) == 0 || e.unknown || e.end-e.body != 4 {
				return e, fieldError("EBML-CRC", ErrInvalidBitstream)
			}
			parent := &c.scopes[len(c.scopes)-1]
			if parent.checksum || parent.child {
				return e, fieldError("EBML-CRC-position", ErrInvalidBitstream)
			}
			parent.crc = e.priorCRC
			c.excludeCRC = true
			var data [4]byte
			_, err := io.ReadFull(c, data[:])
			c.excludeCRC = false
			if err != nil {
				return e, containerReadError("EBML-CRC", err)
			}
			parent.want = binary.LittleEndian.Uint32(data[:])
			parent.checksum = true
			continue
		}
		if len(c.scopes) > 0 {
			c.scopes[len(c.scopes)-1].child = true
		}
		return e, nil
	}
}
func (c *mkvCursor) enter() error {
	if int64(len(c.scopes)) >= c.limits.MaxNestingDepth {
		return fieldError("EBML-depth", ErrResourceLimit)
	}
	if len(c.scopes) == cap(c.scopes) {
		capacity := max(8, 2*cap(c.scopes))
		token, err := c.memory.reserve(int64(capacity) * 16)
		if err != nil {
			return err
		}
		data := make([]mkvScope, len(c.scopes), capacity)
		copy(data, c.scopes)
		c.scopes = data
		c.scopeCharge.release()
		c.scopeCharge = token
	}
	c.scopes = append(c.scopes, mkvScope{})
	return nil
}
func (c *mkvCursor) root() (mkvElement, error) {
	for {
		e, err := c.next(c.size)
		if err != nil || e.id != 0xec {
			return e, err
		}
		if err := c.discard(e.end); err != nil {
			return e, err
		}
	}
}
func (c *mkvCursor) leave() error {
	scope := c.scopes[len(c.scopes)-1]
	c.scopes = c.scopes[:len(c.scopes)-1]
	if !c.discovery && scope.checksum && scope.crc != scope.want {
		return fieldError("EBML-CRC-mismatch", ErrInvalidBitstream)
	}
	return nil
}
func (c *mkvCursor) discard(end int64) error {
	if end < c.pos || end > c.size {
		return fieldError("EBML-extent", ErrInvalidBitstream)
	}
	checksumming := false
	for _, s := range c.scopes {
		checksumming = checksumming || s.checksum && !c.discovery
	}
	if !checksumming {
		return c.seek(end)
	}
	for c.pos < end {
		n := min(int64(len(c.buffer)), end-c.pos)
		if _, err := io.ReadFull(c, c.buffer[:n]); err != nil {
			return containerReadError("EBML-data", err)
		}
	}
	return nil
}
func mkvMaster(id uint64) bool {
	switch id {
	case mkvHeader, mkvSegment, mkvInfo, mkvTracks, mkvCluster, 0xae, 0xe0, 0xe1, 0x6d80, 0x6240, 0x5034, 0x5035, 0xa0, 0x1c53bb6b, 0xbb, 0xb7, 0x114d9b74, 0x4dbb, 0x1043a770, 0x45b9, 0xb6, 0x8f, 0x80, 0x6944, 0x6911, 0x1254c367, 0x7373, 0x63c0, 0x67c8, 0x1941a469, 0x61a7, 0x75a1, 0xa6, 0xe2, 0xe3, 0xe4, 0xe9, 0x5854, 0x55b0, 0x55d0, 0x7670:
		return true
	}
	return false
}
func (c *mkvCursor) skip(e mkvElement) error {
	if e.id == 0x45dd {
		value, err := c.uint(e)
		if err != nil {
			return err
		}
		if value != 0 {
			return fieldError("ordered-chapters", ErrUnsupportedInput)
		}
		return nil
	}
	if !mkvMaster(e.id) {
		return c.discard(e.end)
	}
	if err := c.enter(); err != nil {
		return err
	}
	for {
		child, err := c.next(e.end)
		if err == io.EOF {
			return c.leave()
		}
		if err != nil {
			return err
		}
		if e.unknown && e.id == mkvCluster && mkvLevelOne(child.id) {
			c.scopes[len(c.scopes)-1].crc = child.priorCRC
			c.pending = &child
			return c.leave()
		}
		if err := c.skip(child); err != nil {
			return err
		}
	}
}
func mkvLevelOne(id uint64) bool {
	switch id {
	case mkvInfo, mkvTracks, mkvCluster, 0x114d9b74, 0x1c53bb6b, 0x1043a770, 0x1254c367, 0x1941a469, mkvHeader, mkvSegment:
		return true
	}
	return false
}
func (c *mkvCursor) uint(e mkvElement) (uint64, error) {
	n := e.end - e.body
	if e.unknown || n < 0 || n > 8 {
		return 0, fieldError("EBML-integer-size", ErrInvalidBitstream)
	}
	var data [8]byte
	if _, err := io.ReadFull(c, data[8-n:]); err != nil {
		return 0, containerReadError("EBML-integer", err)
	}
	return binary.BigEndian.Uint64(data[:]), nil
}
func (c *mkvCursor) text(e mkvElement) (string, error) {
	n := e.end - e.body
	if e.unknown || n > 64 {
		return "", fieldError("EBML-string", ErrUnsupportedInput)
	}
	var data [64]byte
	if _, err := io.ReadFull(c, data[:n]); err != nil {
		return "", containerReadError("EBML-string", err)
	}
	value := data[:n]
	if zero := bytes.IndexByte(value, 0); zero >= 0 {
		value = value[:zero]
	}
	return string(value), nil
}
func containerReadError(field string, err error) error {
	if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
		return fieldError(field, errors.Join(ErrInvalidBitstream, ErrIncomplete, err))
	}
	return fieldError(field, err)
}

type mkvTrack struct {
	number, kind, duration, delay uint64
	offset                        int64
	hevc, encoded                 bool
	lacing                        bool
	scale                         float64
	config                        []byte
	configCharge                  *charge
}
type mkvFile struct {
	cursor           *mkvCursor
	tracks           []mkvTrack
	trackNumbers     map[uint64]struct{}
	trackCharge      *charge
	indexBytes       int64
	scale            uint64
	info, trackTable bool
	selected         *mkvTrack
	stream           *Stream
	esOffset         uint64
}

func (f *mkvFile) header(e mkvElement) error {
	c := f.cursor
	if e.id != mkvHeader || e.unknown {
		return fieldError("Matroska-header", ErrInvalidBitstream)
	}
	if err := c.enter(); err != nil {
		return err
	}
	doc := false
	for {
		child, err := c.next(e.end)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch child.id {
		case 0x4282:
			value, err := c.text(child)
			if err != nil {
				return err
			}
			if value != "matroska" {
				return fieldError("DocType", ErrUnsupportedInput)
			}
			doc = true
		case 0x42f7, 0x42f2, 0x42f3, 0x4285:
			value, err := c.uint(child)
			if err != nil {
				return err
			}
			limit := uint64(1)
			switch child.id {
			case 0x42f2, 0x4285:
				limit = 4
			case 0x42f3:
				limit = 8
			}
			if value == 0 || value > limit {
				return fieldError("EBML-version", ErrUnsupportedInput)
			}
		default:
			if err := c.skip(child); err != nil {
				return err
			}
		}
	}
	if !doc {
		return fieldError("DocType", ErrInvalidBitstream)
	}
	return c.leave()
}
func (f *mkvFile) parseInfo(e mkvElement) error {
	c := f.cursor
	if f.info {
		return fieldError("duplicate-SegmentInfo", ErrInvalidBitstream)
	}
	f.info = true
	if err := c.enter(); err != nil {
		return err
	}
	for {
		child, err := c.next(e.end)
		if err == io.EOF {
			return c.leave()
		}
		if err != nil {
			return err
		}
		switch child.id {
		case 0x2ad7b1:
			f.scale, err = c.uint(child)
			if err != nil {
				return err
			}
			if f.scale == 0 {
				return fieldError("TimestampScale", ErrInvalidBitstream)
			}
		case 0x3cb923, 0x3eb923, 0x4444:
			return fieldError("linked-segments", ErrUnsupportedInput)
		default:
			if err := c.skip(child); err != nil {
				return err
			}
		}
	}
}
func (f *mkvFile) parseTracks(e mkvElement) error {
	c := f.cursor
	if f.trackTable {
		return fieldError("duplicate-Tracks", ErrInvalidBitstream)
	}
	f.trackTable = true
	if err := c.enter(); err != nil {
		return err
	}
	for {
		child, err := c.next(e.end)
		if err == io.EOF {
			return c.leave()
		}
		if err != nil {
			return err
		}
		if child.id != 0xae {
			if err := c.skip(child); err != nil {
				return err
			}
			continue
		}
		if f.indexBytes > c.limits.MaxContainerIndexBytes-256 {
			return fieldError("Matroska-index", ErrResourceLimit)
		}
		f.indexBytes += 256
		if _, err := c.memory.reserve(256); err != nil {
			return err
		}
		t := mkvTrack{lacing: true, scale: 1}
		if err := c.enter(); err != nil {
			return err
		}
		for {
			value, err := c.next(child.end)
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			switch value.id {
			case 0xd7, 0x83, 0x23e383, 0x56aa, 0x9c:
				v, err := c.uint(value)
				if err != nil {
					return err
				}
				switch value.id {
				case 0xd7:
					t.number = v
				case 0x83:
					t.kind = v
				case 0x23e383:
					t.duration = v
				case 0x56aa:
					t.delay = v
				case 0x9c:
					if v > 1 {
						return fieldError("FlagLacing", ErrInvalidBitstream)
					}
					t.lacing = v == 1
				}
			case 0x86:
				codec, err := c.text(value)
				if err != nil {
					return err
				}
				t.hevc = codec == "V_MPEGH/ISO/HEVC"
			case 0x63a2:
				if t.configCharge != nil {
					return fieldError("duplicate-CodecPrivate", ErrInvalidBitstream)
				}
				n := value.end - value.body
				if n > c.limits.MaxContainerIndexBytes-f.indexBytes {
					return fieldError("CodecPrivate", ErrResourceLimit)
				}
				f.indexBytes += n
				token, err := c.memory.reserve(n)
				if err != nil {
					return err
				}
				t.configCharge = token
				t.config = make([]byte, n)
				if _, err := io.ReadFull(c, t.config); err != nil {
					return containerReadError("CodecPrivate", err)
				}
			case 0x23314f:
				n := value.end - value.body
				if n != 4 && n != 8 {
					return fieldError("TrackTimestampScale", ErrInvalidBitstream)
				}
				bits, err := c.uint(value)
				if err != nil {
					return err
				}
				if n == 4 {
					t.scale = float64(math.Float32frombits(uint32(bits)))
				} else {
					t.scale = math.Float64frombits(bits)
				}
			case 0x537f:
				n := value.end - value.body
				if n < 1 || n > 8 {
					return fieldError("TrackOffset", ErrInvalidBitstream)
				}
				v, err := c.uint(value)
				if err != nil {
					return err
				}
				if n < 8 && v&(uint64(1)<<(n*8-1)) != 0 {
					v |= math.MaxUint64 << uint(n*8)
				}
				t.offset = int64(v)
			case 0x6d80:
				t.encoded = true
				if err := c.skip(value); err != nil {
					return err
				}
			default:
				if err := c.skip(value); err != nil {
					return err
				}
			}
		}
		if err := c.leave(); err != nil {
			return err
		}
		if t.number == 0 || t.kind == 0 {
			return fieldError("TrackEntry", ErrInvalidBitstream)
		}
		if _, exists := f.trackNumbers[t.number]; exists {
			return fieldError("duplicate-TrackNumber", ErrInvalidBitstream)
		}
		f.trackNumbers[t.number] = struct{}{}
		if len(f.tracks) == cap(f.tracks) {
			oldCapacity := cap(f.tracks)
			capacity := max(8, 2*cap(f.tracks))
			growth := int64(capacity) * 128
			if growth > c.limits.MaxContainerIndexBytes-f.indexBytes {
				return fieldError("Matroska-index", ErrResourceLimit)
			}
			token, err := c.memory.reserve(int64(capacity) * 128)
			if err != nil {
				return err
			}
			data := make([]mkvTrack, len(f.tracks), capacity)
			copy(data, f.tracks)
			f.tracks = data
			f.indexBytes += growth - int64(oldCapacity)*128
			f.trackCharge.release()
			f.trackCharge = token
		}
		f.tracks = append(f.tracks, t)
	}
}
func (f *mkvFile) selectTrack(id uint64) error {
	for i := range f.tracks {
		t := &f.tracks[i]
		if t.hevc && t.kind == 1 && (id == 0 || id == t.number) {
			if f.selected != nil {
				return ErrAmbiguousTrack
			}
			f.selected = t
		}
	}
	if f.selected == nil {
		return fieldError("HEVC-track", ErrUnsupportedInput)
	}
	if f.selected.encoded || f.selected.scale != 1 {
		return fieldError("track-encoding/timing", ErrUnsupportedInput)
	}
	if len(f.selected.config) < 23 || f.selected.config[0] != 1 {
		return fieldError("HEVC-CodecPrivate", ErrInvalidBitstream)
	}
	return nil
}

func (f *mkvFile) segment(e mkvElement, collect bool) error {
	c := f.cursor
	if e.id != mkvSegment {
		return fieldError("Matroska-Segment", ErrInvalidBitstream)
	}
	if err := c.enter(); err != nil {
		return err
	}
	for {
		child, err := c.next(e.end)
		if err == io.EOF {
			return c.leave()
		}
		if err != nil {
			return err
		}
		if !collect {
			c.discoveryEnd = c.pos
			if child.id != mkvCluster {
				c.discoveryEnd = child.end
			}
		}
		switch child.id {
		case mkvInfo:
			if !collect {
				err = f.parseInfo(child)
			} else {
				err = c.skip(child)
			}
		case mkvTracks:
			if !collect {
				err = f.parseTracks(child)
			} else {
				err = c.skip(child)
			}
		case mkvCluster:
			if collect {
				err = f.cluster(child)
			} else if !child.unknown {
				// Collection validates every cluster below; discovery only
				// needs to find Info/Tracks, without touching video bytes.
				err = c.discard(child.end)
			} else {
				err = c.skip(child)
			}
		case mkvSegment, mkvHeader:
			return fieldError("multiple-segments", ErrUnsupportedInput)
		default:
			err = c.skip(child)
		}
		if err != nil {
			return err
		}
	}
}

func (f *mkvFile) block(e mkvElement) (*ebml.Block, *charge, error) {
	c := f.cursor
	// Decode only the small original block prefix before deciding whether the
	// dependency may allocate selected laced frames.
	var prefix [11]byte
	n := 0
	if e.end-e.body < 4 {
		return nil, nil, fieldError("Block-header", ErrInvalidBitstream)
	}
	if _, err := io.ReadFull(c, prefix[:1]); err != nil {
		return nil, nil, containerReadError("Block", err)
	}
	n++
	marker := byte(0x80)
	width := 1
	for marker != 0 && prefix[0]&marker == 0 {
		marker >>= 1
		width++
	}
	if marker == 0 || e.end-e.body < int64(width+3) {
		return nil, nil, fieldError("Block-header", ErrInvalidBitstream)
	}
	if _, err := io.ReadFull(c, prefix[1:width+3]); err != nil {
		return nil, nil, containerReadError("Block", err)
	}
	n = width + 3
	number := uint64(prefix[0] &^ marker)
	for _, v := range prefix[1:width] {
		number = number<<8 | uint64(v)
	}
	if _, known := f.trackNumbers[number]; !known {
		return nil, nil, fieldError("Block-track", ErrInvalidBitstream)
	}
	flags := prefix[n-1]
	if flags&0x70 != 0 || e.id == 0xa1 && flags&0x81 != 0 {
		return nil, nil, fieldError("Block-flags", ErrInvalidBitstream)
	}
	if number != f.selected.number {
		return nil, nil, c.discard(e.end)
	}
	if !f.selected.lacing && flags&6 != 0 {
		return nil, nil, fieldError("forbidden-lacing", ErrInvalidBitstream)
	}
	size := e.end - e.body
	if size > c.limits.MaxBlockBytes || size > math.MaxInt64-32768 {
		return nil, nil, fieldError("Block-bytes", ErrResourceLimit)
	}
	if size > math.MaxInt64/2-32768 || size > int64(math.MaxInt) {
		return nil, nil, ErrResourceLimit
	}
	token, err := c.memory.reserve(2*size + 32768)
	if err != nil {
		return nil, nil, err
	}
	rest := &io.LimitedReader{R: c, N: e.end - c.pos}
	data := make([]byte, size)
	copy(data, prefix[:n])
	if _, err := io.ReadFull(rest, data[n:]); err != nil {
		token.release()
		return nil, nil, containerReadError("Block", err)
	}
	if err := preflightLace(data[n:], flags>>1&3); err != nil {
		token.release()
		return nil, nil, err
	}
	block, err := ebml.UnmarshalBlock(bytes.NewReader(data), size)
	if err != nil {
		token.release()
		return nil, nil, containerReadError("Block-lacing", errors.Join(ErrInvalidBitstream, err))
	}
	if rest.N != 0 || c.readErr != nil {
		token.release()
		if c.readErr != nil {
			return nil, nil, c.readErr
		}
		return nil, nil, fieldError("Block-extent", ErrInvalidBitstream)
	}
	if block.Lacing != ebml.LacingNo && len(block.Data) < 2 {
		token.release()
		return nil, nil, fieldError("single-frame-lacing", ErrInvalidBitstream)
	}
	return block, token, nil
}
func (f *mkvFile) cluster(e mkvElement) error {
	c := f.cursor
	if err := c.enter(); err != nil {
		return err
	}
	var timestamp uint64
	hasTime := false
	for {
		child, err := c.next(e.end)
		if err == io.EOF {
			if !hasTime {
				return fieldError("missing-ClusterTimestamp", ErrInvalidBitstream)
			}
			return c.leave()
		}
		if err != nil {
			return err
		}
		if e.unknown && mkvLevelOne(child.id) {
			if !hasTime {
				return fieldError("missing-ClusterTimestamp", ErrInvalidBitstream)
			}
			c.scopes[len(c.scopes)-1].crc = child.priorCRC
			c.pending = &child
			return c.leave()
		}
		switch child.id {
		case 0xe7:
			if hasTime {
				return fieldError("duplicate-ClusterTimestamp", ErrInvalidBitstream)
			}
			timestamp, err = c.uint(child)
			hasTime = true
		case 0xa3:
			if !hasTime {
				return fieldError("ClusterTimestamp-order", ErrUnsupportedInput)
			}
			var block *ebml.Block
			var token *charge
			block, token, err = f.block(child)
			if err == nil && block != nil {
				err = f.pushBlock(block, timestamp, 0, false)
			}
			token.release()
		case 0xa0:
			if !hasTime {
				return fieldError("ClusterTimestamp-order", ErrUnsupportedInput)
			}
			err = f.blockGroup(child, timestamp)
		default:
			err = c.skip(child)
		}
		if err != nil {
			return err
		}
	}
}
func (f *mkvFile) blockGroup(e mkvElement, timestamp uint64) error {
	c := f.cursor
	if err := c.enter(); err != nil {
		return err
	}
	var block *ebml.Block
	var token *charge
	var duration uint64
	hasDuration, hasBlock, codecState, padding := false, false, false, false
	references, zeroReferences := 0, 0
	defer func() { token.release() }()
	for {
		child, err := c.next(e.end)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch child.id {
		case 0xa1:
			if hasBlock {
				return fieldError("duplicate-Block", ErrInvalidBitstream)
			}
			hasBlock = true
			block, token, err = f.block(child)
		case 0x9b:
			if hasDuration {
				return fieldError("duplicate-BlockDuration", ErrInvalidBitstream)
			}
			hasDuration = true
			duration, err = c.uint(child)
		case 0xa4:
			codecState = true
			err = c.discard(child.end)
		case 0x75a2:
			value, readErr := c.uint(child)
			err = readErr
			padding = value != 0
		case 0xfb:
			if child.end-child.body < 1 {
				return fieldError("ReferenceBlock", ErrInvalidBitstream)
			}
			value, readErr := c.uint(child)
			err = readErr
			references++
			if value == 0 {
				zeroReferences++
			}
		default:
			err = c.skip(child)
		}
		if err != nil {
			return err
		}
	}
	if !hasBlock {
		return fieldError("missing-Block", ErrInvalidBitstream)
	}
	if zeroReferences != 0 && references != 1 {
		return fieldError("ReferenceBlock-zero", ErrInvalidBitstream)
	}
	if err := c.leave(); err != nil {
		return err
	}
	if block != nil {
		if f.selected.duration != 0 && !hasDuration {
			return fieldError("missing-BlockDuration", ErrInvalidBitstream)
		}
		if codecState || padding {
			return fieldError("CodecState/DiscardPadding", ErrUnsupportedInput)
		}
		return f.pushBlock(block, timestamp, duration, hasDuration)
	}
	return nil
}

func timestampRat(value *big.Rat) (Timestamp, error) {
	if !value.Num().IsInt64() || !value.Denom().IsUint64() {
		return Timestamp{}, fieldError("timing-range", ErrUnsupportedInput)
	}
	return Timestamp{Value: value.Num().Int64(), Timescale: value.Denom().Uint64(), Valid: true}, nil
}
func (f *mkvFile) pushBlock(b *ebml.Block, cluster, duration uint64, hasDuration bool) error {
	if len(b.Data) == 0 {
		return fieldError("empty-Block", ErrInvalidBitstream)
	}
	if len(b.Data) > 1 && !hasDuration && f.selected.duration == 0 {
		return fieldError("missing-lace-timing", ErrUnsupportedInput)
	}
	if hasDuration && duration == 0 {
		return fieldError("BlockDuration", ErrInvalidBitstream)
	}
	var ticks, scale, ns, delay, offset, den big.Int
	ticks.SetUint64(cluster)
	offset.SetInt64(int64(b.Timecode))
	ticks.Add(&ticks, &offset)
	scale.SetUint64(f.scale)
	ns.Mul(&ticks, &scale)
	delay.SetUint64(f.selected.delay)
	ns.Sub(&ns, &delay)
	offset.SetInt64(f.selected.offset)
	ns.Add(&ns, &offset)
	den.SetInt64(1000000000)
	var base big.Rat
	base.SetFrac(&ns, &den)
	for i, frame := range b.Data {
		if err := f.cursor.ctx.Err(); err != nil {
			return err
		}
		var step, num, divisor big.Int
		var stamp, advance big.Rat
		if hasDuration {
			step.SetUint64(duration)
			step.Mul(&step, &scale)
			divisor.SetInt64(int64(len(b.Data)) * 1000000000)
		} else {
			step.SetUint64(f.selected.duration)
			divisor.SetInt64(1000000000)
		}
		num.SetInt64(int64(i))
		num.Mul(&num, &step)
		advance.SetFrac(&num, &divisor)
		stamp.Add(&base, &advance)
		pts, err := timestampRat(&stamp)
		if err != nil {
			return err
		}
		if uint64(len(frame)) > math.MaxUint64-f.esOffset {
			return ErrResourceLimit
		}
		marker := Marker{ESOffset: f.esOffset, Kind: SampleStart, PTS: pts, SuppressOutput: b.Invisible}
		if err := f.stream.Push(f.cursor.ctx, Chunk{Data: frame, ESOffset: f.esOffset, Markers: []Marker{marker}}); err != nil {
			return err
		}
		f.esOffset += uint64(len(frame))
	}
	return nil
}

func extractMatroska(ctx context.Context, r io.ReadSeeker, opts Options, l Limits) (result *Extraction, err error) {
	memory := &accounting{max: l.MaxRetainedBytes, shared: opts.Budget}
	defer memory.release()
	if _, err := memory.reserve(2*32768 + 4096); err != nil {
		return nil, err
	}
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	c := &mkvCursor{ctx: ctx, r: r, pos: start, size: size, limits: l, memory: memory, discovery: true, buffer: make([]byte, 32768), readBuffer: make([]byte, 32768)}
	defer func() {
		// Restore the caller's seek position to the last consumed byte if an
		// error leaves unread look-ahead in the cache.
		if c.readLength > 0 && c.pos != c.readStart+int64(c.readLength) {
			position, seekErr := r.Seek(c.pos, io.SeekStart)
			if seekErr == nil && position != c.pos {
				seekErr = io.ErrUnexpectedEOF
			}
			if seekErr != nil {
				result = nil
				err = errors.Join(err, seekErr)
			}
		}
		if err != nil {
			err = &Error{Container: "Matroska", Offset: uint64(max(0, c.pos)), OffsetKnown: true, Field: "container", Err: err}
		}
	}()
	f := mkvFile{cursor: c, scale: 1000000, trackNumbers: make(map[uint64]struct{})}
	head, err := c.root()
	if err != nil {
		return nil, containerReadError("EBML-header", err)
	}
	if err := f.header(head); err != nil {
		return nil, err
	}
	segment, err := c.root()
	if err != nil {
		return nil, containerReadError("Segment", err)
	}
	if err := f.segment(segment, false); err != nil {
		return nil, err
	}
	if _, err := c.root(); err != io.EOF {
		if err == nil {
			return nil, fieldError("multiple-segments", ErrUnsupportedInput)
		}
		return nil, err
	}
	if !f.info || !f.trackTable {
		return nil, fieldError("missing-Info/Tracks", ErrInvalidBitstream)
	}
	if err := f.selectTrack(opts.TrackID); err != nil {
		return nil, err
	}
	f.stream, err = newStream(StreamOptions{Framing: LengthPrefixed, LengthSize: int(f.selected.config[21]&3) + 1, CodecConfig: f.selected.config, Key: StreamKey{TrackID: f.selected.number}, Limits: l, Budget: opts.Budget}, l, memory)
	if err != nil {
		return nil, err
	}
	for i := range f.tracks {
		f.tracks[i].config = nil
		f.tracks[i].configCharge.release()
	}
	if err := c.seek(start); err != nil {
		return nil, err
	}
	c.pending = nil
	c.scopes = c.scopes[:0]
	c.discovery = false
	head, err = c.root()
	if err != nil {
		return nil, err
	}
	if err := f.header(head); err != nil {
		return nil, err
	}
	segment, err = c.root()
	if err != nil {
		return nil, err
	}
	if err := f.segment(segment, true); err != nil {
		f.stream.Abort(err)
		return nil, err
	}
	if _, err := c.root(); err != io.EOF {
		if err == nil {
			return nil, fieldError("multiple-segments", ErrUnsupportedInput)
		}
		return nil, err
	}
	return finishFile(ctx, f.stream, l, opts.Budget)
}

func finishFile(ctx context.Context, s *Stream, l Limits, budget *MemoryBudget) (*Extraction, error) {
	r, err := resolveFile(ctx, s, l)
	if err != nil {
		return nil, err
	}
	return makeExtraction(ctx, r.output, l, budget, r.metadataSeen, s.memory)
}
func resolveFile(ctx context.Context, s *Stream, l Limits) (*resolver, error) {
	c, err := s.Finish(ctx)
	if err != nil {
		return nil, err
	}
	if s.frames > (l.MaxRetainedBytes-resolutionScratch)/512 {
		return nil, ErrResourceLimit
	}
	if _, err := c.memory.reserve(resolutionScratch + s.frames*512 + pocHistoryScratch); err != nil {
		return nil, err
	}
	r := resolver{first: true, limits: l}
	if err := r.resolve(ctx, c, 0); err != nil {
		return nil, err
	}
	if err := r.complete(); err != nil {
		return nil, err
	}
	return &r, nil
}
