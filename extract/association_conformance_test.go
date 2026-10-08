package extract

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func subLayerReferenceSets(t *testing.T, long bool) ([]byte, []byte) {
	_, metadata, _ := headerFixtureParts(t)
	ptl := main10PTL(120) + strings.Repeat("0", 16)
	vps := "0010110000000010" + strings.Repeat("1", 16) + ptl + "0" + ueBits(4) + ueBits(3) + ueBits(0) + "000000" + ueBits(0) + "00"
	tail := "00000"
	if long {
		tail = "1" + ueBits(0) + "0000"
	}
	sps := "00100010" + ptl + ueBits(3) + ueBits(1) + ueBits(128) + ueBits(64) + "0" + ueBits(2) + ueBits(2) + ueBits(4) + "0" + ueBits(4) + ueBits(3) + ueBits(0) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(0) + "0000" + ueBits(0) + tail
	return bytes.Join([][]byte{authoredNAL(32, packedHeader(vps+"1")), authoredNAL(33, packedHeader(sps+"1")), authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1"))}, nil), metadata
}

func subLayerLongPicture(poc uint64, kind, temporal byte, slice uint64, ref *longReference) []byte {
	bits := "1"
	if kind >= 16 {
		bits += "0"
	}
	bits += ueBits(5) + ueBits(slice) + "1"
	if kind != 19 && kind != 20 {
		bits += fmt.Sprintf("%08b", poc) + "0" + ueBits(0) + ueBits(0)
		if ref == nil {
			bits += ueBits(0)
		} else {
			bits += ueBits(1) + fmt.Sprintf("%08b", ref.lsb)
			if ref.used {
				bits += "1"
			} else {
				bits += "0"
			}
			bits += "0"
		}
		if slice != 2 {
			bits += "0"
			if slice == 0 {
				bits += "0"
			}
			bits += ueBits(0)
		}
	}
	bits += seBits(0) + "1"
	nal := authoredNAL(kind, append(packedHeader(bits), 0xff, 0xff))
	nal[4] = temporal + 1
	return nal
}

func TestSubLayerNonReference(t *testing.T) {
	for _, kind := range []byte{6, 8} {
		for _, long := range []bool{false, true} {
			for _, tc := range []struct {
				name                     string
				firstKind, firstTemporal byte
				used, valid              bool
			}{
				{"forbidden-same-layer-used", kind, 1, true, false},
				{"valid-reference-kind", kind + 1, 1, true, true},
				{"valid-lower-layer", kind, 0, true, true},
				{"valid-unused", kind, 1, false, true},
			} {
				t.Run(fmt.Sprintf("kind%d/long%v/%s", kind, long, tc.name), func(t *testing.T) {
					sets, meta := subLayerReferenceSets(t, long)
					data := append(slices.Clone(sets), meta...)
					irapKind := byte(19)
					if kind == 8 {
						irapKind = 21
					}
					currentKind := kind + 1
					var first, current, irap, tail []byte
					currentSlice := uint64(2)
					if tc.used {
						currentSlice = 1
					}
					if long {
						irap = subLayerLongPicture(0, irapKind, 0, 2, nil)
						first = subLayerLongPicture(254, tc.firstKind, tc.firstTemporal, 2, nil)
						current = subLayerLongPicture(253, currentKind, 1, currentSlice, &longReference{lsb: 254, used: tc.used})
						tail = subLayerLongPicture(1, 1, 0, 2, nil)
					} else {
						irap = authoredReferencePicture(0, irapKind, 2, nil, nil)
						first = authoredReferencePicture(254, tc.firstKind, 2, nil, nil)
						first[4] = tc.firstTemporal + 1
						current = authoredReferencePicture(253, currentKind, currentSlice, nil, []shortReference{{1, tc.used}})
						current[4] = 2
						tail = authoredReferencePicture(1, 1, 2, nil, nil)
					}
					data = bytes.Join([][]byte{data, irap, first, current, tail}, nil)
					e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
					if tc.valid {
						if err != nil {
							t.Fatal(err)
						}
						want := []int64{-3, -2, 0, 1}
						if kind == 8 {
							want = []int64{0, 1}
						}
						checkIRAPFrames(t, e, want)
						e.memory.release()
					} else if !errors.Is(err, ErrInvalidBitstream) {
						if err == nil {
							var pocs []int64
							for _, p := range e.Frames {
								pocs = append(pocs, p.POC)
							}
							m, pe := e.PlotMetadata()
							t.Errorf("FORBIDDEN ACCEPTED POCs%v profile%s projectionErr%v", pocs, m.Profile, pe)
							e.memory.release()
						} else {
							t.Errorf("wrong error %v", err)
						}
					}
				})
			}
		}
	}
	for _, kind := range []byte{0, 2, 4} {
		for _, reference := range []bool{false, true} {
			t.Run(fmt.Sprintf("existing-sibling%d/reference%v", kind, reference), func(t *testing.T) {
				sets, meta := subLayerReferenceSets(t, false)
				firstKind := kind
				if reference {
					firstKind++
				}
				first := authoredReferencePicture(1, firstKind, 2, nil, nil)
				first[4] = 2
				current := authoredReferencePicture(2, 1, 1, []shortReference{{-1, true}}, nil)
				current[4] = 2
				data := bytes.Join([][]byte{sets, meta, authoredReferencePicture(0, 19, 2, nil, nil), first, current}, nil)
				e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
				if reference {
					if err != nil {
						t.Fatal(err)
					}
					checkIRAPFrames(t, e, []int64{0, 1, 2})
					e.memory.release()
				} else if !errors.Is(err, ErrInvalidBitstream) {
					t.Fatalf("sibling rejection %v", err)
				}
			})
		}
	}
}

func associationPicture(first bool, short string, long string, mvp string) []byte {
	header := "1" + ueBits(5)
	if !first {
		header = "0" + ueBits(5) + "01"
	} // independent, raster address1
	header += ueBits(2) + "1" + "00000001" + short + long + mvp + seBits(0) + "1"
	return authoredNAL(1, append(packedHeader(header), 0xff, 0xff))
}
func TestSliceSourceAssociation(t *testing.T) {
	sets, metadata, picture := headerFixtureParts(t)
	vps := sets[:bytes.Index(sets, []byte{0, 0, 1, 66, 1})]
	base := conformanceSPS(3, 128, 2, 6)
	for _, tc := range []struct {
		name, sps, short1, short2, long1, long2, mvp1, mvp2 string
		valid                                               bool
	}{
		{name: "valid-MVP0", sps: base[:len(base)-4] + "1000", short1: "011", short2: "011", mvp1: "0", mvp2: "0", valid: true},
		{name: "valid-MVP1", sps: base[:len(base)-4] + "1000", short1: "011", short2: "011", mvp1: "1", mvp2: "1", valid: true},
		{name: "forbidden-MVP-different", sps: base[:len(base)-4] + "1000", short1: "011", short2: "011", mvp1: "0", mvp2: "1"},
		{name: "valid-SPS-short-set0", sps: strings.TrimSuffix(base, "100000") + ueBits(2) + "11" + "011" + "00000", short1: "10", short2: "10", valid: true},
		{name: "forbidden-short-index-different", sps: strings.TrimSuffix(base, "100000") + ueBits(2) + "11" + "011" + "00000", short1: "10", short2: "11"},
		{name: "valid-inline-short-set", sps: strings.TrimSuffix(base, "100000") + ueBits(2) + "11" + "011" + "00000", short1: "0011", short2: "0011", valid: true},
		{name: "forbidden-short-flag-different", sps: strings.TrimSuffix(base, "100000") + ueBits(2) + "11" + "011" + "00000", short1: "10", short2: "0011"},
		{name: "valid-SPS-long-source", sps: base[:len(base)-5] + "1" + ueBits(1) + "00000000" + "0" + "0000", short1: "011", short2: "011", long1: ueBits(1) + ueBits(0) + "0", long2: ueBits(1) + ueBits(0) + "0", valid: true},
		{name: "valid-explicit-long-source", sps: base[:len(base)-5] + "1" + ueBits(1) + "00000000" + "0" + "0000", short1: "011", short2: "011", long1: ueBits(0) + ueBits(1) + "00000000" + "00", long2: ueBits(0) + ueBits(1) + "00000000" + "00", valid: true},
		{name: "forbidden-long-count-source-different", sps: base[:len(base)-5] + "1" + ueBits(1) + "00000000" + "0" + "0000", short1: "011", short2: "011", long1: ueBits(1) + ueBits(0) + "0", long2: ueBits(0) + ueBits(1) + "00000000" + "00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseSPS(packedHeader(tc.sps + "1"))
			if err != nil {
				t.Fatalf("source SPS: %v", err)
			}
			t.Logf("source ctbs%d short%d long%d MVP%v", parsed.ctbs, parsed.shortCount, parsed.longCount, parsed.temporalMVP)
			data := bytes.Join([][]byte{slices.Clone(vps), authoredNAL(33, packedHeader(tc.sps+"1")), authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1")), metadata, picture, associationPicture(true, tc.short1, tc.long1, tc.mvp1), associationPicture(false, tc.short2, tc.long2, tc.mvp2)}, nil)
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				checkIRAPFrames(t, e, []int64{0, 1})
				e.memory.release()
			} else if !errors.Is(err, ErrInvalidBitstream) {
				if err == nil {
					m, pe := e.PlotMetadata()
					t.Errorf("FORBIDDEN ACCEPTED frames%d profile%s projectionErr%v", len(e.Frames), m.Profile, pe)
					e.memory.release()
				} else {
					t.Error(fmt.Sprintf("wrong error %v", err))
				}
			}
		})
	}
}

func mvpPredecessor() []byte {
	bits := "1" + ueBits(5) + ueBits(2) + "1" + "00000010" + "0" + ueBits(1) + ueBits(0) + ueBits(1) + "0" + "1" + seBits(0) + "1"
	return authoredNAL(1, append(packedHeader(bits), 0xff, 0xff))
}
func collocatedPicture(first bool, index int) []byte {
	bits := "1" + ueBits(5)
	if !first {
		bits = "0" + ueBits(5) + "01"
	}
	bits += ueBits(1) + "1" + "00000001" + "0" + ueBits(1) + ueBits(1) + ueBits(0) + "1" + ueBits(0) + "1" + "1" + "0"
	if index < 0 {
		bits += "0"
	} else if index == 0 {
		bits += "10"
	} else {
		bits += "11"
	}
	bits += ueBits(0) + seBits(0) + "1"
	return authoredNAL(1, append(packedHeader(bits), 0xff, 0xff))
}
func TestCollocatedAssociation(t *testing.T) {
	sets, metadata, picture := headerFixtureParts(t)
	vps := sets[:bytes.Index(sets, []byte{0, 0, 1, 66, 1})]
	sps := conformanceSPS(3, 128, 2, 6)
	sps = sps[:len(sps)-4] + "1000"
	pps := conformancePPS(0, 0, 0, false)
	pps = pps[:len(pps)-4] + "1100"
	parsed, err := parsePPS(packedHeader(pps + "1"))
	if err != nil || !parsed.lists {
		t.Fatalf("list-enabled producer PPS: %v %v", parsed, err)
	}
	for _, tc := range []struct {
		name  string
		a, b  int
		valid bool
	}{{"valid-default-both", -1, -1, true}, {"valid-explicit0-both", 0, 0, true}, {"valid-explicit1-both", 1, 1, true}, {"valid-same-derived-reference", -1, 0, true}, {"forbidden-collocated-picture-different", 0, 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			data := bytes.Join([][]byte{vps, authoredNAL(33, packedHeader(sps+"1")), authoredNAL(34, packedHeader(pps+"1")), metadata, picture, mvpPredecessor(), collocatedPicture(true, tc.a), collocatedPicture(false, tc.b)}, nil)
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				checkIRAPFrames(t, e, []int64{0, 1, 2})
				e.memory.release()
			} else if !errors.Is(err, ErrInvalidBitstream) {
				if err == nil {
					m, pe := e.PlotMetadata()
					t.Errorf("FORBIDDEN ACCEPTED frames%d profile%s projectionErr%v", len(e.Frames), m.Profile, pe)
					e.memory.release()
				} else {
					t.Fatalf("wrong error %v", err)
				}
			}
		})
	}
}

