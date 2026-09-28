// Package assert states a test's expectations in one line each.
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
