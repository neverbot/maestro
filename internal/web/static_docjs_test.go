package web_test

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// docScriptSource reads internal/web/static/doc.js straight off disk, the
// same way appScriptSource reads app.js and for the same reason: the
// properties pinned below are about what the source does, so the test
// must fail the moment the source changes rather than only once some
// HTTP response happens to exercise it.
func docScriptSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("static/doc.js")
	assert.Must(t, err == nil, "read static/doc.js: %v", err)
	return string(body)
}

func documentPageSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("static/document.html")
	assert.Must(t, err == nil, "read static/document.html: %v", err)
	return string(body)
}

// htmlSinks matches an actual DOM write that would interpret a string as
// markup, as a call or an assignment rather than as a bare word — the
// same expression TestAppScriptNeverWritesRawHTML uses, so the two files
// are judged by one rule and a sink added to either is caught by the
// same pattern.
var htmlSinks = regexp.MustCompile(`\.innerHTML\s*=|\.outerHTML\s*=|\.insertAdjacentHTML\s*\(|document\.write\s*\(`)

// TestTheDocumentScriptHasExactlyOneHTMLSink is the counterpart of
// TestAppScriptNeverWritesRawHTML, and it is deliberately a different
// rule for a different file.
func TestTheDocumentScriptHasExactlyOneHTMLSink(t *testing.T) {
	t.Parallel()
	source := docScriptSource(t)
	found := htmlSinks.FindAllString(source, -1)
	assert.Must(t, len(found) == 1, "doc.js has %d HTML sinks (%v); want exactly 1, inside setRenderedHTML", len(found), found)
	sink := strings.Index(source, found[0])
	fn := strings.Index(source, "function setRenderedHTML(el, html) {")
	assert.Must(t, fn != -1, "doc.js no longer declares setRenderedHTML, which is where its one HTML sink must live")
	end := strings.Index(source[fn:], "\n}\n")
	assert.Must(t, end != -1 && sink >= fn && sink <= fn+end, "doc.js's HTML sink is outside setRenderedHTML: every markup write must go through that one function")
}

// TestTheDocumentScriptsHTMLSinkIsOnlyFedByARenderedView pins the other
// half, which "exactly one sink" on its own does not: that the one sink
// is only ever handed a field a rendered view answered with, and never a
// string this page assembled. A setRenderedHTML call built from a
// document title or a version message would pass the test above and be
// exactly the stored-XSS this whole arrangement exists to prevent.
func TestTheDocumentScriptsHTMLSinkIsOnlyFedByARenderedView(t *testing.T) {
	t.Parallel()
	source := docScriptSource(t)
	calls := regexp.MustCompile(`setRenderedHTML\(([^)]*)\)`).FindAllStringSubmatch(source, -1)
	// One declaration plus the call sites; the declaration's argument
	// list is the parameter list, which is skipped by name.
	allowed := map[string]bool{
		"el, html":         true, // the declaration itself
		"bodyEl, doc.html": true, // GET /docs/rendered
		// GET /docs/comparison. The answer is bound to a local first —
		// the page reads three other fields off it to decide whether the
		// diff is worth drawing at all — so the binding itself is
		// checked below rather than the guard being loosened to "any
		// `.html`".
		"outEl, comparison.html": true,
	}
	seen := []string{}
	for _, call := range calls {
		args := strings.TrimSpace(call[1])
		seen = append(seen, args)
		assert.Should(t, allowed[args], "setRenderedHTML(%s): the one HTML sink may only be fed a rendered view's own html field", args)
	}
	// The local the comparison arm feeds the sink from, spelled out, so
	// "comparison.html" above cannot come to mean a string this page
	// assembled and called `comparison`.
	if !strings.Contains(source, "const comparison = result.body ?? {};") {
		t.Error("the comparison arm no longer binds the server's answer to `comparison`, " +
			"so `setRenderedHTML(outEl, comparison.html)` is no longer known to be a rendered view's own html")
	}
	if len(seen) != 3 {
		sort.Strings(seen)
		t.Fatalf("found %d setRenderedHTML sites (%v); want the declaration and the two rendered views", len(seen), seen)
	}
}

