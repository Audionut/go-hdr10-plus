package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"strings"
	"testing"
)

func TestTileScanAndGeometry(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	vps := sets[:bytes.Index(sets, []byte{0, 0, 1, 66, 1})]
	for _, tc := range []struct {
		name                      string
		width, height, cols, rows uint64
		widths, heights           []uint64
		addresses                 []uint64
		uniform, valid            bool
	}{
		{"uniform-tile-order", 4, 2, 2, 1, nil, nil, []uint64{0, 1, 4, 5, 2, 3, 6, 7}, true, true},
		{"raster-order-crosses-tiles", 4, 2, 2, 1, nil, nil, []uint64{0, 2, 4, 6}, true, false},
		{"explicit-tile-order", 5, 3, 3, 2, []uint64{1, 3}, []uint64{1}, []uint64{0, 1, 2, 3, 4, 5, 10, 6, 7, 8, 11, 12, 13, 9, 14}, false, true},
		{"explicit-tile-backward", 5, 3, 3, 2, []uint64{1, 3}, []uint64{1}, []uint64{0, 5, 6, 10}, false, false},
		{"uniform-rows", 4, 2, 1, 2, nil, nil, []uint64{0, 4}, true, true},
		{"single-tile", 4, 2, 1, 1, nil, nil, []uint64{0}, true, false},
		{"positive-last-column", 4, 2, 2, 1, []uint64{2}, nil, []uint64{0, 4, 2, 6}, false, true},
		{"zero-last-column", 4, 2, 2, 1, []uint64{4}, nil, []uint64{0}, false, false},
		{"negative-last-column", 4, 2, 2, 1, []uint64{5}, nil, []uint64{0}, false, false},
		{"zero-last-row", 4, 2, 1, 2, nil, []uint64{2}, []uint64{0}, false, false},
		{"negative-last-row", 4, 2, 1, 2, nil, []uint64{3}, []uint64{0}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Four CTBs per authored column unit preserve the ordering cases
			// while satisfying Main/Main10's minimum 256-sample tile width.
			widthCTB := tc.width * 4
			sps := strings.Replace(conformanceSPS(3, widthCTB*64, 2, 6), ueBits(widthCTB*64)+ueBits(64), ueBits(widthCTB*64)+ueBits(tc.height*64), 1)
			pps := ueBits(5) + ueBits(3) + "1100000" + ueBits(0) + ueBits(0) + seBits(0) + "000" + seBits(0) + seBits(0) + "000010" + ueBits(tc.cols-1) + ueBits(tc.rows-1)
			if tc.uniform {
				pps += "1"
			} else {
				pps += "0"
				for _, width := range tc.widths {
					pps += ueBits(width*4 - 1)
				}
				for _, height := range tc.heights {
					pps += ueBits(height - 1)
				}
			}
			pps += "00000" + ueBits(0) + "00" + "1"
			// Repeated PPS exercises its geometry cursor into interned owned bytes.
			data := bytes.Join([][]byte{vps, authoredNAL(33, packedHeader(sps+"1")), authoredNAL(34, packedHeader(pps)), authoredNAL(34, packedHeader(pps)), metadata}, nil)
			for i, address := range tc.addresses {
				address = (address/tc.width)*widthCTB + (address%tc.width)*4
				header := "10" + ueBits(5)
				dependent := i > 0 && i%2 == 0
				if i > 0 {
					header = "00" + ueBits(5)
					if dependent {
						header += "1"
					} else {
						header += "0"
					}
					header += fmt.Sprintf("%0*b", bits.Len64(widthCTB*tc.height-1), address)
				}
				if !dependent {
					header += ueBits(2) + "1" + seBits(0)
				}
				header += ueBits(0) + "1"
				data = append(data, authoredNAL(19, append(packedHeader(header), 0xff, 0xff))...)
			}
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if tc.valid {
				if err != nil || len(e.Frames) != 1 || e.Frames[0].AUOrdinal != 0 {
					t.Fatalf("valid tile picture: %v %v", e, err)
				}
			} else if e != nil || !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("invalid tile picture: %v %v", e, err)
			}
		})
	}
}

