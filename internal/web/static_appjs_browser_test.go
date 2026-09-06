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

// TestTheVendoredRuntimeLoads drives
// internal/web/jstest/vendor_modules_test.mjs, which reads the import
// map out of a shipped shell, resolves each specifier the way a browser
// would, and imports the vendored file it names.
//
// static_vendor_test.go is arithmetic over the vendored bytes — the hash
// matches, the payload fits its budget, every shell carries the same
// map, the server serves each target as JavaScript. All of that is
// satisfiable by three files that do not parse. This is the test that
// they load, that they export the identifiers the components and the
// layout worker are about to import by name, and that dagre lays out a
// graph built from the *separately* vendored graphlib — a tolerance
// dagre provides by reading a graph structurally, which is an upstream
// detail Task 6 depends on entirely and a minor version could withdraw
// without a word.
func TestTheVendoredRuntimeLoads(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/vendor_modules_test.mjs")
}

// TestTheDataClientRules drives internal/web/jstest/client_test.mjs,
// which imports the real internal/web/static/client.js and drives it the
// way a page does: a stubbed fetch, a stubbed event stream carrying real
// text/event-stream bytes, and an injected clock so the 750ms coalescing
// window costs a millisecond of test time.
//
// It is the whole of the evidence for the module every other module in
// this front end depends on. What it holds has no Go counterpart and
// could not have one: that forty position events inside one window are
// one re-read, that a re-read waits for a drag, for an unacknowledged
// write and for a hidden tab, that a payload never becomes state, that a
// position write carries an entity's type and key and never its uuid,
// that a heartbeat comment fires no decision, that the stream reconnects
// when the server closes it — which internal/web/events.go does to every
// stream, on purpose, after five minutes — and that a refusal reaches
// the caller as the server's own code, pointer and message, character
// for character.
//
// internal/web/static_client_test.go holds the two halves of that no Go
// test could see at runtime either: that no string literal in the module
// is a sentence, and that no other module of ours fetches at all.
func TestTheDataClientRules(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/client_test.mjs")
}

// TestTheViewFrame drives internal/web/jstest/frame_test.mjs, which
// imports the real internal/web/static/render/scene.js and asserts over
// the plain data it returns.
//
// It is the whole of the evidence for the surface written *before* any
// renderer, so that no renderer can invent its own version of the states
// below. What it holds has no Go counterpart: that three truncation
// flags are three sentences and never one summary; that a clean envelope
// bands nothing and in particular never claims a complete picture, which
// internal/views/execute.go says the flags cannot support; that the
// ambiguity band counts nodes rather than slots, because the flag is on
// the node; that a stale view under the failing policy draws no picture
// at all and answers with the server's own sentences, their pointers and
// one explicit action to run it anyway; that a best-effort picture bands
// permanently and names what it lost; that a rename is a quiet title
// line and never a band and offers no repair; that an unbound parameter
// is answered in the parameter bar; and that an empty answer is a
// success that guesses at no cause.
//
// internal/web/static_frame_test.go holds the half no harness can see:
// that the component painting all of this writes none of the words.
func TestTheViewFrame(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/frame_test.mjs")
}

// TestTheTextTwin drives internal/web/jstest/twin_test.mjs, which
// imports the real internal/web/static/render/twin.js and — through the
// import map this server ships, read out of a shell by
// internal/web/jstest/importmap_loader.mjs — the real
// internal/web/static/components/mst-twin.js and
// mst-view-frame.js beside it.
//
// The twin is the accessible content of every view, the five graphical
// ones included, and this is the whole of the evidence for it. What it
// holds has no Go counterpart:
//
// That the twin describes the **answer** and not the drawing — a node a
// renderer shelved, dropped beyond a depth bound or collapsed into a
// count chip still has a row, which the harness asserts against a
// stand-in renderer that really does all three, because a twin derived
// from a scene would be a second rendering rather than the ground truth
// the six renderer tasks are checked against.
//
// That a projection slot the query did not find is **visibly absent**
// rather than blank, and that the empty string is not — the distinction
// internal/views/execute.go builds out of jsonb and the last place it
// could be thrown away.
//
// That every coloured node's value is written out as text, in the
// palette's own words, which is what makes "colour is never the only
// carrier" an assertion rather than a claim.
//
// And that a node named `<img src=x onerror=…>` arrives as text: the
// harness walks the emitted Lit template and asserts every game string
// is bound in *child position*, where Lit commits a Text node, and that
// none of them appears in the component's own markup.
// internal/web/static_twin_test.go holds the half no harness can see —
// that no own module reaches for a raw-HTML directive — and
// internal/web/static_frame_test.go holds that the component writes none
// of the twin's words.
func TestTheTextTwin(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/twin_test.mjs")
}

