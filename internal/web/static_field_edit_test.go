package web_test

import "testing"

// TestEditingAFieldValue drives internal/web/jstest/field_edit_test.mjs.
func TestEditingAFieldValue(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/field_edit_test.mjs")
}
