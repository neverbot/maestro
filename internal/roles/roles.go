// Package roles defines the membership role vocabulary shared by every
// package that grants, checks or validates a project role: identity
// (invites carry a role) and projects (memberships carry a role, and
// SetRole assigns one). Before this package existed the same three
// strings were declared twice — identity.inviteRoles and
// projects.membershipRoles — on top of the memberships table's own CHECK
// constraint, plus bare "owner" literals wherever code needed the highest
// role. A fourth role meant a migration and two separate Go edits, with
// nothing failing at compile time if one was missed: an invite minted for
// the missed role would pass Go-side validation and then die on a raw
// CHECK violation deep inside a transaction — precisely the failure mode
// identity.inviteRoles's own doc comment already explained this pattern
// exists to prevent, just one layer further down than that fix reached.
package roles

// Role is a membership role. The zero value ("") is never valid; construct
// one of the named constants, or validate external input with Valid.
type Role string

// The three roles the memberships table's CHECK constraint accepts
// (migration 0001: `role IN ('owner', 'editor', 'viewer')`) and that an
// invite may grant. Keeping this list in sync with that constraint is
// this package's entire reason to exist — see roles_test.go's
// TestAllRolesAcceptedByDatabase, which is the test that actually
// enforces it.
const (
	Owner  Role = "owner"
	Editor Role = "editor"
	Viewer Role = "viewer"
)

// all is the recognised set, in the order All returns them.
var all = []Role{Owner, Editor, Viewer}

// Valid reports whether s names a recognised role. Every caller that
// accepts a role as a plain string — an invite request, a role-change
// request body — validates it through this, in Go, before it ever reaches
// SQL: see this package's own doc comment for why a raw CHECK-constraint
// violation is the wrong place to first discover an unrecognised role.
func Valid(s string) bool {
	for _, r := range all {
		if string(r) == s {
			return true
		}
	}
	return false
}

// All returns every recognised role. Its purpose is a test that walks the
// set and asserts each one is actually accepted by the database (and a
// bogus one rejected) — the one check that catches this package and
// migration 0001's CHECK constraint drifting apart, which no doc comment
// can do on its own.
func All() []Role {
	out := make([]Role, len(all))
	copy(out, all)
	return out
}
