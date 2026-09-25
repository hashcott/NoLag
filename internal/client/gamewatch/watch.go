// Package gamewatch decides when a game's routes should be in the routing table.
//
// It works from a list of running process names — exactly what Task Manager
// shows, through a public API. It never opens a handle into the game, reads its
// memory, hooks it, or injects anything. That is a hard constraint of this
// project and the reason an anti-cheat has nothing to object to: the client is
// never in the game's address space, only in the network stack beneath it.
package gamewatch

import "strings"

// Game is one title this client knows how to route for.
type Game struct {
	ID string
	// ProcessNames are matched case-insensitively against the running set.
	ProcessNames []string
}

// State is which game, if any, should have routes installed right now.
type State struct {
	GameID  string
	Process string
}

// Running reports whether any game is up.
func (s State) Running() bool { return s.GameID != "" }

// Watcher tracks which game is running across polls.
type Watcher struct {
	games []Game
	cur   State
}

func New(games []Game) *Watcher { return &Watcher{games: games} }

// Current returns the state as of the last Poll.
func (w *Watcher) Current() State { return w.cur }

// Poll takes the currently running process names and reports the new state and
// whether it changed.
//
// Changed is compared on the game AND the process name, not merely on whether
// something is running. Closing one game and opening another between two polls
// would otherwise look like no change at all, and the first game's routes would
// stay installed while the second one played — sending its traffic to a relay
// chosen for somewhere else, or nowhere.
func (w *Watcher) Poll(running []string) (State, bool) {
	next := w.match(running)
	changed := next != w.cur
	w.cur = next
	return next, changed
}

func (w *Watcher) match(running []string) State {
	set := make(map[string]bool, len(running))
	for _, p := range running {
		set[normalise(p)] = true
	}
	// Deterministic: games are checked in the order given, so two games running at
	// once resolve the same way every poll rather than flapping between them.
	for _, g := range w.games {
		for _, name := range g.ProcessNames {
			if set[normalise(name)] {
				return State{GameID: g.ID, Process: name}
			}
		}
	}
	return State{}
}

// normalise makes process comparison case-insensitive and tolerant of the .exe
// suffix being present or absent, because the profile and the operating system
// do not always agree about it.
func normalise(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.TrimSuffix(n, ".exe")
}
