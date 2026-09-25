package control

import (
	"sync"
	"time"
)

// Limiter is a fixed-window counter keyed by an arbitrary string.
//
// Spec 10.2 requires the key-consuming endpoints to be limited by IP and by key
// prefix. It matters more than it looks: a contributor key is 80 bits, and the
// reasoning for spending entropy on typability rather than on length is that
// guessing is rate limited. Without this, that reasoning is not true.
//
// It also bounds the other way an attacker can hurt this system without
// guessing anything: every registration with a fresh public key consumes a value
// from the relay subnet sequence, and the sequence does not recycle.
//
// A fixed window rather than a token bucket, deliberately. The imprecision at a
// window boundary (up to 2x the nominal rate across two adjacent windows) does
// not matter for limits measured in tens per hour, and a counter is something a
// reader can verify at a glance — which is worth more here than exactness.
type Limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	now     func() time.Time // injected so tests need not sleep
	entries map[string]*window
	maxKeys int
}

type window struct {
	count int
	start time.Time
}

// NewLimiter allows limit events per key per window.
func NewLimiter(limit int, w time.Duration) *Limiter {
	return &Limiter{
		limit:   limit,
		window:  w,
		now:     time.Now,
		entries: make(map[string]*window),
		// A cap, because the key is partly attacker-chosen: an attacker rotating
		// source addresses would otherwise grow this map without bound and turn a
		// rate limiter into a memory exhaustion bug. At the cap the oldest windows
		// are dropped; the worst case is that an attacker resets their own counter,
		// which is no worse than not having limited them.
		maxKeys: 100_000,
	}
}

// Allow records an event against key and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	e, ok := l.entries[key]
	if !ok || now.Sub(e.start) >= l.window {
		if len(l.entries) >= l.maxKeys {
			l.evictExpiredLocked(now)
		}
		l.entries[key] = &window{count: 1, start: now}
		return true
	}
	e.count++
	return e.count <= l.limit
}

func (l *Limiter) evictExpiredLocked(now time.Time) {
	for k, e := range l.entries {
		if now.Sub(e.start) >= l.window {
			delete(l.entries, k)
		}
	}
	// Still full of live windows: drop everything rather than grow without bound.
	// This is the degenerate case of a flood from many addresses, where being
	// approximately right and bounded beats being exactly right and unbounded.
	if len(l.entries) >= l.maxKeys {
		l.entries = make(map[string]*window)
	}
}

// KeyPrefix returns the part of a contributor key that is safe to use as a rate
// limiting bucket: the literal prefix and the first random group.
//
// Never the whole key. This value is held in memory and appears in logs; the key
// itself is a credential, and a limiter is not a reason to keep one where it does
// not belong.
func KeyPrefix(contributorKey string) string {
	const n = 8 // "GNL-XXXX"
	if len(contributorKey) <= n {
		return contributorKey
	}
	return contributorKey[:n]
}
