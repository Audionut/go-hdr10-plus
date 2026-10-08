package hdr10plus

import (
	"errors"
	"fmt"
	"image"
)

// ErrInvalidOptions identifies an unknown peak source or invalid frame range.
// Callers can classify wrapped errors using errors.Is.
var ErrInvalidOptions = errors.New("invalid HDR10+ plot options")

// FrameRange selects array positions, including both Start and End.
type FrameRange struct {
	Start int
	End   int
}

// Options configures a chart. The zero value selects all frames, PeakHistogram,
// and the title "HDR10+ plot". A nonnil Range{0, 0} selects only the first frame.
type Options struct {
	Title      string
	PeakSource PeakSource
	Range      *FrameRange
}

// Render returns a fresh opaque 3000×1200 *image.RGBA. It validates the selected
// peak source only within the inclusive range, before allocating the canvas.
// Neither metadata nor options is modified. Concurrent calls may share input
// provided callers do not mutate it. Failed calls return a nil image.
func Render(metadata *Metadata, options Options) (image.Image, error) {
	start, end, err := selection(metadata, options)
	if err != nil {
		return nil, err
	}
	data, err := calculate(metadata.Frames[start:end+1], start, options.PeakSource)
	if err != nil {
		return nil, err
	}
	if options.Title == "" {
		options.Title = "HDR10+ plot"
	}
	return renderChart(metadata, options, data)
}

func selection(metadata *Metadata, options Options) (int, int, error) {
	if metadata == nil || len(metadata.Frames) == 0 {
		return 0, 0, fmt.Errorf("%w: at least one frame is required", ErrInvalidMetadata)
	}
	if metadata.SceneCount < 0 {
		return 0, 0, fmt.Errorf("%w: negative SceneCount", ErrInvalidMetadata)
	}
	if options.PeakSource.description() == "" {
		return 0, 0, fmt.Errorf("%w: unknown PeakSource %d", ErrInvalidOptions, options.PeakSource)
	}
	start, end := 0, len(metadata.Frames)-1
	if options.Range != nil {
		start, end = options.Range.Start, options.Range.End
	}
	if start < 0 || end < start || end >= len(metadata.Frames) {
		return 0, 0, fmt.Errorf("%w: range [%d,%d] outside %d frames", ErrInvalidOptions, start, end, len(metadata.Frames))
	}
	return start, end, nil
}
