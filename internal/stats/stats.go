// Package stats turns a batch of round-trip samples into the numbers that decide
// whether a network path is good enough for a game: percentiles, jitter and loss.
//
// Mean latency is deliberately not reported. Players feel jitter, not the mean,
// and a path chosen on mean latency is chosen wrong.
package stats

import (
	"math"
	"sort"
	"time"
)

// Summary is one measurement run reduced to the numbers a verdict needs.
type Summary struct {
	Sent     int     `json:"sent"`
	Received int     `json:"received"`
	LossPct  float64 `json:"loss_pct"`
	P50Ms    float64 `json:"p50_ms"`
	P95Ms    float64 `json:"p95_ms"`
	P99Ms    float64 `json:"p99_ms"`
	JitterMs float64 `json:"jitter_ms"`
}

// Summarize reduces rtts to a Summary. sent is the number of probes emitted,
// which is larger than len(rtts) when probes were lost.
//
// The input slice is not modified: it is copied before sorting, because callers
// keep their own raw samples for the full report.
func Summarize(sent int, rtts []time.Duration) Summary {
	s := Summary{Sent: sent, Received: len(rtts)}

	if sent > 0 {
		lost := sent - len(rtts)
		if lost < 0 {
			// More replies than probes means the path duplicated packets. Loss is not
			// a negative quantity, and a negative percentage reads on a report as
			// better than perfect.
			lost = 0
		}
		s.LossPct = float64(lost) / float64(sent) * 100
	}
	if len(rtts) == 0 {
		return s
	}

	sorted := make([]time.Duration, len(rtts))
	copy(sorted, rtts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	s.P50Ms = percentileMs(sorted, 50)
	s.P95Ms = percentileMs(sorted, 95)
	s.P99Ms = percentileMs(sorted, 99)
	s.JitterMs = s.P95Ms - s.P50Ms
	return s
}

// percentileMs uses the nearest-rank method: index = ceil(p/100 * n) - 1,
// clamped into range. Stated exactly so that a second implementation of this
// tool cannot quietly disagree with the first.
func percentileMs(sorted []time.Duration, p float64) float64 {
	n := len(sorted)
	idx := int(math.Ceil(p/100*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return float64(sorted[idx].Nanoseconds()) / 1e6
}
