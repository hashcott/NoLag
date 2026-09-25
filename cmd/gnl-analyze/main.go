// Command gnl-analyze reads a P0 measurement campaign and prints the verdict.
//
//	gnl-analyze -in results.jsonl
//
// It runs on whichever machine collects the results files, not on the
// measurement hosts.
//
// Exit codes are three, and the third is the point: 0 means at least one
// candidate passed, 1 means every candidate was judged and none passed - the
// NO-GO answer the P0 gate exists to produce - and 2 means the tool could not
// measure anything at all. "We collected nothing" must never be reported in the
// language of "no provider won".
package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"gamenolag/internal/analyze"
	"gamenolag/internal/probe"
)

func main() {
	in := flag.String("in", "results.jsonl", "results file to read (concatenate several with cat first)")
	// 20:00-22:00 is the window the design document section 11.4 prescribes, and
	// the verdict of record is the one taken over it. A wider window pulls in
	// shoulder hours that are less congested, which lowers the ISP baseline and
	// adds runs to every gate - both effects push toward NO-GO.
	peakStart := flag.Int("peak-start", 20, "first hour of the peak window, Vietnam local time")
	peakEnd := flag.Int("peak-end", 22, "hour the peak window ends, exclusive, Vietnam local time")
	minRuns := flag.Int("min-runs", 20, "fewest usable runs inside the peak window, on the weakest leg, before a candidate may pass")
	maxBreachPct := flag.Float64("max-breach-pct", 5, "share of runs allowed to breach a jitter, loss or p99 bar")
	minGainMs := flag.Float64("min-gain-ms", 5, "smallest tunnel advantage that counts as an advantage, in ms")
	flag.Parse()

	// A window the filter cannot express discards every record and produces an
	// empty candidate set, which used to print as a confident NO-GO. Catch it
	// here where the message can name the problem.
	if *peakStart < 0 || *peakEnd > 24 || *peakStart >= *peakEnd {
		fmt.Fprintf(os.Stderr,
			"peak window %d-%d is not a window: need 0 <= -peak-start < -peak-end <= 24.\n",
			*peakStart, *peakEnd)
		fmt.Fprintln(os.Stderr,
			"A window that wraps past midnight is not supported; analyse the two halves separately.")
		os.Exit(2)
	}

	recs, skipped, err := probe.Load(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", *in, err)
		os.Exit(2)
	}
	if len(recs) == 0 {
		if skipped > 0 {
			fmt.Fprintf(os.Stderr, "WARNING: %d unparseable line(s) skipped\n", skipped)
		}
		fmt.Fprintf(os.Stderr, "no records in %s\n", *in)
		os.Exit(2)
	}

	th := analyze.DefaultThresholds()
	th.MinRuns = *minRuns
	th.MaxBreachPct = *maxBreachPct
	th.MinGainMs = *minGainMs
	cands, dupes := analyze.Evaluate(recs, th, *peakStart, *peakEnd)

	fmt.Printf("Records: %d total\n", len(recs))
	if skipped > 0 {
		// Say it out loud. A torn line is normal after a crash, but a campaign
		// quietly missing samples is how a wrong verdict gets believed.
		fmt.Printf("WARNING: %d unparseable line(s) skipped\n", skipped)
	}
	if dupes > 0 {
		// Discarding redundant data silently is still discarding data, and a
		// doubled file is a collection mistake the operator needs to know about.
		fmt.Printf("WARNING: %d duplicate record(s) dropped: the same run appeared more than once.\n", dupes)
		fmt.Printf("         Check for a results file collected twice.\n")
	}
	fmt.Printf("Peak window: %02d:00-%02d:00 Vietnam time\n\n", *peakStart, *peakEnd)

	if len(cands) == 0 {
		fmt.Println("No candidate could be evaluated. This is not a verdict about any provider.")
		fmt.Printf("Records read: %d, inside the peak window: %d.\n",
			len(recs), analyze.CountInWindow(recs, *peakStart, *peakEnd))
		fmt.Println("Check that the file contains leg A, B and C records, that -from/-to names match")
		fmt.Println("across hosts, and that the peak window covers when the data was collected.")
		os.Exit(2)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VPS\tLANDMARK\tISP\tRUNS\tBASELINE\tTUNNEL\tGAIN\tJITTER\tJ-OVER\tLOSS\tL-OVER\tVERDICT")
	passed := 0
	for _, c := range cands {
		verdict := "FAIL"
		if c.Pass {
			verdict = "PASS"
			passed++
		}
		// A candidate missing a baseline, or whose legs produced no usable latency
		// samples, never had Baseline/Tunnel/Gain computed; printing 0.0ms there
		// would be indistinguishable from a measured zero, which is exactly how a
		// dead leg gets read as a fast one.
		baseline, gain := "n/a", "n/a"
		if c.HasBaseline {
			baseline = fmt.Sprintf("%.1fms", c.BaselineP50Ms)
		}
		tunnel := "n/a"
		if c.HasTunnel {
			tunnel = fmt.Sprintf("%.1fms", c.TunnelP50Ms)
		}
		if c.HasGain() {
			gain = fmt.Sprintf("%+.1fms", c.GainMs)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%.1fms\t%.1f%%\t%.2f%%\t%.1f%%\t%s\n",
			c.VPS, c.Landmark, c.ISP, c.Runs, baseline, tunnel, gain,
			c.WorstJitterMs, c.JitterBreachPct, c.WorstLossPct, c.LossBreachPct, verdict)
	}
	w.Flush()
	fmt.Printf("\nJITTER and LOSS are the worst single run of the campaign; J-OVER and L-OVER are\n")
	fmt.Printf("the share of runs at or over the bar, and those are what decide the verdict.\n")

	fmt.Println()
	for _, c := range cands {
		if c.Pass {
			continue
		}
		fmt.Printf("%s via %s to %s failed because:\n", c.VPS, c.ISP, c.Landmark)
		for _, r := range c.Reasons {
			fmt.Printf("  - %s\n", r)
		}
	}

	fmt.Println()
	if passed == 0 {
		fmt.Println("VERDICT: NO-GO. No candidate beats the ISP route at peak hours.")
		fmt.Println("Try more providers before writing any product code. If none win, there is no product.")
		os.Exit(1)
	}
	fmt.Printf("VERDICT: GO. %d candidate(s) passed. Proceed to P1.\n", passed)
}
