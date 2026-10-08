package extract

// Base HEVC header syntax follows H.265 (01/2026), sections 7.3 and 8.3.1.
// Parsing structure is also informed by hevc_parser 0.6.11 (MIT).
import (
	"errors"
	"fmt"
	"io"
	"math/bits"
	"slices"
)

type reference struct {
	delta int32
	used  bool
}
type referenceSet struct {
	refs  [16]reference
	count int
}
type longReference struct {
	lsb       uint64
	cycle     uint64
	used, msb bool
}
type profileInfo struct {
	main  [7]bool
	level [7]uint8
}
type videoSet struct {
	id                       uint64
	layers                   uint
	temporalNesting          bool
	buffer, reorder, latency [7]uint64
	profile                  profileInfo
	data                     []byte
}
type sequence struct {
	id, vps                    uint64
	pocBits                    uint
	ctbs, widthCTB, heightCTB  uint64
	layers                     uint
	temporalNesting            bool
	reorder, buffer, latency   [7]uint64
	sao, temporalMVP, longTerm bool
	short                      [64]referenceSet
	shortCount                 int
	long                       [32]longReference
	longCount                  int
	qpOffset                   int64
	profile                    profileInfo
	width, height              uint64
	minCbBits, ctbBits         uint
	scaling, restrictedLists   bool
	eightBit                   bool
	spatial                    uint64
}
type pictureSet struct {
	id, sps                                                          uint64
	temporal                                                         uint8
	dependent, output                                                bool
	extra                                                            uint
	cabac, chromaOffsets, weighted, weightedB                        bool
	refsL0, refsL1                                                   uint64
	tiles, entropySync, loopFilter, deblockOverride, deblockDisabled bool
	lists, extension                                                 bool
	cols, rows                                                       uint64
	uniform                                                          bool
	tileDimensions                                                   bitReader
	initQP, cbQP, crQP                                               int64
	qpDepth, merge                                                   uint64
	scaling                                                          bool
}
type sliceHeader struct {
	first, dependent, output, noPrior bool
	suppressed                        bool
	pps, lsb, address                 uint64
	kind, temporal                    uint8
	short                             referenceSet
	long                              [16]longReference
	longCount, bytes                  int
	shortSPS, temporalMVP             bool
	shortIndex                        uint8
	longFromSPS                       int
	longIndices                       [16]uint8
	collocatedKnown                   bool
	collocated                        uint8
	refCounts                         [2]uint8
	refLists                          [2][15]uint8
}

func (b *bitReader) skip(n uint) {
	if b.err != nil {
		return
	}
	if uint64(n) > uint64(len(b.data)*8-b.pos) {
		b.err = io.ErrUnexpectedEOF
		return
	}
	b.pos += int(n)
}
func (b *bitReader) invalid() {
	if b.err == nil {
		b.err = ErrInvalidBitstream
	}
}
func (b *bitReader) boundedUE(max uint64) uint64 {
	v := b.ue()
	if v > max {
		if b.err == nil {
			b.invalid()
		}
		return 0
	}
	return v
}
func (b *bitReader) trailing() {
	if !b.flag() && b.err == nil {
		b.invalid()
	}
	left := len(b.data)*8 - b.pos
	if left > 7 {
		b.invalid()
		return
	}
	if b.read(uint(left)) != 0 {
		b.invalid()
	}
}
func syntaxError(b *bitReader, field string) error {
	if b.err != nil {
		if errors.Is(b.err, io.ErrUnexpectedEOF) {
			return fieldError(field, fmt.Errorf("%w: %w: %w", ErrInvalidBitstream, ErrIncomplete, b.err))
		}
		return fieldError(field, fmt.Errorf("%w: %w", ErrInvalidBitstream, b.err))
	}
	return nil
}

