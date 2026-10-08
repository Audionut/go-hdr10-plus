package extract

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
)

func rawFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".hevc")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRawExtraction(t *testing.T) {
	for _, name := range []string{"single", "reordered"} {
		t.Run(name, func(t *testing.T) {
			e, err := Extract(t.Context(), bytes.NewReader(rawFixture(t, name)), Options{})
			if err != nil {
				t.Fatal(err)
			}
			want := []uint64{0}
			if name == "reordered" {
				want = []uint64{0, 2, 1}
			}
			var got []uint64
			for _, f := range e.Frames {
				got = append(got, f.AUOrdinal)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("AU mapping %v, want %v", got, want)
			}
			if name == "reordered" {
				if e.Payloads[e.Frames[0].PayloadIndex].profile() != "A" || e.Payloads[e.Frames[1].PayloadIndex].profile() != "B" || e.Payloads[e.Frames[2].PayloadIndex].profile() != "A" {
					t.Fatal("inheritance applied in display order")
				}
			}
			if _, err := e.PlotMetadata(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStreamSplitsAndOwnership(t *testing.T) {
	data := rawFixture(t, "single")
	for split := range len(data) + 1 {
		s, err := NewStream(StreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range []struct {
			offset int
			data   []byte
		}{{0, data[:split]}, {split, data[split:]}} {
			borrowed := slices.Clone(part.data)
			if err := s.Push(t.Context(), Chunk{Data: borrowed, ESOffset: uint64(part.offset)}); err != nil {
				t.Fatalf("split %d: %v", split, err)
			}
			clear(borrowed)
		}
		c, err := s.Finish(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		r := resolver{first: true, limits: c.limits}
		if err := r.resolve(t.Context(), c, 0); err != nil {
			t.Fatalf("split %d: %v", split, err)
		}
		if err := r.flush(); err != nil {
			t.Fatal(err)
		}
		if len(r.output) != 1 || r.output[0].payload.profile() != "A" {
			t.Fatal("split result")
		}
		c.memory.release()
		if _, err := s.Finish(t.Context()); err == nil {
			t.Fatal("second Finish accepted")
		}
	}
}

func TestRawFailures(t *testing.T) {
	if e, err := Extract(t.Context(), bytes.NewReader(rawFixture(t, "no-metadata")), Options{}); e != nil || !errors.Is(err, ErrNoMetadata) {
		t.Fatalf("absence %v %v", e, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if e, err := Extract(ctx, bytes.NewReader(nil), Options{}); e != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation")
	}
	for _, data := range [][]byte{{0, 0, 1, 0x26}, {0, 0, 1, 0xa6, 1}, {0, 0, 1, 0x26, 0}, {1, 2, 3}} {
		if e, err := Extract(t.Context(), bytes.NewReader(data), Options{}); e != nil || err == nil {
			t.Fatalf("malformed accepted %x", data)
		}
	}
	s, err := NewStream(StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("source read failure")
	s.Abort(cause)
	s.Abort(io.ErrUnexpectedEOF)
	if err := s.Push(t.Context(), Chunk{}); !errors.Is(err, cause) {
		t.Fatal("terminal cause lost")
	}
	if _, err := s.Finish(t.Context()); !errors.Is(err, cause) {
		t.Fatal("terminal finish")
	}
	if s.memory.used != 0 {
		t.Fatal("aborted charges retained")
	}
	if _, err := NewStream(StreamOptions{Framing: LengthPrefixed}); !errors.Is(err, hdr10plus.ErrInvalidOptions) {
		t.Fatal("invalid length size")
	}
}

func TestNativeUpstreamDifferential(t *testing.T) {
	dir := os.Getenv("HDR10PLUS_REFERENCE_DIR")
	if dir == "" {
		t.Skip("opt-in upstream source assets")
	}
	for raw, jsonName := range map[string]string{"regular.hevc": "regular_metadata.json", "dhdr10-opt.hevc": "metadata-dhdr10-opt.json"} {
		t.Run(raw, func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, raw))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			e, err := Extract(t.Context(), f, Options{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := e.PlotMetadata()
			if err != nil {
				t.Fatal(err)
			}
			j, err := os.Open(filepath.Join(dir, jsonName))
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			want, err := hdr10plus.DecodeJSON(j)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("native mismatch: frames %d/%d profile %s/%s scenes %d/%d", len(got.Frames), len(want.Frames), got.Profile, want.Profile, got.SceneCount, want.SceneCount)
			}
		})
	}
}

func FuzzStream(f *testing.F) {
	f.Add(rawFixture(f, "single"), uint8(0))
	f.Add([]byte{0, 0, 1, 0x26}, uint8(2))
	f.Fuzz(func(t *testing.T, data []byte, length uint8) {
		if len(data) > 8192 {
			t.Skip()
		}
		opts := StreamOptions{Limits: Limits{MaxFrames: 32, MaxRetainedBytes: 1 << 20, MaxContainerIndexBytes: 1 << 20, MaxBlockBytes: 1 << 20, MaxSEIBytes: 8192, MaxParameterSetBytes: 8192, MaxSliceHeaderBytes: 128}}
		if length%5 != 0 {
			opts.Framing = LengthPrefixed
			opts.LengthSize = int(length % 5)
		}
		s, err := NewStream(opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Push(t.Context(), Chunk{Data: data}); err != nil {
			return
		}
		c, err := s.Finish(t.Context())
		if err != nil {
			return
		}
		defer c.memory.release()
		r := resolver{first: true, limits: c.limits}
		if err := r.resolve(t.Context(), c, 0); err != nil {
			return
		}
		if err := r.complete(); err != nil {
			return
		}
		e, err := makeExtraction(t.Context(), r.output, c.limits, nil, r.metadataSeen, c.memory)
		if err == nil {
			defer e.memory.release()
			if _, err := e.PlotMetadata(); err != nil {
				t.Fatal(err)
			}
		}
	})
}
