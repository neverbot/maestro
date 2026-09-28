package web_test

import "testing"

// TestTheFirstHumanWrite drives internal/web/jstest/rename_test.mjs.
func TestTheFirstHumanWrite(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/rename_test.mjs")
}
