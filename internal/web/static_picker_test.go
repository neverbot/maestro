package web_test

import "testing"

// TestThePicker drives internal/web/jstest/picker_test.mjs.
//
// The picker is the control the query builder rests on, and it is built
// before the builder because the builder's own spec calls the four
// pickers "the real work" and because three other screens want them: the
// route's step list, the Draw clause's `color_by`, and the frame's
// "start from this room".
//
// What the harness holds is the property the control exists for: a
// choice answers with **the key the game wrote**, never a label a caller
// would have to parse back. A misspelled key is the commonest way a
// hand-written query fails, and a picker that could be misspelled would
// be a longer way to fail the same way.
func TestThePicker(t *testing.T) {
	runJSTest(t, "jstest/picker_test.mjs")
}
