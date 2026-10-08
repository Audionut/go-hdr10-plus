package hdr10plus

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/vector"
)

func testMetadata(t *testing.T) *Metadata {
	t.Helper()
	input, err := os.Open("testdata/minimal.json")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	metadata, err := DecodeJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}

func TestImageGoldensAndPNG(t *testing.T) {
	metadata := testMetadata(t)
	for _, tc := range []struct {
		name    string
		options Options
	}{
		{"default", Options{}},
		{"crop", Options{Title: "Inclusive crop", PeakSource: PeakMaxSCLLuminance, Range: &FrameRange{1, 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img, err := Render(metadata, tc.options)
			if err != nil {
				t.Fatal(err)
			}
			rgba, ok := img.(*image.RGBA)
			if !ok || img.Bounds() != image.Rect(0, 0, 3000, 1200) {
				t.Fatalf("image type/bounds: %T %v", img, img.Bounds())
			}
			for i := 3; i < len(rgba.Pix); i += 4 {
				if rgba.Pix[i] != 255 {
					t.Fatalf("nonopaque pixel %d", i/4)
				}
			}
			for _, p := range []image.Point{{0, 0}, {59, 600}, {2999, 1199}, {1500, 1190}} {
				if rgba.RGBAAt(p.X, p.Y) != (color.RGBA{255, 255, 255, 255}) {
					t.Fatalf("nonwhite margin at %v", p)
				}
			}
			input, err := os.Open("testdata/" + tc.name + ".png")
			if err != nil {
				t.Fatal(err)
			}
			golden, err := png.Decode(input)
			input.Close()
			if err != nil {
				t.Fatal(err)
			}
			decoded := image.NewRGBA(img.Bounds())
			draw.Draw(decoded, decoded.Bounds(), golden, image.Point{}, draw.Src)
			if golden.Bounds() != img.Bounds() || !slices.Equal(decoded.Pix, rgba.Pix) {
				t.Fatal("decoded golden pixels differ")
			}
			var output bytes.Buffer
			if err := png.Encode(&output, img); err != nil {
				t.Fatal(err)
			}
			roundTrip, err := png.Decode(&output)
			if err != nil {
				t.Fatal(err)
			}
			draw.Draw(decoded, decoded.Bounds(), roundTrip, image.Point{}, draw.Src)
			if !slices.Equal(decoded.Pix, rgba.Pix) {
				t.Fatal("PNG round trip changed pixels")
			}
		})
	}
}

func TestSeriesOpacityOrderAndClipping(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, canvasWidth, canvasHeight))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	raster := vector.NewRasterizer(chartRect.Dx(), chartRect.Dy())
	samples := []sample{{nitsToPQ(100), nitsToPQ(1000)}, {nitsToPQ(100), nitsToPQ(1000)}, {nitsToPQ(100), nitsToPQ(1000)}}
	drawSeries(img, raster, samples, true, peakColor, 64)
	assertPixel := func(x, y int, want color.RGBA) {
		t.Helper()
		got := img.RGBAAt(x, y)
		for i, pair := range [][2]uint8{{got.R, want.R}, {got.G, want.G}, {got.B, want.B}, {got.A, want.A}} {
			if math.Abs(float64(pair[0])-float64(pair[1])) > 1 {
				t.Fatalf("pixel (%d,%d) channel %d: %v, want %v", x, y, i, got, want)
			}
		}
	}
	assertPixel(500, 500, color.RGBA{207, 217, 247, 255})
	drawSeries(img, raster, samples, false, averageColor, 128)
	assertPixel(500, 500, color.RGBA{207, 217, 247, 255})
	assertPixel(500, 850, color.RGBA{141, 108, 188, 255})
	// Geometric clipping preserves the slope above PQ one; clamping vertices
	// would leave this near-top pixel white. Data cannot paint outside the plot.
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	drawSeries(img, raster, []sample{{peak: 1.4}, {peak: 0.4}}, true, peakColor, 64)
	assertPixel(chartRect.Min.X+400, chartRect.Min.Y+5, color.RGBA{207, 217, 247, 255})
	assertPixel(chartRect.Min.X+400, chartRect.Min.Y-1, color.RGBA{255, 255, 255, 255})
	assertPixel(chartRect.Max.X+1, chartRect.Min.Y+5, color.RGBA{255, 255, 255, 255})
	// A valid average above peak remains visible in the usual drawing order.
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	samples = []sample{{nitsToPQ(1000), nitsToPQ(100)}, {nitsToPQ(1000), nitsToPQ(100)}}
	drawSeries(img, raster, samples, true, peakColor, 64)
	drawSeries(img, raster, samples, false, averageColor, 128)
	assertPixel(500, 500, color.RGBA{165, 127, 192, 255})
}

