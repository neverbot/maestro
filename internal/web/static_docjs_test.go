package web_test

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// docScriptSource reads internal/web/static/doc.js straight off disk, the
// same way appScriptSource reads app.js and for the same reason: the
// properties pinned below are about what the source does, so the test
// must fail the moment the source changes rather than only once some
// HTTP response happens to exercise it.
func docScriptSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("static/doc.js")
	if err != nil {
		t.Fatalf("read static/doc.js: %v", err)
	}
	return string(body)
}

func documentPageSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("static/document.html")
	if err != nil {
		t.Fatalf("read static/document.html: %v", err)
	}
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
//
// The reading view is the one page in this product whose whole purpose
// is to show markup: GET /docs/rendered and GET /docs/comparison answer
// with HTML that internal/markdown produced — goldmark configured
// without html.WithUnsafe, so a document body cannot contribute a tag at
// all, and RenderDiff, which escapes every line it classes. Inserting
// that as text would show a designer their own angle brackets.
//
// So doc.js may write markup, through exactly one function, and this
// test is what keeps "exactly one" true: a second sink added anywhere —
// to put a link inside a message, say — fails here rather than quietly
// widening the one place in internal/web/static where a string becomes
// markup.
func TestTheDocumentScriptHasExactlyOneHTMLSink(t *testing.T) {
	source := docScriptSource(t)
	found := htmlSinks.FindAllString(source, -1)
	if len(found) != 1 {
		t.Fatalf("doc.js has %d HTML sinks (%v); want exactly 1, inside setRenderedHTML", len(found), found)
	}
	sink := strings.Index(source, found[0])
	fn := strings.Index(source, "function setRenderedHTML(el, html) {")
	if fn == -1 {
		t.Fatal("doc.js no longer declares setRenderedHTML, which is where its one HTML sink must live")
	}
	end := strings.Index(source[fn:], "\n}\n")
	if end == -1 || sink < fn || sink > fn+end {
		t.Fatalf("doc.js's HTML sink is outside setRenderedHTML: every markup write must go through that one function")
	}
}

// TestTheDocumentScriptsHTMLSinkIsOnlyFedByARenderedView pins the other
// half, which "exactly one sink" on its own does not: that the one sink
// is only ever handed a field a rendered view answered with, and never a
// string this page assembled. A setRenderedHTML call built from a
// document title or a version message would pass the test above and be
// exactly the stored-XSS this whole arrangement exists to prevent.
func TestTheDocumentScriptsHTMLSinkIsOnlyFedByARenderedView(t *testing.T) {
	source := docScriptSource(t)
	calls := regexp.MustCompile(`setRenderedHTML\(([^)]*)\)`).FindAllStringSubmatch(source, -1)
	// One declaration plus the call sites; the declaration's argument
	// list is the parameter list, which is skipped by name.
	allowed := map[string]bool{
		"el, html":                 true, // the declaration itself
		"bodyEl, doc.html":         true, // GET /docs/rendered
		"outEl, result.body?.html": true, // GET /docs/comparison
	}
	seen := []string{}
	for _, call := range calls {
		args := strings.TrimSpace(call[1])
		seen = append(seen, args)
		if !allowed[args] {
			t.Errorf("setRenderedHTML(%s): the one HTML sink may only be fed a rendered view's own html field", args)
		}
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
	source := docScriptSource(t)
	shell := documentPageSource(t)
	ids := regexp.MustCompile(`getElementById\("([^"]+)"\)`).FindAllStringSubmatch(source, -1)
	if len(ids) == 0 {
		t.Fatal("doc.js looks up no elements by id; this test no longer pins anything")
	}
	for _, match := range ids {
		if !strings.Contains(shell, `id="`+match[1]+`"`) {
			t.Errorf("doc.js looks up #%s, which document.html does not declare", match[1])
		}
	}
}

// TestTheGamePageDeclaresTheDocumentsElementsAppScriptLooksUp is the same
// check for the four ids the documents catalogue added to game.html.
// app.js is shared by three shells, so it cannot be checked against one
// of them wholesale — these four are named because they are this task's
// own additions.
func TestTheGamePageDeclaresTheDocumentsElementsAppScriptLooksUp(t *testing.T) {
	body, err := os.ReadFile("static/game.html")
	if err != nil {
		t.Fatalf("read static/game.html: %v", err)
	}
	shell := string(body)
	source := appScriptSource(t)
	for _, id := range []string{"docs", "docs-empty", "docs-empty-action", "docs-error", "docs-more"} {
		if !strings.Contains(shell, `id="`+id+`"`) {
			t.Errorf("game.html does not declare #%s", id)
		}
		if !strings.Contains(source, `getElementById("`+id+`")`) {
			t.Errorf("app.js no longer looks up #%s", id)
		}
	}
}

// TestNeitherProseEmptyStateOffersAViewerAWrite pins the rule the parent
// task states for a page: an empty state must not promise an action the
// page cannot perform, and for a viewer the prose routes refuse every
// write, so the sentence a viewer reads must be a different sentence.
//
// Asserted on the source of the two functions that write those
// sentences, because both are branches on a role this test cannot reach
// through an HTTP response: GET /summary carries the role, and the
// branch is taken in the browser.
func TestNeitherProseEmptyStateOffersAViewerAWrite(t *testing.T) {
	source := appScriptSource(t)
	fn := strings.Index(source, "function describeWhoWritesDocuments(role) {")
	if fn == -1 {
		t.Fatal("app.js no longer decides the documents empty state from the caller's role")
	}
	end := strings.Index(source[fn:], "\n}\n")
	if end == -1 {
		t.Fatal("could not read describeWhoWritesDocuments's body")
	}
	body := source[fn : fn+end]
	if !strings.Contains(body, `role === "viewer"`) {
		t.Error("describeWhoWritesDocuments does not branch on the viewer role")
	}
	if !strings.Contains(body, "will refuse a write from you") {
		t.Error("the viewer's sentence no longer says the instance will refuse the write")
	}

	docSource := docScriptSource(t)
	if !strings.Contains(docSource, `role === "viewer"`) {
		t.Error("doc.js no longer tells a viewer that a revert will be refused")
	}
	if !strings.Contains(docSource, "will refuse a revert from you") {
		t.Error("doc.js's viewer note no longer says the instance will refuse the revert")
	}
}

// TestTheDocumentPageShipsItsSectionsHidden pins the real shell's
// initial state, which the Node harness (jstest/document_page_test.mjs)
// deliberately contradicts: that stub starts each flag at the opposite
// of what the case asserts, so it can tell "doc.js set this" from "it
// was already like that", and the price is that it no longer says
// anything about what document.html itself ships.
//
// It matters on its own account too. These four sections are empty until
// a request answers, so a shell that shipped them visible would show a
// reader an empty article, an empty history and two empty pickers for
// however long the network takes — and would leave all of them on screen
// forever if the request never answered.
func TestTheDocumentPageShipsItsSectionsHidden(t *testing.T) {
	shell := documentPageSource(t)
	for _, id := range []string{"doc-error", "doc-body", "doc-content", "doc-entities-empty", "comparison"} {
		element := regexp.MustCompile(`<[a-z]+[^>]*\bid="` + regexp.QuoteMeta(id) + `"[^>]*>`).FindString(shell)
		if element == "" {
			t.Errorf("document.html declares no element with id %q", id)
			continue
		}
		if !strings.Contains(element, "hidden") {
			t.Errorf("document.html ships #%s visible (%s); it is empty until a request answers", id, element)
		}
	}
}
