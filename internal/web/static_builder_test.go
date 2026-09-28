package web_test

import "testing"

// TestTheQueryBuilder drives internal/web/jstest/builder_test.mjs.
func TestTheQueryBuilder(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/builder_test.mjs")
}
