// Command gnl-analyze reads a P0 measurement campaign and prints the verdict.
//
//	gnl-analyze -in results.jsonl
//
// Exit code 0 means at least one candidate passed; 1 means none did, which is
// the NO-GO answer the P0 gate exists to produce.
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
	peakStart := flag.Int("peak-start", 19, "first hour of the peak window, Vietnam local time")
	peakEnd := flag.Int("peak-end", 23, "hour the peak window ends, exclusive, Vietnam local time")
	minRuns := flag.Int("min-runs", 20, "fewest runs inside the peak window before a candidate may pass")
	flag.Parse()

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
	cands := analyze.Evaluate(recs, th, *peakStart, *peakEnd)

	fmt.Printf("Records: %d total\n", len(recs))
	if skipped > 0 {
		// Say it out loud. A torn line is normal after a crash, but a campaign
		// quietly missing samples is how a wrong verdict gets believed.
		fmt.Printf("WARNING: %d unparseable line(s) skipped\n", skipped)
	}
	fmt.Printf("Peak window: %02d:00-%02d:00 Vietnam time\n\n", *peakStart, *peakEnd)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VPS\tLANDMARK\tISP\tRUNS\tBASELINE\tTUNNEL\tGAIN\tJITTER\tLOSS\tVERDICT")
	passed := 0
	for _, c := range cands {
		verdict := "FAIL"
		if c.Pass {
			verdict = "PASS"
			passed++
		}
		// A candidate with no leg-A baseline never had Baseline/Gain computed;
		// printing 0.0ms/+0.0ms there would be indistinguishable from a measured
		// zero and would sort above genuinely-measured negative gains.
		baseline, gain := "n/a", "n/a"
		if c.HasBaseline {
			baseline = fmt.Sprintf("%.1fms", c.BaselineP50Ms)
			gain = fmt.Sprintf("%+.1fms", c.GainMs)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%.1fms\t%s\t%.1fms\t%.2f%%\t%s\n",
			c.VPS, c.Landmark, c.ISP, c.Runs, baseline, c.TunnelP50Ms, gain,
			c.WorstJitterMs, c.WorstLossPct, verdict)
	}
	w.Flush()

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
