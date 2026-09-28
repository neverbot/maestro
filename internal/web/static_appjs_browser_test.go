package web_test

import (
	"os/exec"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
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
	assert.Must(t, err == nil, "%s failed: %v\n%s", script, err, out)
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
func TestInviteFormSendsCapturedToken(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/safe_return_path_test.mjs")
}

// TestYouCanReachAnotherGameFromInsideOne pins the bug the author found
// on the first real session against a running instance: signing in
// landed them in one game and nothing in the product could reach
// another, though the account was a member of three. The picker
// redirected off the remembered game the moment it had one, and the only
// link out of a game was the wordmark, pointing at "/" — the picker,
// which redirected straight back in.
func TestYouCanReachAnotherGameFromInsideOne(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/game_switcher_test.mjs")
}

// TestThePageModulesRenderTheirRoutes drives the seven page modules
// Task 15 put on seven routes — the real, unmodified sources, a stubbed
// DOM and a stubbed fetch — and is the whole of the evidence for the
// half of this product a Go test cannot reach.
func TestThePageModulesRenderTheirRoutes(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/pages_test.mjs")
}

// TestTheDocumentPageRendersADocument drives the reading view the same
// way: internal/web/jstest/document_page_test.mjs loads the real
// internal/web/static/doc.js as an ES module in a DOM stub and asserts
// what the page renders and what it posts.
func TestTheDocumentPageRendersADocument(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/document_page_test.mjs")
}

// TestPaletteRules drives internal/web/jstest/palette_test.mjs, which
// imports the real internal/web/static/palette.js and asserts over the
// plain data it returns.
func TestPaletteRules(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/palette_test.mjs")
}

// TestTheVendoredRuntimeLoads drives
// internal/web/jstest/vendor_modules_test.mjs, which reads the import
// map out of a shipped shell, resolves each specifier the way a browser
// would, and imports the vendored file it names.
func TestTheVendoredRuntimeLoads(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/vendor_modules_test.mjs")
}

// TestTheDataClientRules drives internal/web/jstest/client_test.mjs,
// which imports the real internal/web/static/client.js and drives it the
// way a page does: a stubbed fetch, a stubbed event stream carrying real
// text/event-stream bytes, and an injected clock so the 750ms coalescing
// window costs a millisecond of test time.
func TestTheDataClientRules(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/client_test.mjs")
}

// TestTheViewFrame drives internal/web/jstest/frame_test.mjs, which
// imports the real internal/web/static/render/scene.js and asserts over
// the plain data it returns.
func TestTheViewFrame(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/frame_test.mjs")
}

// TestTheTextTwin drives internal/web/jstest/twin_test.mjs, which
// imports the real internal/web/static/render/twin.js and — through the
// import map this server ships, read out of a shell by
// internal/web/jstest/importmap_loader.mjs — the real
// internal/web/static/components/mst-twin.js and
// mst-view-frame.js beside it.
func TestTheTextTwin(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/twin_test.mjs")
}

// TestLayoutComposition drives internal/web/jstest/layout_test.mjs,
// which imports the real internal/web/static/layout/engine.js,
// compose.js and budget.js and asserts over the plain data they return.
func TestLayoutComposition(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/layout_test.mjs")
}

// TestTheCanvas drives internal/web/jstest/canvas_test.mjs, which is the
// SVG emitter, the pan/zoom transform, the drag layer and the edge join
// the six renderers all consume.
func TestTheCanvas(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/canvas_test.mjs")
}

// TestTheGraphRenderer drives internal/web/jstest/render_graph_test.mjs,
// which is the first of the six renderers and the shape the other five
// copy: a pure function from an envelope and a layout to a scene, with
// no DOM anywhere in it.
func TestTheGraphRenderer(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/render_graph_test.mjs")
}

// TestTheLayeredRenderer drives internal/web/jstest/render_layered_test.mjs.
func TestTheLayeredRenderer(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/render_layered_test.mjs")
}

// TestTheNestedRenderer drives internal/web/jstest/render_nested_test.mjs.
func TestTheNestedRenderer(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/render_nested_test.mjs")
}

// TestTheMapRenderer drives internal/web/jstest/render_map_test.mjs.
func TestTheMapRenderer(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/render_map_test.mjs")
}

// TestTheTableRenderer drives internal/web/jstest/render_table_test.mjs.
func TestTheTableRenderer(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/render_table_test.mjs")
}

// TestTheTimelineRenderer drives
// internal/web/jstest/render_timeline_test.mjs, the last of the six.
func TestTheTimelineRenderer(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/render_timeline_test.mjs")
}

// TestTheTwoWrites drives internal/web/jstest/writes_test.mjs, which
// imports the real internal/web/static/components/mst-canvas.js and
// mst-ground.js and drives them against the real
// internal/web/static/client.js over a stubbed fetch and a stubbed event
// stream carrying real text/event-stream bytes.
func TestTheTwoWrites(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/writes_test.mjs")
}

// TestSaveAs drives internal/web/jstest/save_as_test.mjs, which imports
// the real internal/web/static/components/mst-save-as.js and the real
// internal/web/static/pages/view.js and drives them against the real
// internal/web/static/client.js over a stubbed fetch that keeps every
// request body as **text**.
func TestSaveAs(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	runJSTest(t, "jstest/save_as_test.mjs")
}
