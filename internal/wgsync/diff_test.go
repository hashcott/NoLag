package wgsync

import (
	"sort"
	"testing"

	"gamenolag/internal/api"
)

func peers(pairs ...string) []api.Peer {
	var out []api.Peer
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, api.Peer{PublicKey: pairs[i], InnerIP: pairs[i+1]})
	}
	return out
}

func keysOf(ps []api.Peer) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.PublicKey)
	}
	sort.Strings(out)
	return out
}

func eq(t *testing.T, name string, got, want []string) {
	t.Helper()
	sort.Strings(want)
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", name, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s = %v, want %v", name, got, want)
			return
		}
	}
}

func TestDiffAddsNewPeers(t *testing.T) {
	c := Diff(nil, peers("k1", "10.77.0.5/32", "k2", "10.77.0.6/32"))
	eq(t, "Add", keysOf(c.Add), []string{"k1", "k2"})
	if len(c.Update) != 0 || len(c.Remove) != 0 {
		t.Errorf("Update = %v, Remove = %v; want both empty", c.Update, c.Remove)
	}
}

func TestDiffRemovesPeersNoLongerDesired(t *testing.T) {
	c := Diff(peers("k1", "10.77.0.5/32", "k2", "10.77.0.6/32"), peers("k1", "10.77.0.5/32"))
	sort.Strings(c.Remove)
	eq(t, "Remove", c.Remove, []string{"k2"})
	if len(c.Add) != 0 || len(c.Update) != 0 {
		t.Errorf("Add = %v, Update = %v; want both empty", c.Add, c.Update)
	}
}

func TestDiffUpdatesChangedAddress(t *testing.T) {
	c := Diff(peers("k1", "10.77.0.5/32"), peers("k1", "10.77.0.9/32"))
	if len(c.Update) != 1 || c.Update[0].InnerIP != "10.77.0.9/32" {
		t.Fatalf("Update = %+v, want k1 at 10.77.0.9/32", c.Update)
	}
	if len(c.Add) != 0 || len(c.Remove) != 0 {
		t.Errorf("a moved peer must be an Update, not an Add plus a Remove: Add=%v Remove=%v", c.Add, c.Remove)
	}
}

func TestDiffIsEmptyWhenStatesMatch(t *testing.T) {
	p := peers("k1", "10.77.0.5/32", "k2", "10.77.0.6/32")
	c := Diff(p, p)
	if !c.Empty() {
		t.Errorf("Empty = false for identical states: %+v", c)
	}
}

func TestDiffIgnoresOrdering(t *testing.T) {
	cur := peers("k1", "10.77.0.5/32", "k2", "10.77.0.6/32")
	des := peers("k2", "10.77.0.6/32", "k1", "10.77.0.5/32")
	if c := Diff(cur, des); !c.Empty() {
		t.Errorf("ordering must not produce a change: %+v", c)
	}
}

func TestDiffMixedCase(t *testing.T) {
	cur := peers("keep", "10.77.0.1/32", "move", "10.77.0.2/32", "drop", "10.77.0.3/32")
	des := peers("keep", "10.77.0.1/32", "move", "10.77.0.9/32", "new", "10.77.0.4/32")
	c := Diff(cur, des)
	eq(t, "Add", keysOf(c.Add), []string{"new"})
	eq(t, "Update", keysOf(c.Update), []string{"move"})
	sort.Strings(c.Remove)
	eq(t, "Remove", c.Remove, []string{"drop"})
}

func TestDiffEmptyDesiredRemovesEverything(t *testing.T) {
	c := Diff(peers("k1", "10.77.0.5/32", "k2", "10.77.0.6/32"), nil)
	sort.Strings(c.Remove)
	eq(t, "Remove", c.Remove, []string{"k1", "k2"})
}

func TestDiffBothEmpty(t *testing.T) {
	if c := Diff(nil, nil); !c.Empty() {
		t.Errorf("Empty = false for two empty states: %+v", c)
	}
}
