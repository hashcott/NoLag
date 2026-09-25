package analyze

import (
	"strings"
	"testing"
	"time"

	"gamenolag/internal/probe"
	"gamenolag/internal/stats"
)

// vnTime builds a UTC timestamp for the given Vietnam (UTC+7) wall-clock hour.
func vnTime(day, hourVN int) time.Time { return vnTimeAt(day, hourVN, 0) }

// vnTimeAt adds a minute, so a test can build many runs that are actually many
// runs: Evaluate de-duplicates on (TS, Leg, From, To), so N records sharing a
// timestamp are one run read N times, not N runs.
func vnTimeAt(day, hourVN, minute int) time.Time {
	loc := time.FixedZone("ICT", 7*3600)
	return time.Date(2026, 9, day, hourVN, minute, 0, 0, loc).UTC()
}

// lossyRec is a run that received nothing at all - the shape probe.Run returns
// when a landmark's allowlist is missing this client. p50/p95/p99 are zero
// because there were no samples to take a percentile over, not because the path
// is instant.
func lossyRec(ts time.Time, leg, from, to string) probe.Record {
	return probe.Record{
		TS: ts, Leg: leg, From: from, To: to, Target: "192.0.2.1:51830",
		Summary: stats.Summary{Sent: 1200, Received: 0, LossPct: 100},
	}
}

func rec(ts time.Time, leg, from, to string, p50, p95, p99, loss float64) probe.Record {
	return probe.Record{
		TS: ts, Leg: leg, From: from, To: to, Target: "192.0.2.1:51830",
		Summary: stats.Summary{
			Sent: 1200, Received: 1200,
			LossPct: loss, P50Ms: p50, P95Ms: p95, P99Ms: p99, JitterMs: p95 - p50,
		},
	}
}

// relaxedRuns drops the sample-size floor so a test can exercise the latency and
// steadiness logic with one record per leg.
func relaxedRuns(th Thresholds) Thresholds { th.MinRuns = 1; return th }

// reasonsContain reports whether any reason mentions word. A failing test
// should assert on the specific reason it is named after, not merely that
// Pass is false: Pass == false cannot tell which check fired, so a broken
// check could go unnoticed as long as some other check still fails.
func reasonsContain(reasons []string, word string) bool {
	for _, r := range reasons {
		if strings.Contains(r, word) {
			return true
		}
	}
	return false
}

// Baseline 80ms; tunnel is 25 + 20 = 45ms. Clear win, clean legs.
func TestEvaluatePassesAClearWin(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 80, 86, 92, 0.1),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-a", 25, 28, 31, 0.05),
		rec(vnTime(1, 20), "C", "vps-a", "landmark-sgp", 20, 23, 26, 0.0),
	}
	got, _ := Evaluate(recs, relaxedRuns(DefaultThresholds()), 19, 23)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	c := got[0]
	if !c.Pass {
		t.Errorf("Pass = false, want true; reasons: %v", c.Reasons)
	}
	if c.BaselineP50Ms != 80 {
		t.Errorf("BaselineP50Ms = %v, want 80", c.BaselineP50Ms)
	}
	if c.TunnelP50Ms != 45 {
		t.Errorf("TunnelP50Ms = %v, want 45", c.TunnelP50Ms)
	}
	if c.GainMs != 35 {
		t.Errorf("GainMs = %v, want 35", c.GainMs)
	}
}

// Tunnel is faster on the median but jitter on leg B is 18ms, over the 10ms bar.
func TestEvaluateFailsOnJitter(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 80, 86, 92, 0.1),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-b", 25, 43, 50, 0.05),
		rec(vnTime(1, 20), "C", "vps-b", "landmark-sgp", 20, 23, 26, 0.0),
	}
	got, _ := Evaluate(recs, DefaultThresholds(), 19, 23)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].Pass {
		t.Error("Pass = true, want false: 18ms jitter is over the 10ms bar")
	}
	if !reasonsContain(got[0].Reasons, "jitter") {
		t.Errorf("no reason mentions jitter; reasons were: %v", got[0].Reasons)
	}
}

