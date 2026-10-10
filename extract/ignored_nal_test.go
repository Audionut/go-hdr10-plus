package extract

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func ancillaryNAL(kind, layer byte, body []byte, framing Framing) []byte {
	nal := authoredNAL(kind, body)[3:]
	nal[0] |= layer >> 5
	nal[1] |= (layer & 31) << 3
	if framing == AnnexB {
		return append([]byte{0, 0, 1}, nal...)
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(nal)))
	return append(length[:], nal...)
}

func TestIgnoredNALStorageAndSplits(t *testing.T) {
	body := bytes.Repeat([]byte{0x55}, 70<<10)
	body = append(body, 0, 0, 1, 0x55)
	for _, tc := range []struct {
		kind, layer byte
	}{{38, 0}, {41, 0}, {62, 0}, {63, 0}, {1, 1}, {33, 32}, {35, 63}, {39, 1}, {40, 32}} {
		for _, framing := range []Framing{AnnexB, LengthPrefixed} {
			t.Run(fmt.Sprintf("kind%d-layer%d-framing%d", tc.kind, tc.layer, framing), func(t *testing.T) {
				data := ancillaryNAL(tc.kind, tc.layer, body, framing)
				for _, split := range []int{0, 1, 3, 4, 5, 6, len(data) - 4, len(data) - 3, len(data) - 2, len(data) - 1, len(data)} {
					budget, _ := NewMemoryBudget(8 << 10)
					length := 0
					if framing == LengthPrefixed {
						length = 4
					}
					s, err := NewStream(StreamOptions{Framing: framing, LengthSize: length, Budget: budget})
					if err != nil {
						t.Fatal(err)
					}
					if err = s.Push(t.Context(), Chunk{Data: data[:split]}); err == nil {
						err = s.Push(t.Context(), Chunk{Data: data[split:], ESOffset: uint64(split)})
					}
					if err != nil {
						t.Fatalf("split%d: %v", split, err)
					}
					if cap(s.scanner.data) > 2 || s.scanner.offset != uint64(len(data)) {
						t.Fatalf("ignored body retained or extent lost: capacity=%d offset=%d", cap(s.scanner.data), s.scanner.offset)
					}
					control := ancillaryNAL(35, 0, []byte{0x10}, framing)
					if err := s.Push(t.Context(), Chunk{Data: control, ESOffset: uint64(len(data))}); err != nil {
						t.Fatal(err)
					}
					clip, err := s.Finish(t.Context())
					prefix := 3
					if framing == LengthPrefixed {
						prefix = 4
					}
					if err != nil || len(clip.events) != 1 || clip.events[0].kind != 35 || clip.events[0].offset != uint64(len(data)+prefix) || clip.events[0].endOffset != uint64(len(data)+len(control)) {
						t.Fatalf("ignored syntax changed following control/extent: clip=%v error=%v", clip, err)
					}
					clip.memory.release()
					if used, peak := budget.Usage(); used != 0 || peak > 8<<10 {
						t.Fatalf("bounded storage or cleanup: used=%d peak=%d", used, peak)
					}
				}
			})
		}
	}
}

