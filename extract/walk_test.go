package extract

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	ebml "github.com/at-wat/ebml-go"
)

type pairedConsumer struct {
	streams [2]*Stream
	chunks  int
	offset  uint64
}

func (p *pairedConsumer) Push(ctx context.Context, c Chunk) error {
	if c.ESOffset != p.offset {
		return ErrIncomplete
	}
	p.offset += uint64(len(c.Data))
	p.chunks++
	for _, s := range p.streams {
		if err := s.Push(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func (p *pairedConsumer) Configure(ctx context.Context, config []byte) error {
	for _, stream := range p.streams {
		if err := stream.Configure(ctx, config); err != nil {
			return err
		}
	}
	return nil
}

func TestWalkStandaloneParityAndRelease(t *testing.T) {
	config, sample := containerSample(t, 4)
	cluster := ebmlElement(mkvCluster, ebmlUint(0xe7, 0), mkvTestBlock(t, 0xa3, ebml.Block{TrackNumber: 1, Timecode: 0, Data: [][]byte{sample}}))
	ordinary, _ := mp4OrdinaryFile(t, true, true, true)
	for name, data := range map[string][]byte{"raw": rawFixture(t, "single"), "mkv": mkvTestFile(config, 1000, cluster, nil, true, false), "mp4": ordinary, "fmp4": mp4FragmentFile(t, true, false)} {
		t.Run(name, func(t *testing.T) {
			baseline, err := Extract(t.Context(), bytes.NewReader(data), Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer baseline.Release()
			budget, err := NewMemoryBudget(512 << 20)
			if err != nil {
				t.Fatal(err)
			}
			pair := &pairedConsumer{}
			factories := 0
			err = Walk(t.Context(), bytes.NewReader(data), Options{Budget: budget}, func(info SourceInfo) (Consumer, error) {
				factories++
				for i := range pair.streams {
					s, err := NewStream(StreamOptions{Key: info.Key, Framing: info.Framing, LengthSize: info.LengthSize, CodecConfig: info.CodecConfig, PresentationEdit: info.Edit, Budget: budget})
					if err != nil {
						return nil, err
					}
					pair.streams[i] = s
				}
				return pair, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if factories != 1 || pair.chunks == 0 {
				t.Fatalf("factory=%d chunks=%d", factories, pair.chunks)
			}
			for _, s := range pair.streams {
				clip, err := s.Finish(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				result, err := FinalizeStandalone(t.Context(), clip)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(baseline.Frames, result.Frames) || !reflect.DeepEqual(baseline.Payloads, result.Payloads) || !reflect.DeepEqual(baseline.SceneStarts, result.SceneStarts) || baseline.Profile != result.Profile {
					t.Fatal("shared walk differs from Extract")
				}
				if _, err := FinalizeStandalone(t.Context(), clip); err == nil {
					t.Fatal("reused consumed clip")
				}
				result.Release()
				result.Release()
				clip.Release()
				if _, err := result.PlotMetadata(); err == nil {
					t.Fatal("used released extraction")
				}
			}
			if used, _ := budget.Usage(); used != 0 {
				t.Fatalf("retained %d bytes", used)
			}
		})
	}
}

type rejectingConsumer struct{ err error }

func (c rejectingConsumer) Push(context.Context, Chunk) error { return c.err }

func TestWalkFailureOwnership(t *testing.T) {
	cause := errors.New("consumer failure")
	if err := Walk(t.Context(), bytes.NewReader(rawFixture(t, "single")), Options{}, func(SourceInfo) (Consumer, error) { return rejectingConsumer{cause}, nil }); !errors.Is(err, cause) {
		t.Fatalf("consumer error: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	called := false
	cancel()
	if err := Walk(ctx, bytes.NewReader(nil), Options{}, func(SourceInfo) (Consumer, error) { called = true; return nil, nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancel: %v called=%v", err, called)
	}
}
