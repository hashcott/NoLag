package analyze

import (
	"testing"
	"time"

	"gamenolag/internal/probe"
	"gamenolag/internal/stats"
)

// vnTime builds a UTC timestamp for the given Vietnam (UTC+7) wall-clock hour.
func vnTime(day, hourVN int) time.Time {
	loc := time.FixedZone("ICT", 7*3600)
	return time.Date(2026, 9, day, hourVN, 0, 0, 0, loc).UTC()
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

// Baseline 80ms; tunnel is 25 + 20 = 45ms. Clear win, clean legs.
func TestEvaluatePassesAClearWin(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 80, 86, 92, 0.1),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-a", 25, 28, 31, 0.05),
		rec(vnTime(1, 20), "C", "vps-a", "landmark-sgp", 20, 23, 26, 0.0),
	}
	got := Evaluate(recs, relaxedRuns(DefaultThresholds()), 19, 23)
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
	got := Evaluate(recs, DefaultThresholds(), 19, 23)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].Pass {
		t.Error("Pass = true, want false: 18ms jitter is over the 10ms bar")
	}
	if len(got[0].Reasons) == 0 {
		t.Error("a failing candidate must say why")
	}
}

// Tunnel is slower than the ISP's own route.
func TestEvaluateFailsWhenTunnelIsSlower(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 60, 64, 70, 0.1),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-c", 40, 43, 46, 0.0),
		rec(vnTime(1, 20), "C", "vps-c", "landmark-sgp", 35, 38, 41, 0.0),
	}
	got := Evaluate(recs, DefaultThresholds(), 19, 23)
	if got[0].Pass {
		t.Error("Pass = true, want false: 75ms tunnel against a 60ms baseline")
	}
	if got[0].GainMs != -15 {
		t.Errorf("GainMs = %v, want -15", got[0].GainMs)
	}
}

// Loss of 1.2% on leg C is over the 0.5% bar even though latency is excellent.
func TestEvaluateFailsOnLoss(t *testing.T) {
	recs := []probe.Record{
		rec(vnTime(1, 20), "A", "vn-viettel", "landmark-sgp", 80, 86, 92, 0.1),
		rec(vnTime(1, 20), "B", "vn-viettel", "vps-d", 25, 28, 31, 0.0),
		rec(vnTime(1, 20), "C", "vps-d", "landmark-sgp", 20, 23, 26, 1.2),
	}
	got := Evaluate(recs, DefaultThresholds(), 19, 23)
	if got[0].Pass {
		t.Error("Pass = true, want false: 1.2% loss is over the 0.5% bar")
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
	got := Evaluate(recs, DefaultThresholds(), 19, 23)
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
	got := Evaluate(recs, DefaultThresholds(), 19, 23)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].Pass {
		t.Error("Pass = true without any leg A baseline to compare against")
	}
}

func TestEvaluateEmptyInput(t *testing.T) {
	if got := Evaluate(nil, DefaultThresholds(), 19, 23); len(got) != 0 {
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
			got := Evaluate(recs, relaxedRuns(DefaultThresholds()), 19, 23)
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
	got := Evaluate(recs, DefaultThresholds(), 19, 23) // MinRuns 20, only 1 run present
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
	got := Evaluate(recs, relaxedRuns(DefaultThresholds()), 19, 23)
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