func profileTier(b *bitReader, layers uint) (profileInfo, error) {
	var out profileInfo
	readProfile := func() (bool, error) {
		space := b.read(2)
		b.read(1)
		profile, compat := b.read(5), b.read(32)
		b.skip(48)
		if err := syntaxError(b, "profile-tier-level"); err != nil {
			return false, err
		}
		if space != 0 || profile != 1 && profile != 2 && compat&((1<<30)|(1<<29)) == 0 {
			return false, fieldError("HEVC-profile", ErrUnsupportedInput)
		}
		return profile == 1 || compat&(1<<30) != 0, nil
	}
	var err error
	out.main[layers], err = readProfile()
	if err != nil {
		return out, err
	}
	out.level[layers] = uint8(b.read(8))
	var profiles, levels [7]bool
	for i := range layers {
		profiles[i] = b.flag()
		levels[i] = b.flag()
	}
	if layers > 0 {
		for i := layers; i < 8; i++ {
			if b.read(2) != 0 {
				b.invalid()
			}
		}
	}
	for i := range layers {
		if profiles[i] {
			out.main[i], err = readProfile()
			if err != nil {
				return out, err
			}
		}
		if levels[i] {
			out.level[i] = uint8(b.read(8))
		}
	}
	for i := int(layers) - 1; i >= 0; i-- {
		if !profiles[i] {
			out.main[i] = out.main[i+1]
		}
		if !levels[i] {
			out.level[i] = out.level[i+1]
		}
	}
	return out, syntaxError(b, "profile-tier-level")
}

// H.265 Table A.8. Unknown levels cannot supply the required header limits.
type levelLimit struct {
	samples, slices, cols, rows uint64
}

func limitsForLevel(level uint8) levelLimit {
	switch level {
	case 30:
		return levelLimit{36864, 16, 1, 1}
	case 60:
		return levelLimit{122880, 16, 1, 1}
	case 63:
		return levelLimit{245760, 20, 1, 1}
	case 90:
		return levelLimit{552960, 30, 2, 2}
	case 93:
		return levelLimit{983040, 40, 3, 3}
	case 120, 123:
		return levelLimit{2228224, 75, 5, 5}
	case 150, 153, 156:
		return levelLimit{8912896, 200, 10, 11}
	case 180, 183, 186:
		return levelLimit{35651584, 600, 20, 22}
	case 189:
		return levelLimit{80216064, 600, 20, 22}
	case 210, 213, 216:
		return levelLimit{142606336, 1800, 40, 44}
	}
	return levelLimit{}
}

func (l levelLimit) maxBuffer(samples uint64) uint64 {
	switch {
	case samples <= l.samples/4:
		return 16
	case samples <= l.samples/2:
		return 12
	case samples <= 3*l.samples/4:
		return 8
	default:
		return 6
	}
}

func scalingList(b *bitReader) {
	for size := range 4 {
		step := 1
		if size == 3 {
			step = 3
		}
		for matrix := 0; matrix < 6; matrix += step {
			if !b.flag() {
				b.boundedUE(uint64(matrix / step))
			} else {
				next := int64(8)
				if size > 1 {
					next = b.boundedSE(-7, 247) + 8
				}
				for range min(64, 1<<(4+2*size)) {
					next = (next + b.boundedSE(-128, 127) + 256) % 256
					if next == 0 {
						b.invalid()
					}
				}
			}
		}
	}
}

func shortRPS(b *bitReader, index, spsCount int, sets *[64]referenceSet) referenceSet {
	var out referenceSet
	add := func(delta int32, used bool) {
		if delta == 0 {
			return
		}
		if out.count == len(out.refs) || delta < -32768 || delta > 32767 {
			b.invalid()
			return
		}
		out.refs[out.count] = reference{delta, used}
		out.count++
	}
	if index > 0 && b.flag() {
		referenceIndex := index - 1
		if index == spsCount {
			referenceIndex -= int(b.boundedUE(uint64(index - 1)))
		}
		sign := b.flag()
		delta := int32(b.boundedUE(32767) + 1)
		if sign {
			delta = -delta
		}
		previous := sets[referenceIndex]
		for j := 0; j <= previous.count; j++ {
			used := b.flag()
			include := used
			if !used {
				include = b.flag()
			}
			if include {
				d := delta
				if j < previous.count {
					d += previous.refs[j].delta
				}
				add(d, used)
			}
		}
	} else {
		negative, positive := b.boundedUE(16), b.boundedUE(16)
		if negative+positive > 16 {
			b.invalid()
			return out
		}
		for group, count := range []uint64{negative, positive} {
			delta := int32(0)
			for range count {
				delta += int32(b.boundedUE(32767) + 1)
				used := b.flag()
				if group == 0 {
					add(-delta, used)
				} else {
					add(delta, used)
				}
			}
		}
	}
	slices.SortFunc(out.refs[:out.count], func(a, b reference) int {
		if a.delta < 0 && b.delta >= 0 {
			return -1
		}
		if a.delta >= 0 && b.delta < 0 {
			return 1
		}
		if a.delta < 0 {
			return int(b.delta - a.delta)
		}
		return int(a.delta - b.delta)
	})
	for i := 1; i < out.count; i++ {
		if out.refs[i].delta == out.refs[i-1].delta {
			b.invalid()
		}
	}
	return out
}

