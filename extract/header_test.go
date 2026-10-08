package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"
)

func authoredNAL(kind byte, rbsp []byte) []byte {
	out := []byte{0, 0, 1, kind << 1, 1}
	zeros := 0
	for _, v := range rbsp {
		if zeros == 2 && v <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, v)
		if v == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}
func packedHeader(bits string) []byte {
	bits += strings.Repeat("0", (-len(bits))&7)
	out := make([]byte, len(bits)/8)
	for i, v := range bits {
		out[i/8] = out[i/8]<<1 | byte(v-'0')
	}
	return out
}
func headerFixtureParts(t *testing.T) (sets, metadata, picture []byte) {
	t.Helper()
	raw := rawFixture(t, "single")
	sei := bytes.Index(raw, []byte{0, 0, 1, 78, 1})
	vcl := bytes.Index(raw, []byte{0, 0, 1, 38, 1})
	if sei < 0 || vcl < 0 {
		t.Fatal("authored fixture boundaries")
	}
	return raw[:sei], raw[sei:vcl], raw[vcl:]
}

func TestRequiredSliceHeaderAndLimit(t *testing.T) {
	raw := rawFixture(t, "single")
	if _, err := Extract(t.Context(), bytes.NewReader(raw), Options{Limits: Limits{MaxSliceHeaderBytes: 2}}); !errors.Is(err, ErrResourceLimit) || errors.Is(err, ErrIncomplete) {
		t.Fatalf("header limit: %v", err)
	}
	// A legal PPS with six extra reserved flags makes POC end exactly on byte24.
	// No bit for short_term_ref_pic_set_sps_flag remains in the final NAL.
	sets, metadata, _ := headerFixtureParts(t)
	start := bytes.Index(sets, []byte{0, 0, 1, 68, 1})
	pps, err := unescape(sets[start+5:], true, false)
	if err != nil {
		t.Fatal(err)
	}
	pps = changeBits(pps, 12, 3, 6)
	source := append(slices.Clone(sets[:start]), authoredNAL(34, pps)...)
	source = append(source, metadata...)
	source = append(source, authoredNAL(19, append(packedHeader("11"+"00110"+"000000"+"011"+"1"+"1"+"1"), 0xff))...)
	source = append(source, authoredNAL(1, []byte{0x98, 0x07, 0x01})...)
	if e, err := Extract(t.Context(), bytes.NewReader(source), Options{}); e != nil || !errors.Is(err, ErrIncomplete) {
		t.Fatalf("detectable required-header cut: %v %v", e, err)
	}
}

func TestControlNALSyntax(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tail  []byte
		valid bool
	}{
		{"empty-AUD", authoredNAL(35, nil), false},
		{"reserved-AUD", authoredNAL(35, []byte{0x70}), false},
		{"valid-AUD", authoredNAL(35, []byte{0x10}), true},
		{"empty-EOS", authoredNAL(36, nil), true},
		{"empty-EOB", authoredNAL(37, nil), true},
		{"unexpected-EOS-body", authoredNAL(36, []byte{0x80}), false},
		{"unexpected-EOB-body", authoredNAL(37, []byte{0x80}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := append(rawFixture(t, "single"), tc.tail...)
			e, err := Extract(t.Context(), bytes.NewReader(raw), Options{})
			if tc.valid {
				if err != nil || len(e.Frames) != 1 {
					t.Fatalf("valid boundary: %v", err)
				}
				e.memory.release()
			} else if e != nil || !errors.Is(err, ErrInvalidBitstream) {
				t.Fatalf("invalid boundary: %v %v", e, err)
			}
		})
	}
}

