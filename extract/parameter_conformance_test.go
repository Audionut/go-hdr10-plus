package extract

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

// Independently encode complete supported H.265 7.3.2.2/7.3.2.3 syntax.
// The expected classification comes from 7.4.3.2/7.4.3.3/7.4.5, never parser output.
type parameterSPS struct {
	width, height, minCB, ctb, minTB, diffTB, interDepth, intraDepth uint64
	crop                                                             bool
	crops                                                            [4]uint64
	pcm                                                              bool
	pcmY, pcmC, pcmMin, pcmDiff                                      uint64
	scaling                                                          string
	scalingEnabled                                                   bool
	bufferMinus, reorder                                             uint64
	ptl                                                              string
	depthY, depthC                                                   uint64
	vui                                                              string
	longTerm                                                         bool
}

func defaultParameterSPS() parameterSPS {
	return parameterSPS{width: 128, height: 64, minCB: 3, ctb: 6, diffTB: 3, bufferMinus: 3, reorder: 2, depthY: 2, depthC: 2}
}
func parameterSPSBits(s parameterSPS) string {
	ptl := s.ptl
	if ptl == "" {
		ptl = main10PTL(120)
	}
	h := "00100001" + ptl + ueBits(3) + ueBits(1) + ueBits(s.width) + ueBits(s.height)
	if s.crop {
		h += "1"
		for _, v := range s.crops {
			h += ueBits(v)
		}
	} else {
		h += "0"
	}
	h += ueBits(s.depthY) + ueBits(s.depthC) + ueBits(4) + "1" + ueBits(s.bufferMinus) + ueBits(s.reorder) + ueBits(0) + ueBits(s.minCB-3) + ueBits(s.ctb-s.minCB)
	h += ueBits(s.minTB) + ueBits(s.diffTB) + ueBits(s.interDepth) + ueBits(s.intraDepth)
	if s.scaling != "" {
		h += "11" + s.scaling
	} else if s.scalingEnabled {
		h += "10"
	} else {
		h += "0"
	}
	h += "00"
	if s.pcm {
		h += "1" + fmt.Sprintf("%04b%04b", s.pcmY, s.pcmC) + ueBits(s.pcmMin) + ueBits(s.pcmDiff) + "0"
	} else {
		h += "0"
	}
	h += ueBits(0)
	if s.longTerm {
		h += "1" + ueBits(0)
	} else {
		h += "0"
	}
	h += "00"
	if s.vui != "" {
		h += "1" + s.vui
	} else {
		h += "0"
	}
	return h + "0"
}
func parameterPPSBits(scaling string, qpDepth *uint64, merge uint64) string {
	h := ueBits(5) + ueBits(3) + "1100000" + ueBits(0) + ueBits(0) + seBits(0) + "00"
	if qpDepth != nil {
		h += "1" + ueBits(*qpDepth)
	} else {
		h += "0"
	}
	h += seBits(0) + seBits(0) + "00000000"
	if scaling != "" {
		h += "1" + scaling
	} else {
		h += "0"
	}
	return h + "0" + ueBits(merge) + "00"
}
func parameterSource(t *testing.T, s parameterSPS, pps string) []byte {
	sets, meta, picture := headerFixtureParts(t)
	vps := sets[:bytes.Index(sets, []byte{0, 0, 1, 66, 1})]
	return bytes.Join([][]byte{vps, authoredNAL(33, packedHeader(parameterSPSBits(s)+"1")), authoredNAL(34, packedHeader(pps+"1")), meta, picture}, nil)
}
func checkParameterExtraction(t *testing.T, raw []byte, valid bool) {
	t.Helper()
	e, err := Extract(t.Context(), bytes.NewReader(raw), Options{})
	if err == nil {
		defer e.memory.release()
		checkIRAPFrames(t, e, []int64{0})
		if e.Profile != "A" || len(e.SceneStarts) != 1 || e.SceneStarts[0] != 0 {
			t.Fatalf("incorrect full result %+v", e)
		}
	}
	if valid {
		if err != nil {
			t.Fatalf("LAWFUL control rejected: %v", err)
		}
	} else if !errors.Is(err, ErrInvalidBitstream) {
		t.Errorf("PROHIBITED supported header ACCEPTED; result=%v error=%v", e != nil, err)
	}
}
func TestParameterSPSConstraints(t *testing.T) {
	type item struct {
		name   string
		mutate func(*parameterSPS)
		valid  bool
	}
	for _, tc := range []item{
		{"baseline", func(s *parameterSPS) {}, true},
		{"width-not-minCB-multiple", func(s *parameterSPS) { s.width = 130 }, false},
		{"height-not-minCB-multiple", func(s *parameterSPS) { s.height = 66 }, false},
		{"width-minCB-multiple", func(s *parameterSPS) { s.width = 136 }, true},
		{"height-minCB-multiple", func(s *parameterSPS) { s.height = 72 }, true},
		{"horizontal-crop-empty", func(s *parameterSPS) { s.crop = true; s.crops[0] = 32; s.crops[1] = 32 }, false},
		{"horizontal-crop-positive", func(s *parameterSPS) { s.crop = true; s.crops[0] = 32; s.crops[1] = 31 }, true},
		{"vertical-crop-empty", func(s *parameterSPS) { s.crop = true; s.crops[2] = 16; s.crops[3] = 16 }, false},
		{"vertical-crop-positive", func(s *parameterSPS) { s.crop = true; s.crops[2] = 16; s.crops[3] = 15 }, true},
		{"minTB-equals-minCB", func(s *parameterSPS) { s.minTB = 1; s.diffTB = 2 }, false},
		{"minTB-less-minCB", func(s *parameterSPS) { s.minCB = 4; s.minTB = 1; s.diffTB = 2 }, true},
		{"maxTB-above-CTB", func(s *parameterSPS) { s.ctb = 4; s.diffTB = 3 }, false},
		{"maxTB-equals-CTB16", func(s *parameterSPS) { s.ctb = 4; s.diffTB = 2 }, true},
		{"maxTB-above32", func(s *parameterSPS) { s.minCB = 4; s.minTB = 1; s.diffTB = 3 }, false},
		{"maxTB32", func(s *parameterSPS) { s.minCB = 4; s.minTB = 1; s.diffTB = 2 }, true},
		{"inter-depth-above-bound", func(s *parameterSPS) { s.interDepth = 5 }, false},
		{"inter-depth-inclusive", func(s *parameterSPS) { s.interDepth = 4 }, true},
		{"intra-depth-above-bound", func(s *parameterSPS) { s.intraDepth = 5 }, false},
		{"intra-depth-inclusive", func(s *parameterSPS) { s.intraDepth = 4 }, true},
		{"PCM-depth-luma11", func(s *parameterSPS) { s.pcm = true; s.pcmY = 10; s.pcmC = 9 }, false},
		{"PCM-depth-chroma11", func(s *parameterSPS) { s.pcm = true; s.pcmY = 9; s.pcmC = 10 }, false},
		{"PCM-depth10", func(s *parameterSPS) { s.pcm = true; s.pcmY = 9; s.pcmC = 9 }, true},
		{"PCM-min-above-CTB", func(s *parameterSPS) {
			s.pcm = true
			s.pcmY = 9
			s.pcmC = 9
			s.ctb = 4
			s.diffTB = 2
			s.pcmMin = 2
		}, false},
		{"PCM-min-at-CTB", func(s *parameterSPS) {
			s.pcm = true
			s.pcmY = 9
			s.pcmC = 9
			s.ctb = 4
			s.diffTB = 2
			s.pcmMin = 1
		}, true},
		{"PCM-min-below-minCB", func(s *parameterSPS) { s.pcm = true; s.pcmY = 9; s.pcmC = 9; s.minCB = 4 }, false},
		{"PCM-min-at-minCB", func(s *parameterSPS) { s.pcm = true; s.pcmY = 9; s.pcmC = 9; s.minCB = 4; s.pcmMin = 1 }, true},
		{"PCM-max-above32", func(s *parameterSPS) { s.pcm = true; s.pcmY = 9; s.pcmC = 9; s.pcmDiff = 3 }, false},
		{"PCM-max32", func(s *parameterSPS) { s.pcm = true; s.pcmY = 9; s.pcmC = 9; s.pcmDiff = 2 }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			tc.mutate(&s)
			checkParameterExtraction(t, parameterSource(t, s, conformancePPS(0, 0, 0, false)), tc.valid)
		})
	}
}
func parameterScaling(sizeTarget int, dc, firstDelta int64, predictionDelta uint64, prediction bool) string {
	out := ""
	for size := range 4 {
		step := 1
		if size == 3 {
			step = 3
		}
		for matrix := 0; matrix < 6; matrix += step {
			if size == sizeTarget && matrix == 3 && prediction {
				out += "0" + ueBits(predictionDelta)
				continue
			}
			if size != sizeTarget || matrix != 0 || prediction {
				out += "0" + ueBits(0)
				continue
			}
			out += "1"
			if size > 1 {
				out += seBits(dc)
			}
			for i := range min(64, 1<<(4+2*size)) {
				v := int64(0)
				if i == 0 {
					v = firstDelta
				}
				out += seBits(v)
			}
		}
	}
	return out
}
func TestParameterScalingConstraints(t *testing.T) {
	for _, owner := range []string{"SPS", "PPS"} {
		for _, tc := range []struct {
			name              string
			size              int
			dc, delta         int64
			pred              uint64
			prediction, valid bool
		}{
			{"delta-upper127", 0, 0, 127, 0, false, true},
			{"delta-above128", 0, 0, 128, 0, false, false},
			{"delta-lower-minus128", 0, 0, -128, 0, false, true},
			{"delta-below-minus129", 0, 0, -129, 0, false, false},
			{"zero-coefficient", 0, 0, -8, 0, false, false},
			{"positive-coefficient1", 0, 0, -7, 0, false, true},
			{"DC-lower-minus7", 2, -7, 0, 0, false, true},
			{"DC-below-minus8", 2, -8, 0, 0, false, false},
			{"DC-upper247", 2, 247, 0, 0, false, true},
			{"DC-above248", 2, 248, 0, 0, false, false},
			{"size3-prediction1", 3, 0, 0, 1, true, true},
			{"size3-prediction2", 3, 0, 0, 2, true, false},
		} {
			t.Run(owner+"/"+tc.name, func(t *testing.T) {
				scaling := parameterScaling(tc.size, tc.dc, tc.delta, tc.pred, tc.prediction)
				s := defaultParameterSPS()
				pps := conformancePPS(0, 0, 0, false)
				if owner == "SPS" {
					s.scaling = scaling
				} else {
					s.scalingEnabled = true
					pps = parameterPPSBits(scaling, nil, 0)
				}
				checkParameterExtraction(t, parameterSource(t, s, pps), tc.valid)
			})
		}
	}
	for _, enabled := range []bool{false, true} {
		name := "PPS-present-SPS-disabled"
		if enabled {
			name = "PPS-present-SPS-enabled"
		}
		t.Run(name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.scalingEnabled = enabled
			checkParameterExtraction(t, parameterSource(t, s, parameterPPSBits(parameterScaling(0, 0, 0, 0, false), nil, 0)), enabled)
		})
	}
}
func TestParameterPPSGeometry(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		minCB, ctb, depth, merge uint64
		qp, valid                bool
	}{
		{"QPdepth-equals-diff", 4, 6, 2, 0, true, true},
		{"QPdepth-above-diff", 4, 6, 3, 0, true, false},
		{"merge-equals-CTB16-bound", 3, 4, 0, 2, false, true},
		{"merge-above-CTB16-bound", 3, 4, 0, 3, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.minCB = tc.minCB
			s.ctb = tc.ctb
			s.diffTB = min(uint64(5), s.ctb) - 2
			var depth *uint64
			if tc.qp {
				depth = &tc.depth
			}
			checkParameterExtraction(t, parameterSource(t, s, parameterPPSBits("", depth, tc.merge)), tc.valid)
		})
	}
}