func TestIgnoredNALPictureAndMetadataAlignment(t *testing.T) {
	want, err := Extract(t.Context(), bytes.NewReader(rawFixture(t, "reordered")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer want.memory.release()
	wantModel, err := want.PlotMetadata()
	if err != nil {
		t.Fatal(err)
	}
	for _, container := range []string{"MKV", "MP4", "fragmented-MP4"} {
		config, sample := containerSample(t, 4)
		ignored := ancillaryNAL(63, 0, bytes.Repeat([]byte{0x55}, 70<<10), LengthPrefixed)
		ignored = append(ignored, ancillaryNAL(39, 32, fixture(t, "profile-b"), LengthPrefixed)...)
		sample = append(ignored, sample...)
		e, err := Extract(t.Context(), bytes.NewReader(sampleContainer(t, container, config, [][]byte{sample, sample})), Options{})
		if err != nil || len(e.Frames) != 2 || len(e.Payloads) != 1 || e.Profile != "A" || !slices.Equal(e.SceneStarts, []uint64{0}) || e.Frames[0].AUOrdinal != 0 || e.Frames[1].AUOrdinal != 1 {
			t.Fatalf("%s ignored syntax changed base output: result=%v error=%v", container, e, err)
		}
		e.memory.release()
	}
	raw := rawFixture(t, "reordered")
	ignored := ancillaryNAL(63, 0, bytes.Repeat([]byte{0x55}, 70<<10), AnnexB)
	ignored = append(ignored, ancillaryNAL(19, 1, []byte{0x80, 0xff}, AnnexB)...)
	raw = append(ignored, raw...)
	got, err := Extract(t.Context(), bytes.NewReader(raw), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer got.memory.release()
	model, err := got.PlotMetadata()
	if err != nil || !reflect.DeepEqual(model, wantModel) || !reflect.DeepEqual(got.SceneStarts, want.SceneStarts) || !reflect.DeepEqual(got.Payloads, want.Payloads) {
		t.Fatalf("ignored syntax changed reordered output: error=%v", err)
	}
	if !reflect.DeepEqual(got.Frames, want.Frames) {
		t.Fatalf("picture identity changed: %+v", got.Frames)
	}
}

func TestIgnoredNALMalformedBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		tail []byte
		want error
	}{
		{"invalid-escape", []byte{0, 0, 3, 4, 0x55}, ErrInvalidBitstream},
		{"unescaped-two", []byte{0, 0, 2, 0x55}, ErrInvalidBitstream},
		{"incomplete-escape", []byte{0, 0, 3}, ErrIncomplete},
	} {
		for _, framing := range []Framing{AnnexB, LengthPrefixed} {
			for _, header := range []struct{ kind, layer byte }{{63, 0}, {39, 32}} {
				t.Run(fmt.Sprintf("%s-kind%d-layer%d-framing%d", tc.name, header.kind, header.layer, framing), func(t *testing.T) {
					data := ancillaryNAL(header.kind, header.layer, bytes.Repeat([]byte{0x55}, 256), framing)
					data = append(data, tc.tail...)
					if framing == LengthPrefixed {
						binary.BigEndian.PutUint32(data, uint32(len(data)-4))
					}
					for split := len(data) - len(tc.tail); split <= len(data); split++ {
						length := 0
						if framing == LengthPrefixed {
							length = 4
						}
						s, err := NewStream(StreamOptions{Framing: framing, LengthSize: length})
						if err != nil {
							t.Fatal(err)
						}
						if err = s.Push(t.Context(), Chunk{Data: data[:split]}); err == nil {
							err = s.Push(t.Context(), Chunk{Data: data[split:], ESOffset: uint64(split)})
						}
						if err == nil {
							_, err = s.Finish(t.Context())
						}
						if !errors.Is(err, tc.want) || s.memory.used != 0 {
							t.Fatalf("split%d: error=%v used=%d", split, err, s.memory.used)
						}
					}
				})
			}
		}
	}
}

func TestIgnoredNALHeadersBoundariesAndCancellation(t *testing.T) {
	data := ancillaryNAL(63, 0, []byte{0x55}, LengthPrefixed)
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"truncated-length", data[:3], ErrIncomplete},
		{"truncated-body", data[:len(data)-1], ErrIncomplete},
		{"terminal-zero", append(slices.Clone(data[:len(data)-1]), 0), ErrInvalidBitstream},
		{"forbidden-header", append(slices.Clone(data[:4]), 0xfe, 1, 0x55), ErrInvalidBitstream},
		{"zero-temporal-id", append(slices.Clone(data[:5]), 0, 0x55), ErrInvalidBitstream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewStream(StreamOptions{Framing: LengthPrefixed, LengthSize: 4})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Push(t.Context(), Chunk{Data: tc.data}); err == nil {
				_, err = s.Finish(t.Context())
			}
			if !errors.Is(err, tc.want) || s.memory.used != 0 {
				t.Fatalf("error=%v used=%d", err, s.memory.used)
			}
		})
	}
	s, _ := NewStream(StreamOptions{Framing: LengthPrefixed, LengthSize: 4})
	if err := s.Push(t.Context(), Chunk{Data: data[:6]}); err != nil {
		t.Fatal(err)
	}
	if err := s.Push(t.Context(), Chunk{Data: data[6:], ESOffset: 6, Markers: []Marker{{Kind: SampleStart, ESOffset: 6}}}); !errors.Is(err, ErrIncomplete) || !errors.Is(err, ErrInvalidBitstream) || s.memory.used != 0 {
		t.Fatalf("marker within ignored NAL: error=%v used=%d", err, s.memory.used)
	}
	s, _ = NewStream(StreamOptions{Framing: LengthPrefixed, LengthSize: 4})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Push(ctx, Chunk{Data: data}); !errors.Is(err, context.Canceled) || s.memory.used != 0 {
		t.Fatalf("cancellation: error=%v used=%d", err, s.memory.used)
	}
}

