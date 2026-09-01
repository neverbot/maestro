package roles_test

import (
	"testing"

	"github.com/neverbot/maestro/internal/roles"
)

// TestValid pins Valid's contract directly: every name in All() is
// accepted, and both an unrecognised string and the empty (zero) value
// are refused. The database-agreement half of this vocabulary
// (TestAllRolesAcceptedByDatabase) lives in
// internal/projects/projects_test.go instead — see this package's own
// doc comment for why.
func TestValid(t *testing.T) {
	for _, r := range roles.All() {
		if !roles.Valid(string(r)) {
			t.Errorf("Valid(%q) = false, want true", r)
		}
	}
	if roles.Valid("superadmin") {
		t.Fatal(`Valid("superadmin") = true, want false`)
	}
	if roles.Valid("") {
		t.Fatal(`Valid("") = true, want false`)
	}
}

// TestAtLeast walks every role-and-threshold pair, including both
// positions holding an unrecognised value, so a regression like the one
// AtLeast shipped with once (an unknown min silently ranked as Viewer,
// making a typo in the threshold the most permissive check possible) is
// caught by the table rather than by whichever call site happens to
// exercise it first.
func TestAtLeast(t *testing.T) {
	const bogus = roles.Role("auditor")

	cases := []struct {
		role, min roles.Role
		want      bool
	}{
		{roles.Owner, roles.Owner, true},
		{roles.Owner, roles.Editor, true},
		{roles.Owner, roles.Viewer, true},
		{roles.Editor, roles.Owner, false},
		{roles.Editor, roles.Editor, true},
		{roles.Editor, roles.Viewer, true},
		{roles.Viewer, roles.Owner, false},
		{roles.Viewer, roles.Editor, false},
		{roles.Viewer, roles.Viewer, true},
		// An unrecognised role never meets any threshold, including the
		// lowest one.
		{bogus, roles.Viewer, false},
		// An unrecognised threshold must refuse everything, not silently
		// rank as Viewer (Go's zero value for a missing map entry) and
		// so accept whatever a real role would clear against Viewer.
		{roles.Viewer, bogus, false},
		{roles.Owner, bogus, false},
		{bogus, bogus, false},
		{roles.Role(""), roles.Role(""), false},
	}
	for _, tc := range cases {
		if got := roles.AtLeast(tc.role, tc.min); got != tc.want {
			t.Errorf("AtLeast(%q, %q) = %v, want %v", tc.role, tc.min, got, tc.want)
		}
	}
}