func hrd(b *bitReader, layers uint) {
	nal, vcl := b.flag(), b.flag()
	sub := false
	if nal || vcl {
		sub = b.flag()
		if sub {
			b.skip(19)
		}
		b.skip(8)
		if sub {
			b.skip(4)
		}
		b.skip(15)
	}
	for i := uint(0); i <= layers; i++ {
		fixed := b.flag()
		within := fixed
		if !fixed {
			within = b.flag()
		}
		low := false
		if within {
			b.boundedUE(2047)
		} else {
			low = b.flag()
		}
		count := uint64(0)
		if !low {
			count = b.boundedUE(31)
		}
		for _, present := range []bool{nal, vcl} {
			if present {
				for range count + 1 {
					b.ue()
					b.ue()
					if sub {
						b.ue()
						b.ue()
					}
					b.flag()
				}
			}
		}
	}
}

func vui(b *bitReader, s *sequence, width, height uint64) error {
	if b.flag() {
		if b.read(8) == 255 {
			b.skip(32)
		}
	}
	if b.flag() {
		b.flag()
	}
	if b.flag() {
		b.skip(4)
		if b.flag() {
			b.skip(24)
		}
	}
	if b.flag() {
		b.boundedUE(5)
		b.boundedUE(5)
	}
	b.flag()
	if b.flag() {
		return fieldError("field-sequence", ErrUnsupportedInput)
	}
	b.flag()
	if b.flag() {
		left, right, top, bottom := b.ue(), b.ue(), b.ue(), b.ue()
		if 2*(left+right) >= width || 2*(top+bottom) >= height {
			b.invalid()
		}
	}
	if b.flag() {
		if b.read(32) == 0 || b.read(32) == 0 {
			b.invalid()
		}
		if b.flag() {
			b.ue()
		}
		if b.flag() {
			hrd(b, s.layers)
		}
	}
	if b.flag() {
		b.skip(2)
		s.restrictedLists = b.flag()
		s.spatial = b.boundedUE(4095)
		b.boundedUE(16)
		b.boundedUE(16)
		b.boundedUE(15)
		b.boundedUE(15)
	}
	return syntaxError(b, "VUI")
}

