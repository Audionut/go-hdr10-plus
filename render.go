package hdr10plus

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

const canvasWidth, canvasHeight = 3000, 1200

// The embedded font needs 20 extra pixels between the rotated description and
// the tick labels to keep both readable within the 60-pixel outer margin.
var chartRect = image.Rect(140, 170, 2940, 1110)

var nitTicks = [...]float64{0.01, 0.1, 0.5, 1, 2.5, 5, 10, 25, 50, 100, 200, 400, 600, 1000, 2000, 4000, 10000}

var peakColor = color.NRGBA{R: 65, G: 105, B: 225, A: 255}
var averageColor = color.NRGBA{R: 75, G: 0, B: 130, A: 255}

func renderChart(metadata *Metadata, options Options, data plotData) (image.Image, error) {
	parsed, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, fmt.Errorf("parse embedded font: %w", err)
	}
	titleFace, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 40, DPI: 72})
	if err != nil {
		return nil, err
	}
	defer titleFace.Close()
	labelFace, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 24, DPI: 72})
	if err != nil {
		return nil, err
	}
	defer labelFace.Close()
	tickFace, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 22, DPI: 72})
	if err != nil {
		return nil, err
	}
	defer tickFace.Close()

	img := image.NewRGBA(image.Rect(0, 0, canvasWidth, canvasHeight))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	title := fitText(titleFace, options.Title, canvasWidth-120)
	text(img, titleFace, title, (canvasWidth-font.MeasureString(titleFace, title).Ceil())/2, 64)
	text(img, labelFace, frameCaption(labelFace, metadata, len(data.samples)), chartRect.Min.X, 105)
	text(img, labelFace, "Peak brightness source: "+options.PeakSource.description(), chartRect.Min.X, 135)
	drawAxes(img, labelFace, tickFace, len(data.samples))
	raster := vector.NewRasterizer(chartRect.Dx(), chartRect.Dy())
	drawSeries(img, raster, data.samples, true, peakColor, 64)
	drawSeries(img, raster, data.samples, false, averageColor, 128)
	plotFrame(img)
	drawLegend(img, labelFace, data)
	return img, nil
}

func text(dst draw.Image, face font.Face, value string, x, baseline int) {
	drawer := font.Drawer{Dst: dst, Src: image.Black, Face: face, Dot: fixed.P(x, baseline)}
	drawer.DrawString(value)
}

func frameCaption(face font.Face, metadata *Metadata, count int) string {
	prefix := fmt.Sprintf("Frames: %d. Profile ", count)
	suffix := fmt.Sprintf(". Scenes: %d.", metadata.SceneCount)
	width := chartRect.Dx() - font.MeasureString(face, prefix+suffix).Ceil()
	return prefix + fitText(face, metadata.Profile, width) + suffix
}

// fitText sanitizes display text only, keeping the stored strings unchanged.
func fitText(face font.Face, value string, width int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		if _, ok := face.GlyphAdvance(r); !ok {
			if _, ok := face.GlyphAdvance('\ufffd'); ok {
				return '\ufffd'
			}
			return '?'
		}
		return r
	}, value)
	if font.MeasureString(face, value).Ceil() <= width {
		return value
	}
	const ellipsis = "…"
	limit := fixed.I(width) - font.MeasureString(face, ellipsis)
	var advance fixed.Int26_6
	var previous rune
	for i, r := range value {
		if i > 0 {
			advance += face.Kern(previous, r)
		}
		a, _ := face.GlyphAdvance(r)
		if advance+a > limit {
			return value[:i] + ellipsis
		}
		advance += a
		previous = r
	}
	return value
}

func line(img *image.RGBA, rect image.Rectangle, value color.Color) {
	draw.Draw(img, rect, image.NewUniform(value), image.Point{}, draw.Over)
}

func drawAxes(img *image.RGBA, labelFace, tickFace font.Face, count int) {
	major := color.NRGBA{A: 26}
	minor := color.NRGBA{A: 3}
	for i, nits := range nitTicks {
		y := chartRect.Min.Y + int(math.Round((1-nitsToPQ(nits))*float64(chartRect.Dy())))
		line(img, image.Rect(chartRect.Min.X, y, chartRect.Max.X, y+1).Intersect(chartRect), major)
		if i > 0 {
			mid := (nitsToPQ(nitTicks[i-1]) + nitsToPQ(nits)) / 2
			my := chartRect.Min.Y + int(math.Round((1-mid)*float64(chartRect.Dy())))
			line(img, image.Rect(chartRect.Min.X, my, chartRect.Max.X, my+1).Intersect(chartRect), minor)
		}
		label := fmt.Sprintf("%g", nits)
		text(img, tickFace, label, chartRect.Min.X-8-font.MeasureString(tickFace, label).Ceil(), y+7)
	}
	interval := tickInterval(count)
	for tick := 0; tick <= count; {
		x := chartRect.Min.X + int(math.Round(float64(tick)/float64(count)*float64(chartRect.Dx())))
		line(img, image.Rect(x, chartRect.Min.Y, x+1, chartRect.Max.Y).Intersect(chartRect), major)
		label := fmt.Sprint(tick)
		text(img, tickFace, label, x-font.MeasureString(tickFace, label).Ceil()/2, chartRect.Max.Y+28)
		if interval > count-tick {
			break
		}
		mx := chartRect.Min.X + int(math.Round((float64(tick)+float64(interval)/2)/float64(count)*float64(chartRect.Dx())))
		line(img, image.Rect(mx, chartRect.Min.Y, mx+1, chartRect.Max.Y), minor)
		tick += interval
	}
	const xLabel = "frames"
	text(img, labelFace, xLabel, chartRect.Min.X+(chartRect.Dx()-font.MeasureString(labelFace, xLabel).Ceil())/2, 1164)
	const yLabel = "nits (cd/m²)"
	label := image.NewRGBA(image.Rect(0, 0, font.MeasureString(labelFace, yLabel).Ceil()+4, 36))
	text(label, labelFace, yLabel, 2, 26)
	// Rotate counterclockwise so the y description reads from bottom to top.
	top := chartRect.Min.Y + (chartRect.Dy()-label.Bounds().Dx())/2
	for y := range label.Bounds().Dy() {
		for x := range label.Bounds().Dx() {
			img.SetRGBA(62+y, top+label.Bounds().Dx()-1-x, compositeWhite(label.RGBAAt(x, y)))
		}
	}
	plotFrame(img)
}

