// Copyright (c) 2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later
// Disc metadata fixture adapted from local go-bdinfo collection_test.go.
package bdinfo

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func discFixture(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"PLAYLIST", "CLIPINF", "STREAM"} {
		if err := os.MkdirAll(filepath.Join(root, "BDMV", dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "BDMV", filepath.FromSlash(name)), b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	clpi := make([]byte, 60)
	copy(clpi, "HDMV0200")
	binary.BigEndian.PutUint32(clpi[8:], 60)
	binary.BigEndian.PutUint32(clpi[56:], 2)
	seq := make([]byte, 22)
	seq[1] = 1
	seq[6] = 1
	seq[7] = 7
	binary.BigEndian.PutUint16(seq[8:], 0x1001)
	binary.BigEndian.PutUint32(seq[18:], 90000)
	clpi = append(clpi, 0, 0, 0, 22)
	clpi = append(clpi, seq...)
	binary.BigEndian.PutUint32(clpi[12:], uint32(len(clpi)))
	program := make([]byte, 20)
	program[1] = 1
	program[8] = 1
	program[10] = 0x10
	program[11] = 0x11
	program[12] = 4
	program[13] = 0x24
	program[14] = 0x61
	program[15] = 0x30
	clpi = append(clpi, 0, 0, 0, 20)
	clpi = append(clpi, program...)
	write("CLIPINF/00001.clpi", clpi)
	playlist := make([]byte, 64)
	copy(playlist, "MPLS0200")
	binary.BigEndian.PutUint32(playlist[8:], 64)
	list := make([]byte, 6)
	list[3] = 2
	for range 2 {
		item := make([]byte, 32)
		copy(item, "00001M2TS")
		item[10] = 1
		item[11] = 7
		binary.BigEndian.PutUint32(item[12:], 0)
		binary.BigEndian.PutUint32(item[16:], 4500)
		stn := make([]byte, 14)
		stn[2] = 1
		// HEVC attributes include format/rate, dynamic-range/color and flags.
		stn = append(stn, 3, 1, 0x10, 0x11, 4, 0x24, 0x61, 0x30, 0)
		item = append(item, 0, byte(len(stn)))
		item = append(item, stn...)
		list = append(list, 0, byte(len(item)))
		list = append(list, item...)
	}
	playlist = append(playlist, 0, 0, byte(len(list)>>8), byte(len(list)))
	playlist = append(playlist, list...)
	binary.BigEndian.PutUint32(playlist[12:], uint32(len(playlist)))
	playlist = append(playlist, 0, 0, 0, 2, 0, 0)
	write("PLAYLIST/00000.mpls", playlist)
	write("PLAYLIST/00001.mpls", playlist)

	raw, err := os.ReadFile("../../extract/testdata/reordered.hevc")
	if err != nil {
		t.Fatal(err)
	}
	var cuts []int
	for i := 0; i < len(raw)-5; i++ {
		if raw[i] == 0 && raw[i+1] == 0 && raw[i+2] == 1 && (raw[i+3]>>1)&63 <= 31 {
			cuts = append(cuts, i)
		}
	}
	// AU 2 starts at its preceding prefix SEI, not at its slice.
	third := cuts[2]
	for i := cuts[1] + 3; i < cuts[2]; i++ {
		if raw[i] == 0 && raw[i+1] == 0 && raw[i+2] == 1 && (raw[i+3]>>1)&63 == 39 {
			third = i
			break
		}
	}
	cuts = []int{0, cuts[1], third, len(raw)}
	var transport []byte
	cc := byte(0)
	for i := range 3 {
		pts := uint64(0)
		if i == 1 {
			pts = 6000
		}
		if i == 2 {
			pts = 3000
		}
		stamp := []byte{0x21 | byte((pts>>29)&0xe), byte(pts >> 22), byte((pts>>14)&0xfe) | 1, byte(pts >> 7), byte((pts<<1)&0xfe) | 1}
		body := raw[cuts[i]:cuts[i+1]]
		pes := []byte{0, 0, 1, 0xe0, byte((len(body) + 8) >> 8), byte(len(body) + 8), 0x80, 0x80, 5}
		pes = append(pes, stamp...)
		pes = append(pes, body...)
		first := true
		for len(pes) > 0 {
			size := min(182, len(pes))
			packet := make([]byte, 192)
			packet[4] = 0x47
			packet[5] = 0x10
			if first {
				packet[5] |= 0x40
			}
			packet[6] = 0x11
			packet[7] = 0x30 | cc
			packet[8] = byte(183 - size)
			for j := 10; j < 192-size; j++ {
				packet[j] = 0xff
			}
			copy(packet[192-size:], pes[:size])
			transport = append(transport, packet...)
			pes = pes[size:]
			first = false
			cc = (cc + 1) & 15
		}
	}
	binary.BigEndian.PutUint32(clpi[56:], uint32(len(transport)/192))
	write("CLIPINF/00001.clpi", clpi)
	write("STREAM/00001.m2ts", transport)
	return root
}
