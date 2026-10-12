package extract

import (
	"context"
	"math/big"
)

// PresentationEdit selects one half-open media interval and maps its start to
// Offset. It describes a rate-one MP4 media edit, optionally after an empty edit.
// Original sample PTS/DTS still reach the collector; the edit applies only after
// complete picture resolution. All values are exact, valid, nonnegative times.
type PresentationEdit struct {
	Start, Duration, Offset Timestamp
}

func (e *PresentationEdit) validate() error {
	if e == nil {
		return nil
	}
	for _, stamp := range []Timestamp{e.Start, e.Duration, e.Offset} {
		if !stamp.Valid || stamp.Timescale == 0 || stamp.Value < 0 {
			return fieldError("presentation-edit", ErrUnsupportedInput)
		}
	}
	return nil
}

func applyPresentationEdit(ctx context.Context, input []resolvedPicture, edit *PresentationEdit) ([]resolvedPicture, error) {
	if edit == nil {
		return input, nil
	}
	if err := edit.validate(); err != nil {
		return nil, err
	}
	toRat := func(t Timestamp) *big.Rat {
		return new(big.Rat).SetFrac(big.NewInt(t.Value), new(big.Int).SetUint64(t.Timescale))
	}
	start, duration, offset := toRat(edit.Start), toRat(edit.Duration), toRat(edit.Offset)
	output := input[:0]
	for _, p := range input {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !p.picture.PTS.Valid || p.picture.PTS.Timescale == 0 {
			return nil, fieldError("edit-timing", ErrUnsupportedInput)
		}
		var relative, mapped big.Rat
		relative.Sub(toRat(p.picture.PTS), start)
		if relative.Sign() < 0 || relative.Cmp(duration) >= 0 {
			continue
		}
		mapped.Add(offset, &relative)
		if !mapped.Num().IsInt64() || !mapped.Denom().IsUint64() {
			return nil, fieldError("edit-timing-range", ErrUnsupportedInput)
		}
		p.picture.PTS = Timestamp{Value: mapped.Num().Int64(), Timescale: mapped.Denom().Uint64(), Valid: true}
		output = append(output, p)
	}
	clear(input[len(output):])
	return output, nil
}
