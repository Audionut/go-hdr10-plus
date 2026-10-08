package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
)

// MPEG-2 CRC has the same polynomial as IEEE CRC but no reflection/complement.
func transportCRC(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
func transportSection(id byte, program uint16, body []byte, version byte) []byte {
	n := len(body) + 9
	data := []byte{id, 0xb0 | byte(n>>8), byte(n), byte(program >> 8), byte(program), 0xc1 | version<<1, 0, 0}
	data = append(data, body...)
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], transportCRC(data))
	return append(data, crc[:]...)
}
func tsPacket(pid uint16, cc byte, start bool, payload []byte) []byte {
	if len(payload) > 184 {
		panic("fixture packet too long")
	}
	packet := bytes.Repeat([]byte{0xff}, 188)
	packet[0] = 0x47
	packet[1] = byte(pid >> 8)
	if start {
		packet[1] |= 0x40
	}
	packet[2] = byte(pid)
	packet[3] = 0x10 | cc&15
	offset := 4
	if len(payload) < 184 {
		packet[3] |= 0x20
		packet[4] = byte(183 - len(payload))
		if packet[4] > 0 {
			packet[5] = 0
		}
		offset = 188 - len(payload)
	}
	copy(packet[offset:], payload)
	return packet
}
func tsStamp(v uint64, prefix byte) []byte {
	return []byte{prefix<<4 | byte(v>>29)&14 | 1, byte(v >> 22), byte(v>>14)&0xfe | 1, byte(v >> 7), byte(v<<1) | 1}
}
func tsPES(data []byte, pts, dts uint64, bounded bool) []byte {
	pes := []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0xc0, 10}
	pes = append(pes, tsStamp(pts, 3)...)
	pes = append(pes, tsStamp(dts, 1)...)
	pes = append(pes, data...)
	if bounded {
		binary.BigEndian.PutUint16(pes[4:6], uint16(len(pes)-6))
	}
	return pes
}
func tsPESPackets(pid uint16, pes []byte, cc *byte, split int) [][]byte {
	var packets [][]byte
	first := true
	for len(pes) > 0 {
		n := min(len(pes), 184)
		if first && split > 0 {
			n = min(n, split)
		}
		packets = append(packets, tsPacket(pid, *cc, first, pes[:n]))
		*cc = (*cc + 1) & 15
		pes = pes[n:]
		first = false
	}
	return packets
}
func tsTestFile(t testing.TB, bounded bool, split int, wrap bool) [][]byte {
	t.Helper()
	pat := transportSection(0, 1, []byte{0, 1, 0xe1, 0}, 0)
	pmt := transportSection(2, 1, []byte{0xf0, 0x11, 0xf0, 0, 0x24, 0xf0, 0x11, 0xf0, 0}, 0)
	packets := [][]byte{tsPacket(0, 0, true, append([]byte{0}, pat...)), tsPacket(0x100, 0, true, append([]byte{0}, pmt...))}
	raw := rawFixture(t, "reordered")
	var prefix []byte
	var samples [][]byte
	for pos := 0; pos < len(raw); {
		end := len(raw)
		next := bytes.Index(raw[pos+3:], []byte{0, 0, 1})
		if next >= 0 {
			end = pos + 3 + next
		}
		kind := raw[pos+3] >> 1 & 63
		prefix = append(prefix, raw[pos:end]...)
		if kind <= 31 {
			samples = append(samples, prefix)
			prefix = nil
		}
		pos = end
	}
	cc := byte(0)
	epoch := uint64(0)
	if wrap {
		epoch = (1 << 33) - 3000
	}
	for i, pts := range []uint64{0, 6000, 3000} {
		packets = append(packets, tsPESPackets(0x1011, tsPES(samples[i], (epoch+pts)%(1<<33), (epoch+uint64(i*3000))%(1<<33), bounded), &cc, split)...)
	}
	return packets
}
func TestTransportFullTimeline(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		for _, split := range []int{0, 1, 5, 8, 10, 18} {
			for _, wrap := range []bool{false, true} {
				for _, format := range []Format{TS, M2TS} {
					packets := tsTestFile(t, bounded, split, wrap)
					var data []byte
					for _, packet := range packets {
						if format == M2TS {
							data = append(data, 0, 0, 0, 0)
						}
						data = append(data, packet...)
					}
					e, err := Extract(t.Context(), bytes.NewReader(data), Options{Format: format})
					if err != nil {
						t.Fatalf("bounded%v split%d wrap%v format%d: %v", bounded, split, wrap, format, err)
					}
					if len(e.Frames) != 3 {
						t.Fatalf("frames%d", len(e.Frames))
					}
					for i, au := range []uint64{0, 2, 1} {
						if e.Frames[i].AUOrdinal != au {
							t.Fatalf("mapping%+v", e.Frames)
						}
					}
					if !slices.Equal([]uint32{e.Frames[0].PayloadIndex, e.Frames[1].PayloadIndex, e.Frames[2].PayloadIndex}, []uint32{0, 1, 0}) {
						t.Fatal("inheritance")
					}
					base := int64(0)
					if wrap {
						base = (1 << 33) - 3000
					}
					if e.Frames[0].PTS.Value != base || e.Frames[2].PTS.Value != base+6000 {
						t.Fatalf("transport clock %+v", e.Frames)
					}
					e.memory.release()
				}
			}
		}
	}
}
func TestTransportFailuresDuplicatesAndVersions(t *testing.T) {
	packets := tsTestFile(t, true, 5, false)
	joined := bytes.Join(packets, nil)
	duplicate := bytes.Join(append(append([][]byte{}, packets[:4]...), append([][]byte{packets[3]}, packets[4:]...)...), nil)
	if e, err := Extract(t.Context(), bytes.NewReader(duplicate), Options{}); err != nil || len(e.Frames) != 3 {
		t.Fatalf("exact duplicate %v", err)
	}
	loss := bytes.Join(append(slices.Clone(packets[:3]), packets[4:]...), nil)
	conflict := slices.Clone(duplicate)
	conflict[4*188+187] ^= 1
	scrambled := slices.Clone(joined)
	scrambled[2*188+3] |= 0x80
	discontinuous := slices.Clone(joined)
	discontinuous[2*188+5] |= 0x80
	badCRC := slices.Clone(joined)
	badCRC[187] ^= 1
	pmtChanged := transportSection(2, 1, []byte{0xf0, 0x12, 0xf0, 0, 0x24, 0xf0, 0x12, 0xf0, 0}, 1)
	changed := append(slices.Clone(joined), tsPacket(0x100, 1, true, append([]byte{0}, pmtChanged...))...)
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"loss", loss, ErrIncomplete}, {"conflicting-duplicate", conflict, ErrInvalidBitstream}, {"scrambling", scrambled, ErrUnsupportedInput}, {"discontinuity", discontinuous, ErrUnsupportedInput}, {"CRC", badCRC, ErrInvalidBitstream}, {"changed-PID", changed, ErrUnsupportedInput}, {"partial-packet", joined[:len(joined)-1], ErrIncomplete}, {"bounded-PES-cut", joined[:len(joined)-188], ErrIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if e, err := Extract(t.Context(), bytes.NewReader(tc.data), Options{}); e != nil || !errors.Is(err, tc.want) {
				t.Fatalf("%v %v", e, err)
			}
		})
	}
	pmtVersion := transportSection(2, 1, []byte{0xf0, 0x11, 0xf0, 0, 0x24, 0xf0, 0x11, 0xf0, 0}, 1)
	version := append(slices.Clone(joined), tsPacket(0x100, 1, true, append([]byte{0}, pmtVersion...))...)
	if _, err := Extract(t.Context(), bytes.NewReader(version), Options{}); err != nil {
		t.Fatalf("same mapping new version: %v", err)
	}
}
