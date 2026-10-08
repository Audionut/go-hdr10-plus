package extract

import (
	"slices"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
)

// StreamKey identifies a physical elementary stream within a caller namespace.
type StreamKey struct {
	SourceID string
	ClipID   string
	TrackID  uint64
}

// Timestamp is the exact rational Value/Timescale in seconds. Zero and negative
// values are valid. A valid timestamp requires a nonzero denominator.
type Timestamp struct {
	Value     int64
	Timescale uint64
	Valid     bool
}

// Picture identifies an output picture. Slice position in Extraction.Frames is
// the dense display frame number; physical AU, decode ordinal and POC differ.
type Picture struct {
	Stream            StreamKey
	AUOrdinal         uint64
	OccurrenceOrdinal uint64
	DecodeOrdinal     uint64
	POC               int64
	PTS               Timestamp
	PayloadIndex      uint32
}

// Extraction owns payloads, presentation-ordered pictures, and metadata scene
// starts. Values are caller mutable; concurrent mutation is unsupported.
type Extraction struct {
	Payloads    []Payload
	Frames      []Picture
	SceneStarts []uint64
	Profile     string
	memory      *accounting
}

func (e *Extraction) captions() (string, []uint64) {
	profile := e.Payloads[e.Frames[0].PayloadIndex].profile()
	scenes := []uint64{0}
	for i, f := range e.Frames {
		p := e.Payloads[f.PayloadIndex]
		if p.profile() != profile {
			profile = "N/A"
		}
		if i > 0 && !sceneEqual(p, e.Payloads[e.Frames[i-1].PayloadIndex]) {
			scenes = append(scenes, uint64(i))
		}
	}
	return profile, scenes
}

// PlotMetadata validates the entire extraction and returns a deep copy in the
// root plotting model. It preserves wire ordering and encoded luminance units.
// Mutation that breaks extraction invariants wraps hdr10plus.ErrInvalidMetadata.
func (e *Extraction) PlotMetadata() (*hdr10plus.Metadata, error) {
	bad := func(field string) (*hdr10plus.Metadata, error) {
		return nil, fieldError(field, hdr10plus.ErrInvalidMetadata)
	}
	if e == nil || len(e.Frames) == 0 || len(e.Payloads) == 0 {
		return bad("empty-extraction")
	}
	for i := range e.Payloads {
		if err := e.Payloads[i].validate(); err != nil {
			return nil, err
		}
	}
	for _, f := range e.Frames {
		if uint64(f.PayloadIndex) >= uint64(len(e.Payloads)) || f.PTS.Valid && f.PTS.Timescale == 0 {
			return bad("picture")
		}
	}
	profile, scenes := e.captions()
	if e.Profile != profile || !slices.Equal(e.SceneStarts, scenes) {
		return bad("captions")
	}
	m := &hdr10plus.Metadata{Profile: profile, SceneCount: len(scenes), Frames: make([]hdr10plus.Frame, len(e.Frames))}
	for i, f := range e.Frames {
		p := e.Payloads[f.PayloadIndex]
		values := make([]uint32, len(p.Distributions))
		for j, d := range p.Distributions {
			values[j] = d.Value
		}
		m.Frames[i] = hdr10plus.Frame{AverageRGB: p.AverageRGB, MaxSCL: slices.Clone(p.MaxSCL[:]), DistributionValues: values}
	}
	return m, nil
}
