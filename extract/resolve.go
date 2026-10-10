package extract

import (
	"bytes"
	"context"
	"errors"
	hdr10plus "github.com/Audionut/go-hdr10-plus"
	"math"
	"math/big"
	"slices"
)

type resolvedPicture struct {
	picture             Picture
	payload             *Payload
	output              bool
	reference, longTerm bool
	unavailable         bool
	temporal, kind      uint8
	latency             uint64
}

// Supported POC widths are at most 16 bits. The seed contains the previous
// T0 picture and its resolved RPS; later pictures have unique POCs via seen.
type pocHistory struct {
	lsb       [2][1024]uint64 // Seen and ambiguous LSBs.
	seed      [17]int64
	seedCount int
}

// Reserve seven history values: at most six simultaneous resolver states in
// assembly/replay plus a clone temporary. Each value occupies 16528 bytes on
// amd64; this is additional to the existing fixed resolution reservation.
const pocHistoryScratch = 128 << 10

// Cover seven conservatively distinct parameter tables and resolver scratch
// during assembly/replay. POC histories have their separate reservation.
const resolutionScratch = 2 << 20

func (h *pocHistory) remember(poc int64, width uint) {
	for _, seed := range h.seed[:h.seedCount] {
		if seed == poc {
			return // An unavailable seed may later become a real picture.
		}
	}
	lsb := uint64(poc) & ((uint64(1) << width) - 1)
	word, mask := lsb>>6, uint64(1)<<(lsb&63)
	if h.lsb[0][word]&mask != 0 {
		h.lsb[1][word] |= mask
	}
	h.lsb[0][word] |= mask
}

type resolver struct {
	vps                      [16]*videoSet
	sps                      [16]*sequence
	spsData                  [16][]byte
	pps                      [64]*pictureSet
	payload                  *Payload
	previousPOC              int64
	previousValid            bool
	first                    bool
	noRasl                   bool
	queue                    []resolvedPicture
	output                   []resolvedPicture
	decode                   uint64
	lastPOC                  int64
	lastPOCValid             bool
	limits                   Limits
	metadataSeen             bool
	seen                     map[int64]struct{}
	stsa                     [7]uint64
	stsaKnown                [7]bool
	playlist                 bool
	trace                    *[]Picture
	activeSPS, activeVPS     []byte
	activeSPSID, activeVPSID uint64
	parameterChange          bool
	irapKind                 uint8
	irapPOC                  int64
	irapDecode               uint64
	trailing                 bool
	priorIRAPPOC             int64
	priorIRAPValid           bool
	priorOutputPOC           int64
	priorOutputValid         bool
	maxOutputPOC             int64
	maxOutputValid           bool
	radlPOC, raslPOC         int64
	radlSeen, raslSeen       bool
	lastTemporal             [7]uint64
	temporalKnown            [7]bool
	pocHistory               pocHistory
	spatialMode              uint8
}
type missingReference struct {
	poc     int64
	partial bool
}

func (e *missingReference) Error() string { return "missing reference picture" }
func (e *missingReference) Unwrap() error { return ErrIncomplete }

func compareTime(a, b Timestamp) int {
	var x, y, scale big.Int
	x.SetInt64(a.Value)
	scale.SetUint64(b.Timescale)
	x.Mul(&x, &scale)
	y.SetInt64(b.Value)
	scale.SetUint64(a.Timescale)
	y.Mul(&y, &scale)
	return x.Cmp(&y)
}

