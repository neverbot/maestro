package web

import "testing"

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