type collocatedChoice struct {
	sliceType, refsL0, refsL1 uint64
	modsL0, modsL1            []uint64
	fromL0                    bool
	index                     uint64
}

func collocatedSegment(address uint64, long bool, choice collocatedChoice) []byte {
	header := "1" + ueBits(5)
	if address > 0 {
		header = "0" + ueBits(5) + "0" + fmt.Sprintf("%02b", address)
	}
	header += ueBits(choice.sliceType) + "1" + "00000001" + "0"
	if long {
		header += ueBits(0) + ueBits(0) + ueBits(2) + "00000000" + "10" + "00000010" + "10"
	} else {
		header += ueBits(1) + ueBits(1) + ueBits(0) + "1" + ueBits(0) + "1"
	}
	header += "1" // temporal MVP enabled, also present in I slices.
	if choice.sliceType < 2 {
		header += "1" + ueBits(choice.refsL0-1)
		if choice.sliceType == 0 {
			header += ueBits(choice.refsL1 - 1)
		}
		for list, mods := range [][]uint64{choice.modsL0, choice.modsL1} {
			if list == 1 && choice.sliceType != 0 {
				break
			}
			if mods == nil {
				header += "0"
			} else {
				header += "1"
				for _, index := range mods {
					header += fmt.Sprintf("%01b", index)
				}
			}
		}
		if choice.sliceType == 0 {
			header += "0" // mvd_l1_zero_flag.
			if choice.fromL0 {
				header += "1"
			} else {
				header += "0"
			}
		}
		count := choice.refsL0
		if choice.sliceType == 0 && !choice.fromL0 {
			count = choice.refsL1
		}
		if count > 1 {
			header += ueBits(choice.index)
		}
		header += ueBits(0) // five_minus_max_num_merge_cand.
	}
	header += seBits(0) + "1"
	return authoredNAL(1, append(packedHeader(header), 0xff, 0xff))
}

