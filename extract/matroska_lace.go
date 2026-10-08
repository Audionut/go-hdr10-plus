package extract

// Check lace sizes before ebml-go converts an unsigned size to a native int or
// allocates a frame. The dependency remains the actual block/lacing decoder.
func preflightLace(data []byte, mode byte) error {
	if mode == 0 {
		return nil
	}
	if len(data) == 0 || data[0] == 0 {
		return fieldError("lace-count", ErrInvalidBitstream)
	}
	frames := int(data[0]) + 1
	pos := 1
	remaining := int64(len(data))
	total := int64(0)
	if mode == 2 {
		if (len(data)-1)%frames != 0 || len(data) <= frames {
			return ErrInvalidBitstream
		}
		return nil
	}
	previous := int64(0)
	for i := 0; i < frames-1; i++ {
		var size int64
		if mode == 1 {
			for {
				if pos == len(data) {
					return ErrInvalidBitstream
				}
				v := data[pos]
				pos++
				size += int64(v)
				if v != 255 {
					break
				}
			}
		} else {
			if pos == len(data) {
				return ErrInvalidBitstream
			}
			first := data[pos]
			marker := byte(0x80)
			width := 1
			for marker != 0 && first&marker == 0 {
				width++
				marker >>= 1
			}
			if marker == 0 || width > len(data)-pos {
				return ErrInvalidBitstream
			}
			v := uint64(first &^ marker)
			for _, b := range data[pos+1 : pos+width] {
				v = v<<8 | uint64(b)
			}
			pos += width
			if i == 0 {
				size = int64(v)
			} else {
				size = previous + int64(v) - int64((uint64(1)<<(7*width-1))-1)
			}
		}
		if size <= 0 || size > remaining-total {
			return fieldError("lace-size", ErrInvalidBitstream)
		}
		total += size
		previous = size
	}
	if total >= int64(len(data)-pos) {
		return fieldError("lace-extent", ErrInvalidBitstream)
	}
	return nil
}
