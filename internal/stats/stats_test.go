package stats

import (
	"math"
	"testing"
	"time"
)

func closeTo(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.001 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	s := Summarize(10, nil)
	if s.Sent != 10 || s.Received != 0 {
		t.Fatalf("sent/received = %d/%d, want 10/0", s.Sent, s.Received)
	}
	closeTo(t, "LossPct", s.LossPct, 100)
	closeTo(t, "P50Ms", s.P50Ms, 0)
	closeTo(t, "JitterMs", s.JitterMs, 0)
}

func TestSummarizeSingleSample(t *testing.T) {
	s := Summarize(1, []time.Duration{25 * time.Millisecond})
	closeTo(t, "LossPct", s.LossPct, 0)
	closeTo(t, "P50Ms", s.P50Ms, 25)
	closeTo(t, "P95Ms", s.P95Ms, 25)
	closeTo(t, "P99Ms", s.P99Ms, 25)
	closeTo(t, "JitterMs", s.JitterMs, 0)
}

// 100 samples of 1..100 ms. Nearest rank: index = ceil(p/100*100)-1.
// p50 -> index 49 -> 50 ms. p95 -> index 94 -> 95 ms. p99 -> index 98 -> 99 ms.
func TestSummarizePercentiles(t *testing.T) {
	var rtts []time.Duration
	for i := 100; i >= 1; i-- { // deliberately unsorted input
		rtts = append(rtts, time.Duration(i)*time.Millisecond)
	}
	s := Summarize(100, rtts)
	closeTo(t, "P50Ms", s.P50Ms, 50)
	closeTo(t, "P95Ms", s.P95Ms, 95)
	closeTo(t, "P99Ms", s.P99Ms, 99)
	closeTo(t, "JitterMs", s.JitterMs, 45)
}

func TestSummarizeLoss(t *testing.T) {
	var rtts []time.Duration
	for i := 0; i < 90; i++ {
		rtts = append(rtts, 10*time.Millisecond)
	}
	s := Summarize(100, rtts)
	closeTo(t, "LossPct", s.LossPct, 10)
	if s.Received != 90 {
		t.Errorf("Received = %d, want 90", s.Received)
	}
}

func TestSummarizeDoesNotMutateInput(t *testing.T) {
	rtts := []time.Duration{30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}
	_ = Summarize(3, rtts)
	if rtts[0] != 30*time.Millisecond {
		t.Errorf("input was sorted in place: rtts[0] = %v, want 30ms", rtts[0])
	}
}

func TestSummarizeSentZero(t *testing.T) {
	s := Summarize(0, nil)
	closeTo(t, "LossPct", s.LossPct, 0)
}
