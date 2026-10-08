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

func TestScanTransportPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flag   byte
		fields []byte
		good   bool
	}{
		{"normal-control", 0, nil, true}, {"DSM-slow-motion", 8, []byte{0x21}, false},
		{"pack-fake-body", 1, []byte{0x4e, 2, 0xaa, 0xbb}, false}, {"ES-rate-zero", 0x10, []byte{0x80, 0, 1}, false},
		{"PES-stuffing33", 0, bytes.Repeat([]byte{0xff}, 33), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := discFixture(t)
			raw, err := os.ReadFile("../../extract/testdata/single.hevc")
			if err != nil {
				t.Fatal(err)
			}
			pes := []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80 | tc.flag, byte(5 + len(tc.fields)), 0x21, 0, 1, 0, 1}
			pes = append(pes, tc.fields...)
			pes = append(pes, raw...)
			binary.BigEndian.PutUint16(pes[4:6], uint16(len(pes)-6))
			var transport []byte
			cc := byte(0)
			first := true
			for len(pes) > 0 {
				n := min(182, len(pes))
				packet := bytes.Repeat([]byte{0xff}, 192)
				copy(packet[:4], []byte{0, 0, 0, 0})
				packet[4] = 0x47
				packet[5] = 0x10
				if first {
					packet[5] |= 0x40
				}
				packet[6] = 0x11
				packet[7] = 0x30 | cc
				packet[8] = byte(183 - n)
				packet[9] = 0
				copy(packet[192-n:], pes[:n])
				transport = append(transport, packet...)
				pes = pes[n:]
				cc = (cc + 1) & 15
				first = false
			}
			cp := filepath.Join(root, "BDMV", "CLIPINF", "00001.clpi")
			clpi, e := os.ReadFile(cp)
			if e != nil {
				t.Fatal(e)
			}
			binary.BigEndian.PutUint32(clpi[56:], uint32(len(transport)/192))
			if e = os.WriteFile(cp, clpi, 0644); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(root, "BDMV", "STREAM", "00001.m2ts"), transport, 0644); e != nil {
				t.Fatal(e)
			}
			settings := scanner.DefaultSettings(".")
			settings.FilterLoopingPlaylists = false
			settings.FilterShortPlaylists = false
			settings.BigPlaylistOnly = false
			out, e := Scan(t.Context(), Options{BDInfo: scanner.Options{Path: root, Settings: settings}})
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("sources=%v cleanEOF=%v boundaries=%d", out.SourceErrors, out.Report.Collection[0].CleanEOF, out.Report.Collection[0].Boundaries)
			if len(out.Playlists) != 2 {
				t.Fatalf("unexpected timelines %d", len(out.Playlists))
			}
			for _, r := range out.Playlists {
				if tc.good {
					if r.Err != nil || r.Extraction == nil || len(r.Extraction.Frames) != 2 {
						t.Errorf("valid control: %v", r.Err)
					}
				} else if r.Extraction != nil || r.Err == nil {
					t.Errorf("unsupported/malformed transport falsely complete: %s extraction=%v err=%v", tc.name, r.Extraction != nil, r.Err)
				}
			}
		})
	}
}
