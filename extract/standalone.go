package extract

import (
	"context"
	"fmt"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
)

// RetainedBytes reports accounted extraction-owned storage, excluding caller
// mutations, runtime overhead and later conversions.
func (e *Extraction) RetainedBytes() int64 {
	if e == nil {
		return 0
	}
	if e.memory == nil {
		return 512 + int64(len(e.Frames))*2048 + int64(len(e.Payloads))*4096 + int64(len(e.SceneStarts))*16
	}
	return e.memory.used
}

// Release discards completed syntax and returns its accounting charges. It is
// idempotent. A released clip cannot be finalized or assembled again.
func (c *Clip) Release() {
	if c == nil || c.released {
		return
	}
	c.released = true
	c.memory.release()
	c.events, c.markers = nil, nil
}

// Release discards an extraction and returns its accounting charges. It is
// idempotent; callers must not retain or use aliases to released data.
func (e *Extraction) Release() {
	if e == nil {
		return
	}
	if e.memory != nil {
		e.memory.release()
	}
	e.Frames, e.Payloads, e.SceneStarts = nil, nil, nil
	e.Profile = ""
}

// FinalizeStandalone resolves normally finished syntax using Extract's coverage
// and picture ordering rules. It consumes the clip on success or failure. The
// returned extraction owns separate charges until Release; no input is reread.
func FinalizeStandalone(ctx context.Context, c *Clip) (*Extraction, error) {
	if ctx == nil || c == nil || c.released || c.memory == nil {
		return nil, fmt.Errorf("%w: standalone clip", hdr10plus.ErrInvalidOptions)
	}
	defer c.Release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := resolveStandalone(ctx, c, c.limits)
	if err != nil {
		return nil, err
	}
	output, err := applyPresentationEdit(ctx, r.output, c.edit)
	if err != nil {
		return nil, err
	}
	return makeExtraction(ctx, output, c.limits, c.memory.shared, r.metadataSeen, c.memory)
}

func resolveStandalone(ctx context.Context, c *Clip, l Limits) (*resolver, error) {
	if c.frames > (l.MaxRetainedBytes-resolutionScratch)/512 {
		return nil, ErrResourceLimit
	}
	if _, err := c.memory.reserve(resolutionScratch + c.frames*512 + pocHistoryScratch); err != nil {
		return nil, err
	}
	r := resolver{first: true, limits: l}
	if err := r.resolve(ctx, c, 0); err != nil {
		return nil, err
	}
	if err := r.complete(); err != nil {
		return nil, err
	}
	return &r, nil
}
