package pick

import (
	"errors"
	"testing"
	"time"
)

func ms(id string, d time.Duration) Measurement { return Measurement{RelayID: id, RTT: d} }

func TestBestPicksFastestAndKeepsAFallback(t *testing.T) {
	c, err := Best([]Measurement{
		ms("slow", 80*time.Millisecond),
		ms("fast", 25*time.Millisecond),
		ms("mid", 40*time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Primary != "fast" {
		t.Errorf("Primary = %q, want fast", c.Primary)
	}
	// A fallback matters because the primary can stop answering mid-session, and
	// re-measuring everything at that moment is the worst time to do it.
	if c.Fallback != "mid" {
		t.Errorf("Fallback = %q, want the second fastest", c.Fallback)
	}
}

func TestBestIgnoresRelaysThatDidNotAnswer(t *testing.T) {
	c, err := Best([]Measurement{
		{RelayID: "dead", Err: errors.New("timeout")},
		{RelayID: "silent", RTT: 0},
		ms("alive", 30*time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Primary != "alive" || c.Fallback != "" {
		t.Errorf("Choice = %+v, want only the one that answered", c)
	}
}

func TestBestFailsWhenNothingAnswers(t *testing.T) {
	_, err := Best([]Measurement{
		{RelayID: "a", Err: errors.New("timeout")},
		{RelayID: "b", Err: errors.New("refused")},
	})
	if !errors.Is(err, ErrNoneAnswered) {
		t.Errorf("err = %v, want ErrNoneAnswered", err)
	}
}

// Equal measurements must resolve identically every time. Otherwise a client
// oscillates between two equally fast relays on successive connects, changing
// its inner address and reinstalling every route each time for no gain.
func TestTiesBreakDeterministically(t *testing.T) {
	first, _ := Best([]Measurement{ms("bravo", 30*time.Millisecond), ms("alpha", 30*time.Millisecond)})
	for i := 0; i < 10; i++ {
		c, _ := Best([]Measurement{ms("alpha", 30*time.Millisecond), ms("bravo", 30*time.Millisecond)})
		if c.Primary != first.Primary {
			t.Fatalf("tie resolved to %q then %q", first.Primary, c.Primary)
		}
	}
}

// Switching re-addresses the adapter and reinstalls every route, which a player
// feels. A few milliseconds is inside the noise of two separate measurements.
func TestWorthSwitchingIgnoresNoise(t *testing.T) {
	cur := 40 * time.Millisecond
	if WorthSwitching(cur, 37*time.Millisecond, DefaultSwitchMargin) {
		t.Error("switched for 3ms, which is inside the spread of two handshake measurements")
	}
	if !WorthSwitching(cur, 25*time.Millisecond, DefaultSwitchMargin) {
		t.Error("refused to switch for a 15ms improvement")
	}
	if !WorthSwitching(0, 90*time.Millisecond, DefaultSwitchMargin) {
		t.Error("refused to connect when nothing was in use")
	}
}

// A slower alternative must never be taken, whatever the margin.
func TestNeverSwitchesToSomethingWorse(t *testing.T) {
	if WorthSwitching(30*time.Millisecond, 50*time.Millisecond, DefaultSwitchMargin) {
		t.Error("switched to a slower relay")
	}
}
