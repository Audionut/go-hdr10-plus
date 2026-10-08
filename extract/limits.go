package extract

import (
	"fmt"
	"sync"

	hdr10plus "github.com/Audionut/go-hdr10-plus"
)

// Limits bounds accounted owned storage, not process RSS, caller IO buffers or
// rendering. Zero fields select defaults; negative fields are invalid.
type Limits struct {
	MaxFrames              int64
	MaxRetainedBytes       int64
	MaxContainerIndexBytes int64
	MaxBlockBytes          int64
	MaxParameterSetBytes   int64
	MaxSliceHeaderBytes    int64
	MaxSEIBytes            int64
	MaxReorderPictures     int64
	MaxNestingDepth        int64
}

func (l Limits) normalized() (Limits, error) {
	defaults := []int64{2000000, 512 << 20, 128 << 20, 64 << 20, 64 << 10, 64 << 10, 1 << 20, 64, 32}
	fields := []*int64{&l.MaxFrames, &l.MaxRetainedBytes, &l.MaxContainerIndexBytes, &l.MaxBlockBytes, &l.MaxParameterSetBytes, &l.MaxSliceHeaderBytes, &l.MaxSEIBytes, &l.MaxReorderPictures, &l.MaxNestingDepth}
	for i, p := range fields {
		if *p < 0 {
			return Limits{}, fmt.Errorf("%w: negative limit", hdr10plus.ErrInvalidOptions)
		}
		if *p == 0 {
			*p = defaults[i]
		}
	}
	if l.MaxContainerIndexBytes > l.MaxRetainedBytes || l.MaxBlockBytes > l.MaxRetainedBytes || l.MaxParameterSetBytes > l.MaxRetainedBytes || l.MaxSliceHeaderBytes > l.MaxRetainedBytes || l.MaxSEIBytes > l.MaxRetainedBytes {
		return Limits{}, fmt.Errorf("%w: inconsistent storage limits", hdr10plus.ErrInvalidOptions)
	}
	return l, nil
}

// MemoryBudget is a concurrency-safe, one-session shared accounting quota. Its
// zero value is invalid. Completed results stay charged for the session; this
// quota does not measure caller mutations or Go allocator/runtime overhead.
type MemoryBudget struct {
	mu              sync.Mutex
	max, used, peak int64
}

// Reservation is an opaque owned charge. Release is idempotent and safe across
// concurrent calls and copied aliases. A zero Reservation may be released.
type Reservation struct{ state *reservationState }
type reservationState struct {
	budget *MemoryBudget
	bytes  int64
	once   sync.Once
}

// NewMemoryBudget creates a positive finite session quota.
func NewMemoryBudget(maxBytes int64) (*MemoryBudget, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("%w: memory budget must be positive", hdr10plus.ErrInvalidOptions)
	}
	return &MemoryBudget{max: maxBytes}, nil
}

// Reserve charges before allocation. A failed request does not change usage.
// The caller must release scratch charges or retain the token with owned data.
func (b *MemoryBudget) Reserve(bytes int64) (*Reservation, error) {
	if b == nil || bytes < 0 {
		return nil, fmt.Errorf("%w: invalid reservation", hdr10plus.ErrInvalidOptions)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max <= 0 {
		return nil, fmt.Errorf("%w: uninitialized budget", hdr10plus.ErrInvalidOptions)
	}
	if bytes > b.max-b.used {
		return nil, ErrResourceLimit
	}
	b.used += bytes
	b.peak = max(b.peak, b.used)
	return &Reservation{state: &reservationState{budget: b, bytes: bytes}}, nil
}

// Usage reports currently charged bytes and the greatest simultaneous charge.
// It is safe during collection; these measurements describe accounted storage.
func (b *MemoryBudget) Usage() (usedBytes, peakBytes int64) {
	if b == nil {
		return 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used, b.peak
}

// Release returns a charge exactly once, including aliases.
func (r *Reservation) Release() {
	if r == nil || r.state == nil {
		return
	}
	s := r.state
	s.once.Do(func() { s.budget.mu.Lock(); s.budget.used -= s.bytes; s.budget.mu.Unlock() })
}
