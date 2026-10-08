// SPDX-License-Identifier: GPL-2.0-or-later
package bdinfo

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	scanner "github.com/autobrr/go-bdinfo/pkg/bdinfo"
)

// The producer owns adaptation syntax because callbacks contain only ES/PES
// facts. Seamless controls test field structure without asserting codec splices.
func TestScanTransportAdaptation(t *testing.T) {
	clock := []byte{0, 0, 0, 0, 0x7e, 0}
	for _, tc := range []struct {
		name   string
		flags  byte
		fields []byte
		badPad bool
		valid  bool
	}{
		{"normal", 0, nil, false, true},
		{"stuffing-zero", 0, nil, true, false},
		{"PCR", 0x10, clock, false, true},
		{"PCR-reserved-zero", 0x10, []byte{0, 0, 0, 0, 0, 0}, false, false},
		{"PCR-and-OPCR", 0x18, append(append([]byte(nil), clock...), clock...), false, true},
		{"OPCR-without-PCR", 8, clock, false, false},
		{"extension", 1, []byte{1, 0x1f}, false, true},
		{"extension-reserved-zero", 1, []byte{1, 0x10}, false, false},
		{"extension-reserved-tail", 1, []byte{2, 0x1f, 0xff}, false, true},
		{"extension-tail-zero", 1, []byte{2, 0x1f, 0}, false, false},
		{"defined-piecewise-rate", 1, []byte{6, 0xdf, 0x80, 0, 0xc0, 0, 1}, false, true},
		{"defined-piecewise-zero", 1, []byte{6, 0xdf, 0x80, 0, 0xc0, 0, 0}, false, false},
		{"undefined-piecewise-zero", 1, []byte{6, 0xdf, 0, 0, 0xc0, 0, 0}, false, true},
		{"seamless-structure", 5, []byte{0, 6, 0x3f, 1, 0, 1, 0, 1}, false, true},
		{"seamless-without-countdown", 1, []byte{6, 0x3f, 1, 0, 1, 0, 1}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := discFixture(t)
			raw := fixtureRead(t, "../../extract/testdata/single.hevc")
			pes := []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80, 5, 0x21, 0, 1, 0, 1}
			pes = append(pes, raw...)
			binary.BigEndian.PutUint16(pes[4:6], uint16(len(pes)-6))
			var data []byte
			cc := byte(0)
			for first := true; len(pes) > 0; first = false {
				n := min(182, len(pes))
				if first {
					n = min(n, 182-len(tc.fields))
				}
				packet := bytes.Repeat([]byte{0xff}, 192)
				clear(packet[:4])
				packet[4], packet[5], packet[6], packet[7], packet[8], packet[9] = 0x47, 0x10, 0x11, 0x30|cc, byte(183-n), 0
				if first {
					packet[5] |= 0x40
					packet[9] = tc.flags
					copy(packet[10:], tc.fields)
					if tc.badPad {
						if 10+len(tc.fields) >= 192-n {
							t.Fatal("missing adaptation padding in fixture")
						}
						packet[10+len(tc.fields)] = 0
					}
				}
				copy(packet[192-n:], pes[:n])
				data = append(data, packet...)
				pes = pes[n:]
				cc = (cc + 1) & 15
			}
			cp := filepath.Join(root, "BDMV", "CLIPINF", "00001.clpi")
			clpi := fixtureRead(t, cp)
			binary.BigEndian.PutUint32(clpi[56:], uint32(len(data)/192))
			if err := os.WriteFile(cp, clpi, 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "BDMV", "STREAM", "00001.m2ts"), data, 0644); err != nil {
				t.Fatal(err)
			}
			settings := scanner.DefaultSettings(".")
			settings.FilterLoopingPlaylists, settings.FilterShortPlaylists, settings.BigPlaylistOnly = false, false, false
			base, err := scanner.Run(t.Context(), scanner.Options{Path: root, Settings: settings})
			if err != nil {
				t.Fatal(err)
			}
			out, err := Scan(t.Context(), Options{BDInfo: scanner.Options{Path: root, Settings: settings}})
			if err != nil {
				t.Fatal(err)
			}
			if out.Report.Report != base.Report || out.Report.QuickSummary != base.QuickSummary || out.Report.ForumsBlock != base.ForumsBlock || len(out.Playlists) != 2 {
				t.Fatal("ordinary report changed or missing playlist outcomes")
			}
			for _, result := range out.Playlists {
				if tc.valid {
					if result.Err != nil || result.Extraction == nil || len(result.Extraction.Frames) != 2 {
						t.Fatalf("valid adaptation rejected: %v", result.Err)
					}
				} else if result.Extraction != nil || result.Err == nil {
					t.Errorf("malformed adaptation completed: %+v", result)
				}
			}
		})
	}
}
