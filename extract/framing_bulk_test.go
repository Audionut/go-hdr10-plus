package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func extendedVCLSample(t testing.TB, length, padding int, tail []byte) (config, sample []byte, bodyOffset int) {
	t.Helper()
	config, original := containerSample(t, length)
	var size [4]byte
	copy(size[4-length:], original[:length])
	prefix := length + int(binary.BigEndian.Uint32(size[:]))
	copy(size[4-length:], original[prefix:prefix+length])
	if prefix+length+int(binary.BigEndian.Uint32(size[:])) != len(original) || original[prefix+length]>>1&63 > 31 {
		t.Fatal("fixture must end with one VCL NAL")
	}
	body := bytes.Clone(original[prefix+length:])
	body = append(body, bytes.Repeat([]byte{0x55}, padding)...)
	body = append(body, tail...)
	if uint64(len(body)) >= uint64(1)<<(length*8) {
		t.Fatal("fixture exceeds NAL length field")
	}
	binary.BigEndian.PutUint32(size[:], uint32(len(body)))
	sample = append(bytes.Clone(original[:prefix]), size[4-length:]...)
	bodyOffset = len(sample)
	sample = append(sample, body...)
	return config, sample, bodyOffset
}

func TestLengthPrefixedBodyValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		tail []byte
		want error
	}{
		{"nonzero", []byte{4, 1, 3, 2, 0xff}, nil},
		{"isolated-zero", []byte{0, 4, 0, 5}, nil},
		{"escaped-zero", []byte{0, 0, 3, 0, 5}, nil},
		{"escaped-three", []byte{0, 0, 3, 3, 5}, nil},
		{"cabac-tail", []byte{0, 0, 3}, nil},
		{"unescaped-zero", []byte{0, 0, 0, 5}, ErrInvalidBitstream},
		{"unescaped-one", []byte{0, 0, 1, 5}, ErrInvalidBitstream},
		{"unescaped-two", []byte{0, 0, 2, 5}, ErrInvalidBitstream},
		{"invalid-escape", []byte{0, 0, 3, 4, 5}, ErrInvalidBitstream},
		{"terminal-zero", []byte{0}, ErrInvalidBitstream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, sample, bodyOffset := extendedVCLSample(t, 4, 32780, tc.tail)
			tailStart := len(sample) - len(tc.tail)
			for split := tailStart; split <= len(sample); split++ {
				s, err := NewStream(StreamOptions{Framing: LengthPrefixed, LengthSize: 4, CodecConfig: config})
				if err != nil {
					t.Fatal(err)
				}
				if err = s.Push(t.Context(), Chunk{Data: sample[:split]}); err == nil {
					if split == tailStart && !s.scanner.headerComplete {
						t.Fatal("real slice producer did not complete the required header")
					}
					err = s.Push(t.Context(), Chunk{Data: sample[split:], ESOffset: uint64(split)})
				}
				if tc.want != nil {
					if !errors.Is(err, tc.want) {
						t.Fatalf("split%d: %v, want %v", split, err, tc.want)
					}
					s.memory.release()
					continue
				}
				if err != nil {
					t.Fatalf("split%d: %v", split, err)
				}
				c, err := s.Finish(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				last := c.events[len(c.events)-1]
				if last.kind != 19 || last.offset != uint64(bodyOffset) || last.endOffset != uint64(len(sample)) || s.scanner.offset != uint64(len(sample)) || !last.truncated || len(last.data) > 64 {
					t.Fatalf("wire positions or bounded retention changed: event=%+v scanned=%d", last, s.scanner.offset)
				}
				c.memory.release()
			}
		})
	}
}

func TestLengthPrefixedBodyBoundaries(t *testing.T) {
	for length := 1; length <= 4; length++ {
		padding := 32780
		if length == 1 {
			padding = 96
		}
		config, sample, bodyOffset := extendedVCLSample(t, length, padding, []byte{0, 0, 3, 1, 5})
		for _, container := range []string{"MKV", "MKV-lace", "MP4", "fragmented-MP4"} {
			e, err := Extract(t.Context(), bytes.NewReader(sampleContainer(t, container, config, [][]byte{sample, sample})), Options{})
			if err != nil {
				t.Fatalf("length%d %s: %v", length, container, err)
			}
			if len(e.Frames) != 2 || e.Frames[0].AUOrdinal != 0 || e.Frames[1].AUOrdinal != 1 || len(e.Payloads) != 1 || e.Payloads[0].profile() != "A" {
				t.Fatalf("length%d %s: metadata coverage changed", length, container)
			}
			e.memory.release()
		}
		for _, tc := range []struct {
			name string
			data []byte
			want error
		}{
			{"truncated-body", sample[:len(sample)-1], ErrIncomplete},
			{"next-length", append(bytes.Clone(sample), 0), ErrIncomplete},
			{"forbidden-header", bytes.Clone(sample), ErrInvalidBitstream},
		} {
			if tc.name == "forbidden-header" {
				tc.data[bodyOffset] |= 0x80
			}
			if tc.name == "next-length" && length == 1 {
				tc.want = ErrInvalidBitstream // One zero byte is a complete zero-length field.
			}
			s, err := NewStream(StreamOptions{Framing: LengthPrefixed, LengthSize: length, CodecConfig: config})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Push(t.Context(), Chunk{Data: tc.data}); err == nil {
				_, err = s.Finish(t.Context())
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("length%d %s: %v, want %v", length, tc.name, err, tc.want)
			}
			s.memory.release()
		}
	}
}