func parameterLayerSource(t *testing.T, vps, sps [2][3]uint64, inferVPS, inferSPS bool) []byte {
	_, meta, picture := headerFixtureParts(t)
	ptl := main10PTL(120) + "0000000000000000"
	v := "0010110000000010" + "1111111111111111" + ptl
	s := "00100010" + ptl + ueBits(3) + ueBits(1) + ueBits(128) + ueBits(64) + "0" + ueBits(2) + ueBits(2) + ueBits(4)
	for _, writer := range []struct {
		out    *string
		tables [2][3]uint64
		infer  bool
	}{{&v, vps, inferVPS}, {&s, sps, inferSPS}} {
		first := 0
		if writer.infer {
			*writer.out += "0"
			first = 1
		} else {
			*writer.out += "1"
		}
		for i := first; i < 2; i++ {
			for _, x := range writer.tables[i] {
				*writer.out += ueBits(x)
			}
		}
	}
	v += "000000" + ueBits(0) + "00"
	s += ueBits(0) + ueBits(3) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(0) + "0000" + ueBits(0) + "00000"
	return bytes.Join([][]byte{authoredNAL(32, packedHeader(v+"1")), authoredNAL(33, packedHeader(s+"1")), authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1")), meta, picture}, nil)
}
func TestParameterOrdering(t *testing.T) {
	broad := [2][3]uint64{{4, 3, 0}, {4, 3, 0}}
	legal := [2][3]uint64{{3, 2, 0}, {3, 2, 0}}
	for _, tc := range []struct {
		name                      string
		vps, sps                  [2][3]uint64
		inferVPS, inferSPS, valid bool
	}{
		{"equal-explicit", legal, legal, false, false, true},
		{"equal-inferred", legal, legal, true, true, true},
		{"SPS-buffer-decreases", broad, [2][3]uint64{{4, 2, 0}, {3, 2, 0}}, false, false, false},
		{"SPS-reorder-decreases", broad, [2][3]uint64{{4, 3, 0}, {4, 2, 0}}, false, false, false},
		{"SPS-buffer-exceeds-VPS", legal, [2][3]uint64{{4, 2, 0}, {4, 2, 0}}, false, false, false},
		{"SPS-reorder-exceeds-VPS", legal, [2][3]uint64{{3, 3, 0}, {3, 3, 0}}, false, false, false},
		{"SPS-latency0-VPS1", [2][3]uint64{{3, 2, 1}, {3, 2, 1}}, legal, false, false, false},
		{"SPS-latency2-VPS1", [2][3]uint64{{3, 2, 1}, {3, 2, 1}}, [2][3]uint64{{3, 2, 2}, {3, 2, 2}}, false, false, false},
		{"SPS-latency1-VPS1", [2][3]uint64{{3, 2, 1}, {3, 2, 1}}, [2][3]uint64{{3, 2, 1}, {3, 2, 1}}, false, false, true},
		{"SPS-below-VPS", broad, legal, false, false, true},
		{"VPS-buffer-decreases", [2][3]uint64{{4, 2, 0}, {3, 2, 0}}, legal, false, false, false},
		{"VPS-reorder-decreases", [2][3]uint64{{3, 3, 0}, {3, 2, 0}}, legal, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkParameterExtraction(t, parameterLayerSource(t, tc.vps, tc.sps, tc.inferVPS, tc.inferSPS), tc.valid)
		})
	}
	// Level4 MaxLumaPs=2,228,224. Independently evaluate A-2 at each
	// picture-size regime. VPS declarations match SPS so only the size-dependent
	// DPB bound is crossed. All dimensions are lawful MinCB8 multiples.
	for _, tc := range []struct {
		name                  string
		width, height, buffer uint64
		valid                 bool
	}{
		{"DPB16-small-at", 1024, 512, 16, true},
		{"DPB12-half-at", 1024, 1024, 12, true},
		{"DPB12-half-above", 1024, 1024, 13, false},
		{"DPB8-threequarters-at", 1536, 1024, 8, true},
		{"DPB8-threequarters-above", 1536, 1024, 9, false},
		{"DPB6-full-at", 2048, 1024, 6, true},
		{"DPB6-full-above", 2048, 1024, 7, false},
		{"DPB-quarter-exact16", 1024, 544, 16, true},
		{"DPB-quarter-plus-MinCB12", 1024, 552, 12, true},
		{"DPB-quarter-plus-MinCB13", 1024, 552, 13, false},
		{"DPB-half-exact12", 1024, 1088, 12, true},
		{"DPB-half-exact13", 1024, 1088, 13, false},
		{"DPB-half-plus-MinCB8", 1024, 1096, 8, true},
		{"DPB-half-plus-MinCB9", 1024, 1096, 9, false},
		{"DPB-threequarters-exact8", 1024, 1632, 8, true},
		{"DPB-threequarters-exact9", 1024, 1632, 9, false},
		{"DPB-threequarters-plus-MinCB6", 1024, 1640, 6, true},
		{"DPB-threequarters-plus-MinCB7", 1024, 1640, 7, false},
		{"DPB-MaxLumaPs-exact6", 1024, 2176, 6, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.width = tc.width
			s.height = tc.height
			s.bufferMinus = tc.buffer - 1
			raw := parameterSource(t, s, conformancePPS(0, 0, 0, false))
			end := bytes.Index(raw, []byte{0, 0, 1, 66, 1})
			vps := "0010110000000001" + "1111111111111111" + main10PTL(150) + "1" + ueBits(s.bufferMinus) + ueBits(s.reorder) + ueBits(0) + "000000" + ueBits(0) + "00"
			raw = append(authoredNAL(32, packedHeader(vps+"1")), raw[end:]...)
			checkParameterExtraction(t, raw, tc.valid)
		})
	}
	for _, tc := range []struct {
		name        string
		layers      uint64
		levelFlags  string
		levelSuffix string
		buffers     []uint64
		valid       bool
	}{
		{"DPB-sublevel-absent-infers-general16", 1, "00", "", []uint64{15, 15}, true},
		{"DPB-sublevel3-explicit6", 1, "01", "01011010", []uint64{5, 15}, true},
		{"DPB-sublevel3-explicit7", 1, "01", "01011010", []uint64{6, 15}, false},
		{"DPB-sublevel-absent-infers-next6", 2, "0001", "01011010", []uint64{5, 5, 15}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, meta, picture := headerFixtureParts(t)
			padding := fmt.Sprintf("%0*b", (8-tc.layers)*2, 0)
			ptl := main10PTL(120) + tc.levelFlags + padding + tc.levelSuffix
			v := "001011000000" + fmt.Sprintf("%03b", tc.layers) + "1" + "1111111111111111" + ptl + "1"
			s := "0010" + fmt.Sprintf("%03b", tc.layers) + "1" + ptl + ueBits(3) + ueBits(1) + ueBits(1024) + ueBits(512) + "0" + ueBits(2) + ueBits(2) + ueBits(4) + "1"
			for _, n := range tc.buffers {
				table := ueBits(n) + ueBits(2) + ueBits(0)
				v += table
				s += table
			}
			v += "000000" + ueBits(0) + "00"
			s += ueBits(0) + ueBits(3) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(0) + "0000" + ueBits(0) + "00000"
			raw := bytes.Join([][]byte{authoredNAL(32, packedHeader(v+"1")), authoredNAL(33, packedHeader(s+"1")), authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1")), meta, picture}, nil)
			checkParameterExtraction(t, raw, tc.valid)
		})
	}
}

func TestParameterReferenceLimits(t *testing.T) {
	// Nine real prior TRAIL_R pictures retained in the RPS, no unavailable or
	// same-temporal SLNR references. Main10 level4 permits16 DPB buffers at this
	// small picture size; A.4.1(e) independently caps used current references8.
	for _, tc := range []struct {
		name                      string
		count                     int
		slice                     uint64
		oneUnused, level85, valid bool
	}{
		{"current8", 8, 1, false, false, true},
		{"current9", 9, 1, false, false, false},
		{"RPS9-current8", 9, 1, true, false, true},
		{"I-current8", 8, 2, false, false, true},
		{"I-current9", 9, 2, false, false, false},
		{"level8.5-current9", 9, 1, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.bufferMinus = 15
			ptl := main10PTL(120)
			if tc.level85 {
				ptl = "001" + main10PTL(255)[3:]
				s.ptl = ptl
			}
			raw := parameterSource(t, s, conformancePPS(0, 0, 0, false))
			end := bytes.Index(raw, []byte{0, 0, 1, 66, 1})
			v := "0010110000000001" + "1111111111111111" + ptl + "1" + ueBits(15) + ueBits(2) + ueBits(0) + "000000" + ueBits(0) + "00"
			raw = append(authoredNAL(32, packedHeader(v+"1")), raw[end:]...)
			for poc := uint64(1); poc <= 8; poc++ {
				refs := make([]shortReference, poc)
				for j := range refs {
					refs[j] = shortReference{delta: int16(-j - 1)}
				}
				raw = append(raw, authoredReferencePicture(poc, 1, 2, refs, nil)...)
			}
			refs := make([]shortReference, tc.count)
			for j := range refs {
				refs[j] = shortReference{delta: int16(-j - 1), used: !(tc.oneUnused && j == tc.count-1)}
			}
			raw = append(raw, authoredReferencePicture(9, 1, tc.slice, refs, nil)...)
			e, err := Extract(t.Context(), bytes.NewReader(raw), Options{})
			if err == nil {
				defer e.memory.release()
				checkIRAPFrames(t, e, []int64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
				if e.Profile != "A" || len(e.SceneStarts) != 1 {
					t.Fatal("incorrect complete reference result")
				}
			}
			if tc.valid {
				if err != nil {
					t.Fatalf("LAWFUL control rejected %v", err)
				}
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Errorf("PROHIBITED current-reference count ACCEPTED; result=%v error=%v", e != nil, err)
			}
		})
	}
}

func TestParameterProfileGeometry(t *testing.T) {
	main := func(profile uint64, compat uint64) string {
		return "000" + fmt.Sprintf("%05b%032b", profile, compat) + "1001" + fmt.Sprintf("%044b", 0) + "01111000"
	}
	for _, tc := range []struct {
		name                  string
		profile, compat, y, c uint64
		valid                 bool
	}{
		{"Main8", 1, 1 << 30, 0, 0, true},
		{"Main9-luma", 1, 1 << 30, 1, 0, false},
		{"Main9-chroma", 1, 1 << 30, 0, 1, false},
		{"Main10-compatMain8", 2, (1 << 30) | (1 << 29), 0, 0, true},
		{"Main10-compatMain10", 2, (1 << 30) | (1 << 29), 2, 2, false},
		{"Main10-depth9", 2, 1 << 29, 1, 1, true},
		{"Main10-mixed-depth8and10", 2, 1 << 29, 0, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.ptl = main(tc.profile, tc.compat)
			s.depthY = tc.y
			s.depthC = tc.c
			checkParameterExtraction(t, parameterSource(t, s, conformancePPS(0, 0, 0, false)), tc.valid)
		})
	}
	for _, tc := range []struct {
		name                                       string
		width, height, ctb, cols, rows, firstWidth uint64
		explicit, entropy, valid                   bool
	}{
		{"tile-column256", 512, 128, 6, 2, 1, 0, false, false, true},
		{"tile-column192", 448, 128, 6, 2, 1, 0, false, false, false},
		{"tile-partial-last-CTB-column256", 504, 128, 6, 2, 1, 0, false, false, true},
		{"tile-explicit-column256", 576, 128, 6, 2, 1, 4, true, false, true},
		{"tile-explicit-column192", 576, 128, 6, 2, 1, 3, true, false, false},
		{"tile-row64-CTB16", 512, 128, 4, 2, 2, 0, false, false, true},
		{"tile-row48-CTB16", 512, 112, 4, 2, 2, 0, false, false, false},
		{"level4-tile-cols5", 1280, 128, 6, 5, 1, 0, false, false, true},
		{"level4-tile-cols6", 1536, 128, 6, 6, 1, 0, false, false, false},
		{"level4-tile-rows5", 512, 320, 6, 2, 5, 0, false, false, true},
		{"level4-tile-rows6", 512, 384, 6, 2, 6, 0, false, false, false},
		{"tiles-and-entropy-sync", 512, 128, 6, 2, 1, 0, false, true, false},
		{"entropy-sync-alone", 128, 64, 6, 1, 1, 0, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.width = tc.width
			s.height = tc.height
			s.ctb = tc.ctb
			s.diffTB = min(uint64(5), tc.ctb) - 2
			p := ueBits(5) + ueBits(3) + "1100000" + ueBits(0) + ueBits(0) + seBits(0) + "000" + seBits(0) + seBits(0) + "0000"
			tiles := tc.cols > 1 || tc.rows > 1
			if tiles {
				p += "1"
			} else {
				p += "0"
			}
			if tc.entropy {
				p += "1"
			} else {
				p += "0"
			}
			if tiles {
				p += ueBits(tc.cols-1) + ueBits(tc.rows-1)
				if tc.explicit {
					p += "0" + ueBits(tc.firstWidth-1)
				} else {
					p += "1"
				}
				p += "0"
			}
			p += "0000" + ueBits(0) + "00"
			raw := parameterSource(t, s, p)
			start := bytes.Index(raw, []byte{0, 0, 1, 38, 1})
			picture := "11" + ueBits(5) + ueBits(2) + "1" + seBits(0) + ueBits(0) + "1"
			raw = append(raw[:start], authoredNAL(19, append(packedHeader(picture), 0xff, 0xff))...)
			checkParameterExtraction(t, raw, tc.valid)
		})
	}
}

