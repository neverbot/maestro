package web_test

import "testing"

// TestEditingAFieldValue drives internal/web/jstest/field_edit_test.mjs.
//
// The second write a person can make, and the first that has to know
// what a value *is*: a control per declared type, a refusal that is
// about one field, and the distinction between a value that is empty and
// a value that is not there — which this page is the last place that
// could throw away.
//
// It is driven through `wireFieldEdits` over a painted list rather than
// through the client, for the reason the rename's harness is: *correct
// in the module, dead at the call site* is this repository's most
// repeated defect, and a write is the shape it takes.
func TestEditingAFieldValue(t *testing.T) {
	runJSTest(t, "jstest/field_edit_test.mjs")
}
