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
	MinRuns        int     // fewest usable runs on the weakest of the three legs inside the window before a PASS is trustworthy

	// MaxBreachPct is the share of runs allowed to breach a steadiness bar before
	// the path is judged unsteady. Gating on the single worst run of ~84 fails
	// almost any real path: one bad evening on a home connection, or one VPS
	// reboot, is enough. A path that breaches one run in twenty is genuinely
	// unstable; one in eighty-four is a network having a moment.
	MaxBreachPct float64

	// MinGainMs is the smallest advantage that counts as an advantage. The design
	// document section 13 requires B + C < A clearly, and a gain smaller than the
	// typical jitter of a single leg is not a route advantage, it is measurement
	// noise: two independently measured legs summed against a third cannot
	// distinguish 0.1ms from zero, and a product cannot be built on it.
	MinGainMs float64
}

// DefaultThresholds returns the values fixed in the design document.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MaxJitterMs: 10, MaxLossPct: 0.5, MaxP99ExcessMs: 30, MinRuns: 20,
		MaxBreachPct: 5, MinGainMs: 5,
	}
}

// Candidate is one (ISP, VPS, landmark) triple judged over the campaign.
type Candidate struct {
	VPS      string
	ISP      string
	Landmark string
	// Runs is the smallest usable run count across all three legs inside the
	// window. All three: leg A is the denominator of the entire thesis, and a
	// baseline taken once during an ISP spike would otherwise carry a GO on its
	// own. A run that received no echoes at all is not a usable latency sample
	// and is not counted here.
	Runs            int
	HasBaseline     bool    // false when leg A is missing or produced no usable latency samples
	HasTunnel       bool    // false when leg B or leg C produced no usable latency samples
	BaselineP50Ms   float64 // median of leg A; meaningless when !HasBaseline
	TunnelP50Ms     float64 // median of leg B plus median of leg C; meaningless when !HasTunnel
	GainMs          float64 // BaselineP50Ms - TunnelP50Ms; positive is an improvement. Only computed when HasGain
	WorstJitterMs   float64 // worst single run on either tunnel leg: information, not the gate
	WorstLossPct    float64 // worst single run on either tunnel leg: information, not the gate
	JitterBreachPct float64 // share of runs at or over the jitter bar, worse tunnel leg
	LossBreachPct   float64 // share of runs at or over the loss bar, worse tunnel leg
	P99BreachPct    float64 // share of runs at or over the p99-excess bar, worse tunnel leg
	Pass            bool
	Reasons         []string // why it failed; empty when it passed
}

// HasGain reports whether a gain could be computed at all. A candidate missing
// either side of the comparison has no gain, and printing 0.0ms for it would be
// indistinguishable from a measured dead heat.
func (c Candidate) HasGain() bool { return c.HasBaseline && c.HasTunnel }

type legSamples struct {
	p50, jitter, p99excess []float64 // runs that received at least one echo
	loss                   []float64 // every run, including the ones that received nothing
	runs                   int       // runs with usable latency samples
}

// add files one run into the aggregates it belongs in, which is not all of them.
//
// A run that received nothing carries no latency information: its p50 is 0
// because there was nothing to take a percentile over, not because the path is
// fast. Feeding that into a median produces "the tunnel is 45.0ms slower than
// the ISP route (45.0ms vs 0.0ms)" - a measurement failure in the language of a
// routing finding. It does carry real loss information, though: 100% loss is a
// true fact about that run, so it counts there and nowhere else.
func (l *legSamples) add(r probe.Record) {
	l.loss = append(l.loss, r.LossPct)
	if r.Received == 0 {
		return
	}
	l.p50 = append(l.p50, r.P50Ms)
	l.jitter = append(l.jitter, r.JitterMs)
	l.p99excess = append(l.p99excess, r.P99Ms-r.P50Ms)
	l.runs++
}

// inWindow reports whether a record falls inside the peak window, which is
// half-open, [start, end), in Vietnam local time. A measurement taken at 03:00
// flatters the ISP's route and answers a question nobody asked.
func inWindow(r probe.Record, vn *time.Location, startHourVN, endHourVN int) bool {
	h := r.TS.In(vn).Hour()
	return h >= startHourVN && h < endHourVN
}