func (r *resolver) emit() error {
	index := -1
	for i := range r.queue {
		if r.queue[i].output && (index < 0 || r.queue[i].picture.POC < r.queue[index].picture.POC) {
			index = i
		}
	}
	if index < 0 {
		return fieldError("DPB-without-output", ErrInvalidBitstream)
	}
	p := r.queue[index]
	if r.lastPOCValid && p.picture.POC <= r.lastPOC {
		return fieldError("POC-output-order", ErrInvalidBitstream)
	}
	if len(r.output) > 0 {
		last := r.output[len(r.output)-1].picture.PTS
		if (!r.playlist || r.output[len(r.output)-1].picture.OccurrenceOrdinal == p.picture.OccurrenceOrdinal) && last.Valid && p.picture.PTS.Valid && compareTime(last, p.picture.PTS) >= 0 {
			return fieldError("PTS-output-order", ErrInvalidBitstream)
		}
	}
	r.lastPOC = p.picture.POC
	r.lastPOCValid = true
	r.output = append(r.output, p)
	if p.reference {
		r.queue[index].output = false
	} else {
		r.queue = slices.Delete(r.queue, index, index+1)
	}
	return nil
}

func (r *resolver) flush() error {
	for slices.ContainsFunc(r.queue, func(p resolvedPicture) bool { return p.output }) {
		if err := r.emit(); err != nil {
			return err
		}
	}
	r.queue = nil
	return nil
}

func (r *resolver) complete() error {
	// Changed active definitions may precede the next CVS. Completion requires
	// that boundary to have been proved, including across physical clips.
	if r.parameterChange {
		return fieldError("unresolved-active-parameter-change", ErrIncomplete)
	}
	return r.flush()
}

