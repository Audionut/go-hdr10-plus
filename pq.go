package hdr10plus

import "math"

// Equations adapted from quietvoid/hdr10plus_tool, MIT licensed; see
// THIRD_PARTY_NOTICES.md. Preserve operation order and unbounded forward PQ.
const (
	pqYMax = 10000.0
	pqM1   = 2610.0 / 16384.0
	pqM2   = (2523.0 / 4096.0) * 128.0
	pqC1   = 3424.0 / 4096.0
	pqC2   = (2413.0 / 4096.0) * 32.0
	pqC3   = (2392.0 / 4096.0) * 32.0
)

func nitsToPQ(nits float64) float64 {
	y := math.Pow(nits/pqYMax, pqM1)
	return math.Pow((pqC1+pqC2*y)/(1+pqC3*y), pqM2)
}

func pqToNits(code float64) float64 {
	if code <= 0 {
		return 0
	}
	q := math.Pow(code, 1/pqM2)
	return math.Pow(max(q-pqC1, 0)/(pqC2-pqC3*q), 1/pqM1) * pqYMax
}
