package main

import (
	"fmt"
	"time"

	"gamenolag/internal/client/ipc"
)

type level int

const (
	levelInfo level = iota
	levelWarn
	levelErr
)

type event struct {
	at    time.Time
	text  string
	level level
}

// maxEvents bounds the log. It is for reading back what just happened; the
// service's own log is the record.
const maxEvents = 50

// observed is one poll reduced to the facts whose changes are worth a line.
type observed struct {
	reachable bool
	why       string // friendly(err), when the service did not answer
	connected bool
	relay     string
	game      string
	problem   string
}

func observe(resp ipc.Response, err error) observed {
	if err != nil {
		return observed{why: friendly(err)}
	}
	return observed{
		reachable: true,
		connected: resp.State == "connected",
		relay:     resp.RelayID,
		game:      resp.GameRunning,
		problem:   resp.Error,
	}
}

// changes lists what differs between two consecutive polls, in the order
// somebody reading the log would want to be told it.
func changes(prev, cur observed) []event {
	var out []event
	add := func(l level, format string, a ...any) {
		out = append(out, event{text: fmt.Sprintf(format, a...), level: l})
	}

	if !cur.reachable {
		// Nothing else is known about a poll the service did not answer.
		if prev.reachable {
			add(levelErr, "service lost: %s", cur.why)
		}
		return out
	}
	if !prev.reachable {
		add(levelInfo, "service answering")
		prev = observed{reachable: true}
	}

	switch {
	case cur.connected && !prev.connected:
		add(levelInfo, "connected · relay %s", orDash(cur.relay))
	case !cur.connected && prev.connected && cur.problem != "":
		add(levelWarn, "disconnected: %s", cur.problem)
	case !cur.connected && prev.connected:
		add(levelInfo, "disconnected · traffic on the ordinary path")
	case cur.connected && cur.relay != prev.relay:
		add(levelWarn, "relay %s → %s", orDash(prev.relay), orDash(cur.relay))
	}

	if cur.game != prev.game {
		if prev.game != "" {
			add(levelInfo, "%s stopped", prev.game)
		}
		if cur.game != "" {
			add(levelInfo, "%s running", cur.game)
		}
	}

	if cur.connected {
		switch {
		case cur.problem != "" && cur.problem != prev.problem:
			add(levelErr, "fault: %s", cur.problem)
		case cur.problem == "" && prev.problem != "" && prev.connected:
			add(levelInfo, "fault cleared")
		}
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "--"
	}
	return s
}

// eventLog is what the full window's event panel shows.
type eventLog struct {
	items   []event
	prev    observed
	started bool
}

// record compares one poll with the one before it.
func (l *eventLog) record(at time.Time, resp ipc.Response, err error) {
	cur := observe(resp, err)
	prev := l.prev
	if !l.started {
		// The first poll is compared with a service that answered and was idle,
		// so it logs a connection or an outage and stays quiet about idling.
		prev, l.started = observed{reachable: true}, true
	}
	for _, e := range changes(prev, cur) {
		e.at = at
		l.push(e)
	}
	l.prev = cur
}

// note adds a line that did not come from a poll, such as a button that failed.
func (l *eventLog) note(at time.Time, lv level, text string) {
	l.push(event{at: at, text: text, level: lv})
}

func (l *eventLog) push(e event) {
	if len(l.items) == maxEvents {
		copy(l.items, l.items[1:])
		l.items = l.items[:maxEvents-1]
	}
	l.items = append(l.items, e)
}

// recent returns the newest n events, oldest first.
func (l *eventLog) recent(n int) []event {
	if n > len(l.items) {
		n = len(l.items)
	}
	return l.items[len(l.items)-n:]
}
