package extract

import (
	"bytes"
	ebml "github.com/at-wat/ebml-go"
	"testing"
)

func BenchmarkDecodeT35(b *testing.B) {
	data := fixture(b, "profile-b")
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := DecodeT35(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRawExtraction(b *testing.B) {
	data := rawFixture(b, "reordered")
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := Extract(b.Context(), bytes.NewReader(data), Options{}); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkFileContainers(b *testing.B) {
	config, sample := containerSample(b, 4)
	mkv := mkvTestFile(config, 0, ebmlElement(mkvCluster, ebmlUint(0xe7, 0), mkvTestBlock(b, 0xa3, ebml.Block{TrackNumber: 1, Keyframe: true, Data: [][]byte{sample}})), nil, false, false)
	ordinary, _ := mp4OrdinaryFile(b, true, true, true)
	for _, fixture := range []struct {
		name   string
		format Format
		data   []byte
	}{{"MKV", Matroska, mkv}, {"MP4", MP4, ordinary}, {"fragmented-MP4", MP4, mp4FragmentFile(b, false, true)}, {"TS", TS, bytes.Join(tsTestFile(b, true, 5, true), nil)}} {
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture.data)))
			for b.Loop() {
				if _, err := Extract(b.Context(), bytes.NewReader(fixture.data), Options{Format: fixture.format}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
