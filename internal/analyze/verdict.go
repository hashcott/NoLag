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
	MinRuns        int     // fewest runs on the weaker tunnel leg inside the window before a PASS is trustworthy
}

// DefaultThresholds returns the values fixed in the design document.
func DefaultThresholds() Thresholds {
	return Thresholds{MaxJitterMs: 10, MaxLossPct: 0.5, MaxP99ExcessMs: 30, MinRuns: 20}
}

// Candidate is one (ISP, VPS, landmark) triple judged over the campaign.
type Candidate struct {
	VPS           string
	ISP           string
	Landmark      string
	Runs          int     // smaller of leg B's and leg C's run counts inside the window
	HasBaseline   bool    // false when no leg-A record exists for this (ISP, landmark)
	BaselineP50Ms float64 // median of leg A; meaningless when !HasBaseline
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

// Evaluate judges every (ISP, VPS, landmark) triple in recs.
//
// peakStartHourVN and peakEndHourVN bound the window in Vietnam local time; the
// window is half-open, [start, end). Records outside it are discarded, because a
// measurement taken at 03:00 flatters the ISP's route and answers a question
// nobody asked.
func Evaluate(recs []probe.Record, th Thresholds, peakStartHourVN, peakEndHourVN int) []Candidate {
	vn := time.FixedZone("ICT", vnOffsetSeconds)

	// Leg A is keyed by (ISP, landmark): the baseline depends on which landmark
	// it was measured against. Leg B is keyed by (ISP, VPS): a tunnel run has no
	// landmark of its own, only a VPS hop. Leg C is keyed by (VPS, landmark). A
	// results file can hold measurements to more than one landmark - the runbook
	// has operators rent two, Singapore and Tokyo - so every (ISP, VPS) pair seen
	// in leg B is judged separately against every landmark seen in legs A and C,
	// rather than pooling the landmarks into one blended median.
	baseline := map[[2]string]*legSamples{} // {isp, landmark}
	legB := map[[2]string]*legSamples{}     // {isp, vps}
	legC := map[[2]string]*legSamples{}     // {vps, landmark}
	bPairs := map[[2]string]bool{}          // {isp, vps} seen in leg B
	landmarks := map[string]bool{}          // every landmark seen in leg A or leg C

	for _, r := range recs {
		h := r.TS.In(vn).Hour()
		if h < peakStartHourVN || h >= peakEndHourVN {
			continue
		}
		switch r.Leg {
		case "A":
			landmarks[r.To] = true
			k := [2]string{r.From, r.To}
			if baseline[k] == nil {
				baseline[k] = &legSamples{}
			}
			baseline[k].add(r)
		case "B":
			k := [2]string{r.From, r.To}
			bPairs[k] = true
			if legB[k] == nil {
				legB[k] = &legSamples{}
			}
			legB[k].add(r)
		case "C":
			landmarks[r.To] = true
			k := [2]string{r.From, r.To}
			if legC[k] == nil {
				legC[k] = &legSamples{}
			}
			legC[k].add(r)
		}
	}

	var out []Candidate
	for bp := range bPairs {
		isp, vps := bp[0], bp[1]
		b := legB[bp]

		for lm := range landmarks {
			c := legC[[2]string{vps, lm}]
			a := baseline[[2]string{isp, lm}]

			cand := Candidate{VPS: vps, ISP: isp, Landmark: lm, Runs: b.runs}

			if c == nil {
				cand.Reasons = append(cand.Reasons, "no leg C records: the VPS never measured its own path to the landmark")
				out = append(out, cand)
				continue
			}

			// The floor covers both tunnel legs: a candidate with plenty of leg-B runs
			// and one leg-C run is resting half its verdict on a single sample, and a
			// dead leg-C cron is precisely the broken collection this floor is for.
			cand.Runs = b.runs
			if c != nil && c.runs < cand.Runs {
				cand.Runs = c.runs
			}

			cand.TunnelP50Ms = median(b.p50) + median(c.p50)
			cand.WorstJitterMs = math.Max(maxOf(b.jitter), maxOf(c.jitter))
			cand.WorstLossPct = math.Max(maxOf(b.loss), maxOf(c.loss))
			worstP99Excess := math.Max(maxOf(b.p99excess), maxOf(c.p99excess))

			if a == nil {
				cand.Reasons = append(cand.Reasons, "no leg A baseline for this ISP: nothing to compare against")
			} else {
				cand.HasBaseline = true
				cand.BaselineP50Ms = median(a.p50)
				cand.GainMs = cand.BaselineP50Ms - cand.TunnelP50Ms
				if cand.GainMs <= 0 {
					cand.Reasons = append(cand.Reasons, fmt.Sprintf(
						"tunnel is %.1fms slower than the ISP route (%.1fms vs %.1fms)",
						-cand.GainMs, cand.TunnelP50Ms, cand.BaselineP50Ms))
				}
			}
			// Constraints are strict ("< 10ms", not "<= 10ms"): a value that sits
			// exactly on the bar must fail. 6 lost packets in 1200 is exactly 0.5%,
			// an ordinary count, not a corner case.
			if cand.WorstJitterMs >= th.MaxJitterMs {
				cand.Reasons = append(cand.Reasons, fmt.Sprintf(
					"jitter %.1fms is not under the %.1fms bar", cand.WorstJitterMs, th.MaxJitterMs))
			}
			if cand.WorstLossPct >= th.MaxLossPct {
				cand.Reasons = append(cand.Reasons, fmt.Sprintf(
					"loss %.2f%% is not under the %.2f%% bar", cand.WorstLossPct, th.MaxLossPct))
			}
			if worstP99Excess >= th.MaxP99ExcessMs {
				cand.Reasons = append(cand.Reasons, fmt.Sprintf(
					"p99 sits %.1fms above p50, not under the %.1fms bar", worstP99Excess, th.MaxP99ExcessMs))
			}
			if cand.Runs < th.MinRuns {
				// "Too little data" is a different diagnosis from "bad path": an
				// operator must not read a low-run PASS as a verdict on the route.
				cand.Reasons = append(cand.Reasons, fmt.Sprintf(
					"only %d run(s) inside the peak window, need %d: too little data to judge, "+
						"not evidence of a bad path", cand.Runs, th.MinRuns))
			}
			cand.Pass = len(cand.Reasons) == 0
			out = append(out, cand)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].HasBaseline != out[j].HasBaseline {
			return out[i].HasBaseline // a computed gain sorts before an uncomputed one
		}
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

// maxOf assumes every input is non-negative, true of jitter, loss and the p99
// excess this package feeds it; a genuinely negative value would sort below the
// 0.0 floor and be silently dropped.
func maxOf(xs []float64) float64 {
	m := 0.0
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}
