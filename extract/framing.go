package extract

import (
	"fmt"
	hdr10plus "github.com/Audionut/go-hdr10-plus"
	"io"
)

// nalScanner retains only bounded syntax prefixes, never full VCL bodies.
// Annex B zeros are delayed so start codes may straddle arbitrary pushes.
type nalScanner struct {
	lengthSize                     int
	lengthRead                     int
	length, remaining              uint64
	zeros                          uint64
	ebspZeros                      int
	prevention                     bool
	last                           byte
	headerComplete                 bool
	started                        bool
	data                           []byte
	size                           uint64
	offset                         uint64
	nalOffset                      uint64
	maxParameter, maxSlice, maxSEI int64
	end                            func([]byte, uint64, uint64) error
	grow                           func(int) error
	prefix                         func([]byte) (bool, error)
}

func (n *nalScanner) limit() int64 {
	if len(n.data) < 2 {
		return 2
	}
	kind := (n.data[0] >> 1) & 63
	if kind <= 31 {
		return n.maxSlice
	}
	if kind == 39 || kind == 40 {
		return n.maxSEI
	}
	if kind >= 32 && kind <= 34 {
		return n.maxParameter
	}
	return n.maxParameter
}

func (n *nalScanner) byte(v byte) error {
	n.size++
	n.last = v
	if n.size > 2 {
		if n.prevention {
			if v > 3 {
				return fieldError("emulation-prevention", ErrInvalidBitstream)
			}
			n.prevention = false
		} else if n.ebspZeros == 2 {
			if v == 3 {
				n.prevention = true
				n.ebspZeros = 0
			} else if v < 3 {
				return fieldError("unescaped-RBSP", ErrInvalidBitstream)
			}
		}
		if v == 0 {
			n.ebspZeros++
		} else if v != 3 || !n.prevention {
			n.ebspZeros = 0
		}
	}
	limit := n.limit()
	if n.headerComplete && len(n.data) >= 2 && (n.data[0]>>1)&63 <= 31 {
		return nil
	}
	if int64(len(n.data)) >= limit {
		if len(n.data) >= 2 && (n.data[0]>>1)&63 <= 31 {
			return nil
		}
		return fieldError("NAL-syntax-bytes", ErrResourceLimit)
	}
	if len(n.data) == cap(n.data) {
		capacity := min(int(limit), max(2, 2*cap(n.data)))
		if err := n.grow(capacity); err != nil {
			return err
		}
	}
	n.data = append(n.data, v)
	if !n.headerComplete && len(n.data) >= 2 && (n.data[0]>>1)&63 <= 31 && (len(n.data)&(len(n.data)-1) == 0 || int64(len(n.data)) == limit) && n.prefix != nil {
		complete, err := n.prefix(n.data)
		if err != nil {
			return err
		}
		n.headerComplete = complete
	}
	return nil
}

func (n *nalScanner) finishNAL() error {
	vcl := len(n.data) >= 2 && (n.data[0]>>1)&63 <= 31
	if n.prevention && !vcl {
		return fieldError("emulation-prevention", ErrIncomplete)
	}
	if n.lengthSize != 0 && n.last == 0 {
		return fieldError("NAL-terminal-zero", ErrInvalidBitstream)
	}
	if n.size < 2 {
		return fieldError("NAL-header", fmt.Errorf("%w: %w: %w", ErrInvalidBitstream, ErrIncomplete, io.ErrUnexpectedEOF))
	}
	if n.data[0]&0x80 != 0 || n.data[1]&7 == 0 {
		return fieldError("NAL-header", ErrInvalidBitstream)
	}
	if err := n.end(n.data, n.nalOffset, n.size); err != nil {
		return err
	}
	n.data = n.data[:0]
	n.size = 0
	n.ebspZeros = 0
	n.prevention = false
	n.headerComplete = false
	return nil
}

