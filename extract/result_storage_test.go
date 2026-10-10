package extract

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"unsafe"
)

func TestExtractionStorageScalesWithOwnedOutput(t *testing.T) {
	l, err := (Limits{MaxRetainedBytes: 2 << 20, MaxContainerIndexBytes: 1 << 20, MaxBlockBytes: 1 << 20}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewMemoryBudget(l.MaxRetainedBytes)
	if err != nil {
		t.Fatal(err)
	}
	parent := &accounting{max: l.MaxRetainedBytes, shared: b}
	defer parent.release()
	if _, err := parent.reserve(256 << 10); err != nil {
		t.Fatal(err)
	}
	before, _ := b.Usage()
	payload := expectedPayload("profile-b")
	pictures := make([]resolvedPicture, 8192)
	for i := range pictures {
		pictures[i] = resolvedPicture{picture: Picture{AUOrdinal: uint64(i)}, payload: &payload}
	}
	e, err := makeExtraction(t.Context(), pictures, l, b, true, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Frames) != len(pictures) || cap(e.Frames) != len(pictures) || len(e.Payloads) != 1 || cap(e.Payloads) != 1 || !slices.Equal(e.SceneStarts, []uint64{0}) || cap(e.SceneStarts) != 1 || e.Profile != "B" {
		t.Fatalf("output lengths/capacities: frames=%d/%d payloads=%d/%d scenes=%v/%d profile=%s", len(e.Frames), cap(e.Frames), len(e.Payloads), cap(e.Payloads), e.SceneStarts, cap(e.SceneStarts), e.Profile)
	}
	used, peak := b.Usage()
	if used != before+e.memory.used || parent.used != used || peak <= used || peak > l.MaxRetainedBytes {
		t.Fatalf("scratch/result accounting: before=%d used=%d peak=%d parent=%d result=%d", before, used, peak, parent.used, e.memory.used)
	}
	if _, err := e.PlotMetadata(); err != nil {
		t.Fatal(err)
	}
	e.memory.release()
	if used, _ := b.Usage(); used != before || parent.used != before {
		t.Fatalf("result or index charge leaked: used=%d parent=%d want=%d", used, parent.used, before)
	}
}

func TestExtractionDistinctPayloadsScenesAndCopies(t *testing.T) {
	l, _ := (Limits{}).normalized()
	a, b, equal := expectedPayload("profile-a"), expectedPayload("profile-b"), expectedPayload("profile-b")
	changed := clonePayload(b)
	changed.Curve.Anchors[0]++
	fraction := clonePayload(changed)
	fraction.FractionBrightPixels--
	payloads := []*Payload{&a, &a, &b, &equal, &changed, &fraction, &a}
	pictures := make([]resolvedPicture, len(payloads))
	for i, p := range payloads {
		pictures[i] = resolvedPicture{picture: Picture{AUOrdinal: uint64(i), POC: int64(i)}, payload: p}
	}
	e, err := makeExtraction(t.Context(), pictures, l, nil, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.memory.release()
	wantIndexes := []uint32{0, 0, 1, 2, 3, 4, 0}
	for i, f := range e.Frames {
		if f.PayloadIndex != wantIndexes[i] || f.AUOrdinal != uint64(i) || f.POC != int64(i) {
			t.Fatalf("frame %d: %+v", i, f)
		}
	}
	if len(e.Payloads) != 5 || cap(e.Payloads) != 5 || !slices.Equal(e.SceneStarts, []uint64{0, 2, 4, 6}) || cap(e.SceneStarts) != 4 || e.Profile != "N/A" {
		t.Fatalf("payloads=%d/%d scenes=%v/%d profile=%s", len(e.Payloads), cap(e.Payloads), e.SceneStarts, cap(e.SceneStarts), e.Profile)
	}
	want := &Extraction{Payloads: []Payload{clonePayload(a), clonePayload(b), clonePayload(equal), clonePayload(changed), clonePayload(fraction)}, Frames: slices.Clone(e.Frames), SceneStarts: slices.Clone(e.SceneStarts), Profile: e.Profile}
	if !reflect.DeepEqual(e.Payloads, want.Payloads) {
		t.Fatal("payload fields changed")
	}
	a.Distributions[0].Value++
	b.Curve.Anchors[0]++
	e.Payloads[2].Distributions[0].Value++
	e.Payloads[2].Curve.Anchors[0]++
	if !reflect.DeepEqual(e.Payloads[0], want.Payloads[0]) || !reflect.DeepEqual(e.Payloads[1], want.Payloads[1]) || equal.Distributions[0].Value != want.Payloads[2].Distributions[0].Value || equal.Curve.Anchors[0] != want.Payloads[2].Curve.Anchors[0] {
		t.Fatal("result aliases collection or a distinct payload")
	}
}

func TestExtractionStorageFailureReleasesOnlyOwnedScratch(t *testing.T) {
	l, _ := (Limits{}).normalized()
	p := expectedPayload("profile-b")
	pictures := make([]resolvedPicture, 64)
	for i := range pictures {
		pictures[i].payload = &p
	}
	for _, tc := range []struct {
		name string
		room int64
		ctx  context.Context
		want error
	}{
		{"index-limit", 1, t.Context(), ErrResourceLimit},
		{"output-limit", 5000, t.Context(), ErrResourceLimit},
		{"cancelled", 1 << 20, func() context.Context { ctx, cancel := context.WithCancel(t.Context()); cancel(); return ctx }(), context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := NewMemoryBudget(4096 + tc.room)
			parent := &accounting{max: l.MaxRetainedBytes, shared: b}
			defer parent.release()
			if _, err := parent.reserve(3968); err != nil {
				t.Fatal(err)
			}
			before, _ := b.Usage()
			if e, err := makeExtraction(tc.ctx, pictures, l, b, true, parent); e != nil || !errors.Is(err, tc.want) {
				t.Fatalf("output=%v error=%v want=%v", e, err, tc.want)
			}
			if used, _ := b.Usage(); used != before || parent.used != before {
				t.Fatalf("failed construction changed parent: used=%d parent=%d want=%d", used, parent.used, before)
			}
		})
	}
	if e, err := makeExtraction(t.Context(), pictures, Limits{MaxFrames: 63, MaxRetainedBytes: l.MaxRetainedBytes}, nil, true, nil); e != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("frame limit: output=%v error=%v", e, err)
	}
}