func TestCollocatedListIdentity(t *testing.T) {
	sets, metadata, idr := headerFixtureParts(t)
	vps := sets[:bytes.Index(sets, []byte{0, 0, 1, 66, 1})]
	pps := conformancePPS(0, 0, 0, false)
	pps = pps[:len(pps)-4] + "1100"
	p0 := collocatedChoice{sliceType: 1, refsL0: 1}
	p1 := collocatedChoice{sliceType: 1, refsL0: 2, index: 1}
	b0 := collocatedChoice{refsL0: 1, refsL1: 2, index: 1}
	b1 := collocatedChoice{refsL0: 1, refsL1: 1}
	for _, tc := range []struct {
		name                string
		a, b                collocatedChoice
		firstI, long, valid bool
	}{
		{"P-index1-B-list1-index0", p1, b1, false, false, true},
		{"P-index0-B-list1-index1", p0, b0, false, false, true},
		{"B-list0-and-P", collocatedChoice{refsL0: 1, refsL1: 1, fromL0: true}, p0, false, false, true},
		{"repeated-default-list0", collocatedChoice{sliceType: 1, refsL0: 3, index: 2}, p0, false, false, true},
		{"repeated-default-list1", collocatedChoice{refsL0: 1, refsL1: 3, index: 2}, collocatedChoice{sliceType: 1, refsL0: 1, modsL0: []uint64{1}}, false, false, true},
		{"different-P-and-B", p0, b1, false, false, false},
		{"first-I-then-same-P-B", p0, b0, true, false, true},
		{"first-I-then-different-P-B", p0, b1, true, false, false},
		{"long-default-P-B", p0, b1, false, true, true},
		{"long-index1-P-B", p1, collocatedChoice{refsL0: 1, refsL1: 2, index: 1}, false, true, true},
		{"long-different-P-B", p0, collocatedChoice{refsL0: 1, refsL1: 2, index: 1}, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sps := conformanceSPS(3, 192, 2, 6)
			sps = sps[:len(sps)-4] + "1000"
			prior := "1" + ueBits(5) + ueBits(2) + "1" + "00000010" + "0" + ueBits(1) + ueBits(0) + ueBits(1) + "0"
			if tc.long {
				sps = sps[:len(sps)-5] + "1" + ueBits(0) + "1000"
				prior += ueBits(0)
			}
			prior += "1" + seBits(0) + "1"
			data := bytes.Join([][]byte{vps, authoredNAL(33, packedHeader(sps+"1")), authoredNAL(34, packedHeader(pps+"1")), metadata, idr, authoredNAL(1, append(packedHeader(prior), 0xff, 0xff))}, nil)
			address := uint64(0)
			if tc.firstI {
				data = append(data, collocatedSegment(address, tc.long, collocatedChoice{sliceType: 2})...)
				address++
			}
			data = append(data, collocatedSegment(address, tc.long, tc.a)...)
			data = append(data, collocatedSegment(address+1, tc.long, tc.b)...)
			checkAssociationExtraction(t, data, tc.valid, []int64{0, 1, 2})
		})
	}
}

