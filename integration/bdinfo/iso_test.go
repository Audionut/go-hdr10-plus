// Copyright (c) 2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later
// Fixture layout adapted from go-bdinfo collection_iso_test.go. Only the public
// scanner API is imported; this writes descriptors and implements no ISO reader.
package bdinfo

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	scanner "github.com/autobrr/go-bdinfo/pkg/bdinfo"
)

func fixtureISO(t *testing.T, root string) string {
	t.Helper()
	const sector = 2048
	image := make([]byte, 320*sector)
	descriptor := func(block int, tag uint16) []byte {
		data := image[block*sector : (block+1)*sector]
		binary.LittleEndian.PutUint16(data, tag)
		return data
	}
	put32 := binary.LittleEndian.PutUint32
	put64 := binary.LittleEndian.PutUint64
	copy(image[16*sector+1:], "NSR03")
	anchor := descriptor(256, 2)
	put32(anchor[16:], 3*sector)
	put32(anchor[20:], 257)
	partition := descriptor(257, 5)
	put32(partition[188:], 300)
	put32(partition[192:], 20)
	volume := descriptor(258, 6)
	put32(volume[212:], sector)
	put32(volume[248:], sector)
	put32(volume[264:], 6)
	put32(volume[268:], 1)
	copy(volume[440:], []byte{1, 6, 1, 0, 0, 0})
	descriptor(259, 8)
	fileset := descriptor(300, 256)
	put32(fileset[400:], sector)
	put32(fileset[404:], 1)
	type node struct {
		name     string
		block    uint32
		dir      bool
		children []int
		data     []byte
	}
	nodes := []node{{block: 1, dir: true, children: []int{1}}, {name: "BDMV", block: 2, dir: true, children: []int{2, 3, 4}}, {name: "PLAYLIST", block: 3, dir: true, children: []int{5, 6}}, {name: "CLIPINF", block: 4, dir: true, children: []int{7}}, {name: "STREAM", block: 5, dir: true, children: []int{8}}, {name: "00000.mpls", block: 6}, {name: "00001.mpls", block: 7}, {name: "00001.clpi", block: 8}, {name: "00001.m2ts", block: 9}}
	for i := range nodes {
		n := &nodes[i]
		if n.dir {
			for _, child := range n.children {
				c := nodes[child]
				name := append([]byte{8}, []byte(c.name)...)
				fid := make([]byte, (38+len(name)+3)&^3)
				binary.LittleEndian.PutUint16(fid, 257)
				binary.LittleEndian.PutUint16(fid[16:], 1)
				if c.dir {
					fid[18] = 2
				}
				fid[19] = byte(len(name))
				put32(fid[20:], sector)
				put32(fid[24:], c.block)
				copy(fid[38:], name)
				n.data = append(n.data, fid...)
			}
		} else {
			parent := "PLAYLIST"
			if i == 7 {
				parent = "CLIPINF"
			}
			if i == 8 {
				parent = "STREAM"
			}
			var err error
			n.data, err = os.ReadFile(filepath.Join(root, "BDMV", parent, n.name))
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(n.data) > sector-176 {
			t.Fatal("tiny fixture extent too large")
		}
		entry := descriptor(300+int(n.block), 261)
		entry[27] = 5
		if n.dir {
			entry[27] = 4
			binary.LittleEndian.PutUint16(entry[34:], 3)
		}
		put64(entry[56:], uint64(len(n.data)))
		if n.dir {
			put32(entry[172:], uint32(len(n.data)))
			copy(entry[176:], n.data)
		} else {
			put32(entry[172:], 8)
			put32(entry[176:], uint32(len(n.data)))
			block := uint32(10 + i - 5)
			put32(entry[180:], block)
			copy(image[(300+int(block))*sector:], n.data)
		}
	}
	path := filepath.Join(t.TempDir(), "fixture.iso")
	if err := os.WriteFile(path, image, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestScanFolderAndUDFISO(t *testing.T) {
	folder := discFixture(t)
	iso := fixtureISO(t, folder)
	settings := scanner.DefaultSettings(".")
	settings.FilterLoopingPlaylists = false
	settings.FilterShortPlaylists = false
	settings.BigPlaylistOnly = false
	var expected uint64
	for _, path := range []string{folder, iso} {
		out, err := Scan(t.Context(), Options{BDInfo: scanner.Options{Path: path, Settings: settings}})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.SourceErrors) != 0 || len(out.Playlists) != 2 || len(out.Report.Collection) != 1 || !out.Report.Collection[0].CleanEOF {
			t.Fatalf("source/timeline outcomes %+v", out)
		}
		for _, p := range out.Playlists {
			if p.Err != nil || p.Extraction == nil || len(p.Extraction.Frames) != 6 {
				t.Fatalf("playlist %+v", p)
			}
		}
		bytes := out.Report.Collection[0].DeliveredBytes
		if expected != 0 && bytes != expected {
			t.Fatal("folder/ISO ES differs")
		}
		expected = bytes
		if out.PeakRetainedBytes <= 0 {
			t.Fatal("missing budget instrumentation")
		}
	}
	if err := os.Remove(iso); err != nil {
		t.Fatalf("ISO remains open: %v", err)
	}
}
