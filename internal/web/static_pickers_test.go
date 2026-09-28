package web_test

import "testing"

// TestThePickerSources drives internal/web/jstest/pickers_test.mjs.
func TestThePickerSources(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/pickers_test.mjs")
}
