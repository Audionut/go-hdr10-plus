package hdr10plus

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"testing"
)

type referenceStats struct {
	AverageMax  float64 `json:"average_max"`
	AverageMean float64 `json:"average_mean"`
	PeakMax     float64 `json:"peak_max"`
	PeakMean    float64 `json:"peak_mean"`
}

type numericReference struct {
	PQ []struct {
		Nits, PQ  float64
		RoundTrip float64 `json:"round_trip"`
	}
	Estimators []float64
	Synthetic  []struct {
		Name       string
		Statistics referenceStats
	}
}

func loadReference(t *testing.T) numericReference {
	t.Helper()
	input, err := os.ReadFile("testdata/numeric-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference numericReference
	if err := json.Unmarshal(input, &reference); err != nil {
		t.Fatal(err)
	}
	return reference
}

func assertNits(t *testing.T, got, want float64) {
	t.Helper()
	tolerance := max(1e-6, 1e-9*math.Abs(want))
	if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-want) > tolerance {
		t.Fatalf("got %.17g nits, want %.17g (tolerance %g)", got, want, tolerance)
	}
}

func TestPeakEstimators(t *testing.T) {
	frame := Frame{1234, []uint32{9000, 100, 5000}, []uint32{3000, 1000, 2000}}
	reference := loadReference(t)
	for source, want := range reference.Estimators {
		got, err := peakNits(frame, PeakSource(source))
		if err != nil {
			t.Fatal(err)
		}
		assertNits(t, got, want)
	}
	for _, source := range []PeakSource{PeakHistogram, PeakHistogram99, PeakMaxSCL, PeakMaxSCLLuminance} {
		if _, err := peakNits(Frame{}, source); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("missing source %d: %v", source, err)
		}
	}
	for _, components := range [][]uint32{{17}, {10, 20, 30, 40}} {
		if _, err := peakNits(Frame{MaxSCL: components}, PeakMaxSCL); err != nil {
			t.Fatal(err)
		}
		if _, err := peakNits(Frame{MaxSCL: components}, PeakMaxSCLLuminance); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("luminance component count: %v", err)
		}
	}
	if _, err := peakNits(frame, 255); !errors.Is(err, ErrInvalidOptions) {
		t.Fatal(err)
	}
	// More than ten distribution values is valid on the plotting path.
	if peak, err := peakNits(Frame{DistributionValues: make([]uint32, 11)}, PeakHistogram); err != nil || peak != 0 {
		t.Fatalf("distribution length: %v %v", peak, err)
	}
}

func TestStatisticsAgainstRust(t *testing.T) {
	averages := map[string][]uint32{"zero": {0, 0}, "constant": {1000, 1000}, "variation": {0, 10000}, "above_peak": {10000, 5000}}
	for _, tc := range loadReference(t).Synthetic {
		t.Run(tc.Name, func(t *testing.T) {
			frames := make([]Frame, 2)
			for i, average := range averages[tc.Name] {
				peak := average
				if tc.Name == "above_peak" {
					peak = 1000
				}
				frames[i] = Frame{AverageRGB: average, DistributionValues: []uint32{peak}}
			}
			data, err := calculate(frames, 0, PeakHistogram)
			if err != nil {
				t.Fatal(err)
			}
			assertNits(t, data.average.maximum, tc.Statistics.AverageMax)
			assertNits(t, data.average.mean, tc.Statistics.AverageMean)
			assertNits(t, data.peak.maximum, tc.Statistics.PeakMax)
			assertNits(t, data.peak.mean, tc.Statistics.PeakMean)
			if tc.Name == "variation" {
				if math.Abs(data.average.mean-500) < 100 {
					t.Fatalf("statistics use arithmetic mean nits: %g", data.average.mean)
				}
				peak, average := data.labels()
				if peak != "Maximum (MaxCLL: 1000.00 nits, avg: 24.82 nits)" || average != "Average (MaxFALL: 1000.00 nits, avg: 24.82 nits)" {
					t.Fatalf("legend labels: %s / %s", peak, average)
				}
			}
		})
	}
}

func TestPQAgainstRust(t *testing.T) {
	previous := -1.0
	for _, pair := range loadReference(t).PQ {
		got := nitsToPQ(pair.Nits)
		if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-pair.PQ) > 1e-12 || got <= previous {
			t.Fatalf("PQ(%g)=%g, want %.17g, previous %g", pair.Nits, got, pair.PQ, previous)
		}
		assertNits(t, pqToNits(got), pair.RoundTrip)
		assertNits(t, pqToNits(got), pair.Nits)
		previous = got
	}
	if nitsToPQ(0) <= 0 || pqToNits(0) != 0 || pqToNits(-1) != 0 {
		t.Fatal("zero/negative PQ behavior changed")
	}
}