// TestLayoutComposition drives internal/web/jstest/layout_test.mjs,
// which imports the real internal/web/static/layout/engine.js,
// compose.js and budget.js and asserts over the plain data they return.
//
// It is the whole of the evidence for the one module in this product
// that reads `views.layout_mode`. The column is stored, validated and
// returned by the server and read by **no server code at all** —
// 0008_views.sql says so — so there is no Go test that could see any of
// what follows, and there never will be:
//
// That the engine is deterministic, which is what lets `manual` lay out
// an unplaced node and never write the coordinate back, and what let the
// `layout_seed` column lose its last possible reader. Every determinism
// assertion runs the input a third time with the node and edge arrays
// shuffled, because dagre is deterministic *given an insertion order*
// and not otherwise — measured on the vendored 3.1.1, where reversing
// either array moves every node — so the guarantee comes from engine.js
// sorting by the entity's address and not from the envelope's own order,
// which is an `ORDER BY … capped.id` over uuids and does not survive a
// re-seed.
//
// That a pinned node is restored to the designer's exact stored number,
// asserted with `===` rather than within a tolerance, and that the fit
// which gets it there is a translation and a uniform scale and **never a
// rotation** — against a fixture whose best-fit rotation is 90°, because
// a full similarity fit would find that rotation, would minimise the
// error better, and would hand back a diagram nobody recognises.
//
// That the fit's three degeneracies are three different answers with
// three different fixtures: two or more pins determine it, one
// degenerates to a translation, none is the identity and `mixed` then
// behaves as `auto` — plus the two arrangements that would divide by
// zero if the branch were a tolerance instead of a branch, coincident
// pins and a fit that would prefer a negative scale.
//
// And that the separation pass is a **reduction** of overlap rather than
// a guarantee of none, which is asserted by a fixture that still
// overlaps after it, so the sentence in compose.js cannot quietly become
// a promise.
//
// internal/web/static_layout_test.go holds the three halves no harness
// can see: that nothing in that directory imports by bare specifier (a
// module worker has no import map), that the budget is one number whose
// banner sentence is generated from it, and that the spelling a stored
// position is read back in is the one internal/views.Position actually
// marshals to.
func TestLayoutComposition(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/layout_test.mjs")
}

// TestTheCanvas drives internal/web/jstest/canvas_test.mjs, which is the
// SVG emitter, the pan/zoom transform, the drag layer and the edge join
// the six renderers all consume.
//
// What it holds that no Go test can.
//
// That **one mark becomes one element with the attributes the contract
// names and nothing else**. The emitter is the joint the whole
// sub-project turns on: the renderers are pure functions a mutation
// turns red, and they are only worth testing that way if the thing that
// draws their answer adds nothing of its own and drops nothing of
// theirs. A mark carrying `onload`, `style` and a `href` this instance
// cannot serve reaches the DOM with none of the three.
//
// That **paint order is what the layers say and not what the caller
// pushed**. The fixture emits a label before the node it names and the
// ground after both; SVG paints in document order, so a label under its
// own node is a label nobody can read, and the five layers are what
// decides.
//
// That **a drag touches the dragged subtree and nothing else**, which is
// the one budget in §8.2 a Node harness can actually measure: not a
// frame rate, but a count of elements written to. Two hundred nodes, two
// of them dragged, and three writes per move — one transform for the
// whole detached body, and one endpoint pair for each of the two edges
// that leave the selection and therefore cannot ride it.
//
// That **an endpoint may not be in the node list**, in all three of its
// shapes: joined, one end outside, and *both* ends outside — the last
// being the case an implementation written around "one end is outside"
// answers wrongly, and it has a fixture that can only produce it.
//
// That **zoom and pan are one transform on one element**, which is what
// makes a map honest: the ground and the pins are one coordinate space,
// asserted by ancestry rather than by two strings that happen to match.
// And that labels stop scaling outside a 0.75×–1.5× band, with the
// inside-the-band control that keeps a clamp-everything mutation from
// passing.
//
// internal/web/static_canvas_test.go holds the half no harness can see:
// that no own module names an SVG element which runs code, navigates or
// re-enters the HTML parser, and that the emitter's contract cannot
// become one.
func TestTheCanvas(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/canvas_test.mjs")
}

