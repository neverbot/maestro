// Package assert states a test's expectations in one line each.
//
// Arguments are evaluated whether or not the expectation holds, so a
// message that is only legal on failure (rows[0] where the claim is that
// there is one row) keeps its `if` block.
package assert

import "testing"

// Must is t.Fatalf with the condition stated as an expectation.
func Must(t testing.TB, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

// Should is t.Errorf: the test records the failure and carries on.
func Should(t testing.TB, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Errorf(format, args...)
	}
}

// NoErr fails with "what: err".
func NoErr(t testing.TB, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}
