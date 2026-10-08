package hdr10plus

import (
	"fmt"
	"slices"
)

// PeakSource selects the metadata estimator used for peak brightness.
type PeakSource uint8

const (
	// PeakHistogram uses the maximum supplied distribution value (the default).
	PeakHistogram PeakSource = iota
	// PeakHistogram99 uses the last supplied distribution value, without sorting.
	PeakHistogram99
	// PeakMaxSCL uses the maximum supplied MaxSCL component.
	PeakMaxSCL
	// PeakMaxSCLLuminance weights exactly three ordered RGB components.
	PeakMaxSCLLuminance
)

func (source PeakSource) description() string {
	switch source {
	case PeakHistogram:
		return "Histogram maximum value"
	case PeakHistogram99:
		return "Histogram 99.98% percentile from metadata"
	case PeakMaxSCL:
		return "MaxSCL maximum value"
	case PeakMaxSCLLuminance:
		return "MaxSCL Luminance, calculated from the components"
	default:
		return ""
	}
}

// Estimators and statistics follow quietvoid/hdr10plus_tool; see notices.
func peakNits(frame Frame, source PeakSource) (float64, error) {
	switch source {
	case PeakHistogram, PeakHistogram99:
		if len(frame.DistributionValues) == 0 {
			return 0, fmt.Errorf("%w: DistributionValues requires at least one value", ErrInvalidMetadata)
		}
		if source == PeakHistogram99 {
			return float64(frame.DistributionValues[len(frame.DistributionValues)-1]) / 10, nil
		}
		return float64(slices.Max(frame.DistributionValues)) / 10, nil
	case PeakMaxSCL:
		if len(frame.MaxSCL) == 0 {
			return 0, fmt.Errorf("%w: MaxScl requires at least one component", ErrInvalidMetadata)
		}
		return float64(slices.Max(frame.MaxSCL)) / 10, nil
	case PeakMaxSCLLuminance:
		if len(frame.MaxSCL) != 3 {
			return 0, fmt.Errorf("%w: MaxScl requires exactly three RGB components", ErrInvalidMetadata)
		}
		return (0.2627*float64(frame.MaxSCL[0]) + 0.678*float64(frame.MaxSCL[1]) + 0.0593*float64(frame.MaxSCL[2])) / 10, nil
	default:
		return 0, fmt.Errorf("%w: unknown PeakSource %d", ErrInvalidOptions, source)
	}
}

type sample struct {
	average, peak float64 // PQ codes, without viewport clipping
}

type statistics struct {
	maximum, mean float64 // nits after inverse PQ
}

type plotData struct {
	samples       []sample
	average, peak statistics
}

func calculate(frames []Frame, start int, source PeakSource) (plotData, error) {
	data := plotData{samples: make([]sample, len(frames))}
	var maxAverage, maxPeak, sumAverage, sumPeak float64
	for i, frame := range frames {
		peak, err := peakNits(frame, source)
		if err != nil {
			return plotData{}, fmt.Errorf("SceneInfo[%d].LuminanceParameters: %w", start+i, err)
		}
		s := sample{average: nitsToPQ(float64(frame.AverageRGB) / 10), peak: nitsToPQ(peak)}
		data.samples[i] = s
		maxAverage, maxPeak = max(maxAverage, s.average), max(maxPeak, s.peak)
		sumAverage += s.average
		sumPeak += s.peak
	}
	data.average = statistics{pqToNits(maxAverage), pqToNits(sumAverage / float64(len(frames)))}
	data.peak = statistics{pqToNits(maxPeak), pqToNits(sumPeak / float64(len(frames)))}
	return data, nil
}

func (data plotData) labels() (string, string) {
	return fmt.Sprintf("Maximum (MaxCLL: %.2f nits, avg: %.2f nits)", data.peak.maximum, data.peak.mean),
		fmt.Sprintf("Average (MaxFALL: %.2f nits, avg: %.2f nits)", data.average.maximum, data.average.mean)
}
