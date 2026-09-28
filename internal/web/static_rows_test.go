package web_test

import "testing"

// TestTheListingRows drives internal/web/jstest/rows_test.mjs.
func TestTheListingRows(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/rows_test.mjs")
}
