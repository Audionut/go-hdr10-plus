package hdr10plus_test

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"slices"
	"strings"
	"testing"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
)

func ExampleDecodeJSON() {
	const input = `{"JSONInfo":{"HDR10plusProfile":"B"},"SceneInfo":[{"LuminanceParameters":{"AverageRGB":100,"MaxScl":[1000,2000,3000]}}],"SceneInfoSummary":{"SceneFrameNumbers":[1]}}`
	metadata, err := hdr10plus.DecodeJSON(strings.NewReader(input))
	if err != nil {
		panic(err)
	}
	img, err := hdr10plus.Render(metadata, hdr10plus.Options{Title: "My HDR10+ plot", PeakSource: hdr10plus.PeakMaxSCL})
	if err != nil {
		panic(err)
	}
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		panic(err)
	}
	decoded, err := png.Decode(&output)
	if err != nil {
		panic(err)
	}
	fmt.Println(decoded.Bounds())
	// Output: (0,0)-(3000,1200)
}

func ExampleRender() {
	metadata := &hdr10plus.Metadata{Profile: "A", SceneCount: 2, Frames: []hdr10plus.Frame{
		{AverageRGB: 100, MaxSCL: []uint32{1000, 2000, 3000}},
		{AverageRGB: 5000, MaxSCL: []uint32{7000, 5000, 9000}},
	}}
	// Raw values use 0.1 nit. The range is inclusive and scene count stays 2.
	img, err := hdr10plus.Render(metadata, hdr10plus.Options{PeakSource: hdr10plus.PeakMaxSCLLuminance, Range: &hdr10plus.FrameRange{Start: 1, End: 1}})
	if err != nil {
		panic(err)
	}
	fmt.Println(img.Bounds())
	// Output: (0,0)-(3000,1200)
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestApplicationHandlesEncodingFailure(t *testing.T) {
	img, err := hdr10plus.Render(&hdr10plus.Metadata{Frames: []hdr10plus.Frame{{DistributionValues: []uint32{0}}}}, hdr10plus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("writer failed")
	if err := png.Encode(failingWriter{failure}, img); !errors.Is(err, failure) {
		t.Fatalf("writer failure lost: %v", err)
	}
}

func TestDirectAndDecodedMetadataAgree(t *testing.T) {
	const input = `{"JSONInfo":{"HDR10plusProfile":"B"},"SceneInfo":[{"LuminanceParameters":{"AverageRGB":100,"MaxScl":[1000,2000,3000]}}],"SceneInfoSummary":{"SceneFrameNumbers":[1]}}`
	decoded, err := hdr10plus.DecodeJSON(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	direct := &hdr10plus.Metadata{Profile: "B", SceneCount: 1, Frames: []hdr10plus.Frame{{AverageRGB: 100, MaxSCL: []uint32{1000, 2000, 3000}}}}
	options := hdr10plus.Options{PeakSource: hdr10plus.PeakMaxSCLLuminance}
	a, err := hdr10plus.Render(decoded, options)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hdr10plus.Render(direct, options)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.(*image.RGBA).Pix, b.(*image.RGBA).Pix) {
		t.Fatal("direct and decoded values render differently")
	}
}
