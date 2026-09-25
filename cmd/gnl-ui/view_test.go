package main

import (
	"testing"

	"gamenolag/internal/client/ipc"
)

func TestViewForEachState(t *testing.T) {
	cases := []struct {
		name   string
		resp   ipc.Response
		st     state
		tag    string
		accent rgb
		verb   ipc.Verb
	}{
		{"connected", ipc.Response{State: "connected", RelayID: "rly-a"}, stateOn, "UP", accentOn, ipc.VerbDisconnect},
		{"fault", ipc.Response{State: "connected", Error: "route apply failed"}, stateFault, "FAULT", accentFault, ipc.VerbDisconnect},
		{"idle", ipc.Response{}, stateOff, "OFF", accentOff, ipc.VerbConnect},
	}
	seen := map[rgb]bool{}
	for _, c := range cases {
		v := viewOf(c.resp, nil)
		if v.state != c.st || v.tag != c.tag || v.accent != c.accent || v.verb != c.verb {
			t.Errorf("%s: got state %d tag %q accent %v verb %q", c.name, v.state, v.tag, v.accent, v.verb)
		}
		if !v.canAct {
			t.Errorf("%s: button disabled while the service answers", c.name)
		}
		seen[v.accent] = true
	}
	if len(seen) != 3 {
		t.Error("two states share an accent colour")
	}
}

func TestViewWhenServiceIsDown(t *testing.T) {
	v := viewOf(ipc.Response{}, ipc.ErrNoService)
	if v.state != stateOff || v.canAct {
		t.Errorf("state %d canAct %v, want off and disabled", v.state, v.canAct)
	}
	if v.line != friendly(ipc.ErrNoService) {
		t.Errorf("line %q, want the friendly error", v.line)
	}
	if v.rtt != "--" || v.relay != "--" {
		t.Errorf("rtt %q relay %q, want placeholders rather than stale numbers", v.rtt, v.relay)
	}
}

func TestViewFormatsAConnection(t *testing.T) {
	v := viewOf(ipc.Response{
		State: "connected", RelayID: "rly-sg1a", TunnelRTTms: 37.4, LossPct: 0.26,
		ActiveRoutes: 12, GameRunning: "Valorant",
	}, nil)
	want := view{rtt: "37", loss: "0.3%", relay: "rly-sg1a", routes: "12", game: "Valorant → relay"}
	if v.rtt != want.rtt || v.loss != want.loss || v.relay != want.relay || v.routes != want.routes || v.game != want.game {
		t.Errorf("got rtt %q loss %q relay %q routes %q game %q", v.rtt, v.loss, v.relay, v.routes, v.game)
	}
}

func TestGameIsNotShownAsRoutedWhenIdle(t *testing.T) {
	// Saying "→ relay" while disconnected would tell somebody their game is
	// protected when it is not.
	v := viewOf(ipc.Response{GameRunning: "Valorant"}, nil)
	if v.game != "Valorant" {
		t.Errorf("game = %q, want the bare name", v.game)
	}
}

func TestViewKeepsNonASCIINames(t *testing.T) {
	v := viewOf(ipc.Response{State: "connected", RelayID: "rly-hà-nội", GameRunning: "原神"}, nil)
	if v.relay != "rly-hà-nội" || v.game != "原神 → relay" {
		t.Errorf("relay %q game %q", v.relay, v.game)
	}
}

func TestFaultCarriesItsReason(t *testing.T) {
	v := viewOf(ipc.Response{State: "connected", Error: "route apply failed"}, nil)
	if v.problem != "route apply failed" {
		t.Errorf("problem = %q", v.problem)
	}
	if v := viewOf(ipc.Response{State: "connected"}, nil); v.problem != "" {
		t.Errorf("healthy connection has problem %q", v.problem)
	}
}