func (r *resolver) add(h sliceHeader, s sequence, picture Picture, update *Payload) error {
	irap := h.kind >= 16 && h.kind <= 21
	if irap {
		r.noRasl = r.first || h.kind <= 20
	}
	reset := irap && r.noRasl
	if reset {
		if h.noPrior || h.kind == 21 {
			r.queue = nil
		} else if err := r.flush(); err != nil {
			return err
		}
		r.previousValid = false
		r.lastPOCValid = false
		r.payload = nil
		r.seen = nil
		r.stsaKnown = [7]bool{}
		r.temporalKnown = [7]bool{}
		r.pocHistory = pocHistory{}
		r.maxOutputValid = false
	}
	if update != nil {
		r.payload = update
		r.metadataSeen = true
	}
	poc := int64(h.lsb)
	if !reset {
		if !r.previousValid {
			return fieldError("POC-predecessor", ErrIncomplete)
		}
		mod := int64(1) << s.pocBits
		lsb := ((r.previousPOC % mod) + mod) % mod
		msb := r.previousPOC - lsb
		if poc < lsb && lsb-poc >= mod/2 {
			msb += mod
		} else if poc > lsb && poc-lsb > mod/2 {
			msb -= mod
		}
		poc += msb
	}
	if h.kind == 19 || h.kind == 20 {
		poc = 0
	}
	if poc < math.MinInt32 || poc > math.MaxInt32 {
		return fieldError("POC-range", ErrInvalidBitstream)
	}
	// H.265 7.4.2.2 constrains leading pictures by their associated IRAP,
	// including pictures that are suppressed from output.
	radl, rasl := h.kind == 6 || h.kind == 7, h.kind == 8 || h.kind == 9
	if irap {
		if !reset && r.irapKind != 0 && poc <= r.irapPOC {
			return fieldError("IRAP-picture-order", ErrInvalidBitstream)
		}
		if !reset && r.maxOutputValid && poc <= r.maxOutputPOC {
			return fieldError("IRAP-prior-output-order", ErrInvalidBitstream)
		}
	} else {
		if poc < r.irapPOC {
			if !radl && !rasl || r.trailing || rasl && r.irapKind >= 17 && r.irapKind <= 20 || radl && (r.irapKind == 18 || r.irapKind == 20) {
				return fieldError("IRAP-leading-picture", ErrInvalidBitstream)
			}
		} else {
			if radl || rasl {
				return fieldError("IRAP-trailing-picture", ErrInvalidBitstream)
			}
			r.trailing = true
		}
		if radl {
			if r.priorOutputValid && poc <= r.priorOutputPOC || r.raslSeen && poc <= r.raslPOC {
				return fieldError("RADL-output-order", ErrInvalidBitstream)
			}
			if !r.radlSeen || poc < r.radlPOC {
				r.radlPOC = poc
			}
			r.radlSeen = true
		}
		if rasl {
			if r.radlSeen && poc >= r.radlPOC || r.irapKind == 21 && r.priorIRAPValid && poc <= r.priorIRAPPOC {
				return fieldError("RASL-output-order", ErrInvalidBitstream)
			}
			if !r.raslSeen || poc > r.raslPOC {
				r.raslPOC = poc
			}
			r.raslSeen = true
		}
	}
	if _, ok := r.seen[poc]; ok {
		return fieldError("duplicate-POC", ErrInvalidBitstream)
	}
	if r.seen == nil {
		r.seen = make(map[int64]struct{})
	}
	r.seen[poc] = struct{}{}
	updatesPrevious := h.temporal == 0 && h.kind != 0 && h.kind != 2 && h.kind != 4 && h.kind != 6 && h.kind != 7 && h.kind != 8 && h.kind != 9
	if updatesPrevious {
		r.previousPOC = poc
		r.previousValid = true
	}
	picture.POC = poc
	picture.DecodeOrdinal = r.decode
	r.decode++
	r.first = false
	if err := r.references(h, s, poc, reset && h.kind != 19 && h.kind != 20); err != nil {
		return err
	}
	if s.longTerm {
		// Validate current references against the old previous-T0 history
		// before replacing it with this picture and its actual RPS members.
		if updatesPrevious {
			r.pocHistory = pocHistory{}
			r.pocHistory.remember(poc, s.pocBits)
			r.pocHistory.seed[0], r.pocHistory.seedCount = poc, 1
			for _, ref := range r.queue {
				if ref.reference {
					r.pocHistory.remember(ref.picture.POC, s.pocBits)
					r.pocHistory.seed[r.pocHistory.seedCount] = ref.picture.POC
					r.pocHistory.seedCount++
				}
			}
		} else {
			r.pocHistory.remember(poc, s.pocBits)
		}
	}
	if irap {
		r.priorIRAPPOC, r.priorIRAPValid = r.irapPOC, !reset && r.irapKind != 0
		r.priorOutputPOC, r.priorOutputValid = r.maxOutputPOC, r.maxOutputValid
		r.irapKind, r.irapPOC, r.irapDecode = h.kind, poc, picture.DecodeOrdinal
		r.trailing, r.radlSeen, r.raslSeen = false, false, false
	}
	r.lastTemporal[h.temporal], r.temporalKnown[h.temporal] = picture.DecodeOrdinal, true
	ceiling := s.reorder[s.layers]
	if ceiling > uint64(r.limits.MaxReorderPictures) {
		return fieldError("SPS-reorder-ceiling", ErrResourceLimit)
	}
	latency := uint64(0)
	if s.latency[s.layers] != 0 {
		latency = ceiling + s.latency[s.layers] - 1
	}
	for r.outputCount() > ceiling || r.latencyReached(latency, s.latency[s.layers] != 0) || uint64(len(r.queue)) >= s.buffer[s.layers] {
		if err := r.emit(); err != nil {
			return err
		}
	}
	output := h.output && !((h.kind == 8 || h.kind == 9) && r.noRasl)
	if output {
		if !r.maxOutputValid || poc > r.maxOutputPOC {
			r.maxOutputPOC = poc
		}
		r.maxOutputValid = true
	}
	output = output && !h.suppressed
	if output {
		for i := range r.queue {
			if r.queue[i].output && r.queue[i].picture.POC > poc {
				r.queue[i].latency++
			}
		}
	}
	r.queue = append(r.queue, resolvedPicture{picture: picture, payload: r.payload, output: output, reference: true, temporal: h.temporal, kind: h.kind})
	if r.trace != nil {
		*r.trace = append(*r.trace, picture)
	}
	if h.kind == 4 || h.kind == 5 {
		r.stsa[h.temporal] = picture.DecodeOrdinal
		r.stsaKnown[h.temporal] = true
	}
	for r.outputCount() > ceiling || r.latencyReached(latency, s.latency[s.layers] != 0) {
		if err := r.emit(); err != nil {
			return err
		}
	}
	return nil
}

