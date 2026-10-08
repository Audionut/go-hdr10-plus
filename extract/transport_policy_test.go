// Binary controls follow H.222.0 v9 Tables 2-6, 2-21 and 2-39.
// Non-H.262 seamless splice_type wording conflicts in that edition; DTS
// controls establish bounded structural validity, not decoder continuity.
package extract

import (
	"bytes"
	"errors"
	"testing"
)

func TestTransportPresentationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode byte
		want error
	}{
		{"adaptation-stuffing-ff", 0, nil}, {"adaptation-stuffing-zero", 1, ErrInvalidBitstream},
		{"PES-prefix-10", 2, nil}, {"PES-prefix-00", 3, ErrInvalidBitstream},
		{"DSM-fast-forward", 4, ErrUnsupportedInput}, {"DSM-slow-motion", 5, ErrUnsupportedInput}, {"DSM-freeze-frame", 6, ErrUnsupportedInput}, {"DSM-fast-reverse", 7, ErrUnsupportedInput}, {"DSM-slow-reverse", 8, ErrUnsupportedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pes := tsPES(rawFixture(t, "single"), 0, 0, true)
			if tc.mode == 3 {
				pes[6] = 0
			}
			if tc.mode >= 4 {
				modes := []byte{0x13, 0x21, 0x57, 0x73, 0x81}
				pes = append(append(append([]byte(nil), pes[:19]...), modes[tc.mode-4]), pes[19:]...)
				pes[7] |= 8
				pes[8]++
				n := len(pes) - 6
				pes[4] = byte(n >> 8)
				pes[5] = byte(n)
			}
			packet := tsPacket(0x1011, 0, true, pes)
			if tc.mode == 1 {
				packet[6] = 0
			}
			for _, format := range []Format{TS, M2TS} {
				var data []byte
				for _, pk := range append(tsTestFile(t, true, 0, false)[:2], packet) {
					if format == M2TS {
						data = append(data, 0, 0, 0, 0)
					}
					data = append(data, pk...)
				}
				e, err := Extract(t.Context(), bytes.NewReader(data), Options{Format: format})
				if e != nil {
					defer e.memory.release()
				}
				if tc.want == nil {
					if err != nil || e == nil || len(e.Frames) != 1 {
						t.Errorf("legal format%d rejected: %v", format, err)
					}
				} else if e != nil || !errors.Is(err, tc.want) {
					t.Errorf("format%d expected%v result=%v err=%v", format, tc.want, e != nil, err)
				}
			}
		})
	}
}
