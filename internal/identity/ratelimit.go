package identity

import (
	"sync"
	"time"
)

// sweepEvery is how many Allow calls pass between opportunistic sweeps of
// the whole attempts map. Every key that Allow has ever seen keeps at least
// one recorded timestamp until it is checked again — see Allow's comment —
// so a caller that keys the limiter by an attacker-controlled value (an
// attempted login email, say) can be fed an endless stream of distinct keys
// that are each queried exactly once. Without a sweep, that grows the map
// forever: a memory-exhaustion denial of service, independent of and in
// addition to whatever the max/window accounting is meant to prevent. The
// sweep piggybacks on Allow itself rather than running on a ticker, so the
// limiter needs no goroutine, no Close method and no shutdown path.
const sweepEvery = 1024

// Limiter counts failed attempts per key inside a rolling window. It lives
// in process memory: a restart forgives everyone, which is acceptable for
// login throttling on a self-hosted instance.
//
// The key is whatever the caller passes, and the caller controls how much
// of it the caller (not necessarily the limiter) trusts. A login handler
// keying solely by the attempted email lets one attacker exhaust a
// legitimate user's attempt budget by supplying that email from many
// source IPs; keying solely by source IP lets an attacker who can rotate
// IPs bypass the limit entirely while still hammering one target account.
// Combining both dimensions (for instance "email|ip") is the caller's
// responsibility, not this type's — Limiter only enforces whatever key it
// is given.
type Limiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	max      int
	window   time.Duration
	calls    int
}

// NewLimiter allows max attempts per key per window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{attempts: make(map[string][]time.Time), max: max, window: window}
}

// Allow records an attempt and reports whether it may proceed.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)

	// Filtering in place by writing into the front of the same backing
	// array is safe here: kept only ever holds entries already consumed by
	// the range below, so its write index never overtakes the read index.
	existing := l.attempts[key]
	kept := existing[:0]
	for _, t := range existing {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	allowed := len(kept) < l.max
	if allowed {
		kept = append(kept, now)
	}
	if len(kept) == 0 {
		delete(l.attempts, key)
	} else {
		l.attempts[key] = kept
	}

	l.calls++
	if l.calls >= sweepEvery {
		l.calls = 0
		l.sweepLocked(cutoff)
	}

	return allowed
}

// sweepLocked drops every key whose most recent attempt predates cutoff.
// Callers must hold l.mu. It exists so that a key which is only ever
// queried once (see the package doc on Limiter) is eventually forgotten
// even though nothing ever calls Allow or Reset for it again.
func (l *Limiter) sweepLocked(cutoff time.Time) {
	for key, times := range l.attempts {
		if len(times) == 0 || !times[len(times)-1].After(cutoff) {
			delete(l.attempts, key)
		}
	}
}

// Reset forgets the attempts recorded for a key, after a success.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
