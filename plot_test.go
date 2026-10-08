package hdr10plus

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSelection(t *testing.T) {
	metadata := &Metadata{Profile: "B", SceneCount: 3, Frames: []Frame{
		{AverageRGB: 100, MaxSCL: []uint32{1000}},
		{AverageRGB: 200, DistributionValues: []uint32{3000}},
		{AverageRGB: 300, DistributionValues: []uint32{4000}},
	}}
	for _, tc := range []struct {
		name       string
		rangeValue *FrameRange
		start, end int
	}{
		{"all", nil, 0, 2}, {"first", &FrameRange{0, 0}, 0, 0},
		{"last", &FrameRange{2, 2}, 2, 2}, {"interior", &FrameRange{1, 2}, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end, err := selection(metadata, Options{Range: tc.rangeValue})
			if err != nil || start != tc.start || end != tc.end {
				t.Fatalf("selection: %d %d %v", start, end, err)
			}
		})
	}
	for _, bounds := range []FrameRange{{-1, 1}, {0, -1}, {2, 1}, {0, 3}, {3, 3}} {
		if img, err := Render(metadata, Options{Range: &bounds}); img != nil || !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("invalid range %v: %v %v", bounds, img, err)
		}
	}
	if img, err := Render(metadata, Options{PeakSource: 255}); img != nil || !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("invalid source: %v %v", img, err)
	}
	for _, invalid := range []*Metadata{nil, {}, {SceneCount: -1, Frames: []Frame{{}}}} {
		if img, err := Render(invalid, Options{}); img != nil || !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("invalid metadata: %v %v", img, err)
		}
	}
	// Missing histogram outside the crop is allowed; an error within the crop
	// reports the original frame position rather than the rebased x coordinate.
	if _, err := Render(metadata, Options{Range: &FrameRange{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(metadata, Options{PeakSource: PeakMaxSCL, Range: &FrameRange{1, 2}}); !errors.Is(err, ErrInvalidMetadata) || !strings.Contains(err.Error(), "SceneInfo[1]") {
		t.Fatalf("original frame index: %v", err)
	}
	data, err := calculate(metadata.Frames[1:3], 1, PeakHistogram)
	if err != nil {
		t.Fatal(err)
	}
	assertNits(t, data.peak.maximum, 400)
	assertNits(t, data.average.maximum, 30)
	if len(data.samples) != 2 || !reflect.DeepEqual(metadata.Frames[0].MaxSCL, []uint32{1000}) {
		t.Fatal("crop/data mutation")
	}
}

func BenchmarkRender(b *testing.B) {
	for _, count := range []int{259, 100000, 1000000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			frames := make([]Frame, count)
			for i := range frames {
				frames[i] = Frame{AverageRGB: uint32(500 + i%1000), DistributionValues: []uint32{10000}}
			}
			metadata := &Metadata{Profile: "A", SceneCount: 3, Frames: frames}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Render(metadata, Options{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