func (n *nalScanner) push(data []byte) error {
	for _, v := range data {
		position := n.offset
		n.offset++
		if n.lengthSize != 0 {
			if n.remaining == 0 {
				n.length = n.length<<8 | uint64(v)
				n.lengthRead++
				if n.lengthRead == n.lengthSize {
					if n.length == 0 {
						return fieldError("zero-NAL-length", ErrInvalidBitstream)
					}
					n.remaining = n.length
					n.length = 0
					n.lengthRead = 0
					n.nalOffset = n.offset
				}
				continue
			}
			if err := n.byte(v); err != nil {
				return err
			}
			n.remaining--
			if n.remaining == 0 {
				if err := n.finishNAL(); err != nil {
					return err
				}
			}
			continue
		}
		if v == 0 {
			n.zeros++
			continue
		}
		if v == 1 && n.zeros >= 2 {
			if n.started {
				if err := n.finishNAL(); err != nil {
					return err
				}
			}
			n.started = true
			n.zeros = 0
			n.nalOffset = position + 1
			continue
		}
		if !n.started {
			return fieldError("Annex-B-start-code", ErrInvalidBitstream)
		}
		for n.zeros > 0 {
			if err := n.byte(0); err != nil {
				return err
			}
			n.zeros--
		}
		if err := n.byte(v); err != nil {
			return err
		}
	}
	return nil
}

func (n *nalScanner) finish() error {
	if n.lengthSize != 0 {
		if n.remaining != 0 || n.lengthRead != 0 {
			return fieldError("NAL-length", fmt.Errorf("%w: %w: %w", ErrInvalidBitstream, ErrIncomplete, io.ErrUnexpectedEOF))
		}
		return nil
	}
	if !n.started {
		return fieldError("Annex-B-start-code", ErrIncomplete)
	}
	return n.finishNAL()
}

func unescape(data []byte, complete, vcl bool) ([]byte, error) {
	out := make([]byte, 0, len(data))
	zeros := 0
	for i := 0; i < len(data); i++ {
		v := data[i]
		if zeros == 2 {
			if v == 3 {
				if i+1 == len(data) {
					if complete && !vcl {
						return nil, fieldError("emulation-prevention", ErrInvalidBitstream)
					}
					break
				}
				if data[i+1] > 3 {
					return nil, fieldError("emulation-prevention", ErrInvalidBitstream)
				}
				zeros = 0
				continue
			}
			if v < 3 {
				return nil, fieldError("unescaped-RBSP", ErrInvalidBitstream)
			}
		}
		out = append(out, v)
		if v == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out, nil
}

func decodeSEI(data []byte, suffix bool) (*Payload, error) {
	var update *Payload
	pos := 0
	for pos < len(data) {
		if data[pos] == 0x80 && pos == len(data)-1 {
			return update, nil
		}
		readExtended := func() (int, error) {
			sum := 0
			for pos < len(data) {
				v := int(data[pos])
				pos++
				if sum > int(^uint(0)>>1)-v {
					return 0, fieldError("SEI-extent", ErrInvalidBitstream)
				}
				sum += v
				if v != 255 {
					return sum, nil
				}
			}
			return 0, fieldError("SEI-header", fmt.Errorf("%w: %w", ErrInvalidBitstream, ErrIncomplete))
		}
		kind, err := readExtended()
		if err != nil {
			return nil, err
		}
		size, err := readExtended()
		if err != nil {
			return nil, err
		}
		if size > len(data)-pos {
			return nil, fieldError("SEI-payload", fmt.Errorf("%w: %w", ErrInvalidBitstream, ErrIncomplete))
		}
		payload := data[pos : pos+size]
		pos += size
		if kind != 4 || !matchesT35(payload) {
			continue
		}
		p, err := DecodeT35(payload)
		if err != nil {
			return nil, err
		}
		if suffix {
			return nil, fieldError("HDR10+-suffix-placement", ErrUnsupportedInput)
		}
		if update != nil && !payloadEqual(*update, *p) {
			return nil, fieldError("conflicting-AU-metadata", hdr10plus.ErrInvalidMetadata)
		}
		update = p
	}
	return nil, fieldError("SEI-trailing-bits", ErrInvalidBitstream)
}