func parseSPS(data []byte) (sequence, error) {
	b := bitReader{data: data}
	s := sequence{vps: b.read(4), layers: uint(b.read(3))}
	s.temporalNesting = b.flag()
	if s.layers == 0 && !s.temporalNesting && b.err == nil {
		return s, fieldError("SPS-temporal-nesting", ErrInvalidBitstream)
	}
	if s.layers > 6 {
		return s, fieldError("SPS-sublayers", ErrInvalidBitstream)
	}
	var err error
	s.profile, err = profileTier(&b, s.layers)
	if err != nil {
		return s, err
	}
	s.id = b.boundedUE(15)
	chroma := b.boundedUE(3)
	if chroma == 3 {
		b.flag()
	}
	if err := syntaxError(&b, "chroma-format"); err != nil {
		return s, err
	}
	if chroma != 1 {
		return s, fieldError("chroma-format", ErrUnsupportedInput)
	}
	width, height := b.boundedUE(1<<24), b.boundedUE(1<<24)
	s.width, s.height = width, height
	if width == 0 || height == 0 {
		b.invalid()
	}
	var cropX, cropY uint64
	if b.flag() {
		cropX = b.ue() + b.ue()
		cropY = b.ue() + b.ue()
		if 2*cropX >= width || 2*cropY >= height {
			b.invalid()
		}
	}
	luma, chromabit := b.ue(), b.ue()
	if luma > 2 || chromabit > 2 {
		return s, fieldError("bit-depth", ErrUnsupportedInput)
	}
	s.qpOffset = int64(luma) * 6
	s.eightBit = luma == 0 && chromabit == 0
	for _, main := range s.profile.main[:s.layers+1] {
		if main && !s.eightBit {
			b.invalid()
		}
	}
	s.pocBits = uint(b.boundedUE(12)) + 4
	start := s.layers
	if b.flag() {
		start = 0
	}
	for i := start; i <= s.layers; i++ {
		s.buffer[i] = b.boundedUE(15) + 1
		s.reorder[i] = b.boundedUE(15)
		s.latency[i] = b.ue()
		if s.reorder[i] >= s.buffer[i] || i > start && (s.buffer[i] < s.buffer[i-1] || s.reorder[i] < s.reorder[i-1]) {
			b.invalid()
		}
	}
	for i := uint(0); i < start; i++ {
		s.buffer[i] = s.buffer[start]
		s.reorder[i] = s.reorder[start]
		s.latency[i] = s.latency[start]
	}
	mincb := b.boundedUE(3) + 3
	ctbbits := mincb + b.boundedUE(3)
	s.minCbBits, s.ctbBits = uint(mincb), uint(ctbbits)
	if ctbbits < 4 || ctbbits > 6 {
		b.invalid()
	}
	s.widthCTB = (width + (1 << ctbbits) - 1) >> ctbbits
	s.heightCTB = (height + (1 << ctbbits) - 1) >> ctbbits
	s.ctbs = s.widthCTB * s.heightCTB
	minTB := b.boundedUE(3) + 2
	maxTB := minTB + b.boundedUE(3)
	interDepth, intraDepth := b.boundedUE(4), b.boundedUE(4)
	if width%(1<<mincb) != 0 || height%(1<<mincb) != 0 || minTB >= mincb || maxTB > min(ctbbits, 5) || max(interDepth, intraDepth)+minTB > ctbbits {
		b.invalid()
	}
	if err := syntaxError(&b, "SPS-geometry"); err != nil {
		return s, err
	}
	for i, level := range s.profile.level[:s.layers+1] {
		if level == 255 { // Level 8.5 does not express Annex A level limits.
			continue
		}
		limit := limitsForLevel(level)
		if limit.samples == 0 {
			return s, fieldError("HEVC-level", ErrUnsupportedInput)
		}
		if width*height > limit.samples || width*width > 8*limit.samples || height*height > 8*limit.samples || level >= 150 && ctbbits == 4 || s.buffer[i] > limit.maxBuffer(width*height) {
			b.invalid()
		}
	}
	s.scaling = b.flag()
	if s.scaling && b.flag() {
		scalingList(&b)
	}
	b.flag()
	s.sao = b.flag()
	if b.flag() {
		pcmY, pcmC := b.read(4)+1, b.read(4)+1
		minPCM := b.boundedUE(2) + 3
		maxPCM := minPCM + b.boundedUE(2)
		if pcmY > luma+8 || pcmC > chromabit+8 || minPCM < min(mincb, 5) || maxPCM > min(ctbbits, 5) {
			b.invalid()
		}
		b.flag()
	}
	s.shortCount = int(b.boundedUE(64))
	for i := range s.shortCount {
		s.short[i] = shortRPS(&b, i, s.shortCount, &s.short)
		if uint64(s.short[i].count) >= s.buffer[s.layers] {
			b.invalid()
		}
	}
	s.longTerm = b.flag()
	if s.longTerm {
		s.longCount = int(b.boundedUE(32))
		for i := range s.longCount {
			s.long[i] = longReference{lsb: b.read(s.pocBits), used: b.flag()}
		}
	}
	s.temporalMVP = b.flag()
	b.flag()
	if b.flag() {
		if err := vui(&b, &s, width-2*cropX, height-2*cropY); err != nil {
			return s, err
		}
	}
	if b.flag() && b.read(8) != 0 {
		return s, fieldError("SPS-extension", ErrUnsupportedInput)
	}
	b.trailing()
	return s, syntaxError(&b, "SPS")
}

