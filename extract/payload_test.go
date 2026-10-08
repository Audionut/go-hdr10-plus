package extract

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".t35")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func expectedPayload(name string) Payload {
	p := Payload{ApplicationVersion: 1, NumWindows: 1, MaxSCL: [3]uint32{0, 100000, 45678}, AverageRGB: 12345, FractionBrightPixels: 1023}
	indexes := indexes9
	values := []uint32{0, 100, 200, 300, 400, 500, 600, 700, 99999}
	if name == "profile-b" {
		indexes = indexes10
		values = []uint32{0, 100, 200, 300, 400, 500, 600, 700, 800, 99999}
	}
	for i, v := range values {
		p.Distributions = append(p.Distributions, Distribution{indexes[i], v})
	}
	if name != "profile-a" {
		p.TargetMaximumLuminance = 10000
		p.ToneMapping = true
		p.Curve = &Curve{KneeX: 4095, Anchors: []uint16{}}
		if name == "profile-b" {
			p.Curve.Anchors = []uint16{0, 512, 1023}
		}
	}
	return p
}

func TestDecodeT35(t *testing.T) {
	for name, profile := range map[string]string{"profile-a": "A", "profile-b": "B", "profile-na": "N/A"} {
		t.Run(name, func(t *testing.T) {
			data := fixture(t, name)
			got, err := DecodeT35(data)
			if err != nil {
				t.Fatal(err)
			}
			want := expectedPayload(name)
			if !reflect.DeepEqual(*got, want) || got.profile() != profile {
				t.Fatalf("got %#v (%s), want %#v (%s)", got, got.profile(), want, profile)
			}
			clear(data)
			if !reflect.DeepEqual(*got, want) {
				t.Fatal("borrowed input retained")
			}
		})
	}
}

func changeBits(data []byte, start, width int, value uint64) []byte {
	b := slices.Clone(data)
	for i := range width {
		pos := start + i
		mask := byte(1 << (7 - pos%8))
		b[pos/8] &^= mask
		if value&(1<<uint(width-1-i)) != 0 {
			b[pos/8] |= mask
		}
	}
	return b
}

func TestT35Failures(t *testing.T) {
	a := fixture(t, "profile-a")
	b := fixture(t, "profile-b")
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"identity", changeBits(a, 0, 8, 0), ErrUnsupportedMetadata},
		{"version-zero", changeBits(a, 48, 8, 0), ErrUnsupportedMetadata},
		{"version-unknown", changeBits(a, 48, 8, 7), ErrUnsupportedMetadata},
		{"zero-windows", changeBits(a, 56, 2, 0), hdr10plus.ErrInvalidMetadata},
		{"multiwindow", changeBits(a, 56, 2, 2), ErrUnsupportedMetadata},
		{"target-grid", changeBits(a, 85, 1, 1), ErrUnsupportedMetadata},
		{"target-range", changeBits(a, 58, 27, 10001), hdr10plus.ErrInvalidMetadata},
		{"max-scl-range", changeBits(a, 86, 17, 100001), hdr10plus.ErrInvalidMetadata},
		{"average-range", changeBits(a, 137, 17, 100001), hdr10plus.ErrInvalidMetadata},
		{"count", changeBits(a, 154, 4, 8), hdr10plus.ErrInvalidMetadata},
		{"wrong-index", changeBits(a, 158, 7, 2), hdr10plus.ErrInvalidMetadata},
		{"value-range", changeBits(a, 165, 17, 100001), hdr10plus.ErrInvalidMetadata},
		{"mastering-grid", changeBits(a, 384, 1, 1), ErrUnsupportedMetadata},
		{"saturation", changeBits(a, 386, 1, 1), ErrUnsupportedMetadata},
		{"anchor-count", changeBits(b, 434, 4, 10), hdr10plus.ErrInvalidMetadata},
		{"padding", changeBits(a, 387, 1, 1), hdr10plus.ErrInvalidMetadata},
		{"extra-byte", append(slices.Clone(a), 0), hdr10plus.ErrInvalidMetadata},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeT35(tc.data)
			if got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
			var detail *Error
			if !errors.As(err, &detail) || detail.Field == "" {
				t.Fatal("missing field error")
			}
		})
	}
	for _, data := range [][]byte{a, b, fixture(t, "profile-na")} {
		for end := range len(data) {
			if p, err := DecodeT35(data[:end]); p != nil || !errors.Is(err, hdr10plus.ErrInvalidMetadata) {
				t.Fatalf("prefix %d: %v, %v", end, p, err)
			}
		}
	}
}