// Tunnel is slower than the ISP's own route.
func TestEvaluateFailsWhenTunnelIsSlower(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 60, 64, 70, 0.1),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-c", 40, 43, 46, 0.0),
		rec(vnTime(1, 20), "C", "vps-c", "landmark-sgp", 35, 38, 41, 0.0),
	}
	got, _ := Evaluate(recs, DefaultThresholds(), 19, 23)
	if got[0].Pass {
		t.Error("Pass = true, want false: 75ms tunnel against a 60ms baseline")
	}
	if got[0].GainMs != -15 {
		t.Errorf("GainMs = %v, want -15", got[0].GainMs)
	}
	if !reasonsContain(got[0].Reasons, "slower") {
		t.Errorf("no reason mentions the tunnel being slower; reasons were: %v", got[0].Reasons)
	}
}

// Loss of 1.2% on leg C is over the 0.5% bar even though latency is excellent.
func TestEvaluateFailsOnLoss(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 80, 86, 92, 0.1),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-d", 25, 28, 31, 0.0),
		rec(vnTime(1, 20), "C", "vps-d", "landmark-sgp", 20, 23, 26, 1.2),
	}
	got, _ := Evaluate(recs, DefaultThresholds(), 19, 23)
	if got[0].Pass {
		t.Error("Pass = true, want false: 1.2% loss is over the 0.5% bar")
	}
	if !reasonsContain(got[0].Reasons, "loss") {
		t.Errorf("no reason mentions loss; reasons were: %v", got[0].Reasons)
	}
}

// Records outside 19:00-23:00 Vietnam time must be ignored entirely.
func TestEvaluateIgnoresOffPeakRecords(t *testing.T) {
	recs := []probe.Record{
		// 03:00 VN: a flattering baseline that must not be used.
		rec(vnTime(1, 3), "A", "vn-viettel", "landmark-sgp", 30, 32, 34, 0.0),
		rec(vnTime(1, 3), "B", "vn-viettel", "vps-e", 25, 28, 31, 0.0),
		rec(vnTime(1, 3), "C", "vps-e", "landmark-sgp", 20, 23, 26, 0.0),
		// 20:00 VN: the real picture.
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 90, 96, 102, 0.2),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-e", 26, 29, 32, 0.0),
		rec(vnTime(1, 20), "C", "vps-e", "landmark-sgp", 21, 24, 27, 0.0),
	}
	got, _ := Evaluate(recs, DefaultThresholds(), 19, 23)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].BaselineP50Ms != 90 {
		t.Errorf("BaselineP50Ms = %v, want 90: the 03:00 record must be ignored", got[0].BaselineP50Ms)
	}
	if got[0].Runs != 1 {
		t.Errorf("Runs = %d, want 1", got[0].Runs)
	}
}

func TestEvaluateSkipsCandidateWithNoBaseline(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-f", 25, 28, 31, 0.0),
		rec(vnTime(1, 20), "C", "vps-f", "landmark-sgp", 20, 23, 26, 0.0),
	}
	got, _ := Evaluate(recs, relaxedRuns(DefaultThresholds()), 19, 23)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].Pass {
		t.Error("Pass = true without any leg A baseline to compare against")
	}
	// Assert WHY. Without this the run-count floor, or any future check, could
	// satisfy this test while the no-baseline branch quietly stopped working.
	if !reasonsContain(got[0].Reasons, "baseline") {
		t.Errorf("no reason mentions the missing baseline; reasons were: %v", got[0].Reasons)
	}
}

func TestEvaluateEmptyInput(t *testing.T) {
	if got, _ := Evaluate(nil, DefaultThresholds(), 19, 23); len(got) != 0 {
		t.Errorf("got %d candidates from no records, want 0", len(got))
	}
}

// Exactly at the bar must fail: the constraint is "under 10ms", not "10ms or under".
// 6 lost packets in 1200 is exactly 0.5%, which is an ordinary count, not a corner case.
func TestEvaluateFailsExactlyAtEachBar(t *testing.T) {
	for _, c := range []struct {
		name                string
		p50, p95, p99, loss float64
	}{
		{"jitter exactly 10ms", 25, 35, 40, 0.0},
		{"loss exactly 0.5pct", 25, 28, 31, 0.5},
		{"p99 exactly 30ms above p50", 25, 28, 55, 0.0},
	} {
		t.Run(c.name, func(t *testing.T) {
			recs := []probe.Record{
				rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 80, 86, 92, 0.0),
				rec(vnTime(1, 20), "B", "vn-viettel", "vps-x", c.p50, c.p95, c.p99, c.loss),
				rec(vnTime(1, 20), "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0),
			}
			got, _ := Evaluate(recs, relaxedRuns(DefaultThresholds()), 19, 23)
			if len(got) != 1 {
				t.Fatalf("got %d candidates, want 1", len(got))
			}
			if got[0].Pass {
				t.Errorf("Pass = true at exactly the bar; the constraint is strict. Reasons: %v", got[0].Reasons)
			}
		})
	}
}

