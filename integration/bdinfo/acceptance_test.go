// SPDX-License-Identifier: GPL-2.0-or-later
package bdinfo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fmt"
	"github.com/Audionut/go-hdr10-plus/extract"
	scanner "github.com/autobrr/go-bdinfo/pkg/bdinfo"
	"github.com/autobrr/go-bdinfo/pkg/bdinfo/video"
)

func writeTransport(t testing.TB, path string, data []byte, pts []uint64) uint32 {
	t.Helper()
	var samples [][]byte
	start := 0
	for pos := 0; pos < len(data); {
		end := len(data)
		next := bytes.Index(data[pos+3:], []byte{0, 0, 1})
		if next >= 0 {
			end = pos + 3 + next
		}
		if data[pos+3]>>1&63 <= 31 {
			samples = append(samples, data[start:end])
			start = end
		}
		pos = end
	}
	if len(samples) != len(pts) {
		t.Fatal("fixture timing")
	}
	var transport []byte
	cc := byte(0)
	for i, body := range samples {
		clock := pts[i]
		pes := []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80, 5, 0x21 | byte(clock>>29)&14, byte(clock >> 22), byte(clock>>14)&0xfe | 1, byte(clock >> 7), byte(clock<<1) | 1}
		pes = append(pes, body...)
		first := true
		for len(pes) > 0 {
			n := min(182, len(pes))
			packet := make([]byte, 192)
			packet[4] = 0x47
			packet[5] = 0x10
			if first {
				packet[5] |= 0x40
			}
			packet[6] = 0x11
			packet[7] = 0x30 | cc
			packet[8] = byte(183 - n)
			for j := 10; j < 192-n; j++ {
				packet[j] = 0xff
			}
			copy(packet[192-n:], pes[:n])
			transport = append(transport, packet...)
			pes = pes[n:]
			first = false
			cc = (cc + 1) & 15
		}
	}
	if err := os.WriteFile(path, transport, 0644); err != nil {
		t.Fatal(err)
	}
	return uint32(len(transport) / 192)
}
func TestCompletedCollectorsShareOneQuota(t *testing.T) {
	const quota = 60000
	budget, _ := extract.NewMemoryBudget(quota)
	s := &session{ctx: t.Context(), budget: budget, collectors: make(map[extract.StreamKey]*consumer), samples: make([]extract.StreamKey, 0, 16)}
	raw := fixtureRead(t, "../../extract/testdata/single.hevc")
	completed := 0
	failed := false
	for i := 0; i < 32; i++ {
		info := video.StreamInfo{Source: video.Source{Path: fmt.Sprintf("clip%d", i)}, PID: 4113, Codec: video.HEVC, Occurrences: []video.Occurrence{{Mapping: video.Mapping{Role: video.Primary, EntryType: 1}}}}
		c, err := s.factory(t.Context(), info)
		if err == nil {
			err = c.Consume(t.Context(), video.Chunk{Data: raw})
		}
		if err == nil {
			err = c.Finish(video.End{CleanEOF: true, SourcePacketCount: 1})
		}
		if err != nil {
			if !errors.Is(err, extract.ErrResourceLimit) {
				t.Fatal(err)
			}
			failed = true
			break
		}
		completed++
	}
	used, peak := budget.Usage()
	if !failed || completed < 2 || used == 0 || peak > quota || !s.exhausted {
		t.Fatalf("completed%d exhausted%v used%d peak%d", completed, s.exhausted, used, peak)
	}
	for _, c := range s.collectors {
		if c.err == nil && c.clip == nil {
			t.Fatal("lost completed collector")
		}
	}
}
func fixtureRead(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func fixtureWrite(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}
func TestPublicScanSeamlessReferenceCut(t *testing.T) {
	for _, connection := range []byte{5, 6} {
		root := discFixture(t)
		for i, name := range []string{"seam-profile-b", "seam-leading"} {
			clip := "00001"
			pts := []uint64{0, 6000, 12000}
			if i == 1 {
				clip = "00002"
				pts = []uint64{0}
			}
			data := fixtureRead(t, "../../extract/testdata/"+name+".hevc")
			packets := writeTransport(t, filepath.Join(root, "BDMV", "STREAM", clip+".m2ts"), data, pts)
			clpi := fixtureRead(t, filepath.Join(root, "BDMV", "CLIPINF", "00001.clpi"))
			binary.BigEndian.PutUint32(clpi[56:], packets)
			fixtureWrite(t, filepath.Join(root, "BDMV", "CLIPINF", clip+".clpi"), clpi)
		}
		for _, name := range []string{"00000", "00001"} {
			path := filepath.Join(root, "BDMV", "PLAYLIST", name+".mpls")
			mpls := fixtureRead(t, path)
			copy(mpls[134:], "00002")
			mpls[144] = connection
			binary.BigEndian.PutUint32(mpls[92:], 1500)
			binary.BigEndian.PutUint32(mpls[150:], 1500)
			fixtureWrite(t, path, mpls)
		}
		settings := scanner.DefaultSettings(".")
		settings.FilterLoopingPlaylists = false
		settings.FilterShortPlaylists = false
		settings.BigPlaylistOnly = false
		out, err := Scan(t.Context(), Options{BDInfo: scanner.Options{Path: root, Settings: settings}})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Report.Collection) != 2 || len(out.SourceErrors) != 0 {
			t.Fatalf("physical sources %+v", out)
		}
		for _, p := range out.Playlists {
			if p.Err != nil {
				t.Fatalf("connection%d: %v", connection, p.Err)
			}
			if len(p.Extraction.Frames) != 2 || p.Extraction.Payloads[p.Extraction.Frames[1].PayloadIndex].TargetMaximumLuminance != 10000 || p.Extraction.Frames[1].Stream.ClipID != "BDMV/STREAM/00002.m2ts" {
				t.Fatalf("splice result %+v", p.Extraction)
			}
		}
	}
}
func TestPublicScanDeliversBeyondProbeLimit(t *testing.T) {
	root := discFixture(t)
	raw := fixtureRead(t, "../../extract/testdata/single.hevc")
	raw = append(raw, bytes.Repeat([]byte{0xff}, (5<<20)+65536)...)
	packets := writeTransport(t, filepath.Join(root, "BDMV", "STREAM", "00001.m2ts"), raw, []uint64{0})
	path := filepath.Join(root, "BDMV", "CLIPINF", "00001.clpi")
	clpi := fixtureRead(t, path)
	binary.BigEndian.PutUint32(clpi[56:], packets)
	fixtureWrite(t, path, clpi)
	settings := scanner.DefaultSettings(".")
	settings.FilterLoopingPlaylists = false
	settings.FilterShortPlaylists = false
	settings.BigPlaylistOnly = false
	options := Options{BDInfo: scanner.Options{Path: root, Settings: settings}}
	out, err := Scan(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Report.Collection) != 1 || !out.Report.Collection[0].CleanEOF || out.Report.Collection[0].DeliveredBytes != uint64(len(raw)) {
		t.Fatalf("full ES %+v", out.Report.Collection)
	}
	for _, p := range out.Playlists {
		if p.Err != nil || len(p.Extraction.Frames) != 2 {
			t.Fatalf("complete timeline %+v", p)
		}
	}
	options.MaxScanRetainedBytes = out.PeakRetainedBytes - 1
	limited, err := Scan(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if limited.PeakRetainedBytes > options.MaxScanRetainedBytes || limited.Report.Report != out.Report.Report {
		t.Fatal("quota/report isolation")
	}
	found := false
	for _, p := range limited.Playlists {
		found = found || errors.Is(p.Err, extract.ErrResourceLimit)
	}
	if !found {
		t.Fatalf("quota result %+v", limited.Playlists)
	}
}
