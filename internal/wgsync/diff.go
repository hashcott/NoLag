// Package wgsync reconciles the peers a relay currently holds with the peers the
// control plane says it should hold.
//
// The diff is a pure function with no kernel access, so the reconciliation logic
// is proved correct in tests that run anywhere. apply.go only carries out what
// diff.go decided.
package wgsync

import "gamenolag/internal/api"

// Change is the work needed to move from the current peer set to the desired one.
type Change struct {
	Add    []api.Peer
	Update []api.Peer // same public key, different inner address
	Remove []string   // public keys
}

// Empty reports whether nothing needs doing.
func (c Change) Empty() bool {
	return len(c.Add) == 0 && len(c.Update) == 0 && len(c.Remove) == 0
}

// Diff compares two peer sets by public key.
//
// A peer whose address changed is an Update, never a Remove followed by an Add.
// Removing and re-adding would tear down the peer's session for no reason, and
// on a relay that means dropping somebody mid-match.
func Diff(current, desired []api.Peer) Change {
	cur := make(map[string]api.Peer, len(current))
	for _, p := range current {
		cur[p.PublicKey] = p
	}
	des := make(map[string]api.Peer, len(desired))
	for _, p := range desired {
		des[p.PublicKey] = p
	}

	var c Change
	for _, want := range desired {
		have, ok := cur[want.PublicKey]
		switch {
		case !ok:
			c.Add = append(c.Add, want)
		case have.InnerIP != want.InnerIP:
			c.Update = append(c.Update, want)
		}
	}
	for _, have := range current {
		if _, ok := des[have.PublicKey]; !ok {
			c.Remove = append(c.Remove, have.PublicKey)
		}
	}
	return c
}