func associationSegment(first, dependent bool, kind byte, noPrior bool, long string) []byte {
	h := "1"
	if !first {
		h = "0"
	}
	if kind >= 16 {
		if noPrior {
			h += "1"
		} else {
			h += "0"
		}
	}
	h += ueBits(5)
	if !first {
		if dependent {
			h += "1"
		} else {
			h += "0"
		}
		h += "1"
	}
	if !dependent {
		h += ueBits(2) + "1"
		if kind != 19 && kind != 20 {
			h += "00000001" + "011" + long
		}
		h += seBits(0)
	}
	h += "1"
	return authoredNAL(kind, append(packedHeader(h), 0xff, 0xff))
}
func TestDependentAndLongTermAssociation(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	vps := sets[:bytes.Index(sets, []byte{0, 0, 1, 66, 1})]
	base := conformanceSPS(3, 128, 2, 6)
	duplicateSPS := base[:len(base)-5] + "1" + ueBits(2) + strings.Repeat("000000000", 2) + "0000"
	pps := authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1"))
	for _, tc := range []struct {
		name  string
		a, b  string
		valid bool
	}{{"valid-LT-index0", "0", "0", true}, {"valid-LT-index1", "1", "1", true}, {"forbidden-LT-index-source-different", "0", "1", false}} {
		t.Run(tc.name, func(t *testing.T) {
			sp, err := parseSPS(packedHeader(duplicateSPS + "1"))
			if err != nil || sp.longCount != 2 || sp.long[0] != sp.long[1] {
				t.Fatalf("duplicate SPS LT candidates %v %v", sp, err)
			}
			long := func(index string) string { return ueBits(1) + ueBits(0) + index + "0" }
			data := bytes.Join([][]byte{vps, authoredNAL(33, packedHeader(duplicateSPS+"1")), pps, metadata, associationSegment(true, false, 19, false, ""), associationSegment(true, false, 1, false, long(tc.a)), associationSegment(false, false, 1, false, long(tc.b))}, nil)
			checkAssociationExtraction(t, data, tc.valid, []int64{0, 1})
		})
	}
	for _, tc := range []struct {
		name             string
		a, b             bool
		dependent, valid bool
	}{{"valid-dependent-noPrior0", false, false, true, true}, {"valid-dependent-noPrior1", true, true, true, true}, {"forbidden-dependent-noPrior-different", false, true, true, false}, {"forbidden-independent-noPrior-different", false, true, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			data := bytes.Join([][]byte{vps, authoredNAL(33, packedHeader(base+"1")), pps, metadata, associationSegment(true, false, 19, tc.a, ""), associationSegment(false, tc.dependent, 19, tc.b, "")}, nil)
			checkAssociationExtraction(t, data, tc.valid, []int64{0})
		})
	}
	// The cumulative cycle field already distinguishes raw cycles when the
	// from-SPS boundary and all preceding fields agree. Verify that preservation.
	ltSPS := base[:len(base)-5] + "1" + ueBits(0) + "0000"
	for _, tc := range []struct {
		name  string
		a, b  uint64
		valid bool
	}{{"valid-raw-cycle0", 0, 0, true}, {"forbidden-raw-cycle-different", 0, 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			long := func(n uint64) string { return ueBits(1) + "00000000" + "01" + ueBits(n) }
			data := bytes.Join([][]byte{vps, authoredNAL(33, packedHeader(ltSPS+"1")), pps, metadata, associationSegment(true, false, 19, false, ""), associationSegment(true, false, 1, false, long(tc.a)), associationSegment(false, false, 1, false, long(tc.b))}, nil)
			checkAssociationExtraction(t, data, tc.valid, []int64{0, 1})
		})
	}
}
func checkAssociationExtraction(t *testing.T, data []byte, valid bool, pocs []int64) {
	t.Helper()
	e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
	if valid {
		if err != nil {
			t.Fatal(err)
		}
		checkIRAPFrames(t, e, pocs)
		e.memory.release()
	} else if !errors.Is(err, ErrInvalidBitstream) {
		if err == nil {
			m, pe := e.PlotMetadata()
			t.Errorf("FORBIDDEN ACCEPTED frames%d profile%s projectionErr%v", len(e.Frames), m.Profile, pe)
			e.memory.release()
		} else {
			t.Fatalf("wrong error %v", err)
		}
	}
}

