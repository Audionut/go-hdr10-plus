package extract

import (
	"errors"
	"math"
	"sync"
	"testing"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
)

func TestMemoryBudget(t *testing.T) {
	for _, size := range []int64{0, -1} {
		if b, err := NewMemoryBudget(size); b != nil || !errors.Is(err, hdr10plus.ErrInvalidOptions) {
			t.Fatalf("size %d: %v %v", size, b, err)
		}
	}
	var zero MemoryBudget
	if r, err := zero.Reserve(0); r != nil || !errors.Is(err, hdr10plus.ErrInvalidOptions) {
		t.Fatal("zero budget accepted")
	}
	b, err := NewMemoryBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := b.Reserve(-1); r != nil || !errors.Is(err, hdr10plus.ErrInvalidOptions) {
		t.Fatal("negative reservation accepted")
	}
	a, err := b.Reserve(60)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := b.Reserve(41); r != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatal("over quota")
	}
	c, err := b.Reserve(40)
	if err != nil {
		t.Fatal(err)
	}
	alias := *a
	a.Release()
	alias.Release()
	a.Release()
	if b.used != 40 {
		t.Fatalf("usage %d", b.used)
	}
	a, err = b.Reserve(60)
	if err != nil {
		t.Fatal(err)
	}
	a.Release()
	c.Release()
	if b.used != 0 {
		t.Fatalf("usage %d", b.used)
	}
	var empty Reservation
	empty.Release()
	var absent *Reservation
	absent.Release()
	b, err = NewMemoryBudget(math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	a, err = b.Reserve(math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := b.Reserve(1); r != nil || !errors.Is(err, ErrResourceLimit) {
		t.Fatal("overflow accepted")
	}
	a.Release()
}

func TestMemoryBudgetConcurrentAggregate(t *testing.T) {
	b, err := NewMemoryBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan *Reservation, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			<-start
			r, err := b.Reserve(10)
			if err != nil && !errors.Is(err, ErrResourceLimit) {
				t.Error(err)
			}
			results <- r
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var reservations []*Reservation
	for r := range results {
		if r != nil {
			reservations = append(reservations, r)
		}
	}
	if len(reservations) != 10 || b.used != 100 {
		t.Fatalf("reserved %d charges, %d bytes", len(reservations), b.used)
	}
	// Copy each token and concurrently release both aliases. The charge must
	// return once, even when Release races on an alias of a completed charge.
	for _, r := range reservations {
		alias := *r
		wg.Go(r.Release)
		wg.Go(alias.Release)
	}
	wg.Wait()
	if b.used != 0 {
		t.Fatalf("remaining %d", b.used)
	}
}

func TestLimits(t *testing.T) {
	l, err := (Limits{}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	if l.MaxFrames != 2000000 || l.MaxRetainedBytes != 512<<20 || l.MaxContainerIndexBytes != 128<<20 || l.MaxBlockBytes != 64<<20 || l.MaxParameterSetBytes != 64<<10 || l.MaxSliceHeaderBytes != 64<<10 || l.MaxSEIBytes != 1<<20 || l.MaxReorderPictures != 64 || l.MaxNestingDepth != 32 {
		t.Fatalf("defaults: %#v", l)
	}
	for _, bad := range []Limits{{MaxFrames: -1}, {MaxRetainedBytes: -1}, {MaxContainerIndexBytes: -1}, {MaxBlockBytes: -1}, {MaxParameterSetBytes: -1}, {MaxSliceHeaderBytes: -1}, {MaxSEIBytes: -1}, {MaxReorderPictures: -1}, {MaxNestingDepth: -1}, {MaxRetainedBytes: 1}, {MaxBlockBytes: 513 << 20}} {
		if _, err := bad.normalized(); !errors.Is(err, hdr10plus.ErrInvalidOptions) {
			t.Fatalf("accepted %#v: %v", bad, err)
		}
	}
}
