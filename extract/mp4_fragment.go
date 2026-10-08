package extract

import (
	"errors"
	"io"
	"math"

	mp4 "github.com/abema/go-mp4"
)

type mp4Traf struct {
	header    *mp4.Tfhd
	clock     *mp4.Tfdt
	runs      []*mp4.Trun
	encrypted bool
}

func (f *mp4File) traf(b *mp4.BoxInfo) (*mp4Traf, error) {
	if f.l.MaxNestingDepth < 2 {
		return nil, ErrResourceLimit
	}
	t := &mp4Traf{}
	for {
		child, err := f.box(b.Offset + b.Size)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch child.Type.String() {
		case "tfhd", "tfdt", "trun":
			decoded, err := f.decode(child)
			if err != nil {
				return nil, err
			}
			switch box := decoded.(type) {
			case *mp4.Tfhd:
				if t.header != nil || box.GetFlags()&^uint32(0x03003b) != 0 {
					return nil, ErrInvalidBitstream
				}
				t.header = box
			case *mp4.Tfdt:
				if t.clock != nil {
					return nil, ErrInvalidBitstream
				}
				t.clock = box
			case *mp4.Trun:
				if t.header == nil || box.GetFlags()&0x404 == 0x404 {
					return nil, ErrInvalidBitstream
				}
				if err := f.index(64); err != nil {
					return nil, err
				}
				t.runs = append(t.runs, box)
			}
		case "senc", "saiz", "saio":
			t.encrypted = true
		}
		if _, err := child.SeekToEnd(f); err != nil {
			return nil, err
		}
	}
	if t.header == nil {
		return nil, ErrInvalidBitstream
	}
	return t, nil
}
func signedFileOffset(base uint64, delta int32) (uint64, error) {
	if delta >= 0 {
		if uint64(delta) > math.MaxUint64-base {
			return 0, ErrInvalidBitstream
		}
		return base + uint64(delta), nil
	}
	amount := uint64(-int64(delta))
	if amount > base {
		return 0, ErrInvalidBitstream
	}
	return base - amount, nil
}
func (f *mp4File) fragmentSamples(selected *mp4Track) error {
	if len(f.fragments) == 0 {
		return nil
	}
	if err := f.index(int64(len(selected.samples)) * 64); err != nil {
		return err
	}
	var decode uint64
	known := false
	var sequence uint32
	sequenceKnown := false
	for _, fragment := range f.fragments {
		if _, err := fragment.SeekToPayload(f); err != nil {
			return err
		}
		implicitBase := fragment.Offset
		hasSequence := false
		for {
			child, err := f.box(fragment.Offset + fragment.Size)
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if child.Type.String() == "mfhd" {
				if hasSequence {
					return ErrInvalidBitstream
				}
				decoded, err := f.decode(child)
				if err != nil {
					return err
				}
				next := decoded.(*mp4.Mfhd).SequenceNumber
				if sequenceKnown && next != sequence+1 {
					return fieldError("fragment-sequence", errors.Join(ErrInvalidBitstream, ErrIncomplete))
				}
				sequence = next
				sequenceKnown = true
				hasSequence = true
			}
			if child.Type.String() != "traf" {
				if _, err := child.SeekToEnd(f); err != nil {
					return err
				}
				continue
			}
			if !hasSequence {
				return fieldError("missing-mfhd", ErrInvalidBitstream)
			}
			t, err := f.traf(child)
			if err != nil {
				return err
			}
			h := t.header
			defaults := f.defaults[h.TrackID]
			if defaults == nil {
				return fieldError("missing-trex", ErrInvalidBitstream)
			}
			base := implicitBase
			if h.CheckFlag(mp4.TfhdDefaultBaseIsMoof) {
				base = fragment.Offset
			}
			if h.CheckFlag(mp4.TfhdBaseDataOffsetPresent) {
				if h.CheckFlag(mp4.TfhdDefaultBaseIsMoof) {
					return ErrInvalidBitstream
				}
				base = h.BaseDataOffset
			}
			description, size, duration := defaults.DefaultSampleDescriptionIndex, defaults.DefaultSampleSize, defaults.DefaultSampleDuration
			if h.CheckFlag(mp4.TfhdSampleDescriptionIndexPresent) {
				description = h.SampleDescriptionIndex
			}
			if h.CheckFlag(mp4.TfhdDefaultSampleSizePresent) {
				size = h.DefaultSampleSize
			}
			if h.CheckFlag(mp4.TfhdDefaultSampleDurationPresent) {
				duration = h.DefaultSampleDuration
			}
			isSelected := h.TrackID == selected.id
			if isSelected {
				if t.encrypted {
					return fieldError("encrypted-fragment", ErrUnsupportedInput)
				}
				if t.clock != nil {
					next := t.clock.GetBaseMediaDecodeTime()
					if known && next < decode {
						return fieldError("fragment-decode-overlap", ErrInvalidBitstream)
					}
					decode = next
					known = true
				} else if !known {
					return fieldError("fragment-decode-clock", ErrUnsupportedInput)
				}
			}
			position := base
			for _, run := range t.runs {
				if h.CheckFlag(mp4.TfhdDurationIsEmpty) && run.SampleCount != 0 {
					return ErrInvalidBitstream
				}
				if run.CheckFlag(1) {
					position, err = signedFileOffset(base, run.DataOffset)
					if err != nil {
						return err
					}
				}
				if isSelected {
					if int64(run.SampleCount) > f.l.MaxFrames-int64(len(selected.samples)) {
						return ErrResourceLimit
					}
					if err := f.index(int64(run.SampleCount) * 128); err != nil {
						return err
					}
				}
				for i, e := range run.Entries {
					bytes, ticks := size, duration
					if run.CheckFlag(0x200) {
						bytes = e.SampleSize
					}
					if run.CheckFlag(0x100) {
						ticks = e.SampleDuration
					}
					if bytes == 0 || uint64(bytes) > math.MaxUint64-position {
						return ErrInvalidBitstream
					}
					if isSelected {
						if ticks == 0 || decode > math.MaxInt64 || uint64(ticks) > math.MaxInt64-decode {
							return ErrUnsupportedInput
						}
						offset := run.GetSampleCompositionTimeOffset(i)
						pts := int64(decode)
						if offset > 0 && pts > math.MaxInt64-offset {
							return ErrUnsupportedInput
						}
						pts += offset
						selected.samples = append(selected.samples, mp4Sample{offset: position, size: bytes, description: description, dts: int64(decode), pts: pts})
						decode += uint64(ticks)
					}
					position += uint64(bytes)
				}
			}
			implicitBase = position
			if _, err := child.SeekToEnd(f); err != nil {
				return err
			}
		}
		if !hasSequence {
			return fieldError("missing-mfhd", ErrInvalidBitstream)
		}
	}
	if len(selected.samples) == 0 {
		return fieldError("empty-selected-track", errors.Join(ErrInvalidBitstream, ErrIncomplete))
	}
	return nil
}
