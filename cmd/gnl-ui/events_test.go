package main

import (
	"fmt"
	"strings"
	"testing"

	"gamenolag/internal/client/ipc"
)

type poll struct {
	resp ipc.Response
	err  error
}

var (
	idle   = poll{resp: ipc.Response{OK: true}}
	onA    = poll{resp: ipc.Response{OK: true, State: "connected", RelayID: "rly-a"}}
	down   = poll{err: ipc.ErrNoService}
	onB    = poll{resp: ipc.Response{OK: true, State: "connected", RelayID: "rly-b"}}
	onGame = poll{resp: ipc.Response{OK: true, State: "connected", RelayID: "rly-a", GameRunning: "Valorant"}}
	onBad  = poll{resp: ipc.Response{OK: true, State: "connected", RelayID: "rly-a", Error: "route apply failed"}}
	offWhy = poll{resp: ipc.Response{OK: true, Error: "relay unreachable"}}
)

// replay records polls in order and returns the texts logged by the last one.
func replay(ps ...poll) []event {
	var l eventLog
	for i, p := range ps {
		before := len(l.items)
		l.record(t0, p.resp, p.err)
		if i == len(ps)-1 {
			return append([]event(nil), l.items[before:]...)
		}
	}
	return nil
}

func texts(es []event) string {
	var s []string
	for _, e := range es {
		s = append(s, e.text)
	}
	return strings.Join(s, " | ")
}

func TestFirstPollLogsOnlyWhatIsWorthSaying(t *testing.T) {
	if es := replay(idle); len(es) != 0 {
		t.Errorf("an idle first poll logged %q", texts(es))
	}
	if es := replay(onA); len(es) != 1 || !strings.Contains(es[0].text, "connected") {
		t.Errorf("a connected first poll logged %q, want one connected line", texts(es))
	}
	if es := replay(down); len(es) != 1 || es[0].level != levelErr {
		t.Errorf("an unreachable first poll logged %q, want one error", texts(es))
	}
}

func TestSteadyStateLogsNothing(t *testing.T) {
	// Every 3 s for hours: anything logged on a poll where nothing changed buries
	// the line that mattered.
	for name, p := range map[string]poll{"idle": idle, "connected": onGame, "fault": onBad, "down": down} {
		if es := replay(p, p, p); len(es) != 0 {
			t.Errorf("%s: a repeated poll logged %q", name, texts(es))
		}
	}
}

func TestEachTransitionLogsOneLine(t *testing.T) {
	cases := []struct {
		name     string
		from, to poll
		want     string
		level    level
	}{
		{"connect", idle, onA, "connected · relay rly-a", levelInfo},
		{"disconnect", onA, idle, "disconnected", levelInfo},
		{"disconnect with a reason", onA, offWhy, "disconnected: relay unreachable", levelWarn},
		{"relay change", onA, onB, "relay rly-a → rly-b", levelWarn},
		{"game start", onA, onGame, "Valorant running", levelInfo},
		{"game stop", onGame, onA, "Valorant stopped", levelInfo},
		{"fault", onA, onBad, "fault: route apply failed", levelErr},
		{"fault cleared", onBad, onA, "fault cleared", levelInfo},
		{"service lost", idle, down, "service lost: " + friendly(ipc.ErrNoService), levelErr},
		{"service back", down, idle, "service answering", levelInfo},
	}
	for _, c := range cases {
		es := replay(c.from, c.to)
		if len(es) != 1 {
			t.Errorf("%s: logged %d lines %q, want 1", c.name, len(es), texts(es))
			continue
		}
		if !strings.HasPrefix(es[0].text, c.want) || es[0].level != c.level {
			t.Errorf("%s: logged %q at level %d, want %q at %d", c.name, es[0].text, es[0].level, c.want, c.level)
		}
	}
}

func TestEventLogIsCapped(t *testing.T) {
	var l eventLog
	for i := 0; i < maxEvents+20; i++ {
		l.note(t0, levelInfo, fmt.Sprint(i))
	}
	if len(l.items) != maxEvents {
		t.Fatalf("holds %d events, want %d", len(l.items), maxEvents)
	}
	r := l.recent(3)
	if texts(r) != fmt.Sprintf("%d | %d | %d", maxEvents+17, maxEvents+18, maxEvents+19) {
		t.Errorf("recent(3) = %q, want the newest three oldest first", texts(r))
	}
	if got := len(l.recent(maxEvents + 5)); got != maxEvents {
		t.Errorf("recent past the cap returned %d, want %d", got, maxEvents)
	}
}
