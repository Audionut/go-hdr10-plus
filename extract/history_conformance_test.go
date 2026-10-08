package extract

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Independent H.265 7.3.6.1 headers; arbitrary skipped slice bodies.
func historyParameterSets(t *testing.T, sourced, used bool) ([]byte, []byte) {
	_, meta, _ := headerFixtureParts(t)
	ptl := main10PTL(120) + strings.Repeat("0", 16)
	vps := "0010110000000010" + strings.Repeat("1", 16) + ptl + "0" + ueBits(4) + ueBits(3) + ueBits(0) + "000000" + ueBits(0) + "00"
	tail := "1" + ueBits(0) + "0000"
	if sourced {
		tail = "1" + ueBits(1) + "00000000"
		if used {
			tail += "1"
		} else {
			tail += "0"
		}
		tail += "0000"
	}
	sps := "00100010" + ptl + ueBits(3) + ueBits(1) + ueBits(128) + ueBits(64) + "0" + ueBits(2) + ueBits(2) + ueBits(4) + "0" + ueBits(4) + ueBits(3) + ueBits(0) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(0) + "0000" + ueBits(0) + tail
	return bytes.Join([][]byte{authoredNAL(32, packedHeader(vps+"1")), authoredNAL(33, packedHeader(sps+"1")), authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1"))}, nil), meta
}
func historyPicture(poc uint64, temporal byte, retain, lt, msb, sourced, used bool) []byte {
	slice := uint64(2)
	if lt && used {
		slice = 1
	}
	h := "1" + ueBits(5) + ueBits(slice) + "1" + fmt.Sprintf("%08b", poc) + "0"
	n := uint64(0)
	if retain {
		n = 1
	}
	h += ueBits(n) + ueBits(0)
	if retain {
		h += ueBits(poc-1) + "0"
	}
	n = 0
	if lt {
		n = 1
	}
	if sourced {
		h += ueBits(n) + ueBits(0)
	} else {
		h += ueBits(n)
	}
	if lt {
		if !sourced {
			h += "00000000"
			if used {
				h += "1"
			} else {
				h += "0"
			}
		}
		if msb {
			h += "1" + ueBits(0)
		} else {
			h += "0"
		}
	}
	if slice == 1 {
		h += "0" + ueBits(0)
	}
	h += seBits(0) + "1"
	nal := authoredNAL(1, append(packedHeader(h), 0xff, 0xff))
	nal[4] = temporal + 1
	return nal
}
func historyParts(t *testing.T, retain, msb, sourced, used bool) ([]byte, []byte) {
	sets, meta := historyParameterSets(t, sourced, used)
	a := bytes.Join([][]byte{sets, meta, subLayerLongPicture(0, 19, 0, 2, nil), historyPicture(127, 0, true, false, false, sourced, false), historyPicture(250, 0, retain, false, false, sourced, false), historyPicture(0, 1, false, false, false, sourced, false)}, nil)
	b := historyPicture(1, 1, false, true, msb, sourced, used)
	return a, b
}
func checkHistoryExtraction(t *testing.T, e *Extraction, err error, valid bool) {
	t.Helper()
	if valid {
		if err != nil {
			t.Fatalf("lawful neighbor: %v", err)
		}
		checkIRAPFrames(t, e, []int64{0, 127, 250, 256, 257})
		e.memory.release()
		return
	}
	if !errors.Is(err, ErrInvalidBitstream) {
		if err == nil {
			checkIRAPFrames(t, e, []int64{0, 127, 250, 256, 257})
			t.Errorf("PROHIBITED ACCEPTANCE: previous T0 RPS has POC0; intervening T1 POC256; current POC257 LTlsb0 needs MSB, live DPB no longer has0")
			e.memory.release()
		} else {
			t.Fatalf("wrong rejection: %v", err)
		}
	}
}
func TestLongTermPOCAmbiguityHistory(t *testing.T) {
	for _, sourced := range []bool{false, true} {
		for _, used := range []bool{false, true} {
			for _, retain := range []bool{false, true} {
				for _, msb := range []bool{false, true} {
					t.Run(fmt.Sprintf("SPS=%v/used=%v/prevRPSretain=%v/MSB=%v", sourced, used, retain, msb), func(t *testing.T) {
						a, b := historyParts(t, retain, msb, sourced, used)
						e, err := Extract(t.Context(), bytes.NewReader(append(a, b...)), Options{})
						checkHistoryExtraction(t, e, err, !retain || msb)
					})
				}
			}
		}
	}
	for _, retain := range []bool{false, true} {
		for _, msb := range []bool{false, true} {
			t.Run(fmt.Sprintf("seamless/prevRPSretain=%v/MSB=%v", retain, msb), func(t *testing.T) {
				a, b := historyParts(t, retain, msb, false, false)
				ak, bk := StreamKey{SourceID: "history-A"}, StreamKey{SourceID: "history-B"}
				ac := sourceClip(t, ak, a, []int64{0, 3000, 6000, 9000})
				bc := sourceClip(t, bk, b, []int64{12000})
				p := Playlist{Occurrences: []Occurrence{{Key: ak, Out: 6000, ClockOffset: Timestamp{Timescale: 90000, Valid: true}, Connection: ConnectionReset}, {Key: bk, In: 6000, Out: 7500, PlaylistStart: 6000, ClockOffset: Timestamp{Timescale: 90000, Valid: true}, Connection: ConnectionSeamless}}}
				e, err := Assemble(t.Context(), p, map[StreamKey]*Clip{ak: ac, bk: bc}, AssemblyOptions{})
				checkHistoryExtraction(t, e, err, !retain || msb)
			})
		}
	}
}

func TestPOCSetIdentity(t *testing.T) {
	for _, msb := range []bool{false, true} {
		t.Run(fmt.Sprintf("generatedToReal/MSB=%v", msb), func(t *testing.T) {
			sets, meta := historyParameterSets(t, false, false)
			// BLA0 generates unavailable following POC2, unused. The real T1 POC2 later replaces it.
			h := "10" + ueBits(5) + ueBits(2) + "1" + "00000000" + "0" + ueBits(0) + ueBits(1) + ueBits(1) + "0" + ueBits(0) + seBits(0) + "1"
			bla := authoredNAL(16, append(packedHeader(h), 0xff, 0xff))
			real := historyPicture(2, 1, false, false, false, false, false)
			h = "1" + ueBits(5) + ueBits(2) + "1" + "00000011" + "0" + ueBits(0) + ueBits(0) + ueBits(1) + "00000010" + "0"
			if msb {
				h += "1" + ueBits(0)
			} else {
				h += "0"
			}
			h += seBits(0) + "1"
			last := authoredNAL(1, append(packedHeader(h), 0xff, 0xff))
			last[4] = 2
			data := bytes.Join([][]byte{sets, meta, bla, real, last}, nil)
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if err != nil {
				t.Fatalf("lawful unique set neighbor: %v", err)
			}
			checkIRAPFrames(t, e, []int64{0, 2, 3})
			e.memory.release()
		})
	}
}

func TestPOCHistoryResets(t *testing.T) {
	for _, boundary := range []byte{0, 36, 37} {
		for _, irap := range []byte{16, 19} {
			t.Run(fmt.Sprintf("boundary%d/IRAP%d", boundary, irap), func(t *testing.T) {
				prefix, _ := historyParts(t, true, false, false, false)
				_, metadata := historyParameterSets(t, false, false)
				if boundary != 0 {
					prefix = append(prefix, authoredNAL(boundary, nil)...)
				}
				data := bytes.Join([][]byte{prefix, metadata, subLayerLongPicture(0, irap, 0, 2, nil), historyPicture(1, 1, false, true, false, false, false)}, nil)
				e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
				if err != nil {
					t.Fatal(err)
				}
				checkIRAPFrames(t, e, []int64{0, 127, 250, 256, 0, 1})
				e.memory.release()
			})
		}
	}
}

func TestPOCHistoryMaximumLSB(t *testing.T) {
	sets, metadata := historyParameterSets(t, false, false)
	vps := sets[:bytes.Index(sets, []byte{0, 0, 1, 66, 1})]
	ptl := main10PTL(120) + strings.Repeat("0", 16)
	sps := "00100010" + ptl + ueBits(3) + ueBits(1) + ueBits(128) + ueBits(64) + "0" + ueBits(2) + ueBits(2) + ueBits(12) + "0" + ueBits(4) + ueBits(3) + ueBits(0) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(0) + "0000" + ueBits(0) + "1" + ueBits(0) + "0000"
	// The initial CRA has POC65535. Retain it through T0 POCs98302 and
	// 131065, then drop it before the T1 picture with POC131071. The next
	// LT entry at LSB65535 requires an MSB cycle despite only131071 being live.
	picture := func(kind, temporal byte, lsb, shortDelta uint64, long, used, msb bool) []byte {
		h := "1"
		if kind >= 16 {
			h += "0"
		}
		sliceType := uint64(2)
		if used {
			sliceType = 1
		}
		h += ueBits(5) + ueBits(sliceType) + "1" + fmt.Sprintf("%016b", lsb) + "0"
		if shortDelta > 0 {
			h += ueBits(1) + ueBits(0) + ueBits(shortDelta-1) + "0"
		} else {
			h += ueBits(0) + ueBits(0)
		}
		if long {
			h += ueBits(1) + "1111111111111111"
			if used {
				h += "1"
			} else {
				h += "0"
			}
			if msb {
				h += "1" + ueBits(1)
			} else {
				h += "0"
			}
		} else {
			h += ueBits(0)
		}
		if sliceType == 1 {
			h += "0" + ueBits(0)
		}
		h += seBits(0) + "1"
		nal := authoredNAL(kind, append(packedHeader(h), 0xff, 0xff))
		nal[4] = temporal + 1
		return nal
	}
	for _, msb := range []bool{false, true} {
		t.Run(fmt.Sprintf("MSB%v", msb), func(t *testing.T) {
			data := bytes.Join([][]byte{vps, authoredNAL(33, packedHeader(sps+"1")), authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1")), metadata, picture(21, 0, 65535, 0, false, false, false), picture(1, 0, 32766, 32767, false, false, false), picture(1, 0, 65529, 0, true, false, true), picture(1, 1, 65535, 0, false, false, false), picture(1, 1, 0, 0, true, true, msb)}, nil)
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if msb {
				if err != nil {
					t.Fatal(err)
				}
				checkIRAPFrames(t, e, []int64{65535, 98302, 131065, 131071, 131072})
				e.memory.release()
			} else if !errors.Is(err, ErrInvalidBitstream) {
				if err == nil {
					t.Error("ambiguous maximum LSB accepted")
					e.memory.release()
				} else {
					t.Fatal(err)
				}
			}
		})
	}
}
