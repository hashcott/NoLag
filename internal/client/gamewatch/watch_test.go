package gamewatch

import "testing"

func pubgAndCS2() []Game {
	return []Game{
		{ID: "pubg", ProcessNames: []string{"TslGame.exe", "TslGame_BE.exe"}},
		{ID: "cs2", ProcessNames: []string{"cs2.exe"}},
	}
}

func TestDetectsAGameStartingAndStopping(t *testing.T) {
	w := New(pubgAndCS2())

	if st, changed := w.Poll([]string{"explorer.exe", "chrome.exe"}); st.Running() || changed {
		t.Fatalf("state = %+v changed = %v with no game running", st, changed)
	}
	st, changed := w.Poll([]string{"explorer.exe", "TslGame.exe"})
	if !changed || st.GameID != "pubg" {
		t.Fatalf("state = %+v changed = %v when PUBG started", st, changed)
	}
	if _, changed := w.Poll([]string{"explorer.exe", "TslGame.exe"}); changed {
		t.Error("reported a change while the same game kept running")
	}
	st, changed = w.Poll([]string{"explorer.exe"})
	if !changed || st.Running() {
		t.Fatalf("state = %+v changed = %v when the game exited", st, changed)
	}
}

// Closing one game and opening another between two polls must read as a change.
// Comparing only "is something running" would leave the first game's routes
// installed while the second played, sending its traffic to a relay chosen for
// somewhere else.
func TestSwappingGamesBetweenPollsIsAChange(t *testing.T) {
	w := New(pubgAndCS2())
	if _, changed := w.Poll([]string{"TslGame.exe"}); !changed {
		t.Fatal("PUBG starting was not a change")
	}
	st, changed := w.Poll([]string{"cs2.exe"})
	if !changed {
		t.Error("swapping PUBG for CS2 in one poll was not reported as a change")
	}
	if st.GameID != "cs2" {
		t.Errorf("state = %+v, want cs2", st)
	}
}

// The profile and the operating system do not always agree about the .exe
// suffix or its case.
func TestProcessMatchingIsCaseAndSuffixTolerant(t *testing.T) {
	w := New([]Game{{ID: "pubg", ProcessNames: []string{"TslGame.exe"}}})
	for _, running := range [][]string{
		{"TslGame.exe"}, {"tslgame.exe"}, {"TSLGAME.EXE"}, {"TslGame"},
	} {
		w2 := New([]Game{{ID: "pubg", ProcessNames: []string{"TslGame.exe"}}})
		if st, _ := w2.Poll(running); !st.Running() {
			t.Errorf("did not match %v", running)
		}
	}
	_ = w
}

// Two games running at once must resolve the same way every poll, or the routes
// would flap between them.
func TestTwoGamesAtOnceResolveDeterministically(t *testing.T) {
	w := New(pubgAndCS2())
	first, _ := w.Poll([]string{"cs2.exe", "TslGame.exe"})
	for i := 0; i < 5; i++ {
		st, changed := w.Poll([]string{"TslGame.exe", "cs2.exe"})
		if st.GameID != first.GameID {
			t.Fatalf("resolved to %s then %s; the choice must be stable", first.GameID, st.GameID)
		}
		if changed {
			t.Error("reported a change while both games kept running")
		}
	}
}

// The second process name of a game is the anti-cheat's own launcher; matching
// it matters because that is what is running during a match.
func TestMatchesAnySecondaryProcessName(t *testing.T) {
	w := New(pubgAndCS2())
	st, changed := w.Poll([]string{"TslGame_BE.exe"})
	if !changed || st.GameID != "pubg" {
		t.Errorf("state = %+v, want pubg via its BattlEye process", st)
	}
}