func TestExtractionStorageBoundsCoverPayloadLayout(t *testing.T) {
	// The production bounds cover 64-bit layouts and cloned slice capacities.
	if unsafe.Sizeof(Picture{}) > 128 {
		t.Fatalf("picture exceeds reserved bound: %d", unsafe.Sizeof(Picture{}))
	}
	for _, name := range []string{"profile-a", "profile-b", "profile-na"} {
		p := clonePayload(expectedPayload(name))
		// Exercise the maximum supported array lengths as well as fixture data.
		p.Distributions = slices.Clone(make([]Distribution, 10))
		p.Curve = &Curve{Anchors: slices.Clone(make([]uint16, 9))}
		bytes := unsafe.Sizeof(p) + uintptr(cap(p.Distributions))*unsafe.Sizeof(Distribution{})
		if p.Curve != nil {
			bytes += unsafe.Sizeof(*p.Curve) + uintptr(cap(p.Curve.Anchors))*unsafe.Sizeof(uint16(0))
		}
		if bytes > 256 {
			t.Fatalf("%s payload exceeds reserved bound: %d", name, bytes)
		}
	}
}

func TestExtractionDistinctOutputStillHonorsStorageLimit(t *testing.T) {
	l, err := (Limits{MaxRetainedBytes: 2 << 20, MaxContainerIndexBytes: 1 << 20, MaxBlockBytes: 1 << 20}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewMemoryBudget(l.MaxRetainedBytes)
	parent := &accounting{max: l.MaxRetainedBytes, shared: b}
	defer parent.release()
	pictures := make([]resolvedPicture, 8192)
	for i := range pictures {
		p := expectedPayload("profile-b")
		p.AverageRGB = uint32(i)
		pictures[i].payload = &p
	}
	if e, err := makeExtraction(t.Context(), pictures, l, b, true, parent); e != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("distinct output exceeded storage limit: output=%v error=%v", e, err)
	}
	if used, _ := b.Usage(); used != 0 || parent.used != 0 {
		t.Fatalf("oversized output leaked scratch: used=%d parent=%d", used, parent.used)
	}
}

func TestAssemblyOutputCopiesRemainIndependent(t *testing.T) {
	b, _ := NewMemoryBudget(4 << 20)
	c, key := timedClip(t, b)
	defer c.memory.release()
	p := Playlist{Occurrences: []Occurrence{{Key: key, In: 0, Out: 4500, ClockOffset: Timestamp{Timescale: 1, Valid: true}, Connection: ConnectionReset}}}
	clips := map[StreamKey]*Clip{key: c}
	first, err := Assemble(t.Context(), p, clips, AssemblyOptions{Budget: b})
	if err != nil {
		t.Fatal(err)
	}
	defer first.memory.release()
	want, err := first.PlotMetadata()
	if err != nil {
		t.Fatal(err)
	}
	first.Payloads[0].Distributions[0].Value++
	first.Frames[0].PayloadIndex = 99
	first.SceneStarts[0] = 99
	second, err := Assemble(t.Context(), p, clips, AssemblyOptions{Budget: b})
	if err != nil {
		t.Fatal(err)
	}
	defer second.memory.release()
	got, err := second.PlotMetadata()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("mutated output affected retained clip/replay: model=%v error=%v", got, err)
	}
}