func TestRequiredNALSyntaxLimitsRemainBounded(t *testing.T) {
	for _, kind := range []byte{32, 33, 34, 35, 36, 37, 39, 40} {
		s, err := NewStream(StreamOptions{Limits: Limits{MaxParameterSetBytes: 64, MaxSEIBytes: 64}})
		if err != nil {
			t.Fatal(err)
		}
		data := ancillaryNAL(kind, 0, bytes.Repeat([]byte{0x55}, 128), AnnexB)
		if err := s.Push(t.Context(), Chunk{Data: data}); !errors.Is(err, ErrResourceLimit) || s.memory.used != 0 {
			t.Fatalf("kind%d quota: error=%v used=%d", kind, err, s.memory.used)
		} else if !strings.Contains(err.Error(), fmt.Sprintf("kind=%d layer=0 bytes=65 limit=64", kind)) {
			t.Fatalf("quota diagnostic lacks NAL context: %v", err)
		}
	}
}

func TestIgnoredNALCodecConfiguration(t *testing.T) {
	original, sample := containerSample(t, 4)
	for _, tc := range []struct{ kind, layer byte }{{63, 0}, {33, 32}, {39, 1}} {
		t.Run(fmt.Sprintf("kind%d-layer%d", tc.kind, tc.layer), func(t *testing.T) {
			config := slices.Clone(original)
			config[22]++
			nal := ancillaryNAL(tc.kind, tc.layer, bytes.Repeat([]byte{0x55}, 4096), LengthPrefixed)[4:]
			config = append(config, tc.kind, 0, 1, byte(len(nal)>>8), byte(len(nal)))
			config = append(config, nal...)
			s, err := NewStream(StreamOptions{Framing: LengthPrefixed, LengthSize: 4, CodecConfig: config, Limits: Limits{MaxParameterSetBytes: 128, MaxSEIBytes: 128}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Push(t.Context(), Chunk{Data: sample}); err != nil {
				t.Fatal(err)
			}
			clip, err := s.Finish(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer clip.memory.release()
			r := resolver{first: true, limits: clip.limits}
			if err := r.resolve(t.Context(), clip, 0); err != nil {
				t.Fatal(err)
			}
			if err := r.flush(); err != nil || len(r.output) != 1 || r.output[0].payload.profile() != "A" {
				t.Fatalf("configuration changed base metadata: error=%v output=%v", err, r.output)
			}
			if tc.kind != 63 {
				config[len(config)-len(nal)] |= 0x80
				budget, _ := NewMemoryBudget(1 << 20)
				if bad, err := NewStream(StreamOptions{Framing: LengthPrefixed, LengthSize: 4, CodecConfig: config, Budget: budget}); bad != nil || !errors.Is(err, ErrInvalidBitstream) {
					t.Fatalf("invalid enhancement header: stream=%v error=%v", bad, err)
				}
				if used, _ := budget.Usage(); used != 0 {
					t.Fatalf("failed configuration leaked storage: %d", used)
				}
			}
		})
	}
	if s, err := NewStream(StreamOptions{Framing: LengthPrefixed, LengthSize: 4, CodecConfig: original, Limits: Limits{MaxParameterSetBytes: 2}}); s != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("base configuration quota: stream=%v error=%v", s, err)
	}
}
