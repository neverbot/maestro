package web_test

import (
	"os"
	"strings"
	"testing"
)

// TestTheBuilderDoorIsDecidedFromTheStoredQuery is a source guard over
// one call, and it exists because the thing it protects is a promise
// rather than a behaviour anything else can see.
//
// The builder spike's §4 says the builder **generates and never edits**:
// it opens a stored query only when that document round-trips through
// parse and re-emit unchanged, so a clause it has not learned closes the
// door by itself instead of being dropped on the way through. Two places
// have to ask: the view page, before it offers the way in, and the
// builder itself, because a person can reach its address by hand.
//
// A page that offered the door without asking would look right on every
// view in the seeded games — they all round-trip — and would quietly
// rewrite the first query that did not.
func TestTheBuilderDoorIsDecidedFromTheStoredQuery(t *testing.T) {
	for _, page := range []struct {
		path string
		what string
	}{
		{"static/pages/view.js", "the view page decides whether to offer the way in"},
		{"static/pages/builder.js", "the builder decides whether to open what it was pointed at"},
	} {
		source, err := os.ReadFile(page.path)
		if err != nil {
			t.Fatalf("read %s: %v", page.path, err)
		}
		text := string(source)
		if !strings.Contains(text, "roundTrips(") {
			t.Errorf("%s never calls roundTrips: %s, and §4 says that decision is made against "+
				"the stored bytes every time", page.path, page.what)
		}
		if !strings.Contains(text, "TOO_MUCH") {
			t.Errorf("%s never says TOO_MUCH: a query the builder cannot hold has to be explained "+
				"in the spike's own words rather than by a control that is simply missing", page.path)
		}
	}
}
