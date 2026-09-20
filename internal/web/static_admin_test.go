package web_test

import "testing"

// TestTheAdministrationScreen drives internal/web/jstest/admin_test.mjs:
// what an account's row says, the way in to editing it, and the one
// control a person is not offered — their own administrator flag, which
// would close the page they are standing on.
func TestTheAdministrationScreen(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/admin_test.mjs")
}
