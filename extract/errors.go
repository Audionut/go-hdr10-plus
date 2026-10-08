package extract

import (
	"errors"
	"fmt"
)

// Extraction errors have stable errors.Is identities, not stable text.
var (
	ErrNoMetadata          = errors.New("no HDR10+ metadata")
	ErrUnsupportedInput    = errors.New("unsupported extraction input")
	ErrUnsupportedMetadata = errors.New("unsupported HDR10+ metadata")
	ErrInvalidBitstream    = errors.New("invalid bitstream")
	ErrIncomplete          = errors.New("incomplete extraction")
	ErrResourceLimit       = errors.New("extraction resource limit")
	ErrAmbiguousTrack      = errors.New("ambiguous HEVC track")
)

// Error identifies the field or feature that failed. Offset and AU are zero-based
// and meaningful only when their corresponding Known flag is true. Stream is
// the caller's source identity; Container is empty for standalone T.35 decoding.
type Error struct {
	Container   string
	Stream      StreamKey
	Offset      uint64
	OffsetKnown bool
	AU          uint64
	AUKnown     bool
	Field       string
	Err         error
}

func (e *Error) Error() string { return fmt.Sprintf("HDR10+ %s: %v", e.Field, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

func fieldError(field string, cause error) error { return &Error{Field: field, Err: cause} }
