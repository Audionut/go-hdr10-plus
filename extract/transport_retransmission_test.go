package extract

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func TestTransportPCRRetransmission(t *testing.T) {
	for _, format := range []Format{TS, M2TS} {
		for _, mode := range []string{"exact", "PCR", "PCR-299", "invalid-PCR", "PCR-reserved", "payload", "OPCR", "third-copy"} {
			t.Run(fmt.Sprintf("%d/%s", format, mode), func(t *testing.T) {
				packets := tsTestFile(t, true, 5, false)
				packet := packets[2]
				packet[5] = 0x10
				copy(packet[6:12], []byte{0, 0, 0, 0, 0x7e, 0})
				if mode == "OPCR" {
					packet[5] |= 8
					copy(packet[12:18], []byte{0, 0, 0, 0, 0x7e, 0})
				}
				encode := func(packets [][]byte) []byte {
					var data []byte
					for _, packet := range packets {
						if format == M2TS {
							data = append(data, 0, 0, 0, 0)
						}
						data = append(data, packet...)
					}
					return data
				}
				direct, err := Extract(t.Context(), bytes.NewReader(encode(packets)), Options{Format: format})
				if err != nil {
					t.Fatalf("lawful non-duplicate control rejected: %v", err)
				}
				defer direct.memory.release()
				duplicate := slices.Clone(packet)
				switch mode {
				case "PCR", "third-copy":
					duplicate[9] = 1
				case "invalid-PCR":
					duplicate[10], duplicate[11] = 0x7f, 44 // PCR extension 300 exceeds mod300.
				case "PCR-299":
					duplicate[10], duplicate[11] = 0x7f, 43
				case "PCR-reserved":
					duplicate[10] ^= 2 // Reserved bits are outside the permitted clock fields.
				case "payload":
					duplicate[187] ^= 1
				case "OPCR":
					duplicate[15] = 1
				}
				retransmitted := append(slices.Clone(packets[:3]), duplicate)
				if mode == "third-copy" {
					retransmitted = append(retransmitted, slices.Clone(duplicate))
				}
				retransmitted = append(retransmitted, packets[3:]...)
				out, err := Extract(t.Context(), bytes.NewReader(encode(retransmitted)), Options{Format: format})
				if mode != "exact" && mode != "PCR" && mode != "PCR-299" {
					if out != nil || !errors.Is(err, ErrInvalidBitstream) {
						t.Fatalf("prohibited retransmission accepted: result=%v error=%v", out != nil, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("lawful retransmission rejected: %v", err)
				}
				defer out.memory.release()
				if !reflect.DeepEqual(out.Payloads, direct.Payloads) || !reflect.DeepEqual(out.Frames, direct.Frames) || !slices.Equal(out.SceneStarts, direct.SceneStarts) || out.Profile != direct.Profile {
					t.Fatal("retransmission changed complete extraction")
				}
			})
		}
	}
}

func TestTransportClockFields(t *testing.T) {
	for _, tc := range []struct {
		name      string
		flags     byte
		pcr, opcr uint16
		valid     bool
	}{
		{"PCR-zero", 0x10, 0, 0, true},
		{"PCR-299", 0x10, 299, 0, true},
		{"PCR-300", 0x10, 300, 0, false},
		{"PCR-and-OPCR-299", 0x18, 299, 299, true},
		{"OPCR-300", 0x18, 0, 300, false},
		{"OPCR-without-PCR", 0x08, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fields []byte
			for i, value := range []uint16{tc.pcr, tc.opcr} {
				if tc.flags&(0x10>>i) != 0 {
					fields = append(fields, 0, 0, 0, 0, 0x7e|byte(value>>8), byte(value))
				}
			}
			packets := tsTestFile(t, true, 0, false)[:2]
			payload := tsPES(rawFixture(t, "single"), 0, 0, true)
			n := 1 + len(fields)
			payload = append(payload, bytes.Repeat([]byte{0xff}, 183-n-len(payload))...)
			packet := tsPacket(0x1011, 0, true, payload)
			packet[5] = tc.flags
			copy(packet[6:], fields)
			assertTransportConformance(t, append(packets, packet), tc.valid)
		})
	}
}

func TestTransportRetransmissionAdjacency(t *testing.T) {
	for _, format := range []Format{TS, M2TS} {
		for _, mode := range []string{"same-PID-duplicate", "other-PID-duplicate", "same-PID-next"} {
			t.Run(fmt.Sprintf("%d/%s", format, mode), func(t *testing.T) {
				packets := tsTestFile(t, true, 5, false)
				pid := uint16(0x1011)
				if mode == "other-PID-duplicate" {
					pid = 0x1ffe
				}
				adaptation := tsPacket(pid, 0, false, nil)
				adaptation[3] &^= 0x10 // Adaptation only, all183 bytes including flags/stuffing.
				interposed := append(slices.Clone(packets[:3]), adaptation)
				if mode != "same-PID-next" {
					interposed = append(interposed, slices.Clone(packets[2]))
				}
				interposed = append(interposed, packets[3:]...)
				var data []byte
				for _, packet := range interposed {
					if format == M2TS {
						data = append(data, 0, 0, 0, 0)
					}
					data = append(data, packet...)
				}
				out, err := Extract(t.Context(), bytes.NewReader(data), Options{Format: format})
				if mode == "same-PID-duplicate" {
					if out != nil || !errors.Is(err, ErrInvalidBitstream) {
						t.Fatalf("nonconsecutive same-PID duplicate accepted: result=%v error=%v", out != nil, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("lawful adaptation/continuity control rejected: %v", err)
				}
				defer out.memory.release()
				if len(out.Frames) != 3 {
					t.Fatalf("changed output frame count: %d", len(out.Frames))
				}
			})
		}
	}
}

func TestTransportAdaptationExtensionMinimum(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []byte
		valid  bool
	}{
		{"empty", nil, false},
		{"flags", []byte{0x1f}, true},
		{"private-empty-body", []byte{0x0f, 0x80, 0}, true},
		{"private-body", []byte{0x0f, 0x80, 2, 0xaa, 0xbb}, true},
		{"descriptor-missing-length", []byte{0x0f, 0x80}, false},
		{"descriptor-short-body", []byte{0x0f, 0x80, 2, 0xaa}, false},
		{"LTW-and-private", []byte{0x8f, 0, 0, 0x80, 1, 0xaa}, true},
		{"LTW-and-short-private", []byte{0x8f, 0, 0, 0x80, 1}, false},
		{"two-private", []byte{0x0f, 0x80, 0, 0x81, 1, 0xaa}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packets := tsTestFile(t, true, 0, false)[:2]
			raw := rawFixture(t, "single")
			payload := tsPES(raw, 0, 0, true)
			n := 2 + len(tc.fields)
			payload = append(payload, bytes.Repeat([]byte{0xff}, 183-n-len(payload))...)
			packet := tsPacket(0x1011, 0, true, payload)
			packet[5], packet[6] = 1, byte(len(tc.fields))
			copy(packet[7:], tc.fields)
			assertTransportConformance(t, append(packets, packet), tc.valid)
		})
	}
}
