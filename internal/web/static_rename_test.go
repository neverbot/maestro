package web_test

import "testing"

// TestTheFirstHumanWrite drives internal/web/jstest/rename_test.mjs.
//
// **Why the harness and not a Go test.** The server's half is a
// compare-and-set and is tested where it lives. What is not testable
// there is the page's answer to a refusal, and that answer *is* the
// design: the edit is never lost, and the edit never silently wins. Both
// are properties of the client — "it did not write again on its own" is
// a count of requests across a gesture, and a gesture does not exist on
// the server.
//
// It also holds the defect only this layer could see: the batch route
// answers 200 with a `failed` list rather than 409, so a page reading
// `answer.ok` as "it landed" showed the typed name over a row the server
// had kept. Measured in a browser first, then pinned here.
func TestTheFirstHumanWrite(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/rename_test.mjs")
}
