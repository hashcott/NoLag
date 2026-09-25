package control

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLimiterAllowsUpToTheLimit(t *testing.T) {
	l := NewLimiter(3, time.Hour)
	for i := 1; i <= 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("call %d was refused; the limit is 3", i)
		}
	}
	if l.Allow("a") {
		t.Error("the fourth call was allowed; the limit is 3")
	}
}

func TestLimiterIsPerKey(t *testing.T) {
	l := NewLimiter(1, time.Hour)
	if !l.Allow("a") || !l.Allow("b") {
		t.Error("two different keys must each get their own budget")
	}
	if l.Allow("a") {
		t.Error("key a exceeded its own budget and was allowed")
	}
}

func TestLimiterWindowExpires(t *testing.T) {
	l := NewLimiter(1, time.Hour)
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return base }

	if !l.Allow("a") {
		t.Fatal("first call refused")
	}
	if l.Allow("a") {
		t.Fatal("second call inside the window was allowed")
	}

	l.now = func() time.Time { return base.Add(time.Hour + time.Second) }
	if !l.Allow("a") {
		t.Error("the window elapsed and the key is still refused; a legitimate " +
			"contributor would be locked out permanently")
	}
}

// The map is keyed partly by attacker-chosen input, so it must not grow without
// bound: a limiter that can be turned into memory exhaustion is worse than none.
func TestLimiterIsBounded(t *testing.T) {
	l := NewLimiter(1, time.Hour)
	l.maxKeys = 100
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return base }

	for i := 0; i < 5000; i++ {
		l.Allow(strings.Repeat("x", i%50) + string(rune(i)))
	}
	l.mu.Lock()
	n := len(l.entries)
	l.mu.Unlock()
	if n > l.maxKeys {
		t.Errorf("limiter holds %d keys with a cap of %d", n, l.maxKeys)
	}
}

func TestLimiterIsSafeUnderConcurrency(t *testing.T) {
	l := NewLimiter(1000, time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				l.Allow("shared")
			}
		}()
	}
	wg.Wait()
	// 1000 calls against a limit of 1000: the next must be refused, which only
	// holds if every one of them was counted.
	if l.Allow("shared") {
		t.Error("calls were lost under concurrency; the limit did not bind")
	}
}

// The bucket must never be the whole key. It lives in memory and reaches logs;
// the key itself is a credential.
func TestKeyPrefixDoesNotCarryTheWholeKey(t *testing.T) {
	key := "GNL-VVK6-C2NS-KF7Y-R4T4"
	got := KeyPrefix(key)
	if got != "GNL-VVK6" {
		t.Errorf("KeyPrefix = %q, want the literal prefix and one group", got)
	}
	if strings.Contains(got, "C2NS") || strings.Contains(got, "R4T4") {
		t.Error("the prefix carries later groups of the key")
	}
	if KeyPrefix("short") != "short" {
		t.Error("a short input must not panic or be truncated oddly")
	}
}