// TestTheGraphRenderer drives internal/web/jstest/render_graph_test.mjs,
// which is the first of the six renderers and the shape the other five
// copy: a pure function from an envelope and a layout to a scene, with
// no DOM anywhere in it.
//
// What it holds that no Go test can.
//
// That **the picture and the text twin describe the same answer**. Task
// 5 built the twin before any renderer so that each renderer task could
// assert exactly this, and it is asserted in both halves — every node the
// twin has a row for is a box in the scene or a node the layout placed
// nowhere, and every edge row is a line or an edge that leaves the
// picture — plus that a box's label and its row's cell carry the same
// characters, since both call the palette's own labelFor.
//
// That **two absences are two marks**. A node whose colour slot found
// nothing is unfilled *and* dashed; a node whose size slot found nothing
// takes the range's minimum; a node missing both gets both, independently,
// which is what a single "unknown" treatment would collapse.
//
// That **a size is an area and it is bounded**. A value four times
// another is twice the width, asserted numerically because getting it
// wrong is invisible in a picture and wrong everywhere in it; and a slot
// value of a million is three times the smallest box rather than a
// million times it.
//
// That **grouping draws an enclosure and clustering draws nothing**,
// which is the distinction a designer trips over. "Draws nothing" is
// asserted as no mark attributable to it and — in
// TestLayoutComposition, over the real engine — as an arrangement that
// moved, so it cannot decay into "does nothing". The tooltip on each
// control is the only place that difference is taught, so both sentences
// are asserted rather than admired.
//
// And the negative half the frame does not own: a truncated answer draws
// exactly what an untruncated one draws (the envelope does not say which
// node lost a neighbour, so a mark claiming to know would invent it), an
// empty answer is an empty scene, a node with no position is named
// rather than drawn at the origin, and an edge that leaves the picture
// is a stub whose terminus is outside its group's enclosure — which is
// why an enclosure is a hairline and not a filled panel.
func TestTheGraphRenderer(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/render_graph_test.mjs")
}

// TestTheLayeredRenderer drives internal/web/jstest/render_layered_test.mjs.
//
// What it holds that no Go test can, and what is this renderer's alone.
//
// That **a broken edge is marked rather than hidden**. `layered`'s
// consumption note says "expected mostly acyclic"; a ranked drawing of a
// graph with a cycle is only possible because something ran an edge
// backwards, and a picture that quietly reversed an arrow would show a
// prerequisite chain the wrong way round while looking perfectly
// correct. So the arrowhead is asserted to sit on the relation's **true**
// target and to face back up the picture, the double-slash is asserted
// to straddle the middle of that line, and every other edge is asserted
// to carry neither.
//
// That **the frame's sentence stays a drawing report**. It counts the
// edges running against the ranking and says the graph has a cycle only
// when the traversal actually found one — the two come apart the moment
// `rank_by` names a number field, where a relation from level 5 to level
// 2 runs backwards through a perfectly acyclic graph. It never names the
// nodes and never says "unreachable": sub-project 6 owns that answer,
// and a renderer that guessed would publish a result nobody computed.
//
// That **the two ranking policies produce two captions**, which is the
// entire reason `rank_by` takes a field: ranking by the edges captions
// each band with its index, ranking by `level` captions it with the
// value. One fixture cannot tell those apart, so there are two, and the
// mutation that captions everything with its index turns exactly one of
// them red.
//
// And the negative half: a node whose numeric rank is absent goes to a
// **trailing** band, captioned, dashed, and never to rank zero where it
// would read as the start of a progression; an edge that leaves the
// picture is a stub; a relation to itself is counted rather than
// silently dropped; a node with no position is named rather than drawn
// at the origin; a truncated answer draws exactly what a clean one
// draws; and the twin and the picture agree about every node and edge of
// one answer.
func TestTheLayeredRenderer(t *testing.T) {
	nodeOrSkip(t)
	runJSTest(t, "jstest/render_layered_test.mjs")
}