func TestParameterLevelGeometry(t *testing.T) {
	for _, tc := range []struct {
		name               string
		width, height, ctb uint64
		level              uint8
		valid              bool
	}{
		{"level4-MaxLuma-exact", 4096, 544, 6, 120, true},
		{"level4-MaxLuma-above", 4096, 552, 6, 120, false},
		{"level4-width-nearest-MinCB-below", 4216, 8, 6, 120, true},
		{"level4-width-nearest-MinCB-above", 4224, 8, 6, 120, false},
		{"level4-height-nearest-MinCB-below", 8, 4216, 6, 120, true},
		{"level4-height-nearest-MinCB-above", 8, 4224, 6, 120, false},
		{"level4-CTB16", 128, 64, 4, 120, true},
		{"level5-CTB16", 128, 64, 4, 150, false},
		{"level5-CTB32", 128, 64, 5, 150, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.width = tc.width
			s.height = tc.height
			s.ctb = tc.ctb
			s.diffTB = min(uint64(5), s.ctb) - 2
			s.ptl = main10PTL(tc.level)
			raw := parameterSource(t, s, conformancePPS(0, 0, 0, false))
			end := bytes.Index(raw, []byte{0, 0, 1, 66, 1})
			v := "0010110000000001" + "1111111111111111" + main10PTL(tc.level) + "1" + ueBits(3) + ueBits(2) + ueBits(0) + "000000" + ueBits(0) + "00"
			raw = append(authoredNAL(32, packedHeader(v+"1")), raw[end:]...)
			checkParameterExtraction(t, raw, tc.valid)
		})
	}
	for _, tc := range []struct {
		name  string
		count int
		valid bool
	}{{"level4-segments75", 75, true}, {"level4-segments76", 76, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.width = 1280
			s.height = 256
			raw := parameterSource(t, s, conformancePPS(0, 0, 0, false))
			start := bytes.Index(raw, []byte{0, 0, 1, 38, 1})
			raw = raw[:start]
			for i := range tc.count {
				h := "10" + ueBits(5)
				if i > 0 {
					h = "00" + ueBits(5) + "0" + fmt.Sprintf("%07b", i)
				}
				h += ueBits(2) + "1" + seBits(0) + "1"
				raw = append(raw, authoredNAL(19, append(packedHeader(h), 0xff, 0xff))...)
			}
			checkParameterExtraction(t, raw, tc.valid)
		})
	}
	t.Run("level8.5-wide-CTB16", func(t *testing.T) {
		s := defaultParameterSPS()
		s.width = 4194304
		s.ctb = 4
		s.diffTB = 2
		s.ptl = "001" + main10PTL(255)[3:]
		raw := parameterSource(t, s, conformancePPS(0, 0, 0, false))
		end := bytes.Index(raw, []byte{0, 0, 1, 66, 1})
		v := "0010110000000001" + "1111111111111111" + s.ptl + "1" + ueBits(3) + ueBits(2) + ueBits(0) + "000000" + ueBits(0) + "00"
		raw = append(authoredNAL(32, packedHeader(v+"1")), raw[end:]...)
		checkParameterExtraction(t, raw, true)
	})
}

func TestParameterSliceLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tiles bool
		count uint64
		valid bool
	}{
		{"WPP-entrypoints1", false, 1, true},
		{"WPP-entrypoints2", false, 2, false},
		{"tiles-entrypoints3", true, 3, true},
		{"tiles-entrypoints4", true, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.height = 128
			p := ueBits(5) + ueBits(3) + "1100000" + ueBits(0) + ueBits(0) + seBits(0) + "000" + seBits(0) + seBits(0) + "0000"
			if tc.tiles {
				s.width = 512
				p += "10" + ueBits(1) + ueBits(1) + "10"
			} else {
				p += "01"
			}
			p += "0000" + ueBits(0) + "00"
			raw := parameterSource(t, s, p)
			start := bytes.Index(raw, []byte{0, 0, 1, 38, 1})
			h := "10" + ueBits(5) + ueBits(2) + "1" + seBits(0) + ueBits(tc.count) + ueBits(0) + fmt.Sprintf("%0*b", tc.count, 0) + "1"
			raw = append(raw[:start], authoredNAL(19, append(packedHeader(h), 0xff, 0xff, 0xff, 0xff, 0xff))...)
			checkParameterExtraction(t, raw, tc.valid)
		})
	}
	for _, tc := range []struct {
		name         string
		luma, chroma int
		valid        bool
	}{
		{"weighted-components23", 15, 4, true},
		{"weighted-components24", 14, 5, true},
		{"weighted-components25", 15, 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ueBits(5) + ueBits(3) + "1100000" + ueBits(14) + ueBits(0) + seBits(0) + "000" + seBits(0) + seBits(0) + "01000000" + "00" + ueBits(0) + "00"
			raw := parameterSource(t, defaultParameterSPS(), p)
			h := "1" + ueBits(5) + ueBits(1) + "1" + "00000001" + "0" + ueBits(1) + ueBits(0) + ueBits(0) + "1" + "0" + ueBits(0) + seBits(0)
			for i := range 15 {
				if i < tc.luma {
					h += "1"
				} else {
					h += "0"
				}
			}
			for i := range 15 {
				if i < tc.chroma {
					h += "1"
				} else {
					h += "0"
				}
			}
			for i := range 15 {
				if i < tc.luma {
					h += seBits(0) + seBits(0)
				}
				if i < tc.chroma {
					h += seBits(0) + seBits(0) + seBits(0) + seBits(0)
				}
			}
			h += ueBits(0) + seBits(0) + "1"
			raw = append(raw, authoredNAL(1, append(packedHeader(h), 0xff, 0xff))...)
			e, err := Extract(t.Context(), bytes.NewReader(raw), Options{})
			if err == nil {
				defer e.memory.release()
				checkIRAPFrames(t, e, []int64{0, 1})
				if e.Profile != "A" || len(e.SceneStarts) != 1 {
					t.Fatal("incorrect complete weighted result")
				}
			}
			if tc.valid {
				if err != nil {
					t.Fatalf("LAWFUL control rejected %v", err)
				}
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Errorf("PROHIBITED weighted-component count ACCEPTED; result=%v error=%v", e != nil, err)
			}
		})
	}
	for _, luma0 := range []int{4, 5} {
		name := "B-weighted-components24"
		valid := true
		if luma0 == 5 {
			name = "B-weighted-components25"
			valid = false
		}
		t.Run(name, func(t *testing.T) {
			p := ueBits(5) + ueBits(3) + "1100000" + ueBits(7) + ueBits(7) + seBits(0) + "000" + seBits(0) + seBits(0) + "00100000" + "00" + ueBits(0) + "00"
			raw := parameterSource(t, defaultParameterSPS(), p)
			h := "1" + ueBits(5) + ueBits(0) + "1" + "00000001" + "0" + ueBits(1) + ueBits(0) + ueBits(0) + "1" + "00" + ueBits(0) + seBits(0)
			for _, luma := range []int{luma0, 4} {
				for i := range 8 {
					if i < luma {
						h += "1"
					} else {
						h += "0"
					}
				}
				for i := range 8 {
					if i < 4 {
						h += "1"
					} else {
						h += "0"
					}
				}
				for i := range 8 {
					if i < luma {
						h += seBits(0) + seBits(0)
					}
					if i < 4 {
						h += seBits(0) + seBits(0) + seBits(0) + seBits(0)
					}
				}
			}
			h += ueBits(0) + seBits(0) + "1"
			raw = append(raw, authoredNAL(1, append(packedHeader(h), 0xff, 0xff))...)
			e, err := Extract(t.Context(), bytes.NewReader(raw), Options{})
			if err == nil {
				defer e.memory.release()
				checkIRAPFrames(t, e, []int64{0, 1})
				if e.Profile != "A" || len(e.SceneStarts) != 1 {
					t.Fatal("incorrect complete weighted B result")
				}
			}
			if valid {
				if err != nil {
					t.Fatalf("LAWFUL control rejected %v", err)
				}
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Errorf("PROHIBITED weighted-B aggregate ACCEPTED; result=%v error=%v", e != nil, err)
			}
		})
	}
}