func TestLongTermCycleRange(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	vps := sets[:bytes.Index(sets, []byte{0, 0, 1, 66, 1})]
	pps, err := parsePPS(packedHeader(conformancePPS(0, 0, 0, false) + "1"))
	if err != nil {
		t.Fatal(err)
	}
	idr := authoredNAL(19, append(packedHeader("10"+ueBits(5)+ueBits(2)+"1"+seBits(0)+"1"), 0xff, 0xff))
	for _, width := range []uint{4, 8, 16} {
		sps := "00100001" + main10PTL(120) + ueBits(3) + ueBits(1) + ueBits(128) + ueBits(64) + "0" + ueBits(2) + ueBits(2) + ueBits(uint64(width-4)) + "1" + ueBits(3) + ueBits(2) + ueBits(0) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(0) + "0000" + ueBits(0) + "1" + ueBits(0) + "0000"
		parsed, err := parseSPS(packedHeader(sps + "1"))
		if err != nil || parsed.pocBits != width {
			t.Fatalf("SPS%d-bit POC %v %v", width, parsed, err)
		}
		max := uint64(1) << (32 - width)
		cases := []struct {
			cycles []uint64
			valid  bool
		}{{[]uint64{0}, true}, {[]uint64{1}, true}, {[]uint64{max}, true}, {[]uint64{max + 1}, false}}
		if width == 8 {
			cases = append(cases, struct {
				cycles []uint64
				valid  bool
			}{[]uint64{max, max}, true}, struct {
				cycles []uint64
				valid  bool
			}{[]uint64{max, max + 1}, false})
		}
		for _, tc := range cases {
			t.Run(fmt.Sprintf("pocBits%d/cycles%v", width, tc.cycles), func(t *testing.T) {
				// An unused LT Foll entry may be "no reference picture" (8.3.2 note4).
				// The real current non-IRAP I picture retains the normative raw UE bound.
				bits := "1" + ueBits(5) + ueBits(2) + "1" + fmt.Sprintf("%0*b", width, 1) + "011" + ueBits(uint64(len(tc.cycles)))
				for _, n := range tc.cycles {
					bits += fmt.Sprintf("%0*b", width, 0) + "01" + ueBits(n)
				}
				bits += seBits(0) + "1"
				raw := append(packedHeader(bits), 0xff, 0xff)
				_, headerErr := parseSlice(raw, 1, 0, pps, parsed)
				data := bytes.Join([][]byte{vps, authoredNAL(33, packedHeader(sps+"1")), authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1")), metadata, idr, authoredNAL(1, raw)}, nil)
				e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
				if tc.valid {
					if err != nil || headerErr != nil {
						t.Fatalf("valid bounded cycle: parse%v extract%v", headerErr, err)
					}
					checkIRAPFrames(t, e, []int64{0, 1})
					e.memory.release()
				} else if !errors.Is(err, ErrInvalidBitstream) || !errors.Is(headerErr, ErrInvalidBitstream) {
					if err == nil {
						m, pe := e.PlotMetadata()
						t.Errorf("FORBIDDEN ACCEPTED parse%v frames%d profile%s projectionErr%v", headerErr, len(e.Frames), m.Profile, pe)
						e.memory.release()
					} else {
						t.Fatalf("wrong classification parse%v extract%v", headerErr, err)
					}
				}
			})
		}
	}
}