func TestReservedSliceFlags(t *testing.T) {
	sets, metadata, _ := headerFixtureParts(t)
	start := bytes.Index(sets, []byte{0, 0, 1, 68, 1})
	pps, err := unescape(sets[start+5:], true, false)
	if err != nil {
		t.Fatal(err)
	}
	pps = changeBits(pps, 12, 3, 6)
	for _, flags := range []string{"000000", "100000", "111111"} {
		t.Run(flags, func(t *testing.T) {
			header := "11" + ueBits(5) + flags + ueBits(2) + "1" + seBits(0) + "1"
			data := bytes.Join([][]byte{sets[:start], authoredNAL(34, pps), metadata, authoredNAL(19, append(packedHeader(header), 0xff, 0xff))}, nil)
			if e, err := Extract(t.Context(), bytes.NewReader(data), Options{}); err != nil || len(e.Frames) != 1 {
				t.Fatalf("ignored reserved flags: %v %v", e, err)
			}
		})
	}
}

func TestPPSTemporalAssociation(t *testing.T) {
	raw := rawFixture(t, "single")
	start := bytes.Index(raw, []byte{0, 0, 1, 68, 1})
	for _, temporal := range []byte{0, 1, 6} {
		t.Run(fmt.Sprintf("active%d", temporal), func(t *testing.T) {
			data := slices.Clone(raw)
			data[start+4] = temporal + 1
			e, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if temporal == 0 {
				if err != nil || len(e.Frames) != 1 {
					t.Fatalf("valid PPS temporal reference: %v %v", e, err)
				}
			} else if e != nil || !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("invalid PPS temporal reference: %v %v", e, err)
			}
		})
	}
	sets, metadata, picture := headerFixtureParts(t)
	unused := strings.Replace(conformancePPS(0, 0, 0, false), ueBits(5)+ueBits(3), ueBits(6)+ueBits(3), 1)
	nal := authoredNAL(34, packedHeader(unused+"1"))
	nal[4] = 7
	data := bytes.Join([][]byte{sets, nal, metadata, picture}, nil)
	if e, err := Extract(t.Context(), bytes.NewReader(data), Options{}); err != nil || len(e.Frames) != 1 {
		t.Fatalf("inactive PPS temporal ID: %v %v", e, err)
	}
	ptl := main10PTL(120) + strings.Repeat("0", 16)
	vps := "0010110000000011" + strings.Repeat("1", 16) + ptl + "0" + ueBits(3) + ueBits(2) + ueBits(0) + "000000" + ueBits(0) + "00"
	sps := "00100011" + ptl + ueBits(3) + ueBits(1) + ueBits(128) + ueBits(64) + "0" + ueBits(2) + ueBits(2) + ueBits(4) + "0" + ueBits(3) + ueBits(2) + ueBits(0) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(3) + ueBits(0) + ueBits(0) + "0000" + ueBits(0) + "00000"
	pps := authoredNAL(34, packedHeader(conformancePPS(0, 0, 0, false)+"1"))
	data = bytes.Join([][]byte{authoredNAL(32, packedHeader(vps+"1")), authoredNAL(33, packedHeader(sps+"1")), pps, metadata, picture}, nil)
	pps[4] = 2
	next := authoredReferencePicture(1, 3, 2, nil, nil) // Nested T1 TSA.
	next[4] = 2
	data = append(data, pps...)
	data = append(data, next...)
	if e, err := Extract(t.Context(), bytes.NewReader(data), Options{}); err != nil || len(e.Frames) != 2 {
		t.Fatalf("PPS temporal ID equals later picture: %v %v", e, err)
	}
}

