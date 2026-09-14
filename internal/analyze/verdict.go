// Package analyze turns a campaign of probe records into a go/no-go verdict.
//
// The product is worth building only where, at peak hours,
//
//	RTT(player -> VPS) + RTT(VPS -> game) < RTT(player -> game)
//
// and the tunnel legs are steady enough to play on. A path that wins on the
// median but jitters is worse to play on than a slower steady one, so latency
// alone never decides.
package analyze

import (
	"fmt"
	"math"
	"sort"
	"time"

	"gamenolag/internal/probe"
)

// vnOffsetSeconds is Vietnam's UTC offset. Vietnam does not observe daylight
// saving, so a fixed zone is correct rather than a simplification.
const vnOffsetSeconds = 7 * 3600

// Thresholds are the bars a candidate must clear, from the design document 11.4.
type Thresholds struct {
	MaxJitterMs    float64 // p95 - p50 on either tunnel leg
	MaxLossPct     float64 // on either tunnel leg
	MaxP99ExcessMs float64 // p99 - p50 on either tunnel leg
}

// DefaultThresholds returns the values fixed in the design document.
func DefaultThresholds() Thresholds {
	return Thresholds{MaxJitterMs: 10, MaxLossPct: 0.5, MaxP99ExcessMs: 30}
}

// Candidate is one (ISP, VPS) pair judged over the campaign.
type Candidate struct {
	VPS           string
	ISP           string
	Runs          int
	BaselineP50Ms float64 // median of leg A
	TunnelP50Ms   float64 // median of leg B plus median of leg C
	GainMs        float64 // BaselineP50Ms - TunnelP50Ms; positive is an improvement
	WorstJitterMs float64
	WorstLossPct  float64
	Pass          bool
	Reasons       []string // why it failed; empty when it passed
}

type legSamples struct {
	p50, jitter, loss, p99excess []float64
	runs                         int
}

func (l *legSamples) add(r probe.Record) {
	l.p50 = append(l.p50, r.P50Ms)
	l.jitter = append(l.jitter, r.JitterMs)
	l.loss = append(l.loss, r.LossPct)
	l.p99excess = append(l.p99excess, r.P99Ms-r.P50Ms)
	l.runs++
}

// Evaluate judges every (ISP, VPS) pair in recs.
//
// peakStartHourVN and peakEndHourVN bound the window in Vietnam local time; the
// window is half-open, [start, end). Records outside it are discarded, because a
// measurement taken at 03:00 flatters the ISP's route and answers a question
// nobody asked.
func Evaluate(recs []probe.Record, th Thresholds, peakStartHourVN, peakEndHourVN int) []Candidate {
	vn := time.FixedZone("ICT", vnOffsetSeconds)

	// Leg A is keyed by ISP alone: the baseline does not depend on which VPS is
	// being considered. Legs B and C are keyed by the pair.
	baseline := map[string]*legSamples{}
	legB := map[[2]string]*legSamples{}
	legC := map[[2]string]*legSamples{}
	isps := map[string]bool{}
	vpses := map[string]bool{}
	pairs := map[[2]string]bool{}

	for _, r := range recs {
		h := r.TS.In(vn).Hour()
		if h < peakStartHourVN || h >= peakEndHourVN {
			continue
		}
		switch r.Leg {
		case "A":
			isps[r.From] = true
			if baseline[r.From] == nil {
				baseline[r.From] = &legSamples{}
			}
			baseline[r.From].add(r)
		case "B":
			isps[r.From] = true
			vpses[r.To] = true
			k := [2]string{r.From, r.To}
			pairs[k] = true
			if legB[k] == nil {
				legB[k] = &legSamples{}
			}
			legB[k].add(r)
		case "C":
			vpses[r.From] = true
			if legC[[2]string{"", r.From}] == nil {
				legC[[2]string{"", r.From}] = &legSamples{}
			}
			legC[[2]string{"", r.From}].add(r)
		}
	}

	var out []Candidate
	for k := range pairs {
		isp, vps := k[0], k[1]
		b := legB[k]
		c := legC[[2]string{"", vps}]
		a := baseline[isp]

		cand := Candidate{VPS: vps, ISP: isp, Runs: b.runs}

		if c == nil {
			cand.Reasons = append(cand.Reasons, "no leg C records: the VPS never measured its own path to the landmark")
			out = append(out, cand)
			continue
		}

		cand.TunnelP50Ms = median(b.p50) + median(c.p50)
		cand.WorstJitterMs = math.Max(maxOf(b.jitter), maxOf(c.jitter))
		cand.WorstLossPct = math.Max(maxOf(b.loss), maxOf(c.loss))
		worstP99Excess := math.Max(maxOf(b.p99excess), maxOf(c.p99excess))

		if a == nil {
			cand.Reasons = append(cand.Reasons, "no leg A baseline for this ISP: nothing to compare against")
		} else {
			cand.BaselineP50Ms = median(a.p50)
			cand.GainMs = cand.BaselineP50Ms - cand.TunnelP50Ms
			if cand.GainMs <= 0 {
				cand.Reasons = append(cand.Reasons, fmt.Sprintf(
					"tunnel is %.1fms slower than the ISP route (%.1fms vs %.1fms)",
					-cand.GainMs, cand.TunnelP50Ms, cand.BaselineP50Ms))
			}
		}
		if cand.WorstJitterMs > th.MaxJitterMs {
			cand.Reasons = append(cand.Reasons, fmt.Sprintf(
				"jitter %.1fms is over the %.1fms bar", cand.WorstJitterMs, th.MaxJitterMs))
		}
		if cand.WorstLossPct > th.MaxLossPct {
			cand.Reasons = append(cand.Reasons, fmt.Sprintf(
				"loss %.2f%% is over the %.2f%% bar", cand.WorstLossPct, th.MaxLossPct))
		}
		if worstP99Excess > th.MaxP99ExcessMs {
			cand.Reasons = append(cand.Reasons, fmt.Sprintf(
				"p99 sits %.1fms above p50, over the %.1fms bar", worstP99Excess, th.MaxP99ExcessMs))
		}
		cand.Pass = len(cand.Reasons) == 0
		out = append(out, cand)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].GainMs != out[j].GainMs {
			return out[i].GainMs > out[j].GainMs // best gain first
		}
		return out[i].VPS < out[j].VPS
	})
	return out
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := make([]float64, len(xs))
	copy(s, xs)
	sort.Float64s(s)
	return s[len(s)/2]
}

func maxOf(xs []float64) float64 {
	m := 0.0
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}