func TestOneFrameVisibleIncludingZeroAndOverrange(t *testing.T) {
	for _, value := range []uint32{0, 1000, math.MaxUint32} {
		metadata := &Metadata{Frames: []Frame{{AverageRGB: value, DistributionValues: []uint32{value}}}}
		img, err := Render(metadata, Options{})
		if err != nil {
			t.Fatal(err)
		}
		y := chartRect.Min.Y + int(math.Round((1-nitsToPQ(float64(value)/10))*float64(chartRect.Dy())))
		y = min(chartRect.Max.Y-2, max(chartRect.Min.Y, y))
		// The final frame redraw affects the first row at the top boundary only.
		if img.(*image.RGBA).RGBAAt(chartRect.Min.X+1, y+1) != (color.RGBA{75, 0, 130, 255}) {
			t.Fatalf("single sample %d disappeared", value)
		}
	}
}

func TestCropRebasesCoordinatesAndKeepsGlobalScenes(t *testing.T) {
	metadata := testMetadata(t)
	options := Options{Range: &FrameRange{1, 2}, PeakSource: PeakHistogram99}
	cropped, err := Render(metadata, options)
	if err != nil {
		t.Fatal(err)
	}
	explicit := &Metadata{Profile: metadata.Profile, SceneCount: metadata.SceneCount, Frames: metadata.Frames[1:3]}
	options.Range = nil
	rebased, err := Render(explicit, options)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cropped.(*image.RGBA).Pix, rebased.(*image.RGBA).Pix) {
		t.Fatal("crop did not rebase positions or retain whole-input scene count")
	}
}

func TestGridUsesPQTickPositions(t *testing.T) {
	img, err := Render(&Metadata{Frames: []Frame{{DistributionValues: []uint32{0}}}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, nits := range nitTicks[:len(nitTicks)-1] { // Top tick belongs to the black frame.
		y := chartRect.Min.Y + int(math.Round((1-nitsToPQ(nits))*float64(chartRect.Dy())))
		pixel := img.(*image.RGBA).RGBAAt(chartRect.Min.X+1000, y)
		if pixel != (color.RGBA{229, 229, 229, 255}) {
			t.Fatalf("tick at %g nits, y=%d: %v", nits, y, pixel)
		}
	}
}

func TestTicksTextAndOwnership(t *testing.T) {
	for count, want := range map[int]int{1: 1, 24: 1, 25: 2, 100: 5, 259: 20, 100000: 5000, 1000000: 50000} {
		if got := tickInterval(count); got != want {
			t.Fatalf("tick interval for %d: %d, want %d", count, got, want)
		}
	}
	parsed, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 40, DPI: 72})
	if err != nil {
		t.Fatal(err)
	}
	defer face.Close()
	long := "Long\n\t\x00title \U0010ffff " + strings.Repeat("W", 1000)
	fitted := fitText(face, long, 2880)
	if font.MeasureString(face, fitted).Ceil() > 2880 || !strings.HasSuffix(fitted, "…") || strings.ContainsAny(fitted, "\n\t\x00\U0010ffff") {
		t.Fatalf("text not fitted/sanitized: %q", fitted)
	}
	metadata := testMetadata(t)
	metadata.Profile = long
	caption := frameCaption(face, metadata, 2)
	if !strings.HasPrefix(caption, "Frames: 2. Profile ") || !strings.HasSuffix(caption, ". Scenes: 2.") || font.MeasureString(face, caption).Ceil() > chartRect.Dx() {
		t.Fatalf("long profile hid numeric captions: %q", caption)
	}
	want := testMetadata(t)
	want.Profile = long
	bounds := FrameRange{1, 2}
	options := Options{Title: long, Range: &bounds}
	first, err := Render(metadata, options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(metadata, options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(metadata, want) || bounds != (FrameRange{1, 2}) || options.Title != long {
		t.Fatal("caller input changed")
	}
	if first == second || !slices.Equal(first.(*image.RGBA).Pix, second.(*image.RGBA).Pix) {
		t.Fatal("output ownership or determinism")
	}
	first.(*image.RGBA).SetRGBA(0, 0, color.RGBA{A: 255})
	if second.(*image.RGBA).RGBAAt(0, 0).R != 255 {
		t.Fatal("outputs share pixels")
	}
	// Exercise all four sources on shared immutable metadata with independent
	// mutable faces/rasterizers. The race check validates this real call path.
	var wg sync.WaitGroup
	for source := PeakHistogram; source <= PeakMaxSCLLuminance; source++ {
		wg.Go(func() {
			if _, err := Render(metadata, Options{PeakSource: source}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}