func TestProjectionScenesAndOwnership(t *testing.T) {
	a, b, n := expectedPayload("profile-a"), expectedPayload("profile-b"), expectedPayload("profile-na")
	changed := clonePayload(b)
	changed.Curve.Anchors[0]++
	fraction := clonePayload(changed)
	fraction.FractionBrightPixels--
	e := &Extraction{Payloads: []Payload{a, b, changed, fraction, n}, Frames: []Picture{{PayloadIndex: 0}, {PayloadIndex: 0}, {PayloadIndex: 1}, {PayloadIndex: 2}, {PayloadIndex: 3}, {PayloadIndex: 4}}}
	e.Profile, e.SceneStarts = e.captions()
	if e.Profile != "N/A" || !slices.Equal(e.SceneStarts, []uint64{0, 2, 3, 5}) {
		t.Fatalf("captions %s %v", e.Profile, e.SceneStarts)
	}
	m, err := e.PlotMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if m.SceneCount != 4 || m.Profile != "N/A" || m.Frames[2].AverageRGB != 12345 || !slices.Equal(m.Frames[2].DistributionValues, []uint32{0, 100, 200, 300, 400, 500, 600, 700, 800, 99999}) || !slices.Equal(m.Frames[2].MaxSCL, []uint32{0, 100000, 45678}) {
		t.Fatalf("bad projection: %#v", m)
	}
	m.Frames[0].DistributionValues[0] = 999
	m.Frames[0].MaxSCL[0] = 999
	if e.Payloads[0].Distributions[0].Value != 0 || e.Payloads[0].MaxSCL[0] != 0 || m.Frames[1].DistributionValues[0] != 0 {
		t.Fatal("projection aliases")
	}
	for _, mutate := range []func(*Extraction){
		func(e *Extraction) { e.Frames[0].PayloadIndex = 99 },
		func(e *Extraction) { e.SceneStarts = []uint64{1} },
		func(e *Extraction) { e.Profile = "A" },
		func(e *Extraction) { e.Payloads[0].ToneMapping = true },
		func(e *Extraction) { e.Frames[0].PTS = Timestamp{Valid: true} },
	} {
		copy := &Extraction{Payloads: slices.Clone(e.Payloads), Frames: slices.Clone(e.Frames), SceneStarts: slices.Clone(e.SceneStarts), Profile: e.Profile}
		mutate(copy)
		if m, err := copy.PlotMetadata(); m != nil || !errors.Is(err, hdr10plus.ErrInvalidMetadata) {
			t.Fatalf("mutation accepted: %v %v", m, err)
		}
	}
}

func TestNonmonotonicDistributionValues(t *testing.T) {
	p := expectedPayload("profile-a")
	p.Distributions[1].Value = 99999
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	if payloadEqual(p, expectedPayload("profile-a")) {
		t.Fatal("different payload interned")
	}
}

func FuzzDecodeT35(f *testing.F) {
	for _, name := range []string{"profile-a", "profile-b", "profile-na"} {
		f.Add(fixture(f, name))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1024 {
			t.Skip()
		}
		p, err := DecodeT35(b)
		if err == nil {
			if p == nil || p.validate() != nil {
				t.Fatal("invalid successful payload")
			}
		} else if p != nil {
			t.Fatal("partial payload")
		}
	})
}
