package hdr10plus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrInvalidMetadata identifies invalid plotting data or JSON. Use errors.Is;
// error wording is not a stable API. Underlying JSON and reader errors are kept.
var ErrInvalidMetadata = errors.New("invalid HDR10+ metadata")

// Frame contains luminance values in units of 0.1 nit. DistributionValues
// retains wire order. MaxSCL holds ordered red, green, blue components when
// using PeakMaxSCLLuminance; PeakMaxSCL accepts any nonempty component list.
type Frame struct {
	AverageRGB         uint32
	DistributionValues []uint32
	MaxSCL             []uint32
}

// Metadata holds plotting data in frame order. SceneCount describes the whole
// input, including when rendering a crop. Profile is an unvalidated display label.
type Metadata struct {
	Profile    string
	SceneCount int
	Frames     []Frame
}

type wireMetadata struct {
	JSONInfo         json.RawMessage
	SceneInfo        []json.RawMessage
	SceneInfoSummary json.RawMessage
}

type wireLuminance struct {
	AverageRGB             json.RawMessage
	MaxScl                 json.RawMessage
	LuminanceDistributions json.RawMessage
}

// DecodeJSON reads exactly one upstream-style JSON object, ignoring fields not
// consumed by plotting. Optional peak lists may be unavailable; Render checks
// only the selected source and frames. It does not close r. Returned slices are
// owned by the result. A failed call returns nil metadata.
func DecodeJSON(r io.Reader) (*Metadata, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: nil reader", ErrInvalidMetadata)
	}
	decoder := json.NewDecoder(r)
	var wire *wireMetadata
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("%w: decode root: %w", ErrInvalidMetadata, err)
	}
	if wire == nil {
		return nil, fmt.Errorf("%w: null root", ErrInvalidMetadata)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("%w: after root: %w", ErrInvalidMetadata, err)
		}
		return nil, fmt.Errorf("%w: expected one JSON object", ErrInvalidMetadata)
	}
	var info struct {
		HDR10plusProfile *string
	}
	if err := decodeObject(wire.JSONInfo, "JSONInfo", &info); err != nil {
		return nil, err
	}
	if info.HDR10plusProfile == nil {
		return nil, fmt.Errorf("%w: JSONInfo.HDR10plusProfile is required", ErrInvalidMetadata)
	}
	var summary struct {
		SceneFrameNumbers []*uint64
	}
	if err := decodeObject(wire.SceneInfoSummary, "SceneInfoSummary", &summary); err != nil {
		return nil, err
	}
	if summary.SceneFrameNumbers == nil {
		return nil, fmt.Errorf("%w: SceneInfoSummary.SceneFrameNumbers is required", ErrInvalidMetadata)
	}
	for i, value := range summary.SceneFrameNumbers {
		if value == nil {
			return nil, fmt.Errorf("%w: SceneInfoSummary.SceneFrameNumbers[%d] is null", ErrInvalidMetadata, i)
		}
	}
	if len(wire.SceneInfo) == 0 {
		return nil, fmt.Errorf("%w: SceneInfo requires at least one frame", ErrInvalidMetadata)
	}
	metadata := &Metadata{
		Profile: *info.HDR10plusProfile, SceneCount: len(summary.SceneFrameNumbers),
		Frames: make([]Frame, len(wire.SceneInfo)),
	}
	for i, raw := range wire.SceneInfo {
		path := fmt.Sprintf("SceneInfo[%d]", i)
		var frame struct {
			LuminanceParameters json.RawMessage
		}
		if err := decodeObject(raw, path, &frame); err != nil {
			return nil, err
		}
		path += ".LuminanceParameters"
		var luminance wireLuminance
		if err := decodeObject(frame.LuminanceParameters, path, &luminance); err != nil {
			return nil, err
		}
		if unavailable(luminance.AverageRGB) {
			return nil, fmt.Errorf("%w: %s.AverageRGB is required", ErrInvalidMetadata, path)
		}
		result := &metadata.Frames[i]
		if err := json.Unmarshal(luminance.AverageRGB, &result.AverageRGB); err != nil {
			return nil, fmt.Errorf("%w: %s.AverageRGB: %w", ErrInvalidMetadata, path, err)
		}
		var err error
		result.MaxSCL, err = decodeValues(luminance.MaxScl, path+".MaxScl")
		if err != nil {
			return nil, err
		}
		if !unavailable(luminance.LuminanceDistributions) {
			var distribution struct {
				DistributionValues json.RawMessage
			}
			path += ".LuminanceDistributions"
			if err := decodeObject(luminance.LuminanceDistributions, path, &distribution); err != nil {
				return nil, err
			}
			result.DistributionValues, err = decodeValues(distribution.DistributionValues, path+".DistributionValues")
			if err != nil {
				return nil, err
			}
		}
	}
	return metadata, nil
}

func unavailable(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func decodeObject(raw json.RawMessage, path string, target any) error {
	if unavailable(raw) {
		return fmt.Errorf("%w: %s requires an object", ErrInvalidMetadata, path)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidMetadata, path, err)
	}
	return nil
}

func decodeValues(raw json.RawMessage, path string) ([]uint32, error) {
	if unavailable(raw) {
		return nil, nil
	}
	var values []*uint32
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalidMetadata, path, err)
	}
	result := make([]uint32, len(values))
	for i, value := range values {
		if value == nil {
			return nil, fmt.Errorf("%w: %s[%d] is null", ErrInvalidMetadata, path, i)
		}
		result[i] = *value
	}
	return result, nil
}