func TestEvaluateFailsOnTooFewRuns(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 80, 86, 92, 0.0),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-x", 25, 27, 29, 0.0),
		rec(vnTime(1, 20), "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0),
	}
	got, _ := Evaluate(recs, DefaultThresholds(), 19, 23) // MinRuns 20, only 1 run present
	if got[0].Pass {
		t.Error("Pass = true off a single run; a median of one sample is not evidence")
	}
}

func TestEvaluateKeepsLandmarksApart(t *testing.T) {
	recs := []probe.Record{
		// Singapore: the tunnel wins.
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 80, 84, 88, 0.0),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-x", 25, 27, 29, 0.0),
		rec(vnTime(1, 20), "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0),
		// Tokyo: the same VPS loses badly.
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-tyo", 70, 74, 78, 0.0),
		rec(vnTime(1, 20), "C", "vps-x", "landmark-tyo", 90, 92, 94, 0.0),
	}
	got, _ := Evaluate(recs, relaxedRuns(DefaultThresholds()), 19, 23)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2 (one per landmark)", len(got))
	}
	byLandmark := map[string]Candidate{}
	for _, c := range got {
		byLandmark[c.Landmark] = c
	}
	if !byLandmark["landmark-sgp"].Pass {
		t.Errorf("Singapore should pass: %v", byLandmark["landmark-sgp"].Reasons)
	}
	if byLandmark["landmark-tyo"].Pass {
		t.Error("Tokyo should fail; blending it with Singapore would hide that")
	}
}

