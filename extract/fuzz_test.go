package extract

import (
	"bytes"
	"encoding/binary"
	ebml "github.com/at-wat/ebml-go"
	"testing"
)

func FuzzContainers(f *testing.F) {
	config, sample := containerSample(f, 4)
	block := mkvTestBlock(f, 0xa3, ebml.Block{TrackNumber: 1, Keyframe: true, Data: [][]byte{sample}})
	f.Add(byte(Matroska), mkvTestFile(config, 0, ebmlElement(mkvCluster, ebmlUint(0xe7, 0), block), nil, false, false))
	mp4Data, _ := mp4OrdinaryFile(f, true, true, true)
	f.Add(byte(MP4), mp4Data)
	f.Add(byte(TS), bytes.Join(tsTestFile(f, true, 5, true), nil))
	f.Add(byte(M2TS), []byte{0, 0, 0, 0, 0x47})
	f.Fuzz(func(t *testing.T, format byte, data []byte) {
		if len(data) > 32768 {
			return
		}
		budget, _ := NewMemoryBudget(2 << 20)
		limits := Limits{MaxFrames: 32, MaxRetainedBytes: 2 << 20, MaxContainerIndexBytes: 512 << 10, MaxBlockBytes: 64 << 10, MaxParameterSetBytes: 8192, MaxSliceHeaderBytes: 8192, MaxSEIBytes: 8192, MaxNestingDepth: 8}
		e, err := Extract(t.Context(), bytes.NewReader(data), Options{Format: Format((format-byte(Matroska))%4) + Matroska, Limits: limits, Budget: budget})
		used, peak := budget.Usage()
		if peak > 2<<20 {
			t.Fatal("quota exceeded")
		}
		if err != nil {
			if e != nil || used != 0 {
				t.Fatal("failed result retained state")
			}
			return
		}
		if e == nil || len(e.Frames) > 32 {
			t.Fatal("successful result exceeded bounds")
		}
		if _, err := e.PlotMetadata(); err != nil {
			t.Fatal(err)
		}
		e.memory.release()
		used, _ = budget.Usage()
		if used != 0 {
			t.Fatal("transfer leaked charges")
		}
	})
}
func FuzzHEVCSyntax(f *testing.F) {
	raw := rawFixture(f, "single")
	stream, err := NewStream(StreamOptions{})
	if err != nil {
		f.Fatal(err)
	}
	if err := stream.Push(f.Context(), Chunk{Data: raw}); err != nil {
		f.Fatal(err)
	}
	clip, err := stream.Finish(f.Context())
	if err != nil {
		f.Fatal(err)
	}
	defer clip.memory.release()
	var s sequence
	var p pictureSet
	for _, e := range clip.events {
		switch e.kind {
		case 32, 33, 34:
			f.Add(e.kind, e.data)
			if e.kind == 33 {
				s, _ = parseSPS(e.data)
			}
			if e.kind == 34 {
				p, _ = parsePPS(e.data)
			}
		case 19:
			f.Add(e.kind, e.data)
		}
	}
	f.Fuzz(func(t *testing.T, kind byte, data []byte) {
		if len(data) > 8192 {
			return
		}
		switch kind % 4 {
		case 0:
			_, _ = parseVPS(data)
		case 1:
			_, _ = parseSPS(data)
		case 2:
			_, _ = parsePPS(data)
		case 3:
			_, _ = parseSlice(data, kind%2+1, 0, p, s)
		}
	})
}
func FuzzSEI(f *testing.F) {
	f.Add([]byte{0x80})
	p := fixture(f, "profile-a")
	data := append([]byte{4, byte(len(p))}, p...)
	f.Add(append(data, 0x80))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8192 {
			return
		}
		p, err := decodeSEI(data, false)
		if err == nil && p != nil {
			if err := p.validate(); err != nil {
				t.Fatal(err)
			}
		}
	})
}
func FuzzTimelineArithmetic(f *testing.F) {
	f.Add(make([]byte, 32))
	f.Add(bytes.Repeat([]byte{0xff}, 32))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) != 32 {
			return
		}
		a := Timestamp{Value: int64(binary.LittleEndian.Uint64(data)), Timescale: binary.LittleEndian.Uint64(data[8:]), Valid: true}
		b := Timestamp{Value: int64(binary.LittleEndian.Uint64(data[16:])), Timescale: binary.LittleEndian.Uint64(data[24:]), Valid: true}
		result, err := rationalTime(a, b)
		if err == nil {
			if !result.Valid || result.Timescale == 0 {
				t.Fatal("invalid successful clock")
			}
		}
	})
}
