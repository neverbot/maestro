package web_test

import "testing"

// TestThePicker drives internal/web/jstest/picker_test.mjs.
func TestThePicker(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/picker_test.mjs")
}