func TestParameterVUIConstraints(t *testing.T) {
	vui := func(chroma *[2]uint64, display *[4]uint64, restr *[5]uint64, hrd *uint64) string {
		h := "000"
		if chroma != nil {
			h += "1" + ueBits(chroma[0]) + ueBits(chroma[1])
		} else {
			h += "0"
		}
		h += "000"
		if display != nil {
			h += "1"
			for _, v := range display {
				h += ueBits(v)
			}
		} else {
			h += "0"
		}
		if hrd != nil {
			h += "1" + fmt.Sprintf("%032b%032b", 1, 24) + "01" + "00" + "1" + ueBits(*hrd) + ueBits(0)
		} else {
			h += "0"
		}
		if restr != nil {
			h += "1000"
			for _, v := range restr {
				h += ueBits(v)
			}
		} else {
			h += "0"
		}
		return h
	}
	for _, tc := range []struct {
		name  string
		y, c  uint64
		valid bool
	}{{"chroma-location5", 5, 5, true}, {"chroma-location-top6", 6, 5, false}, {"chroma-location-bottom6", 5, 6, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.vui = vui(&[2]uint64{tc.y, tc.c}, nil, nil, nil)
			checkParameterExtraction(t, parameterSource(t, s, conformancePPS(0, 0, 0, false)), tc.valid)
		})
	}
	for _, tc := range []struct {
		name    string
		display [4]uint64
		crop    bool
		valid   bool
	}{
		{"display-horizontal-positive", [4]uint64{32, 31, 0, 0}, false, true},
		{"display-horizontal-empty", [4]uint64{32, 32, 0, 0}, false, false},
		{"display-vertical-positive", [4]uint64{0, 0, 16, 15}, false, true},
		{"display-vertical-empty", [4]uint64{0, 0, 16, 16}, false, false},
		{"display-plus-conformance-positive", [4]uint64{61, 0, 0, 0}, true, true},
		{"display-plus-conformance-empty", [4]uint64{62, 0, 0, 0}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.crop = tc.crop
			if tc.crop {
				s.crops[0] = 2
			}
			s.vui = vui(nil, &tc.display, nil, nil)
			checkParameterExtraction(t, parameterSource(t, s, conformancePPS(0, 0, 0, false)), tc.valid)
		})
	}
	for _, tc := range []struct {
		name  string
		index int
		v     uint64
		valid bool
	}{
		{"restriction-spatial-unlimited", 0, 0, true}, {"restriction-spatial4096", 0, 4096, false},
		{"restriction-bytes16", 1, 16, true}, {"restriction-bytes17", 1, 17, false},
		{"restriction-bits16", 2, 16, true}, {"restriction-bits17", 2, 17, false},
		{"restriction-MV-horizontal15", 3, 15, true}, {"restriction-MV-horizontal16", 3, 16, false},
		{"restriction-MV-vertical15", 4, 15, true}, {"restriction-MV-vertical16", 4, 16, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			r := [5]uint64{0, 0, 0, 15, 15}
			r[tc.index] = tc.v
			s.vui = vui(nil, nil, &r, nil)
			checkParameterExtraction(t, parameterSource(t, s, conformancePPS(0, 0, 0, false)), tc.valid)
		})
	}
	for _, duration := range []uint64{2047, 2048} {
		name := "HRD-element-duration2047"
		valid := true
		if duration == 2048 {
			name = "HRD-element-duration2048"
			valid = false
		}
		t.Run(name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.vui = vui(nil, nil, nil, &duration)
			checkParameterExtraction(t, parameterSource(t, s, conformancePPS(0, 0, 0, false)), valid)
		})
	}
	for _, tc := range []struct {
		name                   string
		width, height, spatial uint64
		tiles, valid           bool
	}{
		{"spatial-WPP-unlimited", 128, 64, 0, false, true},
		{"spatial-WPP-limit1-small", 128, 64, 1, false, false},
		{"spatial-WPP-limit1-large", 512, 512, 1, false, true},
		{"spatial-tiles-limit4-inclusive", 512, 128, 4, true, true},
		{"spatial-tiles-limit5", 512, 128, 5, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.width = tc.width
			s.height = tc.height
			r := [5]uint64{tc.spatial, 0, 0, 15, 15}
			s.vui = vui(nil, nil, &r, nil)
			p := ueBits(5) + ueBits(3) + "1100000" + ueBits(0) + ueBits(0) + seBits(0) + "000" + seBits(0) + seBits(0) + "0000"
			if tc.tiles {
				p += "10" + ueBits(1) + ueBits(0) + "10"
			} else {
				p += "01"
			}
			p += "0000" + ueBits(0) + "00"
			raw := parameterSource(t, s, p)
			start := bytes.Index(raw, []byte{0, 0, 1, 38, 1})
			h := "10" + ueBits(5) + ueBits(2) + "1" + seBits(0) + ueBits(0) + "1"
			raw = append(raw[:start], authoredNAL(19, append(packedHeader(h), 0xff, 0xff))...)
			checkParameterExtraction(t, raw, tc.valid)
		})
	}
	for _, tc := range []struct {
		name                         string
		restricted, different, valid bool
	}{
		{"restricted-lists-identical", true, false, true},
		{"restricted-lists-different", true, true, false},
		{"unrestricted-lists-different", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.vui = vui(nil, nil, &[5]uint64{0, 0, 0, 15, 15}, nil)
			if tc.restricted {
				s.vui = s.vui[:len(s.vui)-len(ueBits(0)+ueBits(0)+ueBits(0)+ueBits(15)+ueBits(15))-1] + "1" + ueBits(0) + ueBits(0) + ueBits(0) + ueBits(15) + ueBits(15)
			}
			p := ueBits(5) + ueBits(3) + "1100000" + ueBits(0) + ueBits(0) + seBits(0) + "000" + seBits(0) + seBits(0) + "00000000" + "01" + ueBits(0) + "00"
			raw := parameterSource(t, s, p)
			raw = append(raw, authoredReferencePicture(1, 1, 2, []shortReference{{delta: -1}}, nil)...)
			for i := range 2 {
				h := "1" + ueBits(5)
				if i == 1 {
					h = "0" + ueBits(5) + "01"
				}
				h += ueBits(1) + "1" + "00000010" + "0" + ueBits(2) + ueBits(0) + ueBits(0) + "1" + ueBits(0) + "1" + "0" + "1"
				if i == 1 && tc.different {
					h += "1"
				} else {
					h += "0"
				}
				h += ueBits(0) + seBits(0) + "1"
				raw = append(raw, authoredNAL(1, append(packedHeader(h), 0xff, 0xff))...)
			}
			e, err := Extract(t.Context(), bytes.NewReader(raw), Options{})
			if err == nil {
				defer e.memory.release()
				checkIRAPFrames(t, e, []int64{0, 1, 2})
				if e.Profile != "A" || len(e.SceneStarts) != 1 {
					t.Fatal("incorrect complete restricted-list result")
				}
			}
			if tc.valid {
				if err != nil {
					t.Fatalf("LAWFUL control rejected %v", err)
				}
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Errorf("PROHIBITED VUI-restricted lists ACCEPTED; result=%v error=%v", e != nil, err)
			}
		})
	}
	for _, tc := range []struct {
		name           string
		width, spatial uint64
		dependent      []bool
		valid          bool
	}{
		{"spatial-no-WPP-unlimited", 128, 0, nil, true},
		{"spatial-no-WPP-one-slice-too-large", 128, 1, nil, false},
		{"spatial-no-WPP-two-independent-slices", 128, 1, []bool{false}, true},
		{"spatial-no-WPP-dependent-extends-same-slice", 128, 1, []bool{true}, false},
		{"spatial-no-WPP-dependent-then-independent", 192, 1, []bool{true, false}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.width = tc.width
			s.vui = vui(nil, nil, &[5]uint64{tc.spatial, 0, 0, 15, 15}, nil)
			raw := parameterSource(t, s, conformancePPS(0, 0, 0, false))
			start := bytes.Index(raw, []byte{0, 0, 1, 38, 1})
			raw = raw[:start]
			for i := 0; i <= len(tc.dependent); i++ {
				h := "10" + ueBits(5)
				dep := false
				if i > 0 {
					dep = tc.dependent[i-1]
					h = "00" + ueBits(5)
					if dep {
						h += "1"
					} else {
						h += "0"
					}
					width := uint(1)
					if tc.width == 192 {
						width = 2
					}
					h += fmt.Sprintf("%0*b", width, i)
				}
				if !dep {
					h += ueBits(2) + "1" + seBits(0)
				}
				h += "1"
				raw = append(raw, authoredNAL(19, append(packedHeader(h), 0xff, 0xff))...)
			}
			checkParameterExtraction(t, raw, tc.valid)
		})
	}
	for _, tc := range []struct {
		name                         string
		width, height, spatial, rows uint64
		valid                        bool
	}{
		{"spatial-explicit-partial-column-actual264", 520, 64, 3, 1, true},
		{"spatial-explicit-partial-column-limit4", 520, 64, 4, 1, false},
		{"spatial-explicit-partial-row-actual72", 512, 136, 8, 2, true},
		{"spatial-explicit-partial-row-limit12", 512, 136, 12, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.width = tc.width
			s.height = tc.height
			s.vui = vui(nil, nil, &[5]uint64{tc.spatial, 0, 0, 15, 15}, nil)
			p := ueBits(5) + ueBits(3) + "1100000" + ueBits(0) + ueBits(0) + seBits(0) + "000" + seBits(0) + seBits(0) + "0000" + "10" + ueBits(1) + ueBits(tc.rows-1) + "0" + ueBits(3)
			if tc.rows == 2 {
				p += ueBits(0)
			}
			p += "00000" + ueBits(0) + "00"
			raw := parameterSource(t, s, p)
			start := bytes.Index(raw, []byte{0, 0, 1, 38, 1})
			h := "10" + ueBits(5) + ueBits(2) + "1" + seBits(0) + ueBits(0) + "1"
			raw = append(raw[:start], authoredNAL(19, append(packedHeader(h), 0xff, 0xff))...)
			checkParameterExtraction(t, raw, tc.valid)
		})
	}
}