func (r *resolver) outputCount() uint64 {
	var n uint64
	for _, p := range r.queue {
		if p.output {
			n++
		}
	}
	return n
}
func (r *resolver) latencyReached(limit uint64, enabled bool) bool {
	return enabled && slices.ContainsFunc(r.queue, func(p resolvedPicture) bool { return p.output && p.latency >= limit })
}

func (r *resolver) references(h sliceHeader, s sequence, poc int64, allowUnavailable bool) error {
	var retained [16]bool
	mod := int64(1) << s.pocBits
	for i := 0; i < h.short.count+h.longCount; i++ {
		long := i >= h.short.count
		used := false
		target := int64(0)
		partial := false
		if !long {
			ref := h.short.refs[i]
			used = ref.used
			target = poc + int64(ref.delta)
		} else {
			ref := h.long[i-h.short.count]
			used = ref.used
			partial = !ref.msb
			target = poc - int64(ref.cycle)*mod - int64(h.lsb) + int64(ref.lsb)
			if partial {
				target = int64(ref.lsb)
				if r.pocHistory.lsb[1][ref.lsb>>6]&(uint64(1)<<(ref.lsb&63)) != 0 {
					return fieldError("long-term-POC-history", ErrInvalidBitstream)
				}
			}
		}
		found := -1
		for j, p := range r.queue {
			match := p.picture.POC == target
			if partial {
				match = (p.picture.POC & (mod - 1)) == target
			}
			if p.reference && match && (long || !p.longTerm) {
				if found >= 0 {
					return fieldError("ambiguous-long-term-reference", ErrUnsupportedInput)
				}
				found = j
			}
		}
		if found < 0 {
			if allowUnavailable && !partial {
				if len(r.queue) >= 16 {
					return fieldError("unavailable-reference-count", ErrInvalidBitstream)
				}
				found = len(r.queue)
				r.queue = append(r.queue, resolvedPicture{picture: Picture{POC: target, DecodeOrdinal: r.decode - 1}, reference: true, longTerm: long, kind: 1, unavailable: true})
			} else {
				if used {
					return fieldError("missing-reference-picture", &missingReference{poc: target, partial: partial})
				}
				continue
			}
		}
		p := r.queue[found]
		if retained[found] || p.picture.POC == poc {
			return fieldError("duplicate-RPS-reference", ErrInvalidBitstream)
		}
		// H.265 8.3.2 applies some IRAP constraints to the whole RPS and
		// others only to pictures used by the current picture.
		trailing := h.kind < 16 && poc > r.irapPOC
		beforeIRAP := p.picture.POC < r.irapPOC || p.picture.DecodeOrdinal < r.irapDecode
		if (trailing || h.kind == 21 && r.irapKind != 0) && beforeIRAP || used && (trailing && p.unavailable || (h.kind == 6 || h.kind == 7) && (p.kind == 8 || p.kind == 9 || p.unavailable || p.picture.DecodeOrdinal < r.irapDecode)) {
			return fieldError("IRAP-reference-picture", ErrInvalidBitstream)
		}
		if used && s.temporalNesting {
			for layer := uint8(0); layer < p.temporal; layer++ {
				if r.temporalKnown[layer] && r.lastTemporal[layer] > p.picture.DecodeOrdinal {
					return fieldError("reference-temporal-nesting", ErrInvalidBitstream)
				}
			}
		}
		if used && (p.temporal > h.temporal || p.temporal == h.temporal && p.kind <= 8 && p.kind&1 == 0) || (h.kind == 2 || h.kind == 3) && p.temporal >= h.temporal || (h.kind == 4 || h.kind == 5) && used && p.temporal == h.temporal || used && p.temporal == h.temporal && r.stsaKnown[h.temporal] && p.picture.DecodeOrdinal < r.stsa[h.temporal] {
			return fieldError("reference-temporal-layer", ErrInvalidBitstream)
		}
		if long && poc-p.picture.POC >= 1<<24 {
			return fieldError("long-term-reference-range", ErrInvalidBitstream)
		}
		retained[found] = true
		r.queue[found].longTerm = long
	}
	for i := range r.queue {
		r.queue[i].reference = retained[i]
	}
	r.queue = slices.DeleteFunc(r.queue, func(p resolvedPicture) bool { return !p.output && !p.reference })
	return nil
}