func parseVPS(data []byte) (videoSet, error) {
	b := bitReader{data: data}
	v := videoSet{id: b.read(4), data: data}
	internal, external := b.flag(), b.flag()
	layers := b.read(6)
	sublayers := uint(b.read(3))
	nesting := b.flag()
	v.layers, v.temporalNesting = sublayers, nesting
	if err := syntaxError(&b, "VPS-header"); err != nil {
		return v, err
	}
	if !internal || !external || layers != 0 {
		return v, fieldError("multilayer-VPS", ErrUnsupportedInput)
	}
	if sublayers > 6 || sublayers == 0 && !nesting || b.read(16) != 0xffff {
		return v, fieldError("VPS-header", ErrInvalidBitstream)
	}
	var err error
	v.profile, err = profileTier(&b, sublayers)
	if err != nil {
		return v, err
	}
	start := sublayers
	if b.flag() {
		start = 0
	}
	for i := start; i <= sublayers; i++ {
		v.buffer[i] = b.boundedUE(15) + 1
		v.reorder[i] = b.boundedUE(15)
		v.latency[i] = b.ue()
		if v.reorder[i] >= v.buffer[i] || i > start && (v.buffer[i] < v.buffer[i-1] || v.reorder[i] < v.reorder[i-1]) {
			b.invalid()
		}
	}
	for i := uint(0); i < start; i++ {
		v.buffer[i], v.reorder[i], v.latency[i] = v.buffer[start], v.reorder[start], v.latency[start]
	}
	if b.read(6) != 0 || b.ue() != 0 {
		return v, fieldError("VPS-layer-sets", ErrUnsupportedInput)
	}
	if b.flag() {
		if b.read(32) == 0 || b.read(32) == 0 {
			b.invalid()
		}
		if b.flag() {
			b.ue()
		}
		if b.ue() != 0 {
			return v, fieldError("VPS-HRD", ErrUnsupportedInput)
		}
	}
	if b.flag() {
		return v, fieldError("VPS-extension", ErrUnsupportedInput)
	}
	b.trailing()
	return v, syntaxError(&b, "VPS")
}

func (b *bitReader) boundedSE(low, high int64) int64 {
	v := b.se()
	if v < low || v > high {
		b.invalid()
	}
	return v
}

func parsePPS(data []byte) (pictureSet, error) {
	b := bitReader{data: data}
	p := pictureSet{id: b.boundedUE(63), sps: b.boundedUE(15), dependent: b.flag(), output: b.flag(), extra: uint(b.read(3))}
	b.flag()
	p.cabac = b.flag()
	p.refsL0 = b.boundedUE(14) + 1
	p.refsL1 = b.boundedUE(14) + 1
	p.initQP = b.boundedSE(-38, 25)
	b.flag()
	b.flag()
	if b.flag() {
		p.qpDepth = b.boundedUE(3)
	}
	p.cbQP = b.boundedSE(-12, 12)
	p.crQP = b.boundedSE(-12, 12)
	p.chromaOffsets = b.flag()
	p.weighted = b.flag()
	p.weightedB = b.flag()
	b.flag()
	p.tiles = b.flag()
	p.entropySync = b.flag()
	if p.tiles {
		p.cols = b.boundedUE(1023) + 1
		p.rows = b.boundedUE(1023) + 1
		if p.cols == 1 && p.rows == 1 {
			b.invalid()
		}
		p.uniform = b.flag()
		p.tileDimensions = b
		if !p.uniform {
			for range p.cols + p.rows - 2 {
				b.ue()
			}
		}
		b.flag()
	}
	p.loopFilter = b.flag()
	if b.flag() {
		p.deblockOverride = b.flag()
		p.deblockDisabled = b.flag()
		if !p.deblockDisabled {
			b.boundedSE(-6, 6)
			b.boundedSE(-6, 6)
		}
	}
	p.scaling = b.flag()
	if p.scaling {
		scalingList(&b)
	}
	p.lists = b.flag()
	p.merge = b.boundedUE(4)
	p.extension = b.flag()
	if b.flag() && b.read(8) != 0 {
		return p, fieldError("PPS-extension", ErrUnsupportedInput)
	}
	b.trailing()
	return p, syntaxError(&b, "PPS")
}