func compositeWhite(c color.RGBA) color.RGBA {
	return color.RGBA{R: c.R + 255 - c.A, G: c.G + 255 - c.A, B: c.B + 255 - c.A, A: 255}
}

func tickInterval(count int) int {
	minimum := max(1, count/24)
	if count%24 != 0 {
		minimum = max(1, count/24+1)
	}
	for scale := 1; ; scale *= 10 {
		for _, multiplier := range [...]int{1, 2, 5} {
			if scale*multiplier >= minimum {
				return scale * multiplier
			}
		}
	}
}

func plotFrame(img *image.RGBA) {
	r := chartRect
	line(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X+1, r.Min.Y+1), color.Black)
	line(img, image.Rect(r.Min.X, r.Max.Y, r.Max.X+1, r.Max.Y+1), color.Black)
	line(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+1, r.Max.Y+1), color.Black)
	line(img, image.Rect(r.Max.X, r.Min.Y, r.Max.X+1, r.Max.Y+1), color.Black)
}

func drawSeries(img *image.RGBA, raster *vector.Rasterizer, samples []sample, peak bool, value color.NRGBA, alpha uint8) {
	w, h := chartRect.Dx(), chartRect.Dy()
	position := func(i int) (float64, float64) {
		code := samples[i].average
		if peak {
			code = samples[i].peak
		}
		return float64(i) / float64(len(samples)) * float64(w), (1 - code) * float64(h)
	}
	raster.Reset(w, h)
	raster.DrawOp = draw.Over
	raster.MoveTo(0, float32(h))
	for i := range samples {
		x, y := position(i)
		raster.LineTo(float32(x), float32(y))
	}
	x, _ := position(len(samples) - 1)
	raster.LineTo(float32(x), float32(h))
	raster.ClosePath()
	fill := value
	fill.A = alpha // NRGBA supplies straight alpha; Uniform converts to premultiplied.
	raster.Draw(img, chartRect, image.NewUniform(fill), image.Point{})
	// The rasterizer clips filled paths to its viewport. Borders need the same
	// geometric clipping: clamping each endpoint would change crossing slopes.
	raster.Reset(w, h)
	for i := 1; i < len(samples); i++ {
		x0, y0 := position(i - 1)
		x1, y1 := position(i)
		if y0 < 0 && y1 < 0 {
			continue
		}
		if y0 < 0 {
			x0 += (x1 - x0) * (-y0) / (y1 - y0)
			y0 = 0
		} else if y1 < 0 {
			x1 = x0 + (x1-x0)*(-y0)/(y1-y0)
			y1 = 0
		}
		stroke(raster, x0, y0, x1, y1)
	}
	raster.Draw(img, chartRect, image.NewUniform(value), image.Point{})
	if len(samples) == 1 {
		_, y := position(0)
		py := min(chartRect.Max.Y-2, max(chartRect.Min.Y, chartRect.Min.Y+int(math.Round(y))))
		// Start at x+1 so redrawing the axis does not hide the single sample.
		line(img, image.Rect(chartRect.Min.X+1, py, chartRect.Min.X+3, py+2), value)
	}
}

func stroke(raster *vector.Rasterizer, x0, y0, x1, y1 float64) {
	length := math.Hypot(x1-x0, y1-y0)
	if length == 0 {
		return
	}
	dx, dy := (y1-y0)/(2*length), (x0-x1)/(2*length)
	raster.MoveTo(float32(x0+dx), float32(y0+dy))
	raster.LineTo(float32(x1+dx), float32(y1+dy))
	raster.LineTo(float32(x1-dx), float32(y1-dy))
	raster.LineTo(float32(x0-dx), float32(y0-dy))
	raster.ClosePath()
}

func drawLegend(img *image.RGBA, face font.Face, data plotData) {
	peak, average := data.labels()
	width := max(font.MeasureString(face, peak).Ceil(), font.MeasureString(face, average).Ceil()) + 64
	x, bottom := chartRect.Min.X+12, chartRect.Max.Y-12
	box := image.Rect(x, bottom-84, x+width, bottom)
	draw.Draw(img, box, image.White, image.Point{}, draw.Src)
	line(img, image.Rect(box.Min.X, box.Min.Y, box.Max.X, box.Min.Y+1), color.Black)
	line(img, image.Rect(box.Min.X, box.Max.Y-1, box.Max.X, box.Max.Y), color.Black)
	line(img, image.Rect(box.Min.X, box.Min.Y, box.Min.X+1, box.Max.Y), color.Black)
	line(img, image.Rect(box.Max.X-1, box.Min.Y, box.Max.X, box.Max.Y), color.Black)
	for i, label := range [...]string{peak, average} {
		y := box.Min.Y + 28 + i*34
		value := peakColor
		if i == 1 {
			value = averageColor
		}
		line(img, image.Rect(x+12, y-9, x+32, y-7), value)
		text(img, face, label, x+44, y)
	}
}
