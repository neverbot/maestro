package web_test

import "testing"

// TestTheQueryBuilder drives internal/web/jstest/builder_test.mjs.
//
// **This is the screen `claude.md` used to list under "deliberately not
// built"**: a person alone could not compose a view, because the query
// language was written for agents. The emitter (compose_test.mjs) and
// the pickers (pickers_test.mjs) are held elsewhere; what this holds is
// the sentence — that a stack reads in the order a person says it, that
// a diagnostic's JSON pointer finds the line that wrote it, and that a
// save the server would refuse is refused here first with the reason
// beside the button rather than after a press.
func TestTheQueryBuilder(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/builder_test.mjs")
}
