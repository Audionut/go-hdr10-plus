package extract

import (
	"errors"
	"math"
	"testing"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
)

func timedClip(t *testing.T, budget *MemoryBudget) (*Clip, StreamKey) {
	t.Helper()
	key := StreamKey{SourceID: "authored", ClipID: "shared", TrackID: 4113}
	s, err := NewStream(StreamOptions{Key: key, RetainBoundarySyntax: true, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	data := rawFixture(t, "reordered")
	var markers []Marker
	for i := 0; i < len(data)-5; i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			kind := (data[i+3] >> 1) & 63
			if kind <= 31 {
				value := int64(0)
				if len(markers) == 1 {
					value = 6000
				}
				if len(markers) == 2 {
					value = 3000
				}
				markers = append(markers, Marker{ESOffset: uint64(i + 3), Kind: PESStart, Modulo33: true, PTS: Timestamp{Value: value, Timescale: 90000, Valid: true}})
			}
		}
	}
	if err := s.Push(t.Context(), Chunk{Data: data, Markers: markers}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Finish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return c, key
}

func TestAssemblyTrimsRepeatsAndScenes(t *testing.T) {
	c, key := timedClip(t, nil)
	defer c.memory.release()
	p := Playlist{ID: "two uses", Occurrences: []Occurrence{
		{Key: key, In: 1500, Out: 4500, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionReset},
		{Key: key, In: 0, Out: 4500, PlaylistStart: 3000, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionReset},
	}}
	e, err := Assemble(t.Context(), p, map[StreamKey]*Clip{key: c}, AssemblyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantAU := []uint64{2, 1, 0, 2, 1}
	wantTicks := []int64{0, 3000, 6000, 9000, 12000}
	if len(e.Frames) != len(wantAU) {
		t.Fatalf("frames %d", len(e.Frames))
	}
	for i, f := range e.Frames {
		if f.AUOrdinal != wantAU[i] || compareTime(f.PTS, Timestamp{Value: wantTicks[i], Timescale: 90000, Valid: true}) != 0 {
			t.Fatalf("frame %d: %#v", i, f)
		}
		occ := uint64(0)
		if i >= 2 {
			occ = 1
		}
		if f.OccurrenceOrdinal != occ {
			t.Fatal("repeated occurrence identity lost")
		}
	}
	if m, err := e.PlotMetadata(); err != nil || m.SceneCount != 4 || m.Profile != "N/A" {
		t.Fatalf("projection %v %v", m, err)
	}
}

func TestAssemblyFailureAndBudgetRetention(t *testing.T) {
	b, err := NewMemoryBudget(3 << 20)
	if err != nil {
		t.Fatal(err)
	}
	c, key := timedClip(t, b)
	if b.used == 0 {
		t.Fatal("Finish released retained clip")
	}
	if c.memory.used == 0 {
		t.Fatal("clip has no retained charge")
	}
	p := Playlist{Occurrences: []Occurrence{{Key: key, In: 0, Out: 4500, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionReset}}}
	before := b.used
	e, err := Assemble(t.Context(), p, map[StreamKey]*Clip{key: c}, AssemblyOptions{Budget: b})
	if err != nil {
		t.Fatal(err)
	}
	if b.used <= before {
		t.Fatal("output charge not retained")
	}
	e.memory.release()
	if b.used != before {
		t.Fatal("assembly scratch leaked")
	}
	for _, mutate := range []func(*Occurrence){func(o *Occurrence) { o.Connection = ConnectionUnknown }, func(o *Occurrence) { o.ClockOffset.Valid = false }, func(o *Occurrence) { o.Out = o.In }} {
		copy := p
		copy.Occurrences = append([]Occurrence(nil), p.Occurrences...)
		mutate(&copy.Occurrences[0])
		if e, err := Assemble(t.Context(), copy, map[StreamKey]*Clip{key: c}, AssemblyOptions{}); e != nil || !errors.Is(err, ErrUnsupportedInput) {
			t.Fatalf("invalid timeline %v %v", e, err)
		}
	}
	if e, err := Assemble(t.Context(), p, nil, AssemblyOptions{}); e != nil || !errors.Is(err, ErrIncomplete) {
		t.Fatal("missing clip accepted")
	}
	c.memory.release()
	if b.used != 0 {
		t.Fatalf("budget leaked %d", b.used)
	}
	var zero MemoryBudget
	if _, err := NewStream(StreamOptions{Budget: &zero}); !errors.Is(err, hdr10plus.ErrInvalidOptions) {
		t.Fatal("invalid shared budget")
	}
}

func TestAssemblyResolutionQuota(t *testing.T) {
	b, err := NewMemoryBudget(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	c, key := timedClip(t, b)
	p := Playlist{Occurrences: []Occurrence{{Key: key, In: 0, Out: 4500, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionReset}}}
	before := b.used
	if e, err := Assemble(t.Context(), p, map[StreamKey]*Clip{key: c}, AssemblyOptions{Budget: b}); e != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("resolution must reserve before allocation: %v %v", e, err)
	}
	if b.used != before {
		t.Fatal("failed resolution reservation leaked or released retained input")
	}
}

func TestExactTimestampRange(t *testing.T) {
	got, err := rationalTime(Timestamp{Value: 1, Timescale: 4294967311, Valid: true}, Timestamp{Value: 1, Timescale: 3, Valid: true})
	if err != nil || got.Timescale != 12884901933 || got.Value != 4294967314 {
		t.Fatalf("wide denominator %v %v", got, err)
	}
	for _, parts := range [][]Timestamp{
		{{Value: math.MaxInt64, Timescale: 1, Valid: true}, {Value: 1, Timescale: 1, Valid: true}},
		{{Value: 1, Timescale: math.MaxUint64, Valid: true}, {Value: 1, Timescale: math.MaxUint64 - 1, Valid: true}},
	} {
		if _, err := rationalTime(parts...); !errors.Is(err, ErrUnsupportedInput) {
			t.Fatalf("overflow accepted %v", err)
		}
	}
}