// tileDimension derives one axis without allocating a per-CTB address map.
func tileDimension(b *bitReader, uniform bool, count, extent, position, samples, ctbSize, minimum uint64) (start, size, largest uint64) {
	var boundary uint64
	for i := uint64(0); i < count; i++ {
		width := (i+1)*extent/count - i*extent/count
		if !uniform {
			width = extent - boundary
			if i+1 < count {
				width = b.ue() + 1
			}
		}
		if width == 0 || width > extent-boundary || width*ctbSize < minimum {
			b.invalid()
			return
		}
		if position >= boundary && position-boundary < width {
			start, size = boundary, width
		}
		largest = max(largest, min(width*ctbSize, samples-boundary*ctbSize))
		boundary += width
	}
	return
}

func weightedPrediction(b *bitReader, refsL0, refsL1 uint64, bipred bool) {
	denom := b.boundedUE(7)
	b.boundedSE(-int64(denom), 7-int64(denom))
	components := 0
	for list, count := range []uint64{refsL0, refsL1} {
		if list == 1 && !bipred {
			break
		}
		var luma, chroma [15]bool
		for i := range count {
			luma[i] = b.flag()
			if luma[i] {
				components++
			}
		}
		for i := range count {
			chroma[i] = b.flag()
			if chroma[i] {
				components += 2
			}
		}
		for i := range count {
			if luma[i] {
				b.boundedSE(-128, 127)
				b.boundedSE(-128, 127)
			}
			if chroma[i] {
				for range 2 {
					b.boundedSE(-128, 127)
					b.boundedSE(-512, 511)
				}
			}
		}
	}
	if components > 24 {
		b.invalid()
	}
}