func assertTransportConformance(t *testing.T, packets [][]byte, valid bool) {
	t.Helper()
	for _, format := range []Format{TS, M2TS} {
		var data []byte
		for _, packet := range packets {
			if format == M2TS {
				data = append(data, 0, 0, 0, 0)
			}
			data = append(data, packet...)
		}
		e, err := Extract(t.Context(), bytes.NewReader(data), Options{Format: format})
		if valid {
			if err != nil || len(e.Frames) != 1 {
				t.Fatalf("format%d valid header: %v %v", format, e, err)
			}
		} else if e != nil || !errors.Is(err, ErrInvalidBitstream) {
			t.Fatalf("format%d invalid declared extent: %v %v", format, e, err)
		}
	}
}

func TestTransportOptionalExtents(t *testing.T) {
	control := tsTestFile(t, true, 0, false)[:2]
	raw := rawFixture(t, "single")
	for _, tc := range []struct {
		name   string
		flag   byte
		fields []byte
	}{
		{"none", 0, nil},
		{"PCR", 0x10, []byte{0, 0, 0, 0, 0x7e, 0}},
		{"OPCR", 0x18, []byte{0, 0, 0, 0, 0x7e, 0, 0, 0, 0, 0, 0x7e, 0}},
		{"splice", 0x04, []byte{0}},
		{"private", 0x02, []byte{2, 0xaa, 0xbb}},
		{"extension", 0x01, []byte{1, 0x1f}},
		{"LTW", 0x01, []byte{3, 0x9f, 0, 0}},
		{"piecewise", 0x01, []byte{4, 0x5f, 0xc0, 0, 0}},
		{"seamless", 0x05, []byte{0, 6, 0x3f, 1, 0, 1, 0, 1}},
	} {
		for _, truncated := range []bool{false, true} {
			if truncated && tc.flag == 0 {
				continue
			}
			t.Run(fmt.Sprintf("adaptation/%s/truncated%v", tc.name, truncated), func(t *testing.T) {
				fields := tc.fields
				if truncated {
					fields = fields[:len(fields)-1]
				}
				n := 1 + len(fields)
				payload := tsPES(raw, 0, 0, true)
				payload = append(payload, bytes.Repeat([]byte{0xff}, 183-n-len(payload))...)
				packet := tsPacket(0x1011, 0, true, payload)
				packet[5] = tc.flag
				copy(packet[6:], fields)
				assertTransportConformance(t, append(slices.Clone(control), packet), !truncated)
			})
		}
	}
	for _, tc := range []struct {
		name   string
		flag   byte
		fields []byte
	}{
		{"none", 0, nil},
		{"ESCR", 0x20, []byte{0xc4, 0, 4, 0, 4, 1}},
		{"rate", 0x10, []byte{0x80, 0, 3}},
		{"copy", 0x04, []byte{0x80}},
		{"CRC", 0x02, []byte{0, 0}},
		{"extension", 0x01, []byte{0x0e}},
		{"private", 0x01, append([]byte{0x8e}, make([]byte, 16)...)},
		{"pack", 0x01, []byte{0x4e, 14, 0, 0, 1, 0xba, 0x44, 0, 4, 0, 4, 1, 0, 0, 7, 0xf8}},
		{"sequence", 0x01, []byte{0x2e, 0x80, 0x80}},
		{"buffer", 0x01, []byte{0x1e, 0x40, 0}},
		{"extension2", 0x01, []byte{0x0f, 0x81, 0xff}},
	} {
		for _, truncated := range []bool{false, true} {
			if truncated && tc.flag == 0 {
				continue
			}
			t.Run(fmt.Sprintf("PES/%s/truncated%v", tc.name, truncated), func(t *testing.T) {
				fields := tc.fields
				if truncated {
					fields = fields[:len(fields)-1]
				}
				base := tsPES(raw, 0, 0, true)
				pes := append(slices.Clone(base[:19]), fields...)
				pes = append(pes, base[19:]...)
				pes[7] |= tc.flag
				pes[8] = byte(10 + len(fields))
				binary.BigEndian.PutUint16(pes[4:6], uint16(len(pes)-6))
				cc := byte(0)
				packets := tsPESPackets(0x1011, pes, &cc, 7)
				assertTransportConformance(t, append(slices.Clone(control), packets...), !truncated)
			})
		}
	}
}