func TestEvaluateFailsWhenOneLegIsUndersampled(t *testing.T) {
	// Leg B has plenty of runs; leg C has one. Half the verdict would rest on a
	// single sample.
	var recs []probe.Record
	for i := 0; i < 30; i++ {
		recs = append(recs,
			rec(vnTimeAt(1, 20, i), "A", "vn-viettel", "landmark-sgp", 80, 84, 88, 0.0),
			rec(vnTimeAt(1, 20, i), "B", "vn-viettel", "vps-x", 25, 27, 29, 0.0))
	}
	recs = append(recs, rec(vnTime(1, 20), "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0))

	got, _ := Evaluate(recs, DefaultThresholds(), 19, 23) // MinRuns 20
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].Pass {
		t.Error("Pass = true with only one leg-C run; the floor must cover every leg")
	}
	if got[0].Runs != 1 {
		t.Errorf("Runs = %d, want 1: the reported count must be the weaker leg", got[0].Runs)
	}
}

// runs builds n runs of one leg, one per minute so they are n distinct runs.
// bad, when >= 0, is the index of the single run that gets badP95 instead.
func runs(n int, leg, from, to string, p50, p95, p99, loss float64) []probe.Record {
	var out []probe.Record
	for i := 0; i < n; i++ {
		out = append(out, rec(vnTimeAt(1, 20, i), leg, from, to, p50, p95, p99, loss))
	}
	return out
}

// C1: steadiness is gated on the share of runs that breach, not on the worst
// one. Across ~84 runs per leg some run will always be bad; a worst-run gate
// therefore fails almost any real path. One breach in forty is 2.5%, inside the
// 5% allowance, and this tunnel beats the ISP by 35ms.
func TestEvaluatePassesDespiteOneBadRun(t *testing.T) {
	recs := runs(40, "A", "vn-viettel", "landmark-sgp", 80, 84, 88, 0.0)
	recs = append(recs, runs(40, "B", "vn-viettel", "vps-x", 25, 27, 29, 0.0)...)
	recs = append(recs, runs(40, "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0)...)
	// One evening where leg B jittered 14ms, over the 10ms bar.
	recs[40] = rec(vnTimeAt(1, 20, 0), "B", "vn-viettel", "vps-x", 25, 39, 45, 0.0)

	got, _ := Evaluate(recs, DefaultThresholds(), 19, 23)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if !got[0].Pass {
		t.Errorf("Pass = false on one bad run in forty; reasons: %v", got[0].Reasons)
	}
	if got[0].WorstJitterMs != 14 {
		t.Errorf("WorstJitterMs = %v, want 14: the worst run is still reported", got[0].WorstJitterMs)
	}
	if got[0].JitterBreachPct != 2.5 {
		t.Errorf("JitterBreachPct = %v, want 2.5", got[0].JitterBreachPct)
	}
}

// C1, the other side: a path that breaches a quarter of the time is genuinely
// unstable and must still fail.
func TestEvaluateFailsWhenBreachesAreCommon(t *testing.T) {
	recs := runs(40, "A", "vn-viettel", "landmark-sgp", 80, 84, 88, 0.0)
	recs = append(recs, runs(40, "B", "vn-viettel", "vps-x", 25, 27, 29, 0.0)...)
	recs = append(recs, runs(40, "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0)...)
	for i := 0; i < 10; i++ {
		recs[40+i] = rec(vnTimeAt(1, 20, i), "B", "vn-viettel", "vps-x", 25, 39, 45, 0.0)
	}

	got, _ := Evaluate(recs, DefaultThresholds(), 19, 23)
	if got[0].Pass {
		t.Error("Pass = true with a quarter of runs over the jitter bar")
	}
	if got[0].JitterBreachPct != 25 {
		t.Errorf("JitterBreachPct = %v, want 25", got[0].JitterBreachPct)
	}
	if !reasonsContain(got[0].Reasons, "% of runs") {
		t.Errorf("no reason reports the breach share; reasons were: %v", got[0].Reasons)
	}
}

// C2: a run that received nothing has p50 = 0 because there was nothing to take
// a percentile over. Averaging that into the baseline drags it toward zero and
// turns a dead allowlist entry into "the ISP route is instant".
func TestEvaluateExcludesTotalLossRunsFromLatency(t *testing.T) {
	// The allowlist on the landmark was missing this client for the first half of
	// the campaign, so most leg-A runs received nothing. The 25 real samples are
	// the only latency information in the leg.
	recs := runs(25, "A", "vn-viettel", "landmark-sgp", 80, 84, 88, 0.0)
	for i := 0; i < 30; i++ {
		recs = append(recs, lossyRec(vnTimeAt(2, 20, i), "A", "vn-viettel", "landmark-sgp"))
	}
	recs = append(recs, runs(25, "B", "vn-viettel", "vps-x", 25, 27, 29, 0.0)...)
	recs = append(recs, runs(25, "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0)...)

	got, _ := Evaluate(recs, DefaultThresholds(), 19, 23)
	if got[0].BaselineP50Ms != 80 {
		t.Errorf("BaselineP50Ms = %v, want 80: the zero-sample runs must not enter the median", got[0].BaselineP50Ms)
	}
	if got[0].Runs != 25 {
		t.Errorf("Runs = %d, want 25: a run that received nothing is not a latency sample", got[0].Runs)
	}
	if !got[0].Pass {
		t.Errorf("Pass = false; reasons: %v", got[0].Reasons)
	}
}

// C2: total loss on a whole leg is a measurement failure, and must be reported
// as one. The old code produced "tunnel is 45.0ms slower than the ISP route
// (45.0ms vs 0.0ms)" - a broken allowlist wearing the language of a routing
// finding.
func TestEvaluateFailsWhenBaselineIsAllLoss(t *testing.T) {
	var recs []probe.Record
	for i := 0; i < 25; i++ {
		recs = append(recs, lossyRec(vnTimeAt(1, 20, i), "A", "vn-viettel", "landmark-sgp"))
	}
	recs = append(recs, runs(25, "B", "vn-viettel", "vps-x", 25, 27, 29, 0.0)...)
	recs = append(recs, runs(25, "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0)...)

	got, _ := Evaluate(recs, DefaultThresholds(), 19, 23)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	c := got[0]
	if c.Pass {
		t.Error("Pass = true off a baseline that measured nothing")
	}
	if c.HasBaseline || c.HasGain() {
		t.Errorf("HasBaseline = %v, HasGain = %v; neither can be true with no usable leg-A samples", c.HasBaseline, c.HasGain())
	}
	if c.BaselineP50Ms != 0 || c.GainMs != 0 {
		t.Errorf("BaselineP50Ms = %v, GainMs = %v; both must stay uncomputed", c.BaselineP50Ms, c.GainMs)
	}
	if !reasonsContain(c.Reasons, "this is a measurement failure, not a fast route") {
		t.Errorf("no reason names the measurement failure; reasons were: %v", c.Reasons)
	}
	// The operator must not be able to read a routing finding out of this.
	if reasonsContain(c.Reasons, "slower") || reasonsContain(c.Reasons, "vs 0.0ms") {
		t.Errorf("a measurement failure was reported as a route comparison: %v", c.Reasons)
	}
}

// C3: the run floor must cover leg A. A single baseline sample taken during an
// ISP spike is not a baseline, and it is the denominator of the whole thesis.
func TestEvaluateFailsWhenBaselineIsUndersampled(t *testing.T) {
	recs := []probe.Record{
		// One leg-A run, taken while the ISP was having a 400ms moment.
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 400, 404, 408, 0.0),
	}
	recs = append(recs, runs(30, "B", "vn-viettel", "vps-x", 25, 27, 29, 0.0)...)
	recs = append(recs, runs(30, "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0)...)

	got, _ := Evaluate(recs, DefaultThresholds(), 19, 23) // MinRuns 20
	if got[0].Pass {
		t.Errorf("Pass = true off a single baseline sample; gain was %.1fms", got[0].GainMs)
	}
	if got[0].Runs != 1 {
		t.Errorf("Runs = %d, want 1: the count must be the weakest of all three legs", got[0].Runs)
	}
	if !reasonsContain(got[0].Reasons, "too little data") {
		t.Errorf("no reason mentions the run floor; reasons were: %v", got[0].Reasons)
	}
}

// I12: a 2ms edge between two independently measured legs summed against a
// third is noise, not a route advantage.
func TestEvaluateFailsOnThinGainMargin(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 47, 50, 53, 0.0),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-x", 25, 27, 29, 0.0),
		rec(vnTime(1, 20), "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0),
	}
	got, _ := Evaluate(recs, relaxedRuns(DefaultThresholds()), 19, 23)
	if got[0].Pass {
		t.Error("Pass = true on a 2ms gain; the margin is 5ms")
	}
	if !reasonsContain(got[0].Reasons, "margin") {
		t.Errorf("no reason names the margin; reasons were: %v", got[0].Reasons)
	}
}

// I13: duplication can only ever manufacture a PASS, because the run count is
// the one aggregate a repeated value moves.
func TestEvaluateDropsDuplicateRecords(t *testing.T) {
	recs := runs(10, "A", "vn-viettel", "landmark-sgp", 80, 84, 88, 0.0)
	recs = append(recs, runs(10, "B", "vn-viettel", "vps-x", 25, 27, 29, 0.0)...)
	recs = append(recs, runs(10, "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0)...)
	doubled := append(append([]probe.Record{}, recs...), recs...)

	got, dupes := Evaluate(doubled, DefaultThresholds(), 19, 23) // MinRuns 20
	if dupes != 30 {
		t.Errorf("dupes = %d, want 30", dupes)
	}
	if got[0].Runs != 10 {
		t.Errorf("Runs = %d, want 10: the same file read twice is still ten runs", got[0].Runs)
	}
	if got[0].Pass {
		t.Error("Pass = true off ten runs counted twice against a floor of twenty")
	}
}

// A (VPS, landmark) pair nobody measured is not a finding. With three ISPs,
// three VPSes and two landmarks, emitting a row per unmeasured combination
// buries the rows the runbook tells the operator to read.
func TestEvaluateSuppressesUnmeasuredPairs(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 80, 84, 88, 0.0),
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-tyo", 70, 74, 78, 0.0),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-x", 25, 27, 29, 0.0),
		rec(vnTime(1, 20), "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0),
	}
	got, _ := Evaluate(recs, relaxedRuns(DefaultThresholds()), 19, 23)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1: vps-x was never measured against landmark-tyo", len(got))
	}
	if got[0].Landmark != "landmark-sgp" {
		t.Errorf("Landmark = %q, want landmark-sgp", got[0].Landmark)
	}
}

// A file with leg A and leg C but no leg B has nothing to judge. The CLI turns
// an empty candidate list into "could not measure", never into a NO-GO.
func TestEvaluateReturnsNothingWithoutLegB(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 80, 84, 88, 0.0),
		rec(vnTime(1, 20), "C", "vps-x", "landmark-sgp", 20, 22, 24, 0.0),
	}
	if got, _ := Evaluate(recs, relaxedRuns(DefaultThresholds()), 19, 23); len(got) != 0 {
		t.Errorf("got %d candidates with no leg B, want 0", len(got))
	}
}
