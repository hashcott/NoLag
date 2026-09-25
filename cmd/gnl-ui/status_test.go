package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gamenolag/internal/client/ipc"
)

func TestASilentServiceIsNamedAsSuch(t *testing.T) {
	// The two failures somebody will actually hit. Anything else is shown as it
	// came, because inventing wording for an unknown fault hides it.
	if got := friendly(fmt.Errorf("wrapped: %w", ipc.ErrNoService)); got != "The GameNoLag service is not running" {
		t.Errorf("no-service reads as %q", got)
	}
	if got := friendly(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)); got != "The service is not answering" {
		t.Errorf("timeout reads as %q", got)
	}
	if got := friendly(fmt.Errorf("something specific went wrong")); got != "something specific went wrong" {
		t.Errorf("an unknown error was rewritten to %q", got)
	}
}

func TestSummaryNamesTheRelayAndTheRoundTrip(t *testing.T) {
	got := summary(ipc.Response{OK: true, State: "connected", RelayID: "sgn-01", TunnelRTTms: 23.7}, nil)
	if !strings.Contains(got, "sgn-01") || !strings.Contains(got, "24 ms") {
		t.Fatalf("summary = %q", got)
	}
}

func TestSummaryOfAFaultWhileDisconnected(t *testing.T) {
	got := summary(ipc.Response{State: "disconnected", Error: "no relay answered"}, nil)
	if !strings.Contains(got, "no relay answered") {
		t.Fatalf("the reason was dropped: %q", got)
	}
}

func TestTooltipSaysWhetherAnythingIsBeingRouted(t *testing.T) {
	// Connected with no game running means no game routes are installed and
	// nothing is going through the relay. An interface that says only
	// "Connected" there invites somebody to conclude it is not working.
	idle := tooltip(ipc.Response{OK: true, State: "connected", RelayID: "sgn-01"}, nil)
	if !strings.Contains(idle, "nothing is being routed") {
		t.Errorf("idle tooltip = %q", idle)
	}
	playing := tooltip(ipc.Response{OK: true, State: "connected", RelayID: "sgn-01", GameRunning: "pubg"}, nil)
	if !strings.Contains(playing, "pubg") {
		t.Errorf("playing tooltip = %q", playing)
	}
}

func TestTooltipIsCutOnARuneBoundary(t *testing.T) {
	// A relay id or game name may be non-ASCII, and a cut through the middle of a
	// rune reaches Windows as a broken string.
	long := ipc.Response{OK: true, State: "connected", RelayID: strings.Repeat("đ", 200)}
	got := tooltip(long, nil)
	if n := len([]rune(got)); n > maxTooltip {
		t.Fatalf("tooltip is %d runes, past the %d Windows shows", n, maxTooltip)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated tooltip does not say it was cut: %q", got)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("the cut fell inside a rune")
	}
}

func TestTheIconFollowsWhatTheServiceSaid(t *testing.T) {
	cases := []struct {
		name string
		resp ipc.Response
		err  error
		want state
	}{
		{"service unreachable", ipc.Response{}, ipc.ErrNoService, stateOff},
		{"idle", ipc.Response{OK: true, State: "disconnected"}, nil, stateOff},
		{"connected", ipc.Response{OK: true, State: "connected", RelayID: "sgn-01"}, nil, stateOn},
		// A green icon over a complaint is the one outcome that actively misleads:
		// the tunnel is up and not carrying what the player thinks it is.
		{"connected but complaining", ipc.Response{State: "connected", Error: "routes failed"}, nil, stateFault},
	}
	for _, c := range cases {
		if got := iconFor(c.resp, c.err); got != c.want {
			t.Errorf("%s: icon = %d, want %d", c.name, got, c.want)
		}
	}
}
