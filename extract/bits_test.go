package extract

import (
	"errors"
	"io"
	"testing"
)

func TestBitReaderBoundaries(t *testing.T) {
	b := bitReader{data: []byte{0xa5, 0x7e, 0xff}}
	if b.read(0) != 0 || b.read(3) != 5 || b.read(7) != 21 || b.read(6) != 62 || b.read(8) != 255 || b.err != nil {
		t.Fatalf("reader %#v", b)
	}
	pos := b.pos
	if b.read(1) != 0 || !errors.Is(b.err, io.ErrUnexpectedEOF) || b.pos != pos {
		t.Fatal("out of bounds read")
	}
	if b.read(1) != 0 || b.pos != pos {
		t.Fatal("error not sticky")
	}
	for width := range 65 {
		data := make([]byte, 8)
		for i := range data {
			data[i] = 255
		}
		b := bitReader{data: data}
		want := uint64(0)
		if width > 0 {
			want = ^uint64(0) >> (64 - width)
		}
		if got := b.read(uint(width)); got != want || b.err != nil {
			t.Fatalf("width %d: %x %v", width, got, b.err)
		}
	}
	b = bitReader{data: make([]byte, 16)}
	b.read(65)
	if b.err == nil || b.pos != 0 {
		t.Fatal("invalid width accepted")
	}
}

func TestExpGolombBounds(t *testing.T) {
	// 1, 010, 011, 00100, 00101 => 0, 1, 2, 3, 4.
	b := bitReader{data: []byte{0xa6, 0x42, 0x80}}
	for _, want := range []uint64{0, 1, 2, 3, 4} {
		if got := b.ue(); got != want || b.err != nil {
			t.Fatalf("got %d %v, want %d", got, b.err, want)
		}
	}
	b = bitReader{data: []byte{0xa6, 0x42, 0x80}}
	for _, want := range []int64{0, 1, -1, 2, -2} {
		if got := b.se(); got != want || b.err != nil {
			t.Fatalf("got %d %v, want %d", got, b.err, want)
		}
	}
	for size := range 5 {
		b := bitReader{data: make([]byte, size)}
		b.ue()
		if b.err == nil || b.pos > 32 {
			t.Fatalf("zero scan %#v", b)
		}
	}
	// The largest accepted unsigned code has 31 zero bits and a 31-bit suffix.
	b = bitReader{data: []byte{0, 0, 0, 1, 0xff, 0xff, 0xff, 0xfe}}
	if b.ue() != 0xfffffffe || b.err != nil {
		t.Fatalf("maximum code: %#v", b)
	}
}

func FuzzBitReader(f *testing.F) {
	f.Add([]byte{0xa6, 0x42, 0x80}, uint8(17))
	f.Add([]byte{0, 0, 0, 0}, uint8(64))
	f.Fuzz(func(t *testing.T, data []byte, width uint8) {
		if len(data) > 1024 {
			t.Skip()
		}
		b := bitReader{data: data}
		b.read(uint(width))
		b.ue()
		b.se()
		if b.pos > len(data)*8 {
			t.Fatal("read beyond input")
		}
	})
}
