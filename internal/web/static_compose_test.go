package web_test

import (
	"testing"
)

// TestComposingAQuery drives internal/web/jstest/compose_test.mjs.
func TestComposingAQuery(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/compose_test.mjs")
}
