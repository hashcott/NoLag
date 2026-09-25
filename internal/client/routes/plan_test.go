package routes

import (
	"reflect"
	"testing"
)

func TestPlanInstallsGameRoutesOnlyWhileTheGameRuns(t *testing.T) {
	pin := "203.0.113.10/32"
	lobby := []string{"52.1.2.3/32"}
	game := []string{"20.24.48.0/20", "52.139.208.0/20"}

	connected := Want(pin, lobby, nil)
	c := Plan(Table{}, connected)
	if c.AddPin != pin {
		t.Errorf("AddPin = %q, want the relay pin first", c.AddPin)
	}
	if len(c.AddGame) != 0 {
		t.Errorf("AddGame = %v with no game running", c.AddGame)
	}

	playing := Want(pin, lobby, game)
	c = Plan(connected, playing)
	if !reflect.DeepEqual(c.AddGame, game) {
		t.Errorf("AddGame = %v, want %v", c.AddGame, game)
	}
	if len(c.AddLobby) != 0 || len(c.DelLobby) != 0 {
		t.Errorf("the lobby was disturbed by a game starting: +%v -%v", c.AddLobby, c.DelLobby)
	}

	// Game exits. Its routes go; the lobby's stay, because a lobby lives on a
	// different clock and tearing it down between matches drops the player out of
	// the screen they return to.
	c = Plan(playing, connected)
	if len(c.DelGame) != 2 {
		t.Errorf("DelGame = %v, want both game routes removed", c.DelGame)
	}
	if len(c.DelLobby) != 0 {
		t.Errorf("DelLobby = %v; the lobby must survive a match ending", c.DelLobby)
	}
}

func TestPlanIsEmptyWhenNothingChanged(t *testing.T) {
	tbl := Want("203.0.113.10/32", []string{"52.1.2.3/32"}, []string{"20.24.48.0/20"})
	if c := Plan(tbl, tbl); !c.Empty() {
		t.Errorf("Plan = %+v for identical tables", c)
	}
}

// A user must never be left routing traffic into a tunnel that no longer exists.
func TestDisconnectingRemovesEverything(t *testing.T) {
	tbl := Want("203.0.113.10/32", []string{"52.1.2.3/32"}, []string{"20.24.48.0/20"})
	c := Plan(tbl, Disconnected())
	if c.RemovePin == "" {
		t.Error("the relay pin was left behind")
	}
	if len(c.DelLobby) != 1 || len(c.DelGame) != 1 {
		t.Errorf("left routes behind: lobby %v game %v", c.DelLobby, c.DelGame)
	}
	if c.AddPin != "" || len(c.AddGame) != 0 || len(c.AddLobby) != 0 {
		t.Errorf("disconnecting tried to add something: %+v", c)
	}
}

// Changing relay must move the pin, or the tunnel's own packets would be routed
// to the old relay through a route that no longer belongs to it.
func TestChangingRelayMovesThePin(t *testing.T) {
	a := Want("203.0.113.10/32", nil, nil)
	b := Want("198.51.100.20/32", nil, nil)
	c := Plan(a, b)
	if c.AddPin != "198.51.100.20/32" || c.RemovePin != "203.0.113.10/32" {
		t.Errorf("Plan = %+v, want the old pin removed and the new one added", c)
	}
}

func TestOrderIsStableSoPlansCompareEqual(t *testing.T) {
	x := Want("p/32", []string{"b/32", "a/32"}, []string{"d/32", "c/32"})
	y := Want("p/32", []string{"a/32", "b/32"}, []string{"c/32", "d/32"})
	if !reflect.DeepEqual(x, y) {
		t.Errorf("tables built from the same set in a different order differ:\n %+v\n %+v", x, y)
	}
}