func TestCABACZeroWordBoundaries(t *testing.T) {
	raw := rawFixture(t, "single")
	for _, tail := range [][]byte{{0, 0, 3}, {0, 0, 3, 0}, {0, 0, 3, 0, 0, 0, 1, 70, 1, 0x10}, {0, 0, 3, 0, 0, 0, 0, 1, 70, 1, 0x10}} {
		source := append(slices.Clone(raw), tail...)
		for split := range len(source) + 1 {
			s, err := NewStream(StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Push(t.Context(), Chunk{Data: source[:split]}); err == nil {
				err = s.Push(t.Context(), Chunk{Data: source[split:], ESOffset: uint64(split)})
			}
			if err != nil {
				t.Fatalf("split%d tail%x: %v", split, tail, err)
			}
			c, err := s.Finish(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			c.memory.release()
		}
	}
	for size := 1; size <= 4; size++ {
		var encoded []byte
		for pos := 0; pos < len(raw); {
			next := bytes.Index(raw[pos+3:], []byte{0, 0, 1})
			end := len(raw)
			if next >= 0 {
				end = pos + 3 + next
			}
			nal := slices.Clone(raw[pos+3 : end])
			if nal[0]>>1&63 <= 31 {
				nal = append(nal, 0, 0, 3)
			}
			var length [4]byte
			binary.BigEndian.PutUint32(length[:], uint32(len(nal)))
			encoded = append(encoded, length[4-size:]...)
			encoded = append(encoded, nal...)
			pos = end
		}
		s, err := NewStream(StreamOptions{Framing: LengthPrefixed, LengthSize: size})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Push(t.Context(), Chunk{Data: encoded}); err != nil {
			t.Fatalf("length-size%d: %v", size, err)
		}
		c, err := s.Finish(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		c.memory.release()
	}
	if _, err := Extract(t.Context(), bytes.NewReader(append(slices.Clone(raw), 0, 0, 3, 4)), Options{}); !errors.Is(err, ErrInvalidBitstream) {
		t.Fatalf("internal invalid escape: %v", err)
	}
	// An entropy tail cut after a complete required header remains structurally
	// indistinguishable from EOF; this documents the advertised boundary.
	if _, err := Extract(t.Context(), bytes.NewReader(raw[:len(raw)-1]), Options{}); err != nil {
		t.Fatalf("unobservable entropy cut: %v", err)
	}
}

func TestSEIRetainedStorageAndInterning(t *testing.T) {
	p := fixture(t, "profile-a")
	size := 256 << 10
	rbsp := []byte{5}
	for n := size; n >= 255; n -= 255 {
		rbsp = append(rbsp, 255)
	}
	rbsp = append(rbsp, byte(size%255))
	rbsp = append(rbsp, bytes.Repeat([]byte{0xff}, size)...)
	rbsp = append(rbsp, 4, byte(len(p)))
	rbsp = append(rbsp, p...)
	rbsp = append(rbsp, 0x80)
	data := authoredNAL(39, rbsp)
	s, err := NewStream(StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := s.Push(t.Context(), Chunk{Data: data, ESOffset: uint64(i * len(data))}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := s.Finish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer c.memory.release()
	if c.memory.used > 64<<10 {
		t.Fatalf("discarded SEI bytes stayed charged: %d", c.memory.used)
	}
	if c.events[0].update != c.events[1].update || c.events[1].update != c.events[2].update {
		t.Fatal("equivalent updates retained repeatedly")
	}
	e, err := Extract(t.Context(), bytes.NewReader(rawFixture(t, "reordered")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.memory.release()
	got := []uint32{e.Frames[0].PayloadIndex, e.Frames[1].PayloadIndex, e.Frames[2].PayloadIndex}
	if !slices.Equal(got, []uint32{0, 1, 0}) || len(e.Payloads) != 2 {
		t.Fatalf("exact inherited indexes %v payloads%d", got, len(e.Payloads))
	}
}

func TestMetadataPresenceOnNonOutputPicture(t *testing.T) {
	sets, metadata, picture := headerFixtureParts(t)
	source := append(slices.Clone(sets), picture...)
	source = append(source, metadata...)
	// Non-IDR I picture, POC1, no short-term refs, output false, QP0, alignment.
	source = append(source, authoredNAL(1, append(packedHeader("1"+"00110"+"011"+"0"+"00000001"+"0"+"1"+"1"+"1"+"1"), 0xff))...)
	if e, err := Extract(t.Context(), bytes.NewReader(source), Options{}); e != nil || !errors.Is(err, ErrIncomplete) || errors.Is(err, ErrNoMetadata) {
		t.Fatalf("presence/coverage: %v %v", e, err)
	}
}

func TestAutoKnownContainerSignatures(t *testing.T) {
	for _, data := range [][]byte{{0, 0, 0, 8, 'f', 't', 'y', 'p'}, {0x47, 0x40, 0, 0x10}, {0, 0, 0, 0, 0x47, 0x40, 0, 0x10}} {
		if _, err := Extract(t.Context(), bytes.NewReader(data), Options{}); !errors.Is(err, ErrUnsupportedInput) && !errors.Is(err, ErrInvalidBitstream) {
			t.Fatalf("signature %x: %v", data, err)
		}
		if _, err := Extract(t.Context(), bytes.NewReader(data), Options{Format: RawHEVC}); !errors.Is(err, ErrInvalidBitstream) {
			t.Fatalf("explicit raw %x: %v", data, err)
		}
	}
}
