package identity

import (
	"strings"
	"sync"
	"time"
)

// sweepEvery is how many Allowed/Record calls pass between opportunistic
// sweeps of the whole attempts map. Every key Limiter has ever seen keeps
// at least one recorded timestamp until it is touched again — see the
// package doc below — so a caller that keys the limiter by an
// attacker-controlled value (an attempted login email, say) can otherwise
// grow the map by one entry per distinct value ever tried, forever. The
// sweep does not prevent that growth outright — nothing stops an attacker
// from trying sweepEvery distinct keys between two sweeps — it converts
// unbounded growth into growth bounded by however much distinct-key
// traffic arrives within roughly one window, which is the honest claim:
// memory is bounded by recent activity, not by the lifetime of the
// process. The sweep piggybacks on Allowed and Record themselves rather
// than running on a ticker, so the limiter needs no goroutine, no Close
// method and no shutdown path.
const sweepEvery = 1024

// Limiter counts failed attempts per key inside a rolling window. It lives
// in process memory: a restart forgives everyone, which is acceptable for
// login throttling on a self-hosted instance.
type Limiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	max      int
	window   time.Duration
	calls    int
	now      func() time.Time // overridden by tests; defaults to time.Now
}

// NewLimiter allows max attempts per key per window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{
		attempts: make(map[string][]time.Time),
		max:      max,
		window:   window,
		now:      time.Now,
	}
}

// Allowed reports whether key may make another attempt right now. It is a
// pure check: it never itself charges an attempt against the budget, so
// calling it any number of times, or not following it with Record, never
// spends anything. Callers check this before attempting the guarded
// operation, then call Record only if that attempt fails — see Record's
// doc comment for why the split matters.
func (l *Limiter) Allowed(key string) bool {
	key = normalizeKey(key)

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)
	kept := filterAfter(l.attempts[key], cutoff)
	l.storeLocked(key, kept)
	l.maybeSweepLocked(cutoff)
	return len(kept) < l.max
}

// Record charges one attempt against key. Callers call this only on the
// failure path of whatever Allowed is guarding.
func (l *Limiter) Record(key string) {
	key = normalizeKey(key)

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)
	kept := filterAfter(l.attempts[key], cutoff)
	kept = append(kept, now)
	l.storeLocked(key, kept)
	l.maybeSweepLocked(cutoff)
}

// storeLocked writes kept back for key, or removes the key entirely once
// it has nothing left to remember. Callers must hold l.mu.
func (l *Limiter) storeLocked(key string, kept []time.Time) {
	if len(kept) == 0 {
		delete(l.attempts, key)
		return
	}
	l.attempts[key] = kept
}

// maybeSweepLocked runs sweepLocked every sweepEvery calls. Callers must
// hold l.mu.
func (l *Limiter) maybeSweepLocked(cutoff time.Time) {
	l.calls++
	if l.calls < sweepEvery {
		return
	}
	l.calls = 0
	l.sweepLocked(cutoff)
}

// sweepLocked drops every key whose most recent attempt predates cutoff.
func (l *Limiter) sweepLocked(cutoff time.Time) {
	for key, times := range l.attempts {
		if len(times) == 0 || !times[len(times)-1].After(cutoff) {
			delete(l.attempts, key)
		}
	}
}

// normalizeKey lower-cases and trims a key so that callers do not have to
// pre-normalize case or whitespace themselves. See Limiter's doc comment
// for what this does and does not protect against.
func normalizeKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// filterAfter returns the subset of times strictly after cutoff.
func filterAfter(times []time.Time, cutoff time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}