// Count actual luma samples in a raster CTB interval, clipping edge padding.
func sliceLumaSamples(s sequence, start, end uint64) uint64 {
	prefix := func(address uint64) uint64 {
		size := uint64(1) << s.ctbBits
		row, col := address/s.widthCTB, address%s.widthCTB
		top := min(row*size, s.height)
		return top*s.width + min(size, s.height-top)*min(col*size, s.width)
	}
	return prefix(end) - prefix(start)
}

func (r *resolver) resolve(ctx context.Context, c *Clip, occurrence uint64) error {
	var pending *Payload
	var current *sliceHeader
	var active sequence
	var currentPicture Picture
	var currentUpdate *Payload
	currentSuppressed := false
	var segmentCount, sliceStart uint64
	spatialSlice := false
	var au uint64
	markerIndex := 0
	positionIndex := 0
	var position *Marker
	var lastSample bool
	var wrapBase, lastClock int64
	clockKnown := false
	commit := func() error {
		if current == nil {
			return nil
		}
		if spatialSlice && sliceLumaSamples(active, sliceStart, active.ctbs) > 4*active.width*active.height/(active.spatial+4) {
			return fieldError("slice-spatial-segmentation", ErrInvalidBitstream)
		}
		header := *current
		header.suppressed = currentSuppressed
		err := r.add(header, active, currentPicture, currentUpdate)
		current = nil
		currentUpdate = nil
		currentSuppressed = false
		return err
	}
	for _, e := range c.events {
		if err := ctx.Err(); err != nil {
			return err
		}
		for positionIndex < len(c.markers) && c.markers[positionIndex].ESOffset <= e.offset {
			position = &c.markers[positionIndex]
			positionIndex++
		}
		if c.packetRange != nil {
			if position == nil || !position.HasSourcePosition {
				return fieldError("unknown-source-packet", ErrUnsupportedInput)
			}
			selected := position.PacketIndex >= c.packetRange.Start && position.PacketIndex < c.packetRange.End
			for j := positionIndex; j < len(c.markers) && c.markers[j].ESOffset < e.endOffset; j++ {
				m := c.markers[j]
				if !m.HasSourcePosition || selected != (m.PacketIndex >= c.packetRange.Start && m.PacketIndex < c.packetRange.End) {
					return fieldError("NAL-crosses-clock-epoch", ErrUnsupportedInput)
				}
			}
			if !selected && (e.kind < 32 || e.kind > 34) {
				if e.kind <= 31 && len(e.data) > 0 && e.data[0]&0x80 != 0 {
					au++
				}
				continue
			}
		}
		if e.kind >= 32 && e.kind <= 35 || e.kind == 39 {
			if err := commit(); err != nil {
				return err
			}
		}
		switch e.kind {
		case 32:
			v, err := parseVPS(e.data)
			if err != nil {
				return err
			}
			if r.activeVPS != nil && v.id == r.activeVPSID && !bytes.Equal(e.data, r.activeVPS) {
				r.parameterChange = true
			}
			r.vps[v.id] = &v
		case 33:
			s, err := parseSPS(e.data)
			if err != nil {
				return err
			}
			r.sps[s.id] = &s
			if r.activeSPS != nil && s.id == r.activeSPSID && !bytes.Equal(e.data, r.activeSPS) {
				r.parameterChange = true
			}
			r.spsData[s.id] = e.data
		case 34:
			p, err := parsePPS(e.data)
			if err != nil {
				return err
			}
			p.temporal = e.temporal
			r.pps[p.id] = &p
		case 39:
			r.metadataSeen = true
			if pending != nil && !payloadEqual(*pending, *e.update) {
				return fieldError("conflicting-AU-metadata", hdr10plus.ErrInvalidMetadata)
			}
			pending = e.update
		case 36, 37:
			if err := commit(); err != nil {
				return err
			}
			if err := r.flush(); err != nil {
				return err
			}
			r.previousValid = false
			r.first = true
			r.payload = nil
			r.lastPOCValid = false
			r.irapKind = 0
			r.maxOutputValid = false
			r.temporalKnown = [7]bool{}
			r.pocHistory = pocHistory{}
			r.spatialMode = 0
		default:
			if e.kind > 31 {
				continue
			}
			b := bitReader{data: e.data}
			first := b.flag()
			if e.kind >= 16 && e.kind <= 23 {
				b.flag()
			}
			id := b.boundedUE(63)
			if b.err != nil {
				return syntaxError(&b, "slice-prefix")
			}
			p := r.pps[id]
			if p == nil || r.sps[p.sps] == nil || r.vps[r.sps[p.sps].vps] == nil {
				return fieldError("slice-parameter-reference", ErrIncomplete)
			}
			s := *r.sps[p.sps]
			v := r.vps[s.vps]
			if v.layers < s.layers || !s.temporalNesting && v.temporalNesting {
				return fieldError("SPS-VPS-temporal-nesting", ErrInvalidBitstream)
			}
			for i := uint(0); i <= s.layers; i++ {
				if s.buffer[i] > v.buffer[i] || s.reorder[i] > v.reorder[i] || v.latency[i] != 0 && (s.latency[i] == 0 || s.latency[i] > v.latency[i]) {
					return fieldError("SPS-VPS-ordering", ErrInvalidBitstream)
				}
			}
			for i, level := range v.profile.level[:v.layers+1] {
				if v.profile.main[i] && !s.eightBit {
					return fieldError("VPS-Main-bit-depth", ErrInvalidBitstream)
				}
				if level == 255 {
					continue
				}
				limit := limitsForLevel(level)
				if limit.samples == 0 {
					return fieldError("HEVC-level", ErrUnsupportedInput)
				}
				if v.buffer[i] > limit.maxBuffer(s.width*s.height) {
					return fieldError("VPS-picture-buffer", ErrInvalidBitstream)
				}
			}
			h, err := parseSlice(e.data, e.kind, e.temporal, *p, s)
			if err != nil {
				if e.truncated && errors.Is(err, ErrIncomplete) {
					return fieldError("slice-header-bytes", ErrResourceLimit)
				}
				return err
			}
			if first {
				if err := commit(); err != nil {
					return err
				}
				newCVS := h.kind >= 16 && h.kind <= 21 && (r.first || h.kind <= 20)
				// H.265 7.4.2.4.2 keeps active VPS/SPS RBSPs stable for a CVS.
				if !newCVS && r.activeSPS != nil && (r.parameterChange || !bytes.Equal(r.activeSPS, r.spsData[s.id]) || !bytes.Equal(r.activeVPS, v.data)) {
					return fieldError("active-parameter-change", ErrInvalidBitstream)
				}
				r.activeSPS, r.activeVPS = r.spsData[s.id], v.data
				r.activeSPSID, r.activeVPSID = s.id, s.vps
				r.parameterChange = false
				if newCVS {
					r.spatialMode = 0
				}
				if s.spatial != 0 {
					mode := uint8(1) // Slice areas, tiles, or WPP throughout this CVS.
					if p.tiles {
						mode = 2
					} else if p.entropySync {
						mode = 3
					}
					if r.spatialMode != 0 && r.spatialMode != mode {
						return fieldError("CVS-spatial-segmentation-mode", ErrInvalidBitstream)
					}
					r.spatialMode = mode
				}
				current = &h
				active = s
				segmentCount, sliceStart = 0, 0
				spatialSlice = s.spatial != 0 && !p.tiles && !p.entropySync
				currentPicture = Picture{Stream: c.key, AUOrdinal: au, OccurrenceOrdinal: occurrence}
				au++
				currentUpdate = pending
				pending = nil
				var stamp *Marker
				for markerIndex < len(c.markers) && c.markers[markerIndex].ESOffset <= e.offset {
					m := c.markers[markerIndex]
					markerIndex++
					if c.packetRange != nil && (m.PacketIndex < c.packetRange.Start || m.PacketIndex >= c.packetRange.End) {
						continue
					}
					if stamp != nil && stamp.Kind == SampleStart {
						return fieldError("sample-picture-count", ErrUnsupportedInput)
					}
					if stamp != nil && stamp.PTS.Valid && m.PTS.Valid {
						return fieldError("ambiguous-PES-timing", ErrUnsupportedInput)
					}
					stamp = &m
				}
				if lastSample && (stamp == nil || stamp.Kind != SampleStart) {
					return fieldError("sample-picture-count", ErrUnsupportedInput)
				}
				if stamp != nil {
					currentSuppressed = stamp.SuppressOutput
					lastSample = stamp.Kind == SampleStart
					pts := stamp.PTS
					if stamp.Modulo33 {
						clock := stamp.DTS
						if !clock.Valid {
							clock = pts
						}
						if clock.Valid {
							if !clockKnown && c.packetRange != nil {
								diff := c.packetRange.ClockAnchor.Value - clock.Value
								if diff > 1<<32 {
									wrapBase = 1 << 33
								} else if diff < -(1 << 32) {
									wrapBase = -(1 << 33)
								}
							}
							if clockKnown {
								diff := clock.Value - lastClock
								if diff < -(1 << 32) {
									wrapBase += 1 << 33
								} else if diff > 1<<32 {
									wrapBase -= 1 << 33
								}
							}
							lastClock = clock.Value
							clockKnown = true
							if pts.Valid {
								diff := pts.Value - clock.Value
								if diff < -(1 << 32) {
									pts.Value += 1 << 33
								} else if diff > 1<<32 {
									pts.Value -= 1 << 33
								}
								pts.Value += wrapBase
							}
						}
					}
					currentPicture.PTS = pts
				}
			} else {
				if current == nil || h.pps != current.pps || h.kind != current.kind || h.temporal != current.temporal || h.address <= current.address || h.noPrior != current.noPrior || !h.dependent && (h.lsb != current.lsb || h.output != current.output || h.short != current.short || h.long != current.long || h.longCount != current.longCount || h.shortSPS != current.shortSPS || h.shortIndex != current.shortIndex || h.longFromSPS != current.longFromSPS || h.longIndices != current.longIndices || h.temporalMVP != current.temporalMVP || h.collocatedKnown && current.collocatedKnown && h.collocated != current.collocated) {
					return fieldError("slice-picture-association", ErrInvalidBitstream)
				}
				if h.collocatedKnown {
					current.collocatedKnown, current.collocated = true, h.collocated
				}
				if !h.dependent {
					if spatialSlice && sliceLumaSamples(active, sliceStart, h.address) > 4*active.width*active.height/(active.spatial+4) {
						return fieldError("slice-spatial-segmentation", ErrInvalidBitstream)
					}
					sliceStart = h.address
					if s.restrictedLists {
						for list, count := range h.refCounts {
							if count == 0 {
								continue
							}
							previous := current.refCounts[list]
							if previous != 0 && (previous != count || !slices.Equal(current.refLists[list][:previous], h.refLists[list][:count])) {
								return fieldError("restricted-reference-lists", ErrInvalidBitstream)
							}
							current.refCounts[list], current.refLists[list] = count, h.refLists[list]
						}
					}
				}
				current.address = h.address
			}
			segmentCount++
			for _, level := range s.profile.level[h.temporal : s.layers+1] {
				if level != 255 && segmentCount > limitsForLevel(level).slices {
					return fieldError("picture-slice-segments", ErrInvalidBitstream)
				}
			}
		}
	}
	if pending != nil {
		return fieldError("unassociated-prefix-metadata", ErrIncomplete)
	}
	for _, m := range c.markers[markerIndex:] {
		if m.Kind == SampleStart {
			return fieldError("sample-without-picture", ErrIncomplete)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	return nil
}

func makeExtraction(ctx context.Context, pictures []resolvedPicture, l Limits, budget *MemoryBudget, metadataSeen bool, parent *accounting) (*Extraction, error) {
	if len(pictures) == 0 {
		return nil, fieldError("no-output-pictures", ErrIncomplete)
	}
	if int64(len(pictures)) > l.MaxFrames {
		return nil, ErrResourceLimit
	}
	known := false
	unknown := false
	for _, p := range pictures {
		if p.payload == nil {
			unknown = true
		} else {
			known = true
		}
	}
	if !known {
		if metadataSeen {
			return nil, ErrIncomplete
		}
		return nil, ErrNoMetadata
	}
	if unknown {
		return nil, ErrIncomplete
	}
	// Bound the multiplications below before reserving or allocating indexes.
	if int64(len(pictures)) > (l.MaxRetainedBytes-512)/128 {
		return nil, ErrResourceLimit
	}
	a := &accounting{max: l.MaxRetainedBytes, shared: budget, parent: parent}
	// Cover the temporary pointer index, including map growth. Its charge
	// overlaps collected syntax, resolver state and the completed result.
	indexCharge, err := a.reserve(512 + int64(len(pictures))*64)
	if err != nil {
		return nil, err
	}
	defer indexCharge.release()
	indexes := make(map[*Payload]uint32)
	scenes := 1
	for i, p := range pictures {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, exists := indexes[p.payload]; !exists {
			if uint64(len(indexes)) > math.MaxUint32 {
				return nil, ErrResourceLimit
			}
			indexes[p.payload] = uint32(len(indexes))
		}
		if i > 0 && !sceneEqual(*p.payload, *pictures[i-1].payload) {
			scenes++
		}
	}
	// Fixed capacities avoid per-frame payload/scene slots and slice growth.
	// Bounds include 64-bit Picture/Payload layouts, up to ten distributions,
	// a Curve with nine anchors, and rounding of cloned slice capacities.
	bytes := 512 + int64(len(pictures))*128
	if int64(len(indexes)) > (l.MaxRetainedBytes-bytes)/256 {
		return nil, ErrResourceLimit
	}
	bytes += int64(len(indexes)) * 256
	if int64(scenes) > (l.MaxRetainedBytes-bytes)/8 {
		return nil, ErrResourceLimit
	}
	bytes += int64(scenes) * 8
	if _, err := a.reserve(bytes); err != nil {
		return nil, err
	}
	e := &Extraction{Frames: make([]Picture, len(pictures)), Payloads: make([]Payload, 0, len(indexes)), SceneStarts: make([]uint64, 0, scenes), Profile: pictures[0].payload.profile(), memory: a}
	for i, p := range pictures {
		if err := ctx.Err(); err != nil {
			a.release()
			return nil, err
		}
		index := indexes[p.payload]
		if int(index) == len(e.Payloads) {
			e.Payloads = append(e.Payloads, clonePayload(*p.payload))
		}
		f := p.picture
		f.PayloadIndex = index
		e.Frames[i] = f
		if p.payload.profile() != e.Profile {
			e.Profile = "N/A"
		}
		if i == 0 || !sceneEqual(*p.payload, *pictures[i-1].payload) {
			e.SceneStarts = append(e.SceneStarts, uint64(i))
		}
	}
	return e, nil
}
