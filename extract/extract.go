package extract

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
)

// Format is an input envelope. Auto examines signatures, never extensions.
type Format uint8

const (
	Auto Format = iota
	RawHEVC
	Matroska
	MP4
	TS
	M2TS
)

// Options selects a container stream and finite limits. TrackID zero requires a
// unique HEVC candidate. Raw HEVC rejects nonzero track IDs.
type Options struct {
	Format  Format
	TrackID uint64
	Limits  Limits
	Budget  *MemoryBudget
}

// Extract reads a caller-owned seeker through normal completion. It currently
// supports raw Annex B HEVC, Matroska, MP4, TS and M2TS within the documented
// dialect and timing scope. Auto selects signatures rather than extensions.
// Successful extraction validates delivered structure and metadata coverage,
// not entropy-coded picture integrity. It does not close r or decode pixels.
func Extract(ctx context.Context, r io.ReadSeeker, opts Options) (*Extraction, error) {
	if ctx == nil || r == nil || opts.Format > M2TS {
		return nil, fmt.Errorf("%w: extraction arguments", hdr10plus.ErrInvalidOptions)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, err := opts.Limits.normalized()
	if err != nil {
		return nil, err
	}
	format, err := detectFormat(ctx, r, opts.Format)
	if err != nil {
		return nil, err
	}
	opts.Format = format

	if opts.Format == Matroska {
		return extractMatroska(ctx, r, opts, l)
	}
	if opts.Format == MP4 {
		return extractMP4(ctx, r, opts, l)
	}
	if opts.Format == TS || opts.Format == M2TS {
		return extractTransport(ctx, r, opts, l)
	}
	if opts.Format != RawHEVC {
		return nil, fieldError("container-adapter", ErrUnsupportedInput)
	}
	if opts.TrackID != 0 {
		return nil, fieldError("raw-track-ID", hdr10plus.ErrInvalidOptions)
	}
	s, err := NewStream(StreamOptions{Limits: l, Budget: opts.Budget})
	if err != nil {
		return nil, err
	}
	defer s.memory.release()
	if _, err := s.memory.reserve(32768); err != nil {
		s.Abort(err)
		return nil, err
	}
	if err := walkRaw(ctx, r, s); err != nil {
		s.Abort(err)
		return nil, err
	}
	return finishFile(ctx, s, l, opts.Budget)
}

func detectFormat(ctx context.Context, r io.ReadSeeker, format Format) (Format, error) {
	if format == Auto {
		var probe [8]byte
		position, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return Auto, err
		}
		n := 0
		for n < len(probe) {
			if err := ctx.Err(); err != nil {
				return Auto, err
			}
			read, readErr := r.Read(probe[n:])
			n += read
			if readErr != nil {
				if readErr != io.EOF {
					return Auto, readErr
				}
				break
			}
			if read == 0 {
				return Auto, io.ErrNoProgress
			}
		}
		if _, err := r.Seek(position, io.SeekStart); err != nil {
			return Auto, err
		}
		switch {
		case n >= 4 && binary.BigEndian.Uint32(probe[:4]) == 0x1a45dfa3:
			format = Matroska
		case n >= 8 && (string(probe[4:8]) == "ftyp" || string(probe[4:8]) == "moov" || string(probe[4:8]) == "mdat" || string(probe[4:8]) == "free" || string(probe[4:8]) == "styp"):
			format = MP4
		case n >= 1 && probe[0] == 0x47:
			format = TS
		case n >= 5 && probe[4] == 0x47:
			format = M2TS
		default:
			zeros := 0
			for zeros < n && probe[zeros] == 0 {
				zeros++
			}
			if zeros < n && (zeros < 2 || probe[zeros] != 1) {
				return Auto, fieldError("unrecognized-input", ErrUnsupportedInput)
			}
			format = RawHEVC
		}
	}
	return format, nil
}

func combineReadError(err, readErr error) error {
	if readErr != nil && readErr != io.EOF {
		return errors.Join(err, readErr)
	}
	return err
}
