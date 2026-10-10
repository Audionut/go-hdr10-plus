package bdinfo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/Audionut/go-hdr10-plus/extract"
	scanner "github.com/autobrr/go-bdinfo/pkg/bdinfo"
	"os"
	"path/filepath"
	"testing"
)

func TestSelectedSTCEpoch(t *testing.T) {
	root := discFixture(t)
	raw, _ := os.ReadFile("../../extract/testdata/single.hevc")
	payload, _ := os.ReadFile("../../extract/testdata/profile-b.t35")
	seiStart, sliceStart := -1, -1
	for i := 0; i < len(raw)-5; i++ {
		if bytes.Equal(raw[i:i+3], []byte{0, 0, 1}) {
			k := raw[i+3] >> 1 & 63
			if k == 39 {
				seiStart = i
			}
			if k <= 31 {
				sliceStart = i
			}
		}
	}
	if seiStart < 0 || sliceStart < 0 {
		t.Fatal("fixture")
	}
	sei := []byte{4, byte(len(payload))}
	sei = append(sei, payload...)
	sei = append(sei, 0x80)
	newSEI := []byte{0, 0, 1, 78, 1}
	zeros := 0
	for _, v := range sei {
		if zeros == 2 && v <= 3 {
			newSEI = append(newSEI, 3)
			zeros = 0
		}
		newSEI = append(newSEI, v)
		if v == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	rawB := append(append(append([]byte(nil), raw[:seiStart]...), newSEI...), raw[sliceStart:]...)
	var transport []byte
	cc := byte(0)
	firstEnd := 0
	for i, body := range [][]byte{raw, rawB} {
		pes := []byte{0, 0, 1, 0xe0, byte((len(body) + 8) >> 8), byte(len(body) + 8), 0x80, 0x80, 5, 0x21, 0, 1, 0, 1}
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
			cc = (cc + 1) & 15
			first = false
		}
		if i == 0 {
			firstEnd = len(transport) / 192
		}
	}
	cp := filepath.Join(root, "BDMV", "CLIPINF", "00001.clpi")
	old, _ := os.ReadFile(cp)
	programOffset := binary.BigEndian.Uint32(old[12:])
	clpi := append([]byte(nil), old[:60]...)
	binary.BigEndian.PutUint32(clpi[56:], uint32(len(transport)/192))
	seq := make([]byte, 36)
	seq[1] = 1
	seq[6] = 2
	seq[7] = 7
	binary.BigEndian.PutUint16(seq[8:], 0x1001)
	binary.BigEndian.PutUint32(seq[18:], 90000)
	binary.BigEndian.PutUint16(seq[22:], 0x1001)
	binary.BigEndian.PutUint32(seq[24:], uint32(firstEnd))
	binary.BigEndian.PutUint32(seq[32:], 90000)
	clpi = append(clpi, 0, 0, 0, 36)
	clpi = append(clpi, seq...)
	binary.BigEndian.PutUint32(clpi[12:], uint32(len(clpi)))
	clpi = append(clpi, old[programOffset:]...)
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
	out, err := Scan(t.Context(), Options{BDInfo: scanner.Options{Path: root, Settings: settings}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("source-errors=%v collection=%#v", out.SourceErrors, out.Report.Collection)
	if len(out.SourceErrors) != 0 {
		t.Fatal(out.SourceErrors)
	}
	for _, p := range out.Playlists {
		if p.Err != nil {
			t.Fatal(p.Err)
		}
		if len(p.Extraction.Frames) != 2 {
			t.Fatalf("frames %d", len(p.Extraction.Frames))
		}
		for _, f := range p.Extraction.Frames {
			if f.AUOrdinal != 0 || p.Extraction.Payloads[f.PayloadIndex].TargetMaximumLuminance != 0 {
				t.Fatalf("wrong epoch: %#v", f)
			}
		}
	}

	// The same physical bytes must also resolve the later clock epoch, with
	// source identity preserved instead of renumbering its AU to zero.
	for _, name := range []string{"00000.mpls", "00001.mpls"} {
		path := filepath.Join(root, "BDMV", "PLAYLIST", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data[87] = 8
		data[146] = 8
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	out, err = Scan(t.Context(), Options{BDInfo: scanner.Options{Path: root, Settings: settings}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range out.Playlists {
		if p.Err != nil {
			t.Fatal(p.Err)
		}
		for _, f := range p.Extraction.Frames {
			if f.AUOrdinal != 1 || p.Extraction.Payloads[f.PayloadIndex].TargetMaximumLuminance != 10000 {
				t.Fatalf("later epoch: %#v", f)
			}
		}
	}
}
func TestFailedPlaylistOutcomesStayBounded(t *testing.T) {
	root := discFixture(t)
	mp := filepath.Join(root, "BDMV", "PLAYLIST", "00000.mpls")
	m, _ := os.ReadFile(mp)
	m[86] = 5
	m[145] = 5
	for i := 0; i < 1500; i++ {
		name := fmt.Sprintf("%05d.mpls", i)
		if err := os.WriteFile(filepath.Join(root, "BDMV", "PLAYLIST", name), m, 0644); err != nil {
			t.Fatal(err)
		}
	}
	settings := scanner.DefaultSettings(".")
	settings.FilterLoopingPlaylists = false
	settings.FilterShortPlaylists = false
	settings.BigPlaylistOnly = false
	const limit = 50000
	out, err := Scan(t.Context(), Options{BDInfo: scanner.Options{Path: root, Settings: settings}, MaxScanRetainedBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	if out.OmittedPlaylistResults == 0 || uint64(len(out.Playlists))+out.OmittedPlaylistResults != 1500 || len(out.ExhaustedPlaylists) > 16 {
		t.Fatalf("unbounded/missing outcomes: retained%d omitted%d samples%d", len(out.Playlists), out.OmittedPlaylistResults, len(out.ExhaustedPlaylists))
	}
	for _, p := range out.Playlists {
		if p.Extraction != nil || (!errors.Is(p.Err, extract.ErrResourceLimit) && !errors.Is(p.Err, extract.ErrUnsupportedInput)) {
			t.Fatal(p.Err)
		}
	}
}