// CountInWindow reports how many records fall inside the peak window. A caller
// that evaluated nothing needs this to tell "we collected nothing" apart from
// "we collected it all outside the window", which are different problems with
// different fixes.
func CountInWindow(recs []probe.Record, peakStartHourVN, peakEndHourVN int) int {
	vn := time.FixedZone("ICT", vnOffsetSeconds)
	n := 0
	for _, r := range recs {
		if inWindow(r, vn, peakStartHourVN, peakEndHourVN) {
			n++
		}
	}
	return n
}

// dedupKey identifies one measurement run. Two records agreeing on all four
// fields are the same run read twice, not two runs.
type dedupKey struct {
	ts            int64
	leg, from, to string
}

// Evaluate judges every (ISP, VPS, landmark) triple in recs, and reports how
// many duplicate records it dropped before aggregating.
//
// peakStartHourVN and peakEndHourVN bound the window in Vietnam local time; the
// window is half-open, [start, end). Records outside it are discarded.
func Evaluate(recs []probe.Record, th Thresholds, peakStartHourVN, peakEndHourVN int) ([]Candidate, int) {
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

	// Duplicates can only ever manufacture a PASS. Medians and maxima are immune
	// to a repeated value, but the run count is not, and the run count is the
	// floor that gates everything: the same ten runs read twice clear a floor of
	// twenty. The runbook collects with a glob, so one stray backup directory is
	// enough to double a file.
	seen := map[dedupKey]bool{}
	dupes := 0

	for _, r := range recs {
		if !inWindow(r, vn, peakStartHourVN, peakEndHourVN) {
			continue
		}
		k := dedupKey{ts: r.TS.UnixNano(), leg: r.Leg, from: r.From, to: r.To}
		if seen[k] {
			dupes++
			continue
		}
		seen[k] = true

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

			// No leg C for this pair means nobody measured this VPS against this
			// landmark. With three ISPs, three VPSes and two landmarks that is most
			// of the grid, and a table of rows about combinations nobody measured
			// buries the rows the operator is told to read.
			if c == nil {
				continue
			}

			cand := Candidate{VPS: vps, ISP: isp, Landmark: lm}

			// unusable records the measurement failure and reports it, so the caller
			// can keep the leg out of every latency aggregate. The wording is
			// deliberate: an operator must not read it as a finding about the route.
			unusable := func(name string, l *legSamples) bool {
				if len(l.p50) > 0 {
					return false
				}
				cand.Reasons = append(cand.Reasons, fmt.Sprintf(
					"leg %s produced no usable latency samples (every run lost every packet): "+
						"this is a measurement failure, not a fast route", name))
				return true
			}

			// The floor covers all three legs. A candidate with plenty of leg-B runs
			// and one leg-C run rests half its verdict on a single sample; one with a
			// single leg-A run rests the entire comparison on whatever the ISP was
			// doing that minute. A dead cron on any leg is precisely what this is for.
			cand.Runs = b.runs
			if c.runs < cand.Runs {
				cand.Runs = c.runs
			}
			if a != nil && a.runs < cand.Runs {
				cand.Runs = a.runs
			}

			tunnelDead := unusable("B", b)
			if unusable("C", c) {
				tunnelDead = true
			}
			if !tunnelDead {
				cand.HasTunnel = true
				cand.TunnelP50Ms = median(b.p50) + median(c.p50)
			}

			cand.WorstJitterMs = math.Max(maxOf(b.jitter), maxOf(c.jitter))
			cand.WorstLossPct = math.Max(maxOf(b.loss), maxOf(c.loss))
			worstP99Excess := math.Max(maxOf(b.p99excess), maxOf(c.p99excess))

			if a == nil {
				cand.Reasons = append(cand.Reasons, "no leg A baseline for this ISP: nothing to compare against")
			} else if !unusable("A", a) {
				cand.HasBaseline = true
				cand.BaselineP50Ms = median(a.p50)
			}

			if cand.HasGain() {
				cand.GainMs = cand.BaselineP50Ms - cand.TunnelP50Ms
				switch {
				case cand.GainMs <= 0:
					cand.Reasons = append(cand.Reasons, fmt.Sprintf(
						"tunnel is %.1fms slower than the ISP route (%.1fms vs %.1fms)",
						-cand.GainMs, cand.TunnelP50Ms, cand.BaselineP50Ms))
				case cand.GainMs < th.MinGainMs:
					cand.Reasons = append(cand.Reasons, fmt.Sprintf(
						"tunnel is only %.1fms faster than the ISP route (%.1fms vs %.1fms), "+
							"under the %.1fms margin: a gain smaller than one leg's own jitter is "+
							"measurement noise, not a route advantage",
						cand.GainMs, cand.TunnelP50Ms, cand.BaselineP50Ms, th.MinGainMs))
				}
			}

			// Steadiness is judged on the share of runs that breach, not on the worst
			// one. Across ~84 runs per leg the probability that none ever breaches is
			// near zero, so a worst-run gate fails almost any real path: one VPS
			// reboot during any peak hour is a 100%-loss run and a permanent fail.
			// The bars themselves stay strict - "under 10ms", not "10ms or under" -
			// so breaching is at or above the bar, and 6 lost packets in 1200 is
			// exactly 0.5% and counts as a breach.
			cand.JitterBreachPct = math.Max(breachPct(b.jitter, th.MaxJitterMs), breachPct(c.jitter, th.MaxJitterMs))
			cand.LossBreachPct = math.Max(breachPct(b.loss, th.MaxLossPct), breachPct(c.loss, th.MaxLossPct))
			cand.P99BreachPct = math.Max(breachPct(b.p99excess, th.MaxP99ExcessMs), breachPct(c.p99excess, th.MaxP99ExcessMs))

			if cand.JitterBreachPct > th.MaxBreachPct {
				cand.Reasons = append(cand.Reasons, fmt.Sprintf(
					"jitter is at or over %.1fms on %.1f%% of runs (worst %.1fms), above the %.1f%% allowance",
					th.MaxJitterMs, cand.JitterBreachPct, cand.WorstJitterMs, th.MaxBreachPct))
			}
			if cand.LossBreachPct > th.MaxBreachPct {
				cand.Reasons = append(cand.Reasons, fmt.Sprintf(
					"loss is at or over %.2f%% on %.1f%% of runs (worst %.2f%%), above the %.1f%% allowance",
					th.MaxLossPct, cand.LossBreachPct, cand.WorstLossPct, th.MaxBreachPct))
			}
			if cand.P99BreachPct > th.MaxBreachPct {
				cand.Reasons = append(cand.Reasons, fmt.Sprintf(
					"p99 is at or over %.1fms above p50 on %.1f%% of runs (worst %.1fms), above the %.1f%% allowance",
					th.MaxP99ExcessMs, cand.P99BreachPct, worstP99Excess, th.MaxBreachPct))
			}
			if cand.Runs < th.MinRuns {
				// "Too little data" is a different diagnosis from "bad path": an
				// operator must not read a low-run PASS as a verdict on the route.
				cand.Reasons = append(cand.Reasons, fmt.Sprintf(
					"only %d usable run(s) inside the peak window on the weakest leg, need %d: "+
						"too little data to judge, not evidence of a bad path", cand.Runs, th.MinRuns))
			}
			cand.Pass = len(cand.Reasons) == 0
			out = append(out, cand)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].HasGain() != out[j].HasGain() {
			return out[i].HasGain() // a computed gain sorts before an uncomputed one
		}
		if out[i].GainMs != out[j].GainMs {
			return out[i].GainMs > out[j].GainMs // best gain first
		}
		return out[i].VPS < out[j].VPS
	})
	return out, dupes
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

// breachPct is the share of runs whose value is at or above bar. The comparison
// is inclusive for the same reason the thresholds are: "under the bar" means
// under it.
func breachPct(xs []float64, bar float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	n := 0
	for _, x := range xs {
		if x >= bar {
			n++
		}
	}
	return float64(n) / float64(len(xs)) * 100
}
