package main

import (
	"math"
	"time"

	"gamenolag/internal/client/ipc"
)

// historyLen is how many polls the window remembers: three minutes at pollEvery.
const historyLen = 60

// sample is one poll, reduced to what the chart draws.
type sample struct {
	at   time.Time
	rtt  float64 // milliseconds; meaningful only when ok
	loss float64 // percent
	// ok means connected, answering, and carrying a measurement. Anything else is
	// a gap in the chart, never a zero.
	ok bool
}

func sampleFrom(at time.Time, resp ipc.Response, err error) sample {
	s := sample{at: at}
	if err == nil && resp.State == "connected" && resp.TunnelRTTms > 0 {
		s.rtt, s.loss, s.ok = resp.TunnelRTTms, resp.LossPct, true
	}
	return s
}

// history is a fixed ring of the newest samples. Fixed rather than a growing
// slice: the tray runs for as long as somebody is logged in.
type history struct {
	buf  [historyLen]sample
	next int
	n    int
}

func (h *history) add(s sample) {
	h.buf[h.next] = s
	h.next = (h.next + 1) % historyLen
	if h.n < historyLen {
		h.n++
	}
}

// samples returns what is held, oldest first.
func (h *history) samples() []sample {
	out := make([]sample, 0, h.n)
	start := (h.next - h.n + historyLen) % historyLen
	for i := 0; i < h.n; i++ {
		out = append(out, h.buf[(start+i)%historyLen])
	}
	return out
}

// rttRange is the vertical scale: the measured span with some headroom, and
// never narrower than 10 ms, so a steady 37 ms is drawn as the flat line it is
// rather than as jitter blown up to fill the box.
func rttRange(ss []sample) (lo, hi float64, any bool) {
	for _, s := range ss {
		if !s.ok {
			continue
		}
		if !any {
			lo, hi, any = s.rtt, s.rtt, true
			continue
		}
		lo, hi = math.Min(lo, s.rtt), math.Max(hi, s.rtt)
	}
	if !any {
		return 0, 0, false
	}
	mid, half := (lo+hi)/2, math.Max((hi-lo)/2*1.2, 5)
	lo, hi = mid-half, mid+half
	if lo < 0 {
		hi, lo = hi-lo, 0
	}
	return lo, hi, true
}

type point struct{ X, Y int }

// chart lays the history out in a w×h box as runs of joined points. A poll that
// failed or was not connected ends a run.
//
// The x axis is always the full three minutes with the newest sample on the
// right edge, so a short history is short on the left rather than stretched.
func (h *history) chart(w, ht int) [][]point {
	ss := h.samples()
	lo, hi, any := rttRange(ss)
	if !any || w < 2 || ht < 2 {
		return nil
	}
	var runs [][]point
	var run []point
	offset := historyLen - len(ss)
	for i, s := range ss {
		if !s.ok {
			if run != nil {
				runs, run = append(runs, run), nil
			}
			continue
		}
		x := (offset + i) * (w - 1) / (historyLen - 1)
		y := int(math.Round(float64(ht-1) * (hi - s.rtt) / (hi - lo)))
		run = append(run, point{x, y})
	}
	if run != nil {
		runs = append(runs, run)
	}
	return runs
}

// sparkLevels is how many bar heights the sparkline has.
const sparkLevels = 8

// spark gives the newest n samples as bar levels 0..sparkLevels-1, oldest first.
// A missing measurement is -1, and so is the part not yet filled, so the result
// is always n long and the layout never shifts.
func (h *history) spark(n int) []int {
	ss := h.samples()
	if len(ss) > n {
		ss = ss[len(ss)-n:]
	}
	lo, hi, _ := rttRange(ss)
	out := make([]int, 0, n)
	for i := len(ss); i < n; i++ {
		out = append(out, -1)
	}
	for _, s := range ss {
		if !s.ok {
			out = append(out, -1)
			continue
		}
		k := int((s.rtt - lo) / (hi - lo) * sparkLevels)
		out = append(out, min(max(k, 0), sparkLevels-1))
	}
	return out
}
