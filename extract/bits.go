package extract

import "io"

// bitReader does not allocate and never scans beyond its supplied extent.
type bitReader struct {
	data []byte
	pos  int
	err  error
}

func (b *bitReader) read(n uint) uint64 {
	if b.err != nil {
		return 0
	}
	if n > 64 || int(n) > len(b.data)*8-b.pos {
		b.err = io.ErrUnexpectedEOF
		return 0
	}
	var v uint64
	for range n {
		v = v<<1 | uint64((b.data[b.pos/8]>>(7-uint(b.pos%8)))&1)
		b.pos++
	}
	return v
}

func (b *bitReader) flag() bool { return b.read(1) != 0 }

func (b *bitReader) expect(n uint, value uint64) {
	if b.read(n) != value {
		b.invalid()
	}
}

func (b *bitReader) ue() uint64 {
	var zeros uint
	for b.err == nil && b.read(1) == 0 {
		zeros++
		if zeros > 31 {
			b.err = ErrInvalidBitstream
			return 0
		}
	}
	return (uint64(1) << zeros) - 1 + b.read(zeros)
}

func (b *bitReader) se() int64 {
	v := b.ue()
	if v&1 != 0 {
		return int64((v + 1) / 2)
	}
	return -int64(v / 2)
}
