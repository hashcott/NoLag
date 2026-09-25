package main

import (
	"errors"
	"testing"
	"time"

	"gamenolag/internal/client/ipc"
)

var t0 = time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC)

func measured(rtt float64) sample { return sample{rtt: rtt, ok: true} }

func TestHistoryKeepsTheNewestInOrder(t *testing.T) {
	// It runs for as long as somebody is logged in, so it must stop growing.
	var h history
	for i := 0; i < historyLen+10; i++ {
		h.add(measured(float64(i)))
	}
	ss := h.samples()
	if len(ss) != historyLen {
		t.Fatalf("holds %d samples, want %d", len(ss), historyLen)
	}
	if ss[0].rtt != 10 || ss[len(ss)-1].rtt != historyLen+9 {
		t.Errorf("oldest %v newest %v, want 10 and %d", ss[0].rtt, ss[len(ss)-1].rtt, historyLen+9)
	}
}

func TestChartBreaksWhereAPollFailed(t *testing.T) {
	// A gap says "no measurement". A line drawn through the failure would say the
	// link was fine, or zero milliseconds, and it was neither.
	var h history
	h.add(measured(30))
	h.add(measured(31))
	h.add(sample{})
	h.add(measured(32))
	runs := h.chart(600, 100)
	if len(runs) != 2 || len(runs[0]) != 2 || len(runs[1]) != 1 {
		t.Fatalf("runs = %v, want two runs of 2 and 1 points", runs)
	}
}

func TestChartStaysInsideTheBox(t *testing.T) {
	var h history
	for i, v := range []float64{12, 900, 35, 36, 1, 250, 37, 37, 38, 2000} {
		if i == 4 {
			h.add(sample{})
		}
		h.add(measured(v))
	}
	const w, ht = 300, 80
	for _, run := range h.chart(w, ht) {
		for _, p := range run {
			if p.X < 0 || p.X >= w || p.Y < 0 || p.Y >= ht {
				t.Errorf("point %v outside %dx%d", p, w, ht)
			}
		}
	}
}

func TestChartPutsNewestOnTheRightEdge(t *testing.T) {
	// Three minutes is always the width, so a short history is short on the left
	// rather than stretched across the box.
	var h history
	h.add(measured(40))
	runs := h.chart(600, 100)
	if len(runs) != 1 || runs[0][0].X != 599 {
		t.Fatalf("runs = %v, want one point at x=599", runs)
	}
}

func TestChartOfNothingMeasuredIsEmpty(t *testing.T) {
	var h history
	h.add(sample{})
	h.add(sample{})
	if runs := h.chart(600, 100); runs != nil {
		t.Errorf("runs = %v, want nil", runs)
	}
}

func TestHigherRTTIsDrawnHigher(t *testing.T) {
	var h history
	h.add(measured(30))
	h.add(measured(90))
	run := h.chart(600, 100)[0]
	if run[1].Y >= run[0].Y {
		t.Errorf("90 ms at y=%d is not above 30 ms at y=%d", run[1].Y, run[0].Y)
	}
}

func TestFlatRTTIsDrawnFlat(t *testing.T) {
	// A steady link must look steady, not like noise stretched to fill the box.
	var h history
	for i := 0; i < 5; i++ {
		h.add(measured(37))
	}
	run := h.chart(600, 100)[0]
	for _, p := range run {
		if p.Y != run[0].Y {
			t.Fatalf("flat RTT drawn at varying heights: %v", run)
		}
	}
	if run[0].Y < 20 || run[0].Y > 80 {
		t.Errorf("flat RTT drawn at y=%d, want it near the middle of 100", run[0].Y)
	}
}

func TestSparkIsAlwaysNLong(t *testing.T) {
	// The mini panel lays the sparkline out in a fixed box; a changing length
	// would make it jump as history fills.
	for _, n := range []int{3, 20} {
		var h history
		for i := 0; i < n; i++ {
			h.add(measured(float64(30 + i)))
			if got := len(h.spark(7)); got != 7 {
				t.Fatalf("after %d samples spark has %d levels, want 7", i+1, got)
			}
		}
	}
}

func TestSparkMarksGapsAndStaysInRange(t *testing.T) {
	var h history
	h.add(measured(30))
	h.add(sample{})
	h.add(measured(90))
	lv := h.spark(5)
	want0 := []int{-1, -1} // not yet filled
	for i, w := range want0 {
		if lv[i] != w {
			t.Errorf("level %d = %d, want %d for an unfilled slot", i, lv[i], w)
		}
	}
	if lv[3] != -1 {
		t.Errorf("failed poll has level %d, want -1", lv[3])
	}
	for i, l := range []int{lv[2], lv[4]} {
		if l < 0 || l > sparkLevels-1 {
			t.Errorf("measured level %d = %d, outside 0..%d", i, l, sparkLevels-1)
		}
	}
	if lv[4] <= lv[2] {
		t.Errorf("90 ms level %d not above 30 ms level %d", lv[4], lv[2])
	}
}

func TestSampleFromCountsOnlyMeasurements(t *testing.T) {
	cases := []struct {
		name string
		resp ipc.Response
		err  error
		ok   bool
	}{
		{"connected", ipc.Response{State: "connected", TunnelRTTms: 37, LossPct: 0.2}, nil, true},
		{"idle", ipc.Response{TunnelRTTms: 37}, nil, false},
		{"no measurement yet", ipc.Response{State: "connected"}, nil, false},
		{"service down", ipc.Response{}, errors.New("no pipe"), false},
	}
	for _, c := range cases {
		s := sampleFrom(t0, c.resp, c.err)
		if s.ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, s.ok, c.ok)
		}
		if s.ok && (s.rtt != 37 || s.loss != 0.2 || !s.at.Equal(t0)) {
			t.Errorf("%s: sample = %+v", c.name, s)
		}
	}
}