// TestTheDocumentPageDeclaresEveryElementItsScriptLooksUp catches the
// failure this page is most exposed to and the one no Go handler test
// can see: doc.js addresses the shell entirely by id, and a renamed or
// mistyped id makes a whole section — the history, the comparison, the
// failure line — silently not render, with no error anywhere. The page
// would look like a document with no history rather than like a broken
// page.
func TestTheDocumentPageDeclaresEveryElementItsScriptLooksUp(t *testing.T) {
	t.Parallel()
	source := docScriptSource(t)
	shell := documentPageSource(t)
	ids := regexp.MustCompile(`getElementById\("([^"]+)"\)`).FindAllStringSubmatch(source, -1)
	assert.Must(t, len(ids) != 0, "doc.js looks up no elements by id; this test no longer pins anything")
	for _, match := range ids {
		if !strings.Contains(shell, `id="`+match[1]+`"`) {
			t.Errorf("doc.js looks up #%s, which document.html does not declare", match[1])
		}
	}
}

// TestTheGamePageDeclaresTheDocumentsElementsItsScriptLooksUp is the same
// check for the five ids the prose lane needs on game.html.
func TestTheGamePageDeclaresTheDocumentsElementsItsScriptLooksUp(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("static/game.html")
	assert.Must(t, err == nil, "read static/game.html: %v", err)
	shell := string(body)
	source := homeScriptSource(t)
	for _, id := range []string{"docs", "docs-empty", "docs-error", "docs-more"} {
		assert.Should(t, strings.Contains(shell, `id="`+id+`"`), "game.html does not declare #%s", id)
		assert.Should(t, strings.Contains(source, `getElementById("`+id+`")`), "static/pages/home.js no longer looks up #%s", id)
	}
}

// homeScriptSource reads the module that draws the game home page.
func homeScriptSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("static/pages/home.js")
	assert.Must(t, err == nil, "read static/pages/home.js: %v", err)
	return string(body)
}

// TestNeitherProseEmptyStateOffersAViewerAWrite pins the rule the parent
// task states for a page: an empty state must not promise an action the
// page cannot perform, and for a viewer the prose routes refuse every
// write, so the sentence a viewer reads must be a different sentence.
func TestNeitherProseEmptyStateOffersAViewerAWrite(t *testing.T) {
	t.Parallel()
	// The sentence moved with the page in Task 15, into
	// static/pages/page.js's `whoWrites` — one function for both prose
	// and vocabulary, because "who may do this" is one rule with two
	// subjects rather than two functions that can come to disagree.
	raw, err := os.ReadFile("static/pages/page.js")
	assert.Must(t, err == nil, "read static/pages/page.js: %v", err)
	source := string(raw)
	fn := strings.Index(source, "export function whoWrites(role, what) {")
	assert.Must(t, fn != -1, "static/pages/page.js no longer decides an empty state's action from the caller's role")
	end := strings.Index(source[fn:], "\n}\n")
	assert.Must(t, end != -1, "could not read whoWrites's body")
	body := source[fn : fn+end]
	assert.Should(t, strings.Contains(body, "role === ROLE_VIEWER"), "whoWrites does not branch on the viewer role")
	assert.Should(t, strings.Contains(body, "will refuse a write from you"), "the viewer's sentence no longer says the instance will refuse the write")

	docSource := docScriptSource(t)
	assert.Should(t, strings.Contains(docSource, `role === "viewer"`), "doc.js no longer tells a viewer that a revert will be refused")
	assert.Should(t, strings.Contains(docSource, "will refuse a revert from you"), "doc.js's viewer note no longer says the instance will refuse the revert")
}

// TestTheDocumentPageShipsItsSectionsHidden pins the real shell's
// initial state, which the Node harness (jstest/document_page_test.mjs)
// deliberately contradicts: that stub starts each flag at the opposite
// of what the case asserts, so it can tell "doc.js set this" from "it
// was already like that", and the price is that it no longer says
// anything about what document.html itself ships.
func TestTheDocumentPageShipsItsSectionsHidden(t *testing.T) {
	t.Parallel()
	shell := documentPageSource(t)
	for _, id := range []string{
		"doc-error", "doc-body", "doc-content", "doc-entities-empty",
		"compare-note", "comparison",
	} {
		element := regexp.MustCompile(`<[a-z]+[^>]*\bid="` + regexp.QuoteMeta(id) + `"[^>]*>`).FindString(shell)
		if element == "" {
			t.Errorf("document.html declares no element with id %q", id)
			continue
		}
		assert.Should(t, strings.Contains(element, "hidden"), "document.html ships #%s visible (%s); it is empty until a request answers", id, element)
	}
}