func TestParameterPCMAndTransformNeighbors(t *testing.T) {
	// H.265 caps PCM block sizes at 32 even when MinCB and CTB are 64.
	// The minimum transform size may be 32 when MinCB is 64.
	for _, tc := range []struct {
		name                     string
		transform, pcmMin, depth uint64
		valid                    bool
	}{
		{"MinCB64-PCM32", 0, 2, 0, true},
		{"MinCB64-PCM64", 0, 3, 0, false},
		{"MinCB64-MinTB32-depth1", 3, 2, 1, true},
		{"MinCB64-MinTB32-depth2", 3, 2, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.minCB = 6
			s.minTB = tc.transform
			s.diffTB = 3 - tc.transform
			s.interDepth, s.intraDepth = tc.depth, tc.depth
			s.pcm, s.pcmY, s.pcmC, s.pcmMin = true, 9, 9, tc.pcmMin
			checkParameterExtraction(t, parameterSource(t, s, conformancePPS(0, 0, 0, false)), tc.valid)
		})
	}
}

func TestParameterVPSProfile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		depth uint64
		valid bool
	}{
		{"VPS-Main-SPS-Main10-depth8", 0, true},
		{"VPS-Main-SPS-Main10-depth10", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.depthY, s.depthC = tc.depth, tc.depth
			raw := parameterSource(t, s, conformancePPS(0, 0, 0, false))
			end := bytes.Index(raw, []byte{0, 0, 1, 66, 1})
			main := "000" + fmt.Sprintf("%05b%032b", 1, 1<<30) + main10PTL(120)[40:]
			vps := "0010110000000001" + "1111111111111111" + main + "1" + ueBits(3) + ueBits(2) + ueBits(0) + "000000" + ueBits(0) + "00"
			raw = append(authoredNAL(32, packedHeader(vps+"1")), raw[end:]...)
			checkParameterExtraction(t, raw, tc.valid)
		})
	}
}

