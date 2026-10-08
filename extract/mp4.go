package extract

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/big"
	"slices"

	mp4 "github.com/abema/go-mp4"
)

type mp4Extent struct{ start, end uint64 }
type mp4Sample struct {
	offset            uint64
	size, description uint32
	dts, pts          int64
}
type mp4Track struct {
	id              uint32
	scale           uint32
	video, external bool
	encrypted       bool
	configs         [][]byte
	stsz            *mp4.Stsz
	sizes           []uint32
	stsc            *mp4.Stsc
	stts            *mp4.Stts
	ctts            *mp4.Ctts
	chunks          []uint64
	edits           *mp4.Elst
	samples         []mp4Sample
	seen            map[string]bool
}
type mp4File struct {
	ctx        context.Context
	r          io.ReadSeeker
	size       uint64
	l          Limits
	memory     *accounting
	indexBytes int64
	tracks     []*mp4Track
	media      []mp4Extent
	fragments  []mp4.BoxInfo
	defaults   map[uint32]*mp4.Trex
	movieScale uint32
	readErr    error
}

func (f *mp4File) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	if f.readErr != nil {
		return 0, f.readErr
	}
	n, err := f.r.Read(p)
	if n == 0 && len(p) > 0 && err == nil {
		err = io.ErrNoProgress
	}
	if err != nil && err != io.EOF {
		f.readErr = err
	}
	return n, err
}
func (f *mp4File) Seek(n int64, w int) (int64, error) { return f.r.Seek(n, w) }
func (f *mp4File) index(n int64) error {
	if n < 0 || n > f.l.MaxContainerIndexBytes-f.indexBytes {
		return fieldError("MP4-index", ErrResourceLimit)
	}
	if _, err := f.memory.reserve(n); err != nil {
		return err
	}
	f.indexBytes += n
	return nil
}
func (f *mp4File) box(end uint64) (*mp4.BoxInfo, error) {
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	if uint64(pos) == end {
		return nil, io.EOF
	}
	if pos < 0 || uint64(pos) > end || end-uint64(pos) < 8 {
		return nil, fieldError("MP4-box-header", errors.Join(ErrInvalidBitstream, ErrIncomplete))
	}
	b, err := mp4.ReadBoxInfo(f)
	if err != nil {
		return nil, containerReadError("MP4-box-header", err)
	}
	if b.Size < b.HeaderSize || b.Size > end-b.Offset {
		return nil, fieldError("MP4-box-extent", errors.Join(ErrInvalidBitstream, ErrIncomplete))
	}
	return b, nil
}

