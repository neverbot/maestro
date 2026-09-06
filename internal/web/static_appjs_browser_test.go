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

// TestGameHomePageRendersItsSummary drives the game home page the way a
// browser does — the real app.js, a stubbed DOM and a stubbed fetch —
// and pins the three things about that page a Go test of the API
// underneath it cannot see: that a game's own labels reach the DOM as
// text and never as markup, that a game with nothing in it gets its
// empty states instead of two blank lists, and that a failed summary
// leaves the server's own message on screen rather than an empty
// catalogue that reads exactly like a game with no content.
//
// It also pins the property that makes this page a summary at all: it
// issues exactly three requests — the game list, the summary and one
// bounded keyset page of documents — and none of them enumerates
// entities or relations, so a game holding four hundred entities
// renders like one holding four. See
// internal/web/jstest/game_summary_test.mjs for the harness.
func TestGameHomePageRendersItsSummary(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/game_summary_test.mjs")
}

// TestTheDocumentPageRendersADocument drives the reading view the same
// way: internal/web/jstest/document_page_test.mjs loads the real
// internal/web/static/doc.js as an ES module in a DOM stub and asserts
// what the page renders and what it posts.
//
// It is the only place any of this is checked. doc.js is the one file in
// this product that writes markup, and the properties that matter — that
// only a rendered view's html reaches that sink, that a history names
// people rather than uuids, that a revert sends the document's current
// version as expected_version, that a failed read does not render a
// plausible blank — are properties of the page, not of any route, so no
// Go test against internal/web/api_docs.go can see one of them.
func TestTheDocumentPageRendersADocument(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/document_page_test.mjs")
}

// TestPaletteRules drives internal/web/jstest/palette_test.mjs, which
// imports the real internal/web/static/palette.js and asserts over the
// plain data it returns.
//
// It sits with the browser-behaviour harnesses rather than beside the
// token arithmetic in static_tokens_test.go because it is the same kind
// of test as the four above — the product code is JavaScript, and the
// only honest way to test JavaScript is to run it. What it holds is the
// half of the palette a Go test cannot see: which slot a value gets
// (hashed from the value's text, never from its rank in the result, so
// that adding a zone does not recolour a saved view), and that a slot
// absent from `attrs` and a slot holding the empty string stay two
// legend rows — the guarantee internal/views/execute.go goes out of its
// way to provide and the one the legend is most likely to spend.
//
// static_tokens_test.go holds the other half — that the tokens this
// module names exist in both themes and are far enough apart to be told
// apart — and TestThePaletteModuleAndTheStylesheetAgreeOnEight is the
// seam that stops the two halves drifting.
func TestPaletteRules(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/palette_test.mjs")
}