func TestSpatialSegmentationModeAcrossCVS(t *testing.T) {
	pps := func(id uint64, tile bool) string {
		h := ueBits(id) + ueBits(3) + "1100000" + ueBits(0) + ueBits(0) + seBits(0) + "000" + seBits(0) + seBits(0) + "0000"
		if tile {
			h += "10" + ueBits(1) + ueBits(0) + "10"
		} else {
			h += "01"
		}
		return h + "0000" + ueBits(0) + "00"
	}
	for _, tc := range []struct {
		name                                string
		firstTile, secondTile, reset, valid bool
		longTerm                            bool
	}{
		{"WPP-WPP", false, false, false, true, false},
		{"tiles-tiles", true, true, false, true, false},
		{"WPP-tiles-one-CVS", false, true, false, false, false},
		{"tiles-WPP-one-CVS", true, false, false, false, false},
		{"WPP-tiles-IDR-reset", false, true, true, true, false},
		{"tiles-WPP-IDR-reset", true, false, true, true, false},
		{"long-term-WPP-WPP", false, false, false, true, true},
		{"long-term-tiles-tiles", true, true, false, true, true},
		{"long-term-WPP-tiles-one-CVS", false, true, false, false, true},
		{"long-term-tiles-WPP-one-CVS", true, false, false, false, true},
		{"long-term-WPP-tiles-IDR-reset", false, true, true, true, true},
		{"long-term-tiles-WPP-IDR-reset", true, false, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaultParameterSPS()
			s.longTerm = tc.longTerm
			s.width, s.height = 512, 512
			s.vui = "0000000001000" + ueBits(1) + ueBits(0) + ueBits(0) + ueBits(15) + ueBits(15)
			raw := parameterSource(t, s, pps(5, tc.firstTile))
			start := bytes.Index(raw, []byte{0, 0, 1, 38, 1})
			first := "10" + ueBits(5) + ueBits(2) + "1" + seBits(0) + ueBits(0) + "1"
			raw = append(raw[:start], authoredNAL(19, append(packedHeader(first), 0xff, 0xff))...)
			_, meta, _ := headerFixtureParts(t)
			raw = append(raw, authoredNAL(34, packedHeader(pps(6, tc.secondTile)+"1"))...)
			raw = append(raw, meta...)
			kind := byte(1)
			second := "1" + ueBits(6) + ueBits(2) + "1" + fmt.Sprintf("%08b", 1) + "0" + ueBits(1) + ueBits(0) + ueBits(0) + "0"
			if tc.longTerm {
				second += ueBits(0)
			}
			second += seBits(0) + ueBits(0) + "1"
			order := []int64{0, 1}
			if tc.reset {
				kind = 19
				second = "10" + ueBits(6) + ueBits(2) + "1" + seBits(0) + ueBits(0) + "1"
				order[1] = 0
			}
			raw = append(raw, authoredNAL(kind, append(packedHeader(second), 0xff, 0xff))...)
			e, err := Extract(t.Context(), bytes.NewReader(raw), Options{})
			if err == nil {
				defer e.memory.release()
				checkIRAPFrames(t, e, order)
				if e.Profile != "A" || len(e.SceneStarts) != 1 {
					t.Fatal("incorrect complete mode result")
				}
			}
			if tc.valid {
				if err != nil {
					t.Fatalf("lawful mode/reset control rejected: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Errorf("different segmentation modes in one CVS accepted: result=%v error=%v", e != nil, err)
			}
		})
	}
}