func parseSlice(data []byte, kind, temporal uint8, p pictureSet, s sequence) (sliceHeader, error) {
	b := bitReader{data: data}
	h := sliceHeader{first: b.flag(), output: true, kind: kind, temporal: temporal}
	if temporal > 6 || uint(temporal) > s.layers || p.temporal > temporal {
		return h, fieldError("slice-parameter-temporal-id", ErrInvalidBitstream)
	}
	if s.temporalNesting && temporal > 0 && !(kind == 2 || kind == 3 || kind >= 6 && kind <= 9) {
		return h, fieldError("slice-temporal-nesting", ErrInvalidBitstream)
	}
	if (kind >= 2 && kind <= 5) && temporal == 0 {
		b.invalid()
	}
	if kind >= 16 && kind <= 23 {
		h.noPrior = b.flag()
		if temporal != 0 {
			b.invalid()
		}
	}
	h.pps = b.boundedUE(63)
	if h.pps != p.id || s.ctbs == 0 || p.tiles && (p.cols > s.widthCTB || p.rows > s.heightCTB) {
		b.invalid()
	}
	if p.initQP < -(26+s.qpOffset) || p.qpDepth > uint64(s.ctbBits-s.minCbBits) || p.merge+2 > uint64(s.ctbBits) || p.scaling && !s.scaling || p.tiles && p.entropySync {
		b.invalid()
	}
	for _, level := range s.profile.level[:s.layers+1] {
		if level != 255 {
			limit := limitsForLevel(level)
			if p.tiles && (p.cols > limit.cols || p.rows > limit.rows) {
				b.invalid()
			}
		}
	}
	if !h.first {
		if p.dependent {
			h.dependent = b.flag()
		}
		h.address = b.read(uint(bits.Len64(s.ctbs - 1)))
		if h.address == 0 || h.address >= s.ctbs {
			b.invalid()
		}
	}
	if p.tiles && b.err == nil {
		geometry := p.tileDimensions
		x, y := h.address%s.widthCTB, h.address/s.widthCTB
		ctbSize := uint64(1) << s.ctbBits
		col, width, maxWidth := tileDimension(&geometry, p.uniform, p.cols, s.widthCTB, x, s.width, ctbSize, 256)
		row, height, maxHeight := tileDimension(&geometry, p.uniform, p.rows, s.heightCTB, y, s.height, ctbSize, 64)
		if err := syntaxError(&geometry, "tile-geometry"); err != nil {
			return h, err
		}
		if s.spatial != 0 && maxWidth*maxHeight > 4*s.width*s.height/(s.spatial+4) {
			b.invalid()
		}
		// H.265 6.5.1: slice syntax is raster addressed; NAL order is tile scan.
		h.address = row*s.widthCTB + col*height + (y-row)*width + x - col
	}
	if s.spatial != 0 && p.entropySync && (2*s.height+s.width)*(uint64(1)<<s.ctbBits) > 4*s.width*s.height/(s.spatial+4) {
		b.invalid()
	}
	if !h.dependent {
		b.skip(p.extra) // slice_reserved_flag values are ignored by decoders.
		sliceType := b.boundedUE(2)
		if kind >= 16 && sliceType != 2 {
			b.invalid()
		}
		if p.output {
			h.output = b.flag()
		}
		if kind != 19 && kind != 20 {
			h.lsb = b.read(s.pocBits)
			h.shortSPS = b.flag()
			if !h.shortSPS {
				h.short = shortRPS(&b, s.shortCount, s.shortCount, &s.short)
			} else {
				index := uint64(0)
				if s.shortCount > 1 {
					index = b.read(uint(bits.Len(uint(s.shortCount - 1))))
				}
				if s.shortCount == 0 || index >= uint64(s.shortCount) {
					b.invalid()
				} else {
					h.shortIndex = uint8(index)
					h.short = s.short[index]
				}
			}
			if s.longTerm {
				fromSPS := uint64(0)
				if s.longCount > 0 {
					fromSPS = b.boundedUE(uint64(s.longCount))
				}
				count := fromSPS + b.boundedUE(16)
				if count > 16 || uint64(h.short.count)+count >= s.buffer[temporal] {
					b.invalid()
					count = 0
				}
				var cycle uint64
				h.longFromSPS = int(fromSPS)
				h.longCount = int(count)
				for i := range count {
					ref := longReference{}
					if i < fromSPS {
						index := uint64(0)
						if s.longCount > 1 {
							index = b.read(uint(bits.Len(uint(s.longCount - 1))))
						}
						if index >= uint64(s.longCount) {
							b.invalid()
						} else {
							h.longIndices[i] = uint8(index)
							ref = s.long[index]
						}
					} else {
						ref.lsb = b.read(s.pocBits)
						ref.used = b.flag()
					}
					ref.msb = b.flag()
					if i == 0 || i == fromSPS {
						cycle = 0
					}
					if ref.msb {
						cycle += b.boundedUE(uint64(1) << (32 - s.pocBits))
						ref.cycle = cycle
					}
					h.long[i] = ref
				}
			}
			if s.temporalMVP {
				h.temporalMVP = b.flag()
			}
		}
		if uint64(h.short.count+h.longCount) >= s.buffer[s.layers] {
			b.invalid()
		}
		total := uint64(0)
		for _, ref := range h.short.refs[:h.short.count] {
			if ref.used {
				total++
			}
		}
		for _, ref := range h.long[:h.longCount] {
			if ref.used {
				total++
			}
		}
		for _, level := range s.profile.level[temporal : s.layers+1] {
			if level != 255 && total > 8 {
				b.invalid()
			}
		}
		saoLuma, saoChroma := false, false
		if s.sao {
			saoLuma = b.flag()
			saoChroma = b.flag()
		}
		if sliceType < 2 {
			refsL0, refsL1 := p.refsL0, p.refsL1
			if b.flag() {
				refsL0 = b.boundedUE(14) + 1
				if sliceType == 0 {
					refsL1 = b.boundedUE(14) + 1
				}
			}
			if total == 0 {
				b.invalid()
			}
			var lists [2][15]uint8
			if total > 0 {
				for list := range lists {
					for i := range lists[list] {
						lists[list][i] = uint8(uint64(i) % total)
					}
				}
			}
			if p.lists && total > 1 {
				width := uint(bits.Len64(total - 1))
				for list, count := range []uint64{refsL0, refsL1} {
					if list == 1 && sliceType != 0 {
						break
					}
					if b.flag() {
						for i := range count {
							entry := b.read(width)
							if entry >= total {
								b.invalid()
							}
							lists[list][i] = uint8(entry)
						}
					}
				}
			}
			if b.err == nil {
				for list, count := range []uint64{refsL0, refsL1} {
					if list == 1 && sliceType != 0 {
						break
					}
					var order [16]uint8
					n := 0
					for group := range 2 {
						negative := group == list
						for i, ref := range h.short.refs[:h.short.count] {
							if ref.used && (ref.delta < 0) == negative {
								order[n] = uint8(i)
								n++
							}
						}
					}
					for i, ref := range h.long[:h.longCount] {
						if ref.used {
							order[n] = uint8(h.short.count + i)
							n++
						}
					}
					h.refCounts[list] = uint8(count)
					for i := range count {
						h.refLists[list][i] = order[lists[list][i]]
					}
				}
			}
			if sliceType == 0 {
				b.flag()
			}
			if p.cabac {
				b.flag()
			}
			if h.temporalMVP {
				list := 0
				if sliceType == 0 {
					if !b.flag() {
						list = 1
					}
				}
				count := refsL0
				if list == 1 {
					count = refsL1
				}
				index := uint64(0)
				if count > 1 {
					index = b.boundedUE(count - 1)
				}
				if b.err == nil {
					// H.265 8.3.4: list 0 starts with negative short-term
					// references; list 1 starts with positive references.
					// Normalize syntax to a shared RPS entry, so different
					// list encodings selecting the same picture agree.
					h.collocated = h.refLists[list][index]
					h.collocatedKnown = true
				}
			}
			if p.weighted && sliceType == 1 || p.weightedB && sliceType == 0 {
				weightedPrediction(&b, refsL0, refsL1, sliceType == 0)
			}
			b.boundedUE(4)
		}
		qp := 26 + p.initQP + b.boundedSE(-87, 77)
		if qp < -s.qpOffset || qp > 51 {
			b.invalid()
		}
		if p.chromaOffsets {
			cb := p.cbQP + b.boundedSE(-12, 12)
			cr := p.crQP + b.boundedSE(-12, 12)
			if cb < -12 || cb > 12 || cr < -12 || cr > 12 {
				b.invalid()
			}
		}
		disabled := p.deblockDisabled
		if p.deblockOverride && b.flag() {
			disabled = b.flag()
			if !disabled {
				b.boundedSE(-6, 6)
				b.boundedSE(-6, 6)
			}
		}
		if p.loopFilter && (saoLuma || saoChroma || !disabled) {
			b.flag()
		}
	}
	if p.tiles || p.entropySync {
		maximum := s.heightCTB - 1
		if p.tiles {
			maximum = p.cols*p.rows - 1
		}
		count := b.boundedUE(maximum)
		if count > 0 {
			width := uint(b.boundedUE(31) + 1)
			if count > uint64(len(data)*8)/uint64(width) {
				b.err = io.ErrUnexpectedEOF
			} else {
				b.skip(uint(count) * width)
			}
		}
	}
	if p.extension {
		size := b.ue()
		if size > uint64(len(data)) {
			b.err = io.ErrUnexpectedEOF
		} else {
			b.skip(uint(size) * 8)
		}
	}
	if !b.flag() {
		if b.err == nil {
			b.invalid()
		}
	}
	if b.read(uint((-b.pos)&7)) != 0 {
		b.invalid()
	}
	h.bytes = b.pos / 8
	return h, syntaxError(&b, "slice-header")
}
