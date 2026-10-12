package extract

import (
	"context"
	"fmt"
	"io"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
)

// Consumer receives borrowed compressed bytes synchronously. Walk never calls
// Finish or Abort: the caller owns collector completion and failure isolation.
type Consumer interface {
	Push(context.Context, Chunk) error
}

// ConfigurableConsumer accepts hvcC changes between complete MP4 samples.
// Configuration bytes are borrowed for the duration of Configure. A producer
// rejects configuration changes when a consumer does not implement this method.
type ConfigurableConsumer interface {
	Consumer
	Configure(context.Context, []byte) error
}

// SourceInfo describes the selected physical stream. CodecConfig is borrowed
// during the factory call; consumers must copy any configuration they retain.
type SourceInfo struct {
	Format      Format
	Key         StreamKey
	Framing     Framing
	LengthSize  int
	CodecConfig []byte
	Edit        *PresentationEdit
}

// ConsumerFactory is called once, after unique or explicit track selection.
type ConsumerFactory func(SourceInfo) (Consumer, error)

// Walk delivers one selected compressed payload traversal to a caller-owned
// consumer. It supports raw HEVC, Matroska and MP4. Container header discovery may
// seek, but skips video bodies. Success means normal EOF and envelope validation;
// it does not certify compressed syntax or finalize a metadata collector.
// Options.Limits and Budget bound producer storage independently of consumers.
func Walk(ctx context.Context, r io.ReadSeeker, opts Options, factory ConsumerFactory) error {
	if ctx == nil || r == nil || factory == nil || opts.Format > M2TS {
		return fmt.Errorf("%w: walk arguments", hdr10plus.ErrInvalidOptions)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l, err := opts.Limits.normalized()
	if err != nil {
		return err
	}
	format, err := detectFormat(ctx, r, opts.Format)
	if err != nil {
		return err
	}
	opts.Format = format
	if format == Matroska {
		_, err := readMatroska(ctx, r, opts, l, factory)
		return err
	}
	if format == MP4 {
		_, err := readMP4(ctx, r, opts, l, factory)
		return err
	}
	if format == TS || format == M2TS {
		_, err := readTransport(ctx, r, opts, l, factory)
		return err
	}
	if format != RawHEVC {
		return fieldError("walk-container", ErrUnsupportedInput)
	}
	if opts.TrackID != 0 {
		return fieldError("raw-track-ID", hdr10plus.ErrInvalidOptions)
	}
	a := &accounting{max: l.MaxRetainedBytes, shared: opts.Budget}
	defer a.release()
	if _, err := a.reserve(32768); err != nil {
		return err
	}
	consumer, err := factory(SourceInfo{Format: RawHEVC, Framing: AnnexB})
	if err != nil {
		return err
	}
	if consumer == nil {
		return fmt.Errorf("%w: nil consumer", hdr10plus.ErrInvalidOptions)
	}
	return walkRaw(ctx, r, consumer)
}

func walkRaw(ctx context.Context, r io.Reader, consumer Consumer) error {
	buffer := make([]byte, 32768)
	var offset uint64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := r.Read(buffer)
		if n > 0 {
			if err := consumer.Push(ctx, Chunk{Data: buffer[:n], ESOffset: offset}); err != nil {
				return combineReadError(err, readErr)
			}
			offset += uint64(n)
		}
		if readErr != nil {
			if readErr != io.EOF {
				return readErr
			}
			return ctx.Err()
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
}
