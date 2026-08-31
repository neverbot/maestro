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
//
// A key is lower-cased and trimmed of surrounding whitespace before it is
// recorded or checked, so a caller does not have to pre-normalize an email
// address itself: "Bob@x.com", "bob@x.com" and " bob@x.com " share one
// budget. This must match whatever normalization the downstream lookup
// applies — Authenticate lower-cases and trims the email it looks up, so
// keying the login limiter on the raw, unnormalized address would still be
// wrong today were it not for this; a caller keying on something Authenticate
// does not normalize the same way must not assume this helps.
//
// This is the only normalization Limiter performs — it does not make an
// attacker-controlled key safe to use alone. Keying solely by an attempted
// email lets one attacker exhaust a legitimate user's budget from many
// source IPs; keying solely by source IP lets an attacker who can rotate
// IPs bypass the limit while still hammering one target; keying by a
// secret the caller is trying to protect (an invite token, an API key)
// hands the attacker an endless stream of fresh keys, since every guess is
// by definition a new value — that budget caps nothing. Choosing a key
// that is both stable for a legitimate user and expensive for an attacker
// to rotate (the client's source IP, typically) is the caller's
// responsibility, not this type's.
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
//
// The two used to be one method: Allow both decided and charged in the
// same call, so a caller had to charge optimistically before attempting
// the guarded operation and then call Reset to refund on success. Any
// early return between the charge and the Reset — for instance, a login
// that authenticates correctly but then fails to issue a session — spent a
// legitimate user's budget for no reason. Splitting the decision (Allowed)
// from the recording (Record) removes the refund path entirely: success
// never charges anything in the first place, because nothing is charged
// until Record is explicitly called on failure.
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

// Reset forgets every attempt recorded for a key. Callers use it to
// forgive a run of prior failures once the caller no longer wants them
// counted — for instance, an operator manually clearing a lockout. A
// successful attempt does not need this: Allowed never charges, so there
// is nothing a success needs refunded.
func (l *Limiter) Reset(key string) {
	key = normalizeKey(key)

	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
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
// Callers must hold l.mu. It exists so that a key which is only ever
// touched once (see the package doc on Limiter) is eventually forgotten
// even though nothing ever calls Allowed, Record or Reset for it again.
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
//
// It filters in place by writing into the front of times's own backing
// array, which is safe here because the write index (len(kept)) never
// overtakes the read index (the loop's position in times): kept only ever
// holds entries the loop has already consumed.
func filterAfter(times []time.Time, cutoff time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}
