package extract

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

func checkIRAPFrames(t *testing.T, e *Extraction, want []int64) {
	t.Helper()
	var got []int64
	for _, frame := range e.Frames {
		got = append(got, frame.POC)
	}
	plot, err := e.PlotMetadata()
	if err != nil || plot.Profile != "A" || !slices.Equal(got, want) {
		t.Fatalf("POCs=%v want=%v, plot=%+v error=%v", got, want, plot, err)
	}
}

func TestLeadingConstraints(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	for _, tc := range []struct {
		name  string
		kinds []byte
		pocs  []uint64
		valid bool
	}{
		{"valid-IDR-W-RADL", []byte{19, 7, 1}, []uint64{0, 255, 1}, true},
		{"valid-CRA-RADL", []byte{21, 7, 1}, []uint64{0, 255, 1}, true},
		{"valid-CRA-RASL", []byte{21, 9, 1}, []uint64{0, 255, 1}, true},
		{"IDR-N-LP-forbidden-RADL", []byte{20, 7, 1}, []uint64{0, 255, 1}, false},
		{"BLA-N-LP-forbidden-RADL", []byte{18, 7, 1}, []uint64{0, 255, 1}, false},
		{"IDR-forbidden-RASL", []byte{19, 9, 1}, []uint64{0, 255, 1}, false},
		{"BLA-W-RADL-forbidden-RASL", []byte{17, 9, 1}, []uint64{0, 255, 1}, false},
		{"TRAIL-is-leading", []byte{21, 1, 1}, []uint64{0, 255, 1}, false},
		{"RADL-is-trailing", []byte{21, 7, 1}, []uint64{0, 1, 2}, false},
		{"leading-after-trailing", []byte{21, 1, 7}, []uint64{0, 1, 255}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := append(slices.Clone(sets), metadata...)
			for i, poc := range tc.pocs {
				data = append(data, authoredReferencePicture(poc, tc.kinds[i], 2, nil, nil)...)
			}
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				want := []int64{-1, 0, 1}
				if tc.kinds[1] == 9 {
					want = []int64{0, 1}
				}
				checkIRAPFrames(t, e, want)
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("error=%v, want invalid leading-picture structure", err)
			}
		})
	}
}
func TestLeadingReferences(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	type pic struct {
		poc                uint64
		kind               byte
		slice              uint64
		negative, positive []shortReference
	}
	for _, tc := range []struct {
		name  string
		pics  []pic
		valid bool
	}{
		{"valid-trailing-ref-IRAP", []pic{{0, 21, 2, nil, nil}, {1, 1, 1, []shortReference{{-1, true}}, nil}}, true},
		{"valid-RADL-ref-IRAP", []pic{{0, 21, 2, nil, nil}, {255, 7, 1, nil, []shortReference{{1, true}}}, {1, 1, 2, nil, nil}}, true},
		{"trailing-used-RADL", []pic{{0, 21, 2, nil, nil}, {255, 7, 2, nil, nil}, {1, 1, 1, []shortReference{{-2, true}}, nil}}, false},
		{"trailing-unused-RADL", []pic{{0, 21, 2, nil, nil}, {255, 7, 2, nil, nil}, {1, 1, 2, []shortReference{{-2, false}}, nil}}, false},
		{"RADL-used-RASL", []pic{{0, 21, 2, nil, nil}, {254, 9, 2, nil, nil}, {255, 7, 1, []shortReference{{-1, true}}, nil}, {1, 1, 2, nil, nil}}, false},
		{"RADL-generated-unavailable", []pic{{0, 16, 2, []shortReference{{-2, false}}, nil}, {255, 7, 1, []shortReference{{-1, true}}, nil}, {1, 1, 2, nil, nil}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := append(slices.Clone(sets), metadata...)
			for _, p := range tc.pics {
				data = append(data, authoredReferencePicture(p.poc, p.kind, p.slice, p.negative, p.positive)...)
			}
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				want := []int64{-1, 0, 1}
				if tc.name == "valid-trailing-ref-IRAP" {
					want = []int64{0, 1}
				}
				checkIRAPFrames(t, e, want)
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("error=%v, want invalid associated-IRAP reference", err)
			}
		})
	}
}
func TestTemporalNesting(t *testing.T) {
	_, metadata, picture := headerFixtureParts(t)
	ptl := main10PTL(120) + strings.Repeat("0", 16)
	vps := "0010110000000010" + strings.Repeat("1", 16) + ptl + "0" + ueBits(4) + ueBits(3) + ueBits(0) + "000000" + ueBits(0) + "00"
	for _, tc := range []struct {
		name       string
		nesting    bool
		kind       byte
		valid      bool
		vpsNesting bool
	}{
		{"valid-unconstrained-TRAIL", false, 1, true, false},
		{"valid-nested-TSA", true, 3, true, false},
		{"forbidden-nested-TRAIL", true, 1, false, false},
		{"valid-VPS-SPS-nesting", true, 3, true, true},
		{"forbidden-VPS-SPS-nesting", false, 3, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nesting := "0"
			if tc.nesting {
				nesting = "1"
			}
			sps := "0010001" + nesting + ptl + ueBits(3) + ueBits(1) + ueBits(128) + ueBits(64) + "0" + ueBits(2) + ueBits(2) + ueBits(4) + "0" + ueBits(4) + ueBits(3) + ueBits(0) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(0) + "0000" + ueBits(0) + "00000"
			vpsBits := vps
			if tc.vpsNesting {
				vpsBits = vps[:15] + "1" + vps[16:]
			}
			data := bytes.Join([][]byte{authoredNAL(32, packedHeader(vpsBits+"1")), authoredNAL(33, packedHeader(sps+"1")), authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1")), metadata, picture}, nil)
			next := authoredReferencePicture(1, tc.kind, 2, nil, nil)
			next[4] = 2
			e, err := Extract(t.Context(), bytes.NewReader(append(data, next...)), Options{})
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				checkIRAPFrames(t, e, []int64{0, 1})
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("error=%v, want invalid temporal nesting NAL type", err)
			}
		})
	}
}
func TestCRAPreviousIRAPReferences(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	for _, tc := range []struct {
		name   string
		target int16
		valid  bool
	}{{"valid-previous-CRA", -1, true}, {"forbidden-before-previous-CRA", -2, false}} {
		t.Run(tc.name, func(t *testing.T) {
			data := append(slices.Clone(sets), metadata...)
			data = append(data, authoredReferencePicture(0, 19, 2, nil, nil)...)
			data = append(data, authoredReferencePicture(1, 21, 2, []shortReference{{-1, false}}, nil)...)
			data = append(data, authoredReferencePicture(2, 21, 2, []shortReference{{tc.target, false}}, nil)...)
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				checkIRAPFrames(t, e, []int64{0, 1, 2})
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("error=%v, want invalid CRA RPS before preceding IRAP", err)
			}
		})
	}
}
func TestTemporalNestingReference(t *testing.T) {
	_, metadata, picture := headerFixtureParts(t)
	ptl := main10PTL(120) + strings.Repeat("0", 16)
	vps := "0010110000000010" + strings.Repeat("1", 16) + ptl + "0" + ueBits(4) + ueBits(3) + ueBits(0) + "000000" + ueBits(0) + "00"
	for _, nested := range []bool{false, true} {
		name := "valid-unnested"
		flag := "0"
		if nested {
			name = "forbidden-nested-reference"
			flag = "1"
		}
		t.Run(name, func(t *testing.T) {
			sps := "0010001" + flag + ptl + ueBits(3) + ueBits(1) + ueBits(128) + ueBits(64) + "0" + ueBits(2) + ueBits(2) + ueBits(4) + "0" + ueBits(4) + ueBits(3) + ueBits(0) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(0) + "0000" + ueBits(0) + "00000"
			data := bytes.Join([][]byte{authoredNAL(32, packedHeader(vps+"1")), authoredNAL(33, packedHeader(sps+"1")), authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1")), metadata, picture}, nil)
			first := authoredReferencePicture(254, 7, 2, nil, nil)
			first[4] = 2
			middle := authoredReferencePicture(255, 7, 2, []shortReference{{-1, false}}, nil)
			last := authoredReferencePicture(253, 7, 1, nil, []shortReference{{1, true}})
			last[4] = 2
			data = bytes.Join([][]byte{data, first, middle, last}, nil)
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if !nested {
				if err != nil {
					t.Fatal(err)
				}
				checkIRAPFrames(t, e, []int64{-3, -2, -1, 0})
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("error=%v, want invalid pre-lower-temporal-picture reference", err)
			}
		})
	}
}
func TestIRAPOutputConstraints(t *testing.T) {
	_, metadata, _ := headerFixtureParts(t)
	vps := "0010110000000001" + strings.Repeat("1", 16) + main10PTL(120) + "1" + ueBits(4) + ueBits(3) + ueBits(0) + "000000" + ueBits(0) + "00"
	sps := strings.Replace(conformanceSPS(3, 128, 2, 6), ueBits(3)+ueBits(2)+ueBits(0)+ueBits(0)+ueBits(3), ueBits(4)+ueBits(3)+ueBits(0)+ueBits(0)+ueBits(3), 1)
	parsed, err := parseSPS(packedHeader(sps + "1"))
	if err != nil || parsed.buffer[0] != 5 || parsed.reorder[0] != 3 {
		t.Fatalf("probe ordering bounds: %+v %v", parsed, err)
	}
	sets := bytes.Join([][]byte{authoredNAL(32, packedHeader(vps+"1")), authoredNAL(33, packedHeader(sps+"1")), authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1"))}, nil)
	for _, tc := range []struct {
		name  string
		kinds []byte
		pocs  []uint64
		valid bool
	}{
		{"valid-prior-output-before-RADL", []byte{19, 1, 21, 7}, []uint64{0, 2, 4, 3}, true},
		{"forbidden-prior-output-after-RADL", []byte{19, 1, 21, 7}, []uint64{0, 2, 4, 1}, false},
		{"forbidden-prior-output-after-IRAP", []byte{19, 1, 21, 7}, []uint64{0, 5, 4, 3}, false},
		{"valid-RASL-before-RADL", []byte{19, 21, 7, 9}, []uint64{0, 4, 2, 1}, true},
		{"forbidden-RASL-after-RADL", []byte{19, 21, 7, 9}, []uint64{0, 4, 1, 2}, false},
		{"valid-RASL-after-previous-CRA", []byte{19, 21, 21, 9}, []uint64{0, 4, 8, 6}, true},
		{"forbidden-RASL-before-previous-CRA", []byte{19, 21, 21, 9}, []uint64{0, 4, 8, 2}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := append(slices.Clone(sets), metadata...)
			for i, poc := range tc.pocs {
				data = append(data, authoredReferencePicture(poc, tc.kinds[i], 2, nil, nil)...)
			}
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				want := []int64{0, 2, 3, 4}
				if tc.name == "valid-RASL-before-RADL" {
					want = []int64{0, 1, 2, 4}
				} else if tc.name == "valid-RASL-after-previous-CRA" {
					want = []int64{0, 4, 6, 8}
				}
				checkIRAPFrames(t, e, want)
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("error=%v, want invalid IRAP output relation", err)
			}
		})
	}
}
func TestAssemblyLeadingConstraints(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	for _, kind := range []byte{19, 20} {
		name := "valid-IDR-W-RADL"
		if kind == 20 {
			name = "forbidden-IDR-N-LP-RADL"
		}
		t.Run(name, func(t *testing.T) {
			data := append(slices.Clone(sets), metadata...)
			data = append(data, authoredReferencePicture(0, kind, 2, nil, nil)...)
			data = append(data, authoredReferencePicture(255, 7, 2, nil, nil)...)
			data = append(data, authoredReferencePicture(1, 1, 2, nil, nil)...)
			key := StreamKey{SourceID: name}
			clip := sourceClip(t, key, data, []int64{3000, 0, 6000})
			e, err := Assemble(t.Context(), Playlist{Occurrences: []Occurrence{{Key: key, Out: 4500, ClockOffset: Timestamp{Timescale: 90000, Valid: true}, Connection: ConnectionReset}}}, map[StreamKey]*Clip{key: clip}, AssemblyOptions{})
			if kind == 19 {
				if err != nil {
					t.Fatal(err)
				}
				checkIRAPFrames(t, e, []int64{-1, 0, 1})
			} else if !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("error=%v, want invalid leading-picture assembly", err)
			}
		})
	}
}

func TestIRAPBoundaryState(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	t.Run("generated-following-reference", func(t *testing.T) {
		for _, used := range []bool{false, true} {
			data := bytes.Join([][]byte{sets, metadata, authoredReferencePicture(0, 19, 2, nil, nil), metadata,
				authoredReferencePicture(0, 16, 2, nil, []shortReference{{2, false}})}, nil)
			slice := uint64(2)
			if used {
				slice = 1
			}
			data = append(data, authoredReferencePicture(1, 1, slice, nil, []shortReference{{1, used}})...)
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if used {
				if !errors.Is(err, ErrInvalidBitstream) {
					t.Fatalf("used unavailable reference: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				checkIRAPFrames(t, e, []int64{0, 0, 1})
			}
		}
	})
	t.Run("non-output-associated-IRAP", func(t *testing.T) {
		hidden := "10" + ueBits(5) + ueBits(2) + "0" + "00000100" + "0" + ueBits(0) + ueBits(0) + seBits(0) + "1"
		for _, poc := range []uint64{2, 6} {
			data := bytes.Join([][]byte{sets, metadata, authoredReferencePicture(0, 19, 2, nil, nil),
				authoredNAL(21, append(packedHeader(hidden), 0xff)), authoredReferencePicture(poc, 21, 2, nil, nil)}, nil)
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if poc == 2 {
				if !errors.Is(err, ErrInvalidBitstream) {
					t.Fatalf("CRA preceding hidden IRAP: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				checkIRAPFrames(t, e, []int64{0, 6})
			}
		}
	})
	t.Run("seamless-trailing-state", func(t *testing.T) {
		for _, kind := range []byte{1, 7} {
			aKey, bKey := StreamKey{SourceID: "trailing-predecessor"}, StreamKey{SourceID: "trailing-destination"}
			a := sourceClip(t, aKey, bytes.Join([][]byte{sets, metadata, authoredReferencePicture(0, 19, 2, nil, nil), authoredReferencePicture(1, 1, 2, nil, nil)}, nil), []int64{0, 3000})
			poc := uint64(2)
			if kind == 7 {
				poc = 255
			}
			b := sourceClip(t, bKey, authoredReferencePicture(poc, kind, 2, nil, nil), []int64{0})
			p := Playlist{Occurrences: []Occurrence{
				{Key: aKey, In: 1500, Out: 3000, ClockOffset: Timestamp{Timescale: 90000, Valid: true}, Connection: ConnectionReset},
				{Key: bKey, Out: 1500, PlaylistStart: 1500, ClockOffset: Timestamp{Timescale: 90000, Valid: true}, Connection: ConnectionSeamless},
			}}
			e, err := Assemble(t.Context(), p, map[StreamKey]*Clip{aKey: a, bKey: b}, AssemblyOptions{})
			if kind == 7 {
				if !errors.Is(err, ErrInvalidBitstream) {
					t.Fatalf("leading after trailing across clips: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				checkIRAPFrames(t, e, []int64{1, 2})
			}
		}
	})
}

func TestTemporalNestingDeclarations(t *testing.T) {
	raw := rawFixture(t, "single")
	for _, kind := range []byte{32, 33} {
		name := "VPS"
		if kind == 33 {
			name = "SPS"
		}
		t.Run(name, func(t *testing.T) {
			for _, nesting := range []bool{false, true} {
				data := slices.Clone(raw)
				start := bytes.Index(data, []byte{0, 0, 1, kind << 1, 1})
				if start < 0 {
					t.Fatal("missing fixture parameter set")
				}
				offset := start + 5
				if kind == 32 {
					offset++
				}
				if !nesting {
					data[offset] &^= 1
				}
				e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
				if nesting {
					if err != nil || len(e.Frames) != 1 {
						t.Fatalf("valid single-sublayer nesting: %v %v", e, err)
					}
				} else if !errors.Is(err, ErrInvalidBitstream) {
					t.Fatalf("unset single-sublayer nesting: %v", err)
				}
			}
		})
	}
}

func TestIRAPContainerSuppression(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	for _, prior := range []uint64{2, 5} {
		key := StreamKey{SourceID: "suppressed-container-preroll"}
		s, err := NewStream(StreamOptions{Key: key, RetainBoundarySyntax: true})
		if err != nil {
			t.Fatal(err)
		}
		chunks := [][]byte{bytes.Join([][]byte{sets, metadata, authoredReferencePicture(0, 19, 2, nil, nil)}, nil),
			authoredReferencePicture(prior, 1, 2, nil, nil), authoredReferencePicture(4, 21, 2, nil, nil)}
		var offset uint64
		for i, data := range chunks {
			marker := Marker{ESOffset: offset, Kind: SampleStart, PTS: Timestamp{Value: int64(i) * 3000, Timescale: 90000, Valid: true}, SuppressOutput: i == 1}
			if err := s.Push(t.Context(), Chunk{ESOffset: offset, Data: data, Markers: []Marker{marker}}); err != nil {
				t.Fatal(err)
			}
			offset += uint64(len(data))
		}
		clip, err := s.Finish(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(clip.memory.release)
		p := Playlist{Occurrences: []Occurrence{{Key: key, Out: 4500, ClockOffset: Timestamp{Timescale: 90000, Valid: true}, Connection: ConnectionReset}}}
		e, err := Assemble(t.Context(), p, map[StreamKey]*Clip{key: clip}, AssemblyOptions{})
		if prior == 5 {
			if !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("container suppression hid prohibited prior output: %v", err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			checkIRAPFrames(t, e, []int64{0, 4})
		}
	}
}
