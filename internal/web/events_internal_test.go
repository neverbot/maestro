package web

import (
	"testing"
	"time"
)

// TestSSERecheckOutcome pins sseRecheckOutcome's retry policy directly,
// with no real identity/projects service, no live connection, and no
// need to inject a database failure into a real *pgxpool.Pool: exactly
// one transient failure in a row is tolerated, a non-transient failure
// closes immediately regardless of history, and two transient failures
// in a row close on the second. See sseRecheckOutcome's own doc comment
// (events.go) for why this policy is factored out into a pure function
// specifically so it can be tested this way.
func TestSSERecheckOutcome(t *testing.T) {
	cases := []struct {
		name             string
		reason           string
		transient        bool
		previouslyFailed bool
		want             sseRecheckAction
	}{
		{"access still checks out", "", false, false, sseRecheckOK},
		{"access still checks out after a prior transient failure", "", false, true, sseRecheckOK},
		{"first transient failure is tolerated", "db unreachable", true, false, sseRecheckTolerate},
		{"second transient failure in a row closes", "db unreachable", true, true, sseRecheckClose},
		{"a definitive failure closes immediately", "token no longer resolves", false, false, sseRecheckClose},
		{"a definitive failure closes even after a tolerated transient one", "no longer a member", false, true, sseRecheckClose},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sseRecheckOutcome(tc.reason, tc.transient, tc.previouslyFailed); got != tc.want {
				t.Errorf("sseRecheckOutcome(%q, %v, %v) = %v, want %v", tc.reason, tc.transient, tc.previouslyFailed, got, tc.want)
			}
		})
	}
}

// TestSSESawGap pins sseSawGap's contract directly, including the two
// edges most likely to be gotten wrong: seq 0 is never a real value (see
// realtime.Hub.Publish, which starts counting at 1) so lastSeq == 0 must
// never report a gap regardless of seq, and a seq that goes backward
// (out of order delivery, which should not happen but is not this
// function's job to rule out structurally) still counts as a gap rather
// than being silently accepted.
func TestSSESawGap(t *testing.T) {
	cases := []struct {
		name         string
		lastSeq, seq uint64
		want         bool
	}{
		{"first event ever on this stream is never a gap", 0, 1, false},
		{"first event ever, arbitrarily high seq, still not a gap", 0, 500, false},
		{"consecutive events", 1, 2, false},
		{"one event dropped", 1, 3, true},
		{"many events dropped", 1, 100, true},
		{"seq went backward", 5, 4, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sseSawGap(tc.lastSeq, tc.seq); got != tc.want {
				t.Errorf("sseSawGap(%d, %d) = %v, want %v", tc.lastSeq, tc.seq, got, tc.want)
			}
		})
	}
}

// TestJitteredSSEMaxLifetime pins jitteredSSEMaxLifetime's bound
// directly: every result stays within ±sseLifetimeJitterFraction of
// base, and — since a jitter implementation that always rounds the same
// direction would defeat its own purpose — both a below-base and an
// above-base result actually occur across enough samples.
func TestJitteredSSEMaxLifetime(t *testing.T) {
	const base = 5 * time.Minute
	spread := time.Duration(float64(base) * sseLifetimeJitterFraction)
	lo, hi := base-spread, base+spread

	var sawBelow, sawAbove bool
	for i := 0; i < 200; i++ {
		got := jitteredSSEMaxLifetime(base)
		if got < lo || got > hi {
			t.Fatalf("jitteredSSEMaxLifetime(%v) = %v, want within [%v, %v]", base, got, lo, hi)
		}
		switch {
		case got < base:
			sawBelow = true
		case got > base:
			sawAbove = true
		}
	}
	if !sawBelow || !sawAbove {
		t.Fatalf("200 samples never varied in both directions (below=%v, above=%v) — jitter looks one-sided or absent", sawBelow, sawAbove)
	}
}
