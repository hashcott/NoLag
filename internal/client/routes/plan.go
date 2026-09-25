// Package routes decides what belongs in the Windows routing table.
//
// The whole design rests on a distinction that is easy to lose: WireGuard's
// AllowedIPs is a cryptographic ACL and stays fixed, while the routing table is
// what actually decides which packets enter the tunnel. Game routes go in when
// the game starts and come out when it stops, because the address ranges belong
// to AWS and Azure and are shared with thousands of unrelated services — leaving
// them installed would drag other applications' traffic through a relay that
// exists to carry game packets.
//
// Nothing here calls the operating system. The plan is computed, compared and
// tested here; applying it is a separate, thin layer.
package routes

import "sort"

// Table is the set of prefixes that should be routed into the tunnel, split by
// the clock each one lives on.
type Table struct {
	// RelayPin is the /32 for the relay's own address, routed through the
	// PHYSICAL adapter. Without it the packets carrying the tunnel would be routed
	// into the tunnel, which is a loop that takes the machine's connectivity with
	// it.
	RelayPin string
	// Lobby routes are installed while connected and stay through a game exiting.
	// A lobby lives on a different clock from a match: tearing its routes down
	// between games would drop the player out of the very screen they return to.
	Lobby []string
	// Game routes exist only while the game process is running.
	Game []string
}

// Change is the work needed to move the table from one state to another.
type Change struct {
	AddPin    string
	RemovePin string
	AddLobby  []string
	DelLobby  []string
	AddGame   []string
	DelGame   []string
}

// Empty reports whether nothing needs doing.
func (c Change) Empty() bool {
	return c.AddPin == "" && c.RemovePin == "" &&
		len(c.AddLobby) == 0 && len(c.DelLobby) == 0 &&
		len(c.AddGame) == 0 && len(c.DelGame) == 0
}

// Plan computes the change from the table currently installed to the one wanted.
//
// Ordering matters when this is applied and is expressed by the field order: the
// pin goes in before anything else and comes out last. A game route installed
// before the pin would briefly send the tunnel's own packets into the tunnel.
func Plan(current, want Table) Change {
	var c Change
	if current.RelayPin != want.RelayPin {
		if want.RelayPin != "" {
			c.AddPin = want.RelayPin
		}
		if current.RelayPin != "" {
			c.RemovePin = current.RelayPin
		}
	}
	c.AddLobby, c.DelLobby = diff(current.Lobby, want.Lobby)
	c.AddGame, c.DelGame = diff(current.Game, want.Game)
	return c
}

func diff(current, want []string) (add, del []string) {
	cur := set(current)
	wnt := set(want)
	for p := range wnt {
		if !cur[p] {
			add = append(add, p)
		}
	}
	for p := range cur {
		if !wnt[p] {
			del = append(del, p)
		}
	}
	sort.Strings(add)
	sort.Strings(del)
	return add, del
}

func set(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// Want builds the table that should be installed for a given situation.
//
// gameCIDRs are the profile's ranges for the game that is running; pass none
// when no game is up. That is the entire mechanism by which unrelated traffic
// stops flowing through a contributor's relay the moment a player closes the
// game.
func Want(relayPin string, lobby, gameCIDRs []string) Table {
	t := Table{RelayPin: relayPin, Lobby: append([]string(nil), lobby...)}
	if len(gameCIDRs) > 0 {
		t.Game = append([]string(nil), gameCIDRs...)
	}
	sort.Strings(t.Lobby)
	sort.Strings(t.Game)
	return t
}

// Disconnected is the table to aim for when the tunnel is going down: nothing.
//
// Every route this client installs is non-persistent, so a crash or a power cut
// leaves a clean table on the next boot. This is the orderly version of the same
// guarantee: a user must never be left with a machine that routes traffic into a
// tunnel which no longer exists.
func Disconnected() Table { return Table{} }
