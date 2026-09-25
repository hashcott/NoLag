// Package pick chooses which relay to use from measurements the client took
// itself.
//
// The control plane filters — it removes relays that are down, unverified or
// still inside their observation window — but it cannot rank, because it has no
// idea what any individual player's path looks like. A relay that is closest for
// a Viettel subscriber in Hanoi may be the worst for an FPT one in Da Nang. So
// the client measures every candidate and decides locally.
package pick

import (
	"fmt"
	"sort"
	"time"
)

// Measurement is what a handshake against one relay produced.
type Measurement struct {
	RelayID string
	// RTT of the handshake. Zero means it never completed.
	RTT time.Duration
	// Err is why it failed, when it did.
	Err error
}

// Ok reports whether the relay answered.
func (m Measurement) Ok() bool { return m.Err == nil && m.RTT > 0 }

// Choice is the outcome of a selection.
type Choice struct {
	Primary  string
	Fallback string
	// Ranked holds every relay that answered, fastest first.
	Ranked []Measurement
}

// ErrNoneAnswered means no candidate completed a handshake.
var ErrNoneAnswered = fmt.Errorf("pick: no relay answered")

// Best chooses a primary and a fallback.
//
// A fallback is kept because the primary can stop answering mid-session, and
// re-running the whole measurement at that moment — when the network is already
// misbehaving — is the worst time to do it.
//
// Ties break on relay id so the same inputs always give the same answer.
// Otherwise a client could oscillate between two equally fast relays on
// successive connects, changing its inner address and reinstalling every route
// each time for no gain.
func Best(ms []Measurement) (Choice, error) {
	var ok []Measurement
	for _, m := range ms {
		if m.Ok() {
			ok = append(ok, m)
		}
	}
	if len(ok) == 0 {
		return Choice{}, ErrNoneAnswered
	}
	sort.Slice(ok, func(i, j int) bool {
		if ok[i].RTT != ok[j].RTT {
			return ok[i].RTT < ok[j].RTT
		}
		return ok[i].RelayID < ok[j].RelayID
	})
	c := Choice{Primary: ok[0].RelayID, Ranked: ok}
	if len(ok) > 1 {
		c.Fallback = ok[1].RelayID
	}
	return c, nil
}

// WorthSwitching reports whether a newly measured alternative beats the relay in
// use by enough to justify moving.
//
// Switching is not free: it re-addresses the tunnel adapter and reinstalls every
// route, which a player feels. A few milliseconds is inside the noise of two
// separate measurements and is not a reason to do that mid-session — so the
// margin exists to stop the client chasing measurement jitter around the fleet.
func WorthSwitching(current, alternative time.Duration, margin time.Duration) bool {
	if current <= 0 {
		return true // nothing in use, anything that answers is better
	}
	return current-alternative >= margin
}

// DefaultSwitchMargin is the improvement an alternative must show.
//
// Ten milliseconds: large enough to sit outside the spread of two handshake
// measurements on a residential link, small enough that a genuinely better path
// is still taken.
const DefaultSwitchMargin = 10 * time.Millisecond
