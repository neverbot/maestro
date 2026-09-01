package web_test

import (
	"os/exec"
	"testing"
)

// nodeOrSkip returns the "node" binary's presence, skipping t rather than
// failing it when Node is not on PATH: this package's product code has no
// JavaScript build or test dependency of its own (Task 15's whole point
// is no build step), so an environment without Node must still be able
// to run `go test ./internal/web/...` — it just does not get the two
// regression checks below, which drive internal/web/static/app.js's real
// code paths the way a browser would rather than reimplementing its
// logic in Go, where reimplementing it could make, and hide, the same
// mistake a review already found once.
func nodeOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH; skipping this app.js browser-behaviour regression check")
	}
}

func runJSTest(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("node", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", script, err, out)
	}
	t.Log(string(out))
}

// TestInviteFormSendsCapturedToken pins the invite-redemption bug a
// live-browser review found: the form's submit handler used to re-read
// window.location.hash for the invite token, but history.replaceState —
// run on page load to scrub the token out of the visible URL and
// history, closing the Referer leak this same round of review found —
// had already cleared it by the time anyone could submit the form, so
// every redemption silently sent an empty invite_token and failed with
// "this instance only admits invited users". The backend was never the
// bug: a direct call with the same token succeeded (confirmed again by
// hand for this fix: three separate real-browser redemptions, one of
// them field-by-field with each value read back before submitting, all
// three ending in a real session cookie and a 200 from GET /api/me).
// Only the page was broken, which is exactly why a Go test hitting POST
// /api/auth/register directly (api_auth_test.go already has several)
// could never have caught it — nothing about the API changed.
//
// This test instead shells out to Node to load the real, unmodified
// internal/web/static/app.js as an ES module in a minimal DOM stub and
// drive its actual invite submit handler
// (internal/web/jstest/invite_redemption_test.mjs has the harness and
// its own doc comment).
func TestInviteFormSendsCapturedToken(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/invite_redemption_test.mjs")
}

// TestSafeReturnPathRejectsOffOriginBypasses pins the second bug the same
// review found: safeReturnPath (app.js) used to accept any value with
// exactly one leading slash, which let three payloads a browser's own
// URL parser treats differently than a character-counting regex through:
// "/\evil.example" and "/\/evil.example" (a backslash is a path
// separator on a special scheme, same as a second forward slash to that
// parser) and a raw control character such as a newline between two
// slashes (stripped before the parser ever looks at the string) — all
// three still resolve to a different host once a browser actually
// navigates. safeReturnPath is fixed to resolve the candidate through
// the URL constructor and compare the *parsed* origin, which is robust
// to this whole class of variant by construction rather than by
// enumeration; see internal/web/jstest/safe_return_path_test.mjs for the
// harness driving the real, unmodified app.js through a login redirect
// with each payload, and its own doc comment for why a regex fix alone
// would not be trusted here again.
func TestSafeReturnPathRejectsOffOriginBypasses(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/safe_return_path_test.mjs")
}
