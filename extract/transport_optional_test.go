// Binary controls follow H.222.0 v9 Tables 2-6, 2-21 and 2-39.
// Non-H.262 seamless splice_type wording conflicts in that edition; DTS
// controls establish bounded structural validity, not decoder continuity.
package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestTransportOptionalSyntax(t *testing.T) {
	packBits := "00000000000000000000000110111010" + "01" + "000" + "1" + "000000000000000" + "1" + "000000000000000" + "1" + "000000000" + "1" + "0000000000000000000001" + "11" + "11111" + "000"
	pack := make([]byte, len(packBits)/8)
	for i, c := range packBits {
		if c == '1' {
			pack[i/8] |= 1 << uint(7-i%8)
		}
	}
	packFields := append([]byte{0x6e, byte(len(pack))}, pack...)
	packFields = append(packFields, 0x80, 0xc0)
	cases := []struct {
		name       string
		adaptation bool
		fields     []byte
		flag       byte
		valid      bool
	}{
		{"copy-valid", false, []byte{0x80}, 4, true},
		{"copy-marker-zero", false, []byte{0}, 4, false},
		{"sequence-valid", false, []byte{0x2e, 0x80, 0x80}, 1, true},
		{"sequence-first-marker", false, []byte{0x2e, 0, 0x80}, 1, false},
		{"sequence-second-marker", false, []byte{0x2e, 0x80, 0}, 1, false},
		{"PSTD-valid-prefix", false, []byte{0x1e, 0x40, 0}, 1, true},
		{"PSTD-prefix-00", false, []byte{0x1e, 0, 0}, 1, false},
		{"PSTD-prefix-10", false, []byte{0x1e, 0x80, 0}, 1, false},
		{"PSTD-prefix-11", false, []byte{0x1e, 0xc0, 0}, 1, false},
		{"ext2-no-TREF", false, []byte{0x0f, 0x81, 0xff}, 1, true},
		{"ext2-TREF-zero", false, []byte{0x0f, 0x86, 0xfe, 0xf1, 0, 1, 0, 1}, 1, true},
		{"ext2-length-marker-zero", false, []byte{0x0f, 1, 0xff}, 1, false},
		{"ext2-missing-first-byte", false, []byte{0x0f, 0x80}, 1, false},
		{"ext2-missing-TREF", false, []byte{0x0f, 0x81, 0xfe}, 1, false},
		{"ext2-partial-TREF", false, []byte{0x0f, 0x85, 0xfe, 0xf1, 0, 1, 0}, 1, false},
		{"ext2-TREF-first-marker", false, []byte{0x0f, 0x86, 0xfe, 0xf0, 0, 1, 0, 1}, 1, false},
		{"ext2-TREF-second-marker", false, []byte{0x0f, 0x86, 0xfe, 0xf1, 0, 0, 0, 1}, 1, false},
		{"ext2-TREF-third-marker", false, []byte{0x0f, 0x86, 0xfe, 0xf1, 0, 1, 0, 0}, 1, false},
		{"stuffing-32", false, bytes.Repeat([]byte{0xff}, 32), 0, true},
		{"stuffing-33", false, bytes.Repeat([]byte{0xff}, 33), 0, false},
		{"stuffing-byte-zero", false, []byte{0}, 0, false},
		{"private-opaque", false, append([]byte{0x8e}, bytes.Repeat([]byte{0xa5}, 16)...), 1, true},
		{"pack-MPEG2-valid", false, packFields, 1, true},
		{"pack-missing-header", false, []byte{0x4e, 0}, 1, false},
		{"pack-fake-two-byte-header", false, []byte{0x4e, 2, 0xaa, 0xbb}, 1, false},
		{"pack-prefix-invalid", false, append([]byte{0x4e, byte(len(pack)), 0x01}, pack[1:]...), 1, false},
		{"pack-SCR-first-marker", false, append([]byte{0x4e, byte(len(pack))}, changeBits(pack, 37, 1, 0)...), 1, false},
		{"pack-SCR-300", false, append([]byte{0x4e, byte(len(pack))}, changeBits(pack, 70, 9, 300)...), 1, false},
		{"pack-rate-zero", false, append([]byte{0x4e, byte(len(pack))}, changeBits(pack, 80, 22, 0)...), 1, false},
		{"pack-stuffing-missing", false, append([]byte{0x4e, byte(len(pack))}, changeBits(pack, 109, 3, 1)...), 1, false},
		{"AF-DTS-zero", true, []byte{0x3f, 1, 0, 1, 0, 1}, 0, true},
		{"AF-DTS-first-marker", true, []byte{0x3f, 0, 0, 1, 0, 1}, 0, false},
		{"AF-DTS-second-marker", true, []byte{0x3f, 1, 0, 0, 0, 1}, 0, false},
		{"AF-DTS-third-marker", true, []byte{0x3f, 1, 0, 1, 0, 0}, 0, false},
		{"ESCR-zero", false, []byte{0xc4, 0, 4, 0, 4, 1}, 0x20, true},
		{"ESCR-299", false, []byte{0xc4, 0, 4, 0, 6, 87}, 0x20, true},
		{"ESCR-300", false, []byte{0xc4, 0, 4, 0, 6, 89}, 0x20, false},
		{"ESCR-first-marker", false, []byte{0xc0, 0, 4, 0, 4, 1}, 0x20, false},
		{"ESCR-second-marker", false, []byte{0xc4, 0, 0, 0, 4, 1}, 0x20, false},
		{"ESCR-third-marker", false, []byte{0xc4, 0, 4, 0, 0, 1}, 0x20, false},
		{"ESCR-last-marker", false, []byte{0xc4, 0, 4, 0, 4, 0}, 0x20, false},
		{"ES-rate-one", false, []byte{0x80, 0, 3}, 0x10, true},
		{"ES-rate-zero", false, []byte{0x80, 0, 1}, 0x10, false},
		{"ES-rate-first-marker", false, []byte{0, 0, 3}, 0x10, false},
		{"ES-rate-last-marker", false, []byte{0x80, 0, 2}, 0x10, false},
		{"ESCR-reserved-zero", false, []byte{4, 0, 4, 0, 4, 1}, 0x20, false},
		{"PES-extension-reserved-valid", false, []byte{0x0e}, 1, true},
		{"PES-extension-reserved-zero", false, []byte{0}, 1, false},
		{"ext2-fd-private-stream-valid", false, []byte{0x0f, 0x81, 0x40}, 1, true},
		{"ext2-e0-stream-id-invalid", false, []byte{0x0f, 0x81, 0x40}, 1, false},
		{"ext2-six-reserved-zero", false, []byte{0x0f, 0x81, 0x81}, 1, false},
		{"ext2-TREF-reserved-zero", false, []byte{0x0f, 0x86, 0xfe, 0x01, 0, 1, 0, 1}, 1, false},
		{"ext2-tail-reserved-ff", false, []byte{0x0f, 0x82, 0xff, 0xff}, 1, true},
		{"ext2-tail-reserved-zero", false, []byte{0x0f, 0x82, 0xff, 0}, 1, false},
		{"pack-reserved-zero", false, append([]byte{0x4e, byte(len(pack))}, changeBits(pack, 104, 5, 0)...), 1, false},
		{"PCR-reserved-valid", true, []byte{0, 0, 0, 0, 0x7e, 0}, 0, true},
		{"PCR-reserved-zero", true, []byte{0, 0, 0, 0, 0, 0}, 0, false},
		{"OPCR-reserved-valid", true, []byte{0, 0, 0, 0, 0x7e, 0}, 0, true},
		{"OPCR-reserved-zero", true, []byte{0, 0, 0, 0, 0, 0}, 0, false},
		{"AF-extension-reserved-valid", true, []byte{0x1f}, 0, true},
		{"AF-extension-reserved-zero", true, []byte{0x10}, 0, false},
		{"AF-extension-tail-reserved-ff", true, []byte{0x1f, 0xff}, 0, true},
		{"AF-extension-tail-reserved-zero", true, []byte{0x1f, 0}, 0, false},
		{"piecewise-defined-rate-one", true, []byte{0xdf, 0x80, 0, 0xc0, 0, 1}, 0, true},
		{"piecewise-reserved-zero", true, []byte{0xdf, 0x80, 0, 0, 0, 1}, 0, false},
		{"piecewise-defined-rate-zero", true, []byte{0xdf, 0x80, 0, 0xc0, 0, 0}, 0, false},
		{"piecewise-undefined-without-ltw", true, []byte{0x5f, 0xc0, 0, 0}, 0, true},
		{"piecewise-undefined-invalid-ltw", true, []byte{0xdf, 0, 0, 0xc0, 0, 0}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := rawFixture(t, "single")
			packets := tsTestFile(t, true, 0, false)[:2]
			var pes []byte
			if tc.adaptation {
				pes = tsPES(raw, 0, 0, true)
			} else {
				pes = []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80 | tc.flag, byte(5 + len(tc.fields))}
				pes = append(pes, tsStamp(0, 2)...)
				pes = append(pes, tc.fields...)
				pes = append(pes, raw...)
				if tc.name == "ext2-fd-private-stream-valid" {
					pes[3] = 0xfd
				}
				binary.BigEndian.PutUint16(pes[4:6], uint16(len(pes)-6))
			}
			var packet []byte
			packet = tsPacket(0x1011, 0, true, pes)
			if tc.adaptation {
				af := append([]byte{1, byte(len(tc.fields))}, tc.fields...)
				if strings.HasPrefix(tc.name, "AF-DTS-") {
					af = append([]byte{5, 0, byte(len(tc.fields))}, tc.fields...)
				}
				if strings.HasPrefix(tc.name, "PCR-") {
					af = append([]byte{0x10}, tc.fields...)
				}
				if strings.HasPrefix(tc.name, "OPCR-") {
					af = append([]byte{0x18, 0, 0, 0, 0, 0x7e, 0}, tc.fields...)
				}
				if int(packet[4]) < len(af) {
					t.Fatal("proof adaptation does not fit")
				}
				copy(packet[5:], af)
			}
			for _, format := range []Format{TS, M2TS} {
				var data []byte
				for _, p := range append(packets, packet) {
					if format == M2TS {
						data = append(data, 0, 0, 0, 0)
					}
					data = append(data, p...)
				}
				out, err := Extract(t.Context(), bytes.NewReader(data), Options{Format: format})
				if tc.valid {
					if err != nil {
						t.Errorf("valid format%d rejected: %v", format, err)
						continue
					}
					if len(out.Frames) != 1 || out.Frames[0].POC != 0 || out.Profile != "A" {
						t.Errorf("valid output %+v", out)
					}
					if _, e := out.PlotMetadata(); e != nil {
						t.Error(e)
					}
					out.memory.release()
				} else {
					if out != nil {
						out.memory.release()
					}
					if out != nil || !errors.Is(err, ErrInvalidBitstream) {
						t.Errorf("format%d prohibited optional clock accepted: result=%v err=%v", format, out != nil, err)
					}
				}
			}
		})
	}
}