// Preflight each variable table before the dependency allocates its entries.
// The parsed backing arrays, reflection scratch and raw payload coexist within
// this conservative index charge. Unknown boxes are skipped without allocation.
func (f *mp4File) decode(b *mp4.BoxInfo) (mp4.IBox, error) {
	n := b.Size - b.HeaderSize
	if n > uint64(f.l.MaxContainerIndexBytes) {
		return nil, ErrResourceLimit
	}
	var prefix [16]byte
	if _, err := b.SeekToPayload(f); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(f, prefix[:min(n, 16)]); err != nil {
		return nil, containerReadError(b.Type.String(), err)
	}
	count, width, base := uint64(0), uint64(0), uint64(0)
	typ := b.Type.String()
	maxVersion := byte(0)
	if typ == "ctts" || typ == "elst" || typ == "trun" || typ == "tfdt" || typ == "mvhd" || typ == "mdhd" || typ == "tkhd" {
		maxVersion = 1
	}
	if n < 4 || prefix[0] > maxVersion {
		return nil, fieldError(typ+"-version", ErrUnsupportedInput)
	}
	switch typ {
	case "stts", "ctts", "stsc", "stco", "co64", "elst", "trun":
		if n < 8 {
			return nil, fieldError(typ, ErrInvalidBitstream)
		}
		count = uint64(binary.BigEndian.Uint32(prefix[4:8]))
		base = 8
		switch typ {
		case "stts", "ctts":
			width = 8
		case "stsc":
			width = 12
		case "stco":
			width = 4
		case "co64":
			width = 8
		case "elst":
			width = 12
			if prefix[0] == 1 {
				width = 20
			}
		case "trun":
			flags := uint32(prefix[1])<<16 | uint32(prefix[2])<<8 | uint32(prefix[3])
			if flags&^uint32(0xf05) != 0 {
				return nil, ErrUnsupportedInput
			}
			if flags&1 != 0 {
				base += 4
			}
			if flags&4 != 0 {
				base += 4
			}
			for _, flag := range []uint32{0x100, 0x200, 0x400, 0x800} {
				if flags&flag != 0 {
					width += 4
				}
			}
		}
	case "stsz":
		if n < 12 {
			return nil, fieldError(typ, ErrInvalidBitstream)
		}
		count = uint64(binary.BigEndian.Uint32(prefix[8:12]))
		base = 12
		if binary.BigEndian.Uint32(prefix[4:8]) == 0 {
			width = 4
		}
	}
	if base != 0 && (base > n || count > uint64(f.l.MaxFrames) || width != 0 && count > (n-base)/width) {
		if count > uint64(f.l.MaxFrames) {
			return nil, ErrResourceLimit
		}
		return nil, fieldError(typ, errors.Join(ErrInvalidBitstream, ErrIncomplete))
	}
	if base != 0 && base+count*width != n {
		return nil, fieldError(typ+"-extent", ErrInvalidBitstream)
	}
	if count > uint64(math.MaxInt64/128) || n > uint64((math.MaxInt64-1024-int64(count)*128)/4) {
		return nil, ErrResourceLimit
	}
	if err := f.index(int64(n)*4 + 1024 + int64(count)*128); err != nil {
		return nil, err
	}
	data := make([]byte, n)
	if _, err := b.SeekToPayload(f); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, containerReadError(typ, err)
	}
	box, read, err := mp4.UnmarshalAny(bytes.NewReader(data), b.Type, n, mp4.Context{})
	if err != nil {
		return nil, fieldError(typ, errors.Join(ErrInvalidBitstream, err))
	}
	if read != n {
		return nil, fieldError(typ+"-extent", ErrInvalidBitstream)
	}
	return box, nil
}
func (f *mp4File) walk(end uint64, depth int64, t *mp4Track) error {
	if depth > f.l.MaxNestingDepth {
		return ErrResourceLimit
	}
	for {
		b, err := f.box(end)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		typ := b.Type.String()
		switch typ {
		case "moov", "mdia", "minf", "stbl", "edts", "mvex":
			err = f.walk(b.Offset+b.Size, depth+1, t)
		case "trak":
			if t != nil {
				return fieldError("nested-trak", ErrInvalidBitstream)
			}
			if err = f.index(1024); err != nil {
				return err
			}
			track := &mp4Track{seen: make(map[string]bool)}
			if err = f.walk(b.Offset+b.Size, depth+1, track); err != nil {
				return err
			}
			if track.id == 0 || track.scale == 0 {
				return fieldError("track-ID/timescale", ErrInvalidBitstream)
			}
			for _, old := range f.tracks {
				if old.id == track.id {
					return fieldError("duplicate-track-ID", ErrInvalidBitstream)
				}
			}
			f.tracks = append(f.tracks, track)
		case "mdat":
			if depth != 0 {
				return fieldError("nested-mdat", ErrInvalidBitstream)
			}
			if err = f.index(64); err != nil {
				return err
			}
			f.media = append(f.media, mp4Extent{b.Offset + b.HeaderSize, b.Offset + b.Size})
		case "moof":
			if depth != 0 {
				return fieldError("nested-moof", ErrInvalidBitstream)
			}
			if err = f.index(128); err != nil {
				return err
			}
			f.fragments = append(f.fragments, *b)
		case "stsd":
			if t == nil || t.seen[typ] {
				return fieldError("stsd-location", ErrInvalidBitstream)
			}
			t.seen[typ] = true
			err = f.descriptions(b, t, depth+1)
		case "stz2":
			if t == nil || t.seen["stsz"] {
				return ErrInvalidBitstream
			}
			t.seen["stsz"] = true
			err = f.compactSizes(b, t)
		case "dref":
			if t == nil {
				return ErrInvalidBitstream
			}
			var data [8]byte
			if b.Size-b.HeaderSize < 8 {
				return ErrInvalidBitstream
			}
			if _, err = io.ReadFull(f, data[:]); err != nil {
				return err
			}
			if binary.BigEndian.Uint32(data[4:]) != 1 {
				return ErrUnsupportedInput
			}
			child, e := f.box(b.Offset + b.Size)
			if e != nil {
				return e
			}
			var flags [4]byte
			if child.Type.String() != "url " || child.Size-child.HeaderSize != 4 {
				return ErrUnsupportedInput
			}
			if _, err = io.ReadFull(f, flags[:]); err != nil {
				return err
			}
			t.external = binary.BigEndian.Uint32(flags[:]) != 1
		case "dinf":
			err = f.walk(b.Offset+b.Size, depth+1, t)
		case "senc", "saiz", "saio":
			if t != nil {
				t.encrypted = true
			}
		case "tkhd", "mdhd", "hdlr", "stsz", "stsc", "stts", "ctts", "stco", "co64", "elst", "mvhd", "trex":
			if typ != "mvhd" && typ != "trex" {
				if t == nil || t.seen[typ] || ((typ == "stco" || typ == "co64") && (t.seen["stco"] || t.seen["co64"])) {
					return fieldError("duplicate/misplaced-"+typ, ErrInvalidBitstream)
				}
				t.seen[typ] = true
			}
			var decoded mp4.IBox
			decoded, err = f.decode(b)
			if err != nil {
				return err
			}
			switch box := decoded.(type) {
			case *mp4.Tkhd:
				t.id = box.TrackID
			case *mp4.Mdhd:
				t.scale = box.Timescale
			case *mp4.Hdlr:
				t.video = string(box.HandlerType[:]) == "vide"
			case *mp4.Stsz:
				t.stsz = box
			case *mp4.Stsc:
				t.stsc = box
			case *mp4.Stts:
				t.stts = box
			case *mp4.Ctts:
				t.ctts = box
			case *mp4.Stco:
				if err = f.index(int64(len(box.ChunkOffset)) * 8); err != nil {
					return err
				}
				t.chunks = make([]uint64, len(box.ChunkOffset))
				for i, v := range box.ChunkOffset {
					t.chunks[i] = uint64(v)
				}
			case *mp4.Co64:
				t.chunks = box.ChunkOffset
			case *mp4.Elst:
				t.edits = box
			case *mp4.Mvhd:
				if f.movieScale != 0 || box.Timescale == 0 {
					return ErrInvalidBitstream
				}
				f.movieScale = box.Timescale
			case *mp4.Trex:
				if f.defaults[box.TrackID] != nil {
					return ErrInvalidBitstream
				}
				f.defaults[box.TrackID] = box
			}
		}
		if err != nil {
			return err
		}
		if _, err = b.SeekToEnd(f); err != nil {
			return err
		}
	}
}
func (f *mp4File) descriptions(b *mp4.BoxInfo, t *mp4Track, depth int64) error {
	var prefix [8]byte
	if b.Size-b.HeaderSize < 8 {
		return ErrInvalidBitstream
	}
	if _, err := io.ReadFull(f, prefix[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(prefix[:4]) != 0 {
		return ErrUnsupportedInput
	}
	count := uint64(binary.BigEndian.Uint32(prefix[4:]))
	if count > (b.Size-b.HeaderSize-8)/8 {
		return ErrInvalidBitstream
	}
	if count > uint64(f.l.MaxContainerIndexBytes/128) {
		return ErrResourceLimit
	}
	if err := f.index(int64(count) * 128); err != nil {
		return err
	}
	t.configs = make([][]byte, count)
	for i := range t.configs {
		entry, err := f.box(b.Offset + b.Size)
		if err != nil {
			return err
		}
		if entry.Type.String() == "hvc1" || entry.Type.String() == "hev1" {
			if entry.Size-entry.HeaderSize < 78 {
				return ErrInvalidBitstream
			}
			var header [78]byte
			if _, err := io.ReadFull(f, header[:]); err != nil {
				return err
			}
			if binary.BigEndian.Uint16(header[6:8]) != 1 {
				return ErrUnsupportedInput
			}
			for {
				child, err := f.box(entry.Offset + entry.Size)
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				if depth+1 > f.l.MaxNestingDepth {
					return ErrResourceLimit
				}
				if child.Type.String() == "sinf" {
					return ErrUnsupportedInput
				}
				if child.Type.String() == "hvcC" {
					if t.configs[i] != nil {
						return ErrInvalidBitstream
					}
					n := child.Size - child.HeaderSize
					if n < 23 {
						return ErrInvalidBitstream
					}
					if n > uint64(f.l.MaxContainerIndexBytes) {
						return ErrResourceLimit
					}
					if err := f.index(int64(n)); err != nil {
						return err
					}
					t.configs[i] = make([]byte, n)
					if _, err := io.ReadFull(f, t.configs[i]); err != nil {
						return err
					}
				}
				if _, err := child.SeekToEnd(f); err != nil {
					return err
				}
			}
			if t.configs[i] == nil {
				return fieldError("missing-hvcC", ErrInvalidBitstream)
			}
		}
		if _, err := entry.SeekToEnd(f); err != nil {
			return err
		}
	}
	position, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if uint64(position) != b.Offset+b.Size {
		return ErrInvalidBitstream
	}
	return nil
}
func (f *mp4File) compactSizes(b *mp4.BoxInfo, t *mp4Track) error {
	var prefix [12]byte
	n := b.Size - b.HeaderSize
	if n < 12 {
		return ErrInvalidBitstream
	}
	if _, err := io.ReadFull(f, prefix[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(prefix[:4]) != 0 {
		return ErrUnsupportedInput
	}
	width := prefix[7]
	if width != 4 && width != 8 && width != 16 {
		return ErrInvalidBitstream
	}
	count := uint64(binary.BigEndian.Uint32(prefix[8:]))
	if count > uint64(f.l.MaxFrames) {
		return ErrResourceLimit
	}
	if (count*uint64(width)+7)/8 != n-12 {
		return ErrInvalidBitstream
	}
	if err := f.index(int64(count)*4 + 1024); err != nil {
		return err
	}
	t.sizes = make([]uint32, count)
	var data [2]byte
	for i := range t.sizes {
		switch width {
		case 4:
			if i%2 == 0 {
				if _, err := io.ReadFull(f, data[:1]); err != nil {
					return err
				}
				t.sizes[i] = uint32(data[0] >> 4)
			} else {
				t.sizes[i] = uint32(data[0] & 15)
			}
		case 8:
			if _, err := io.ReadFull(f, data[:1]); err != nil {
				return err
			}
			t.sizes[i] = uint32(data[0])
		case 16:
			if _, err := io.ReadFull(f, data[:]); err != nil {
				return err
			}
			t.sizes[i] = uint32(binary.BigEndian.Uint16(data[:]))
		}
	}
	return nil
}
func (f *mp4File) ordinary(t *mp4Track) error {
	if t.stts == nil || t.stsc == nil || !t.seen["stsz"] || (!t.seen["stco"] && !t.seen["co64"]) {
		return fieldError("missing-sample-tables", ErrIncomplete)
	}
	count := len(t.sizes)
	if t.stsz != nil {
		count = int(t.stsz.SampleCount)
	}
	if int64(count) > f.l.MaxFrames {
		return ErrResourceLimit
	}
	if err := f.index(int64(count) * 64); err != nil {
		return err
	}
	t.samples = make([]mp4Sample, count)
	if count == 0 {
		if len(t.stts.Entries) != 0 || len(t.stsc.Entries) != 0 || len(t.chunks) != 0 {
			return ErrInvalidBitstream
		}
		return nil
	}
	if len(t.stsc.Entries) == 0 || t.stsc.Entries[0].FirstChunk != 1 {
		return ErrInvalidBitstream
	}
	for i, e := range t.stsc.Entries {
		if e.SamplesPerChunk == 0 || e.SampleDescriptionIndex == 0 || e.FirstChunk == 0 || uint64(e.FirstChunk) > uint64(len(t.chunks)) || i > 0 && e.FirstChunk <= t.stsc.Entries[i-1].FirstChunk {
			return ErrInvalidBitstream
		}
	}
	sample, run := 0, 0
	for i, offset := range t.chunks {
		if run+1 < len(t.stsc.Entries) && uint64(t.stsc.Entries[run+1].FirstChunk) == uint64(i+1) {
			run++
		}
		entry := t.stsc.Entries[run]
		if uint64(entry.SamplesPerChunk) > uint64(count-sample) {
			return ErrInvalidBitstream
		}
		for range entry.SamplesPerChunk {
			size := uint32(0)
			if t.stsz != nil {
				size = t.stsz.SampleSize
				if size == 0 {
					size = t.stsz.EntrySize[sample]
				}
			} else {
				size = t.sizes[sample]
			}
			if size == 0 || uint64(size) > math.MaxUint64-offset {
				return ErrInvalidBitstream
			}
			t.samples[sample] = mp4Sample{offset: offset, size: size, description: entry.SampleDescriptionIndex}
			offset += uint64(size)
			sample++
		}
	}
	if sample != count {
		return ErrInvalidBitstream
	}
	sample = 0
	dts := int64(0)
	for _, e := range t.stts.Entries {
		if e.SampleCount == 0 || e.SampleDelta == 0 || uint64(e.SampleCount) > uint64(count-sample) {
			return ErrInvalidBitstream
		}
		for range e.SampleCount {
			t.samples[sample].dts = dts
			t.samples[sample].pts = dts
			if int64(e.SampleDelta) > math.MaxInt64-dts {
				return ErrUnsupportedInput
			}
			dts += int64(e.SampleDelta)
			sample++
		}
	}
	if sample != count {
		return ErrInvalidBitstream
	}
	if t.ctts != nil {
		sample = 0
		for i, e := range t.ctts.Entries {
			if e.SampleCount == 0 || uint64(e.SampleCount) > uint64(count-sample) {
				return ErrInvalidBitstream
			}
			offset := t.ctts.GetSampleOffset(i)
			for range e.SampleCount {
				value := t.samples[sample].dts
				if offset > 0 && value > math.MaxInt64-offset || offset < 0 && value < math.MinInt64-offset {
					return ErrUnsupportedInput
				}
				t.samples[sample].pts = value + offset
				sample++
			}
		}
		if sample != count {
			return ErrInvalidBitstream
		}
	}
	return nil
}
func (f *mp4File) inMedia(s mp4Sample) bool {
	for _, e := range f.media {
		if s.offset >= e.start && s.offset <= e.end && uint64(s.size) <= e.end-s.offset {
			return true
		}
	}
	return false
}
func (f *mp4File) validateSamples(t *mp4Track) error {
	if err := f.index(int64(len(t.samples)) * 16); err != nil {
		return err
	}
	extents := make([]mp4Extent, len(t.samples))
	for i, sample := range t.samples {
		if !f.inMedia(sample) {
			return fieldError("sample-mdat-extent", errors.Join(ErrInvalidBitstream, ErrIncomplete))
		}
		extents[i] = mp4Extent{sample.offset, sample.offset + uint64(sample.size)}
	}
	slices.SortFunc(extents, func(a, b mp4Extent) int {
		if a.start < b.start {
			return -1
		}
		if a.start > b.start {
			return 1
		}
		return 0
	})
	for i := 1; i < len(extents); i++ {
		if extents[i].start < extents[i-1].end {
			return fieldError("overlapping-samples", ErrInvalidBitstream)
		}
	}
	return nil
}
func (f *mp4File) collect(t *mp4Track) (*Stream, error) {
	s, err := newStream(StreamOptions{Framing: LengthPrefixed, LengthSize: 4, Key: StreamKey{TrackID: uint64(t.id)}, Limits: f.l, Budget: f.memory.shared}, f.l, f.memory)
	if err != nil {
		return nil, err
	}
	if _, err := f.memory.reserve(32768); err != nil {
		return nil, err
	}
	buffer := make([]byte, 32768)
	var es uint64
	description := uint32(0)
	for i, sample := range t.samples {
		if !f.inMedia(sample) {
			return nil, fieldError("sample-mdat-extent", errors.Join(ErrInvalidBitstream, ErrIncomplete))
		}
		if sample.description == 0 || uint64(sample.description) > uint64(len(t.configs)) || t.configs[sample.description-1] == nil {
			return nil, fieldError("sample-description", ErrUnsupportedInput)
		}
		if sample.description != description {
			config := t.configs[sample.description-1]
			if len(config) < 23 || config[0] != 1 {
				return nil, ErrInvalidBitstream
			}
			s.opts.LengthSize = int(config[21]&3) + 1
			s.scanner.lengthSize = s.opts.LengthSize
			if err := s.config(config); err != nil {
				return nil, err
			}
			description = sample.description
		}
		if sample.offset > math.MaxInt64 {
			return nil, ErrUnsupportedInput
		}
		if _, err := f.Seek(int64(sample.offset), io.SeekStart); err != nil {
			return nil, err
		}
		marker := Marker{ESOffset: es, Kind: SampleStart, PTS: Timestamp{Value: sample.pts, Timescale: uint64(t.scale), Valid: true}, DTS: Timestamp{Value: sample.dts, Timescale: uint64(t.scale), Valid: true}}
		remaining := uint64(sample.size)
		first := true
		for remaining != 0 {
			amount := min(remaining, uint64(len(buffer)))
			if _, err := io.ReadFull(f, buffer[:amount]); err != nil {
				return nil, containerReadError("sample", err)
			}
			chunk := Chunk{ESOffset: es, Data: buffer[:amount]}
			if first {
				chunk.Markers = []Marker{marker}
				first = false
			}
			if amount > math.MaxUint64-es {
				return nil, ErrResourceLimit
			}
			if err := s.Push(f.ctx, chunk); err != nil {
				return nil, &Error{Container: "MP4", Stream: s.opts.Key, Offset: sample.offset, OffsetKnown: true, AU: uint64(i), AUKnown: true, Field: "sample", Err: err}
			}
			es += amount
			remaining -= amount
		}
	}
	if f.readErr != nil {
		return nil, f.readErr
	}
	return s, nil
}
func (f *mp4File) editOutput(t *mp4Track, input []resolvedPicture) ([]resolvedPicture, error) {
	if t.edits == nil {
		return input, nil
	}
	if f.movieScale == 0 {
		return nil, ErrInvalidBitstream
	}
	if len(t.edits.Entries) != 1 && len(t.edits.Entries) != 2 || len(t.edits.Entries) == 2 && t.edits.GetMediaTime(0) != -1 || t.edits.GetMediaTime(len(t.edits.Entries)-1) < 0 {
		return nil, fieldError("edit-layout", ErrUnsupportedInput)
	}
	// Reserve the entire finite worst-case output before repeats are appended.
	count := int64(len(input))
	edits := int64(len(t.edits.Entries))
	if edits != 0 && count > f.l.MaxFrames/edits {
		return nil, ErrResourceLimit
	}
	if _, err := f.memory.reserve(count * edits * 512); err != nil {
		return nil, err
	}
	out := make([]resolvedPicture, 0, count*edits)
	var position big.Rat
	for i, e := range t.edits.Entries {
		if e.MediaRateInteger != 1 || e.MediaRateFraction != 0 {
			return nil, fieldError("edit-rate", ErrUnsupportedInput)
		}
		var duration, media big.Rat
		var num, den big.Int
		num.SetUint64(t.edits.GetSegmentDuration(i))
		den.SetUint64(uint64(f.movieScale))
		duration.SetFrac(&num, &den)
		time := t.edits.GetMediaTime(i)
		if time < -1 {
			return nil, ErrUnsupportedInput
		}
		if time >= 0 {
			num.SetInt64(time)
			den.SetUint64(uint64(t.scale))
			media.SetFrac(&num, &den)
			for _, p := range input {
				var pts, relative, mapped big.Rat
				num.SetInt64(p.picture.PTS.Value)
				den.SetUint64(p.picture.PTS.Timescale)
				pts.SetFrac(&num, &den)
				relative.Sub(&pts, &media)
				if relative.Sign() < 0 || relative.Cmp(&duration) >= 0 {
					continue
				}
				mapped.Add(&position, &relative)
				stamp, err := timestampRat(&mapped)
				if err != nil {
					return nil, err
				}
				p.picture.PTS = stamp
				out = append(out, p)
			}
		}
		position.Add(&position, &duration)
		if _, err := timestampRat(&position); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func extractMP4(ctx context.Context, r io.ReadSeeker, opts Options, l Limits) (result *Extraction, err error) {
	memory := &accounting{max: l.MaxRetainedBytes, shared: opts.Budget}
	defer memory.release()
	if _, err := memory.reserve(8192); err != nil {
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
	if start < 0 || size < start {
		return nil, ErrInvalidBitstream
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	f := &mp4File{ctx: ctx, r: r, size: uint64(size), l: l, memory: memory, defaults: make(map[uint32]*mp4.Trex)}
	defer func() {
		if err != nil {
			position, _ := r.Seek(0, io.SeekCurrent)
			err = &Error{Container: "MP4", Offset: uint64(max(0, position)), OffsetKnown: true, Field: "container", Err: err}
		}
	}()
	if err := f.walk(uint64(size), 0, nil); err != nil {
		return nil, err
	}
	var track *mp4Track
	for _, t := range f.tracks {
		candidate := false
		for _, cfg := range t.configs {
			candidate = candidate || cfg != nil
		}
		if t.video && candidate && (opts.TrackID == 0 || opts.TrackID == uint64(t.id)) {
			if track != nil {
				return nil, ErrAmbiguousTrack
			}
			track = t
		}
	}
	if track == nil || track.external || track.encrypted {
		return nil, fieldError("HEVC-track", ErrUnsupportedInput)
	}
	if err := f.ordinary(track); err != nil {
		return nil, err
	}
	if err := f.fragmentSamples(track); err != nil {
		return nil, err
	}
	if err := f.validateSamples(track); err != nil {
		return nil, err
	}
	s, err := f.collect(track)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveFile(ctx, s, l)
	if err != nil {
		return nil, err
	}
	output, err := f.editOutput(track, resolved.output)
	if err != nil {
		return nil, err
	}
	return makeExtraction(ctx, output, l, opts.Budget, resolved.metadataSeen, memory)
}
