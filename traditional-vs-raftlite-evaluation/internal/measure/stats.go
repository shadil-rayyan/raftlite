package measure

import (
	"math"
	"sort"
)

// Stats are the aggregate latency statistics for one experiment run.
type Stats struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean_ms"`
	Median float64 `json:"median_ms"`
	Min    float64 `json:"min_ms"`
	Max    float64 `json:"max_ms"`
	StdDev float64 `json:"stddev_ms"`
	P95    float64 `json:"p95_ms"`
	P99    float64 `json:"p99_ms"`
}

// ComputeStats returns aggregate statistics over latency samples in
// milliseconds. Samples are never discarded — slow results are data.
func ComputeStats(latenciesMs []float64) Stats {
	n := len(latenciesMs)
	if n == 0 {
		return Stats{}
	}
	sorted := append([]float64(nil), latenciesMs...)
	sort.Float64s(sorted)

	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	mean := sum / float64(n)

	varsum := 0.0
	for _, v := range sorted {
		d := v - mean
		varsum += d * d
	}
	stddev := 0.0
	if n > 1 {
		stddev = math.Sqrt(varsum / float64(n-1))
	}

	return Stats{
		N:      n,
		Mean:   round2(mean),
		Median: round2(percentile(sorted, 50)),
		Min:    round2(sorted[0]),
		Max:    round2(sorted[n-1]),
		StdDev: round2(stddev),
		P95:    round2(percentile(sorted, 95)),
		P99:    round2(percentile(sorted, 99)),
	}
}

// percentile returns the nearest-rank percentile from a sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
