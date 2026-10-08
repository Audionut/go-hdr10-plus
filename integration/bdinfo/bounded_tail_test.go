// SPDX-License-Identifier: GPL-2.0-or-later
package bdinfo

import (
	"bytes"
	"encoding/binary"
	scanner "github.com/autobrr/go-bdinfo/pkg/bdinfo"
	"os"
	"path/filepath"
	"testing"
)

func TestScanBoundedPESTail(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tail     byte
		separate bool
		valid    bool
	}{
		{"same-packet-ff", 255, false, true}, {"same-packet-zero", 0, false, false},
		{"continuation-ff", 255, true, true}, {"continuation-zero", 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := discFixture(t)
			raw := fixtureRead(t, "../../extract/testdata/single.hevc")
			pes := append([]byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80, 5, 0x21, 0, 1, 0, 1}, raw...)
			binary.BigEndian.PutUint16(pes[4:6], uint16(len(pes)-6))
			var transport []byte
			cc := byte(0)
			for first := true; len(pes) > 0; first = false {
				n := min(181, len(pes))
				payload := bytes.Clone(pes[:n])
				pes = pes[n:]
				if len(pes) == 0 && !tc.separate {
					payload = append(payload, tc.tail)
				}
				packet := bytes.Repeat([]byte{255}, 192)
				clear(packet[:4])
				packet[4] = 0x47
				packet[5] = 0x10
				if first {
					packet[5] |= 0x40
				}
				packet[6] = 0x11
				packet[7] = 0x30 | cc
				packet[8] = byte(183 - len(payload))
				packet[9] = 0
				copy(packet[192-len(payload):], payload)
				transport = append(transport, packet...)
				cc = (cc + 1) & 15
			}
			if tc.separate {
				packet := bytes.Repeat([]byte{255}, 192)
				clear(packet[:4])
				packet[4] = 0x47
				packet[5] = 0x10
				packet[6] = 0x11
				packet[7] = 0x30 | cc
				packet[8] = 182
				packet[9] = 0
				packet[191] = tc.tail
				transport = append(transport, packet...)
			}
			cp := filepath.Join(root, "BDMV", "CLIPINF", "00001.clpi")
			clpi := fixtureRead(t, cp)
			binary.BigEndian.PutUint32(clpi[56:], uint32(len(transport)/192))
			if err := os.WriteFile(cp, clpi, 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "BDMV", "STREAM", "00001.m2ts"), transport, 0644); err != nil {
				t.Fatal(err)
			}
			settings := scanner.DefaultSettings(".")
			settings.FilterLoopingPlaylists = false
			settings.FilterShortPlaylists = false
			settings.BigPlaylistOnly = false
			baseline, err := scanner.Run(t.Context(), scanner.Options{Path: root, Settings: settings})
			if err != nil {
				t.Fatal(err)
			}
			out, err := Scan(t.Context(), Options{BDInfo: scanner.Options{Path: root, Settings: settings}})
			if err != nil {
				t.Fatal(err)
			}
			if out.Report.Report != baseline.Report || out.Report.QuickSummary != baseline.QuickSummary || out.Report.ForumsBlock != baseline.ForumsBlock {
				t.Fatal("ordinary report changed with collector")
			}
			if len(out.Playlists) != 2 {
				t.Fatalf("playlists=%d", len(out.Playlists))
			}
			if len(out.Report.Collection) != 1 {
				t.Fatal("missing physical collection outcome")
			}
			collection := out.Report.Collection[0]
			if tc.valid {
				if !collection.CleanEOF || collection.SourcePacketCount != uint64(len(transport)/192) || collection.DeliveredBytes != uint64(len(raw)) || len(out.SourceErrors) != 0 {
					t.Fatalf("valid padding changed delivery or EOF facts: %+v", collection)
				}
			} else if collection.CleanEOF || collection.SourcePacketCount != 0 || len(out.SourceErrors) != 1 {
				t.Fatalf("malformed tail published normal EOF: %+v", collection)
			}
			t.Logf("clean=%v bytes=%d original=%d sources=%v", out.Report.Collection[0].CleanEOF, out.Report.Collection[0].DeliveredBytes, len(raw), out.SourceErrors)
			for _, p := range out.Playlists {
				if tc.valid {
					if p.Err != nil || p.Extraction == nil || len(p.Extraction.Frames) != 2 {
						t.Fatalf("valid padding: %v", p.Err)
					}
				} else if p.Extraction != nil || p.Err == nil {
					t.Errorf("malformed bounded PES tail falsely complete: extraction=%v err=%v", p.Extraction, p.Err)
				}
			}
		})
	}
}
