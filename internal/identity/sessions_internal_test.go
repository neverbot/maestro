// This file's test lives in package identity (not identity_test) so it
// can reach the unexported maxSessionLifetime.
package identity

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// TestMaxSessionLifetimeMatchesExtendSessionSQL pins the agreement
// maxSessionLifetime's own doc comment (sessions.go) already claims but
// nothing previously checked: ExtendSession's SQL (identity.sql) caps a
// renewed session at created_at + interval '90 days', a literal that has
// to keep matching this constant by hand, since nothing in Go re-derives
// or re-checks it at runtime. Before this test existed, changing the
// interval in the query alone — a one-line SQL edit — would silently
// drift the two apart with no build failure and no test failure
// anywhere, and the doc comments on both sides would go on describing an
// agreement that had stopped being true.
func TestMaxSessionLifetimeMatchesExtendSessionSQL(t *testing.T) {
	sqlBytes, err := os.ReadFile("../db/queries/identity.sql")
	if err != nil {
		t.Fatalf("read identity.sql: %v", err)
	}
	re := regexp.MustCompile(`created_at \+ interval '(\d+) days'`)
	m := re.FindStringSubmatch(string(sqlBytes))
	if m == nil {
		t.Fatal(`identity.sql: could not find ExtendSession's "created_at + interval 'N days'" cap expression`)
	}
	days, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parse day count %q: %v", m[1], err)
	}
	got := time.Duration(days) * 24 * time.Hour
	if got != maxSessionLifetime {
		t.Fatalf("identity.sql caps renewal at %d days (%v), but maxSessionLifetime = %v — these must match", days, got, maxSessionLifetime)
	}
}
