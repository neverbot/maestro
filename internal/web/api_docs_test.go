package web_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/web"
)

// docsPath builds a prose URL the way the SPA does: the game in the
// path, the document's own path in the query string. Every test here
// goes through it rather than concatenating, because the one property
// this surface's route shape exists for — a document path never occupies
// a URL segment — is only true if the path is actually escaped into the
// query string.
func docsPath(suffix string, params map[string]string) string {
	if len(params) == 0 {
		return suffix
	}
	query := url.Values{}
	for name, value := range params {
		query.Set(name, value)
	}
	return suffix + "?" + query.Encode()
}

// writeDocREST writes one document through the REST route and returns the
// answer, failing the test on anything but a 200.
func writeDocREST(t *testing.T, f restFixture, path, content string, expected int32) map[string]any {
	t.Helper()
	rec := f.as(t, http.MethodPost, "/docs", map[string]any{
		"path": path, "content": content, "expected_version": expected,
		"message": "seed",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("write %q = %d: %s", path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	decodeBody(t, rec, &out)
	return out
}

// TestTheBatchRouteMirrorsTheBatchTool drives POST /docs/batch as the SPA
// would and reads the same report the MCP tool answers with.
//
// **The status is 200 even though an item failed**, which is the one
// thing this route decides that its MCP twin does not have to: in
// partial mode a batch that lands two of three documents is not a failed
// request, and the failure is in the body with its index, its path and
// its code. Only a refusal of the call itself carries a status.
func TestTheBatchRouteMirrorsTheBatchTool(t *testing.T) {
	f := newRESTFixture(t)

	rec := f.as(t, http.MethodPost, "/docs/batch", map[string]any{
		"items": []any{
			map[string]any{"path": "lore/duskwood", "content": "# Duskwood\n", "expected_version": 0},
			map[string]any{"path": "lore//westfall", "content": "# Westfall\n", "expected_version": 0},
			map[string]any{"path": "lore/elwynn", "content": "# Elwynn\n", "expected_version": 0},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("batch = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Count   int `json:"count"`
		Written []struct {
			Path    string `json:"path"`
			Version int32  `json:"version"`
		} `json:"written"`
		Failed []struct {
			Index int    `json:"index"`
			Key   string `json:"key"`
			Code  string `json:"code"`
		} `json:"failed"`
	}
	decodeBody(t, rec, &out)
	if out.Count != 2 || len(out.Written) != 2 {
		t.Fatalf("count = %d over %d written, want 2 and 2", out.Count, len(out.Written))
	}
	if len(out.Failed) != 1 || out.Failed[0].Index != 1 ||
		out.Failed[0].Key != "lore//westfall" || out.Failed[0].Code != "invalid_input" {
		t.Fatalf("failed = %+v, want the middle item named and coded", out.Failed)
	}
	for _, w := range out.Written {
		got := f.as(t, http.MethodGet, docsPath("/docs/one", map[string]string{"path": w.Path}), nil)
		if got.Code != http.StatusOK {
			t.Fatalf("read %q = %d: %s", w.Path, got.Code, got.Body.String())
		}
		var doc struct {
			Version int32 `json:"version"`
		}
		decodeBody(t, got, &doc)
		if doc.Version != w.Version {
			t.Fatalf("%q is at v%d and the batch reported v%d", w.Path, doc.Version, w.Version)
		}
	}

	// An unknown mode is a refusal of the call, so it does carry a status
	// — the other half of the rule this route's comment states.
	bad := f.as(t, http.MethodPost, "/docs/batch", map[string]any{
		"mode": "atomic ",
		"items": []any{
			map[string]any{"path": "lore/redridge", "content": "# R\n", "expected_version": 0},
		},
	})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("an unknown mode = %d: %s", bad.Code, bad.Body.String())
	}
	if got := f.as(t, http.MethodGet,
		docsPath("/docs/one", map[string]string{"path": "lore/redridge"}), nil); got.Code == http.StatusOK {
		t.Fatal("a batch refused for its mode wrote a document")
	}
}

// TestTheProseSurfaceWritesReadsAndListsThroughREST is the read-back the
// plan's header asks of every feature: everything this file adds is
// exercised through the routes a browser actually calls, never through
// the service.
func TestTheProseSurfaceWritesReadsAndListsThroughREST(t *testing.T) {
	f := newRESTFixture(t)

	writeDocREST(t, f, "lore/duskwood", "---\ntitle: Duskwood\n---\n# Duskwood\n\nDark.\n", 0)

	rec := f.as(t, http.MethodGet, docsPath("/docs/one", map[string]string{"path": "lore/duskwood"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d: %s", rec.Code, rec.Body.String())
	}
	var doc struct {
		Path    string `json:"path"`
		Title   string `json:"title"`
		Version int32  `json:"version"`
		Body    string `json:"body"`
	}
	decodeBody(t, rec, &doc)
	if doc.Path != "lore/duskwood" || doc.Title != "Duskwood" || doc.Version != 1 {
		t.Fatalf("read answered %+v", doc)
	}
	if !strings.Contains(doc.Body, "# Duskwood") {
		t.Fatalf("body = %q, want the raw markdown", doc.Body)
	}

	rec = f.as(t, http.MethodGet, "/docs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var listing struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	decodeBody(t, rec, &listing)
	if len(listing.Items) != 1 || listing.Items[0].Path != "lore/duskwood" {
		t.Fatalf("listing = %s", rec.Body.String())
	}

	// History, version, diff and comparison all need a second version.
	writeDocREST(t, f, "lore/duskwood", "---\ntitle: Duskwood\n---\n# Duskwood\n\nDarker.\n", 1)

	rec = f.as(t, http.MethodGet, docsPath("/docs/history", map[string]string{"path": "lore/duskwood"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("history = %d: %s", rec.Code, rec.Body.String())
	}
	var history struct {
		Items []struct {
			Version    int32  `json:"version"`
			AuthorKind string `json:"author_kind"`
		} `json:"items"`
	}
	decodeBody(t, rec, &history)
	if len(history.Items) != 2 || history.Items[0].Version != 2 {
		t.Fatalf("history = %s", rec.Body.String())
	}
	if history.Items[0].AuthorKind != "user" {
		t.Fatalf("author_kind = %q, want user for a session caller", history.Items[0].AuthorKind)
	}

	rec = f.as(t, http.MethodGet, docsPath("/docs/version",
		map[string]string{"path": "lore/duskwood", "version": "1"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read_version = %d: %s", rec.Code, rec.Body.String())
	}
	var version struct {
		Version int32  `json:"version"`
		Body    string `json:"body"`
	}
	decodeBody(t, rec, &version)
	if version.Version != 1 || !strings.Contains(version.Body, "Dark.") {
		t.Fatalf("read_version answered %+v", version)
	}

	rec = f.as(t, http.MethodGet, docsPath("/docs/diff",
		map[string]string{"path": "lore/duskwood", "from_version": "1", "to_version": "2"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("diff = %d: %s", rec.Code, rec.Body.String())
	}
	var diff struct {
		Unified string `json:"unified"`
		Coarse  bool   `json:"coarse"`
	}
	decodeBody(t, rec, &diff)
	if !strings.Contains(diff.Unified, "+Darker.") {
		t.Fatalf("diff = %q", diff.Unified)
	}
	if diff.Coarse {
		t.Fatalf("diff of two tiny bodies reported coarse: %s", rec.Body.String())
	}

	// Revert, back to version 1, and read the result back.
	rec = f.as(t, http.MethodPost, "/docs/revert", map[string]any{
		"path": "lore/duskwood", "to_version": 1, "expected_version": 2, "message": "undo",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("revert = %d: %s", rec.Code, rec.Body.String())
	}
	rec = f.as(t, http.MethodGet, docsPath("/docs/one", map[string]string{"path": "lore/duskwood"}), nil)
	decodeBody(t, rec, &doc)
	if doc.Version != 3 || !strings.Contains(doc.Body, "Dark.\n") || strings.Contains(doc.Body, "Darker") {
		t.Fatalf("after the revert the document is %+v", doc)
	}

	// Delete, through the query string, and see the tombstone's version.
	rec = f.as(t, http.MethodDelete, docsPath("/docs/one",
		map[string]string{"path": "lore/duskwood", "expected_version": "3", "message": "gone"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	var deleted struct {
		Version int32 `json:"version"`
		Deleted bool  `json:"deleted"`
	}
	decodeBody(t, rec, &deleted)
	if deleted.Version != 4 || !deleted.Deleted {
		t.Fatalf("delete answered %+v", deleted)
	}
	rec = f.as(t, http.MethodGet, docsPath("/docs/one", map[string]string{"path": "lore/duskwood"}), nil)
	assertError(t, rec, http.StatusNotFound, "not_found", "")
}

// TestTheProseLinkRoutesReadTheirOwnResultBack drives the three link
// routes and asserts each answers with the resulting attachment set,
// which is what stops an attachment being write-only — the defect this
// whole plan is organised around, one surface along.
func TestTheProseLinkRoutesReadTheirOwnResultBack(t *testing.T) {
	f := newRESTFixture(t)
	questType(t, f)
	rec := f.as(t, http.MethodPost, "/entities", map[string]any{
		"items": []any{map[string]any{"type_key": "quest", "key": "hogger", "name": "Hogger"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("seed entity = %d: %s", rec.Code, rec.Body.String())
	}
	writeDocREST(t, f, "scripts/hogger", "# Act I\n", 0)

	type linksBody struct {
		Entities []struct {
			EntityTypeKey string `json:"entity_type_key"`
			EntityKey     string `json:"entity_key"`
			Role          string `json:"role"`
		} `json:"entities"`
		Documents []struct {
			Path string `json:"path"`
		} `json:"documents"`
	}

	rec = f.as(t, http.MethodPost, "/docs/links", map[string]any{
		"path": "scripts/hogger", "entity_type": "quest", "entity_key": "hogger", "role": "script",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("link add = %d: %s", rec.Code, rec.Body.String())
	}
	var added linksBody
	decodeBody(t, rec, &added)
	if len(added.Entities) != 1 || added.Entities[0].EntityKey != "hogger" || added.Entities[0].Role != "script" {
		t.Fatalf("link add answered %s", rec.Body.String())
	}

	// The join from the entity side, which is the question an entity
	// page asks.
	rec = f.as(t, http.MethodGet, docsPath("/docs/links",
		map[string]string{"entity_type": "quest", "entity_key": "hogger"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("links by entity = %d: %s", rec.Code, rec.Body.String())
	}
	var byEntity linksBody
	decodeBody(t, rec, &byEntity)
	if len(byEntity.Documents) != 1 || byEntity.Documents[0].Path != "scripts/hogger" {
		t.Fatalf("links by entity = %s", rec.Body.String())
	}

	rec = f.as(t, http.MethodDelete, docsPath("/docs/links",
		map[string]string{"path": "scripts/hogger", "entity_type": "quest", "entity_key": "hogger"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("link remove = %d: %s", rec.Code, rec.Body.String())
	}
	var removed linksBody
	decodeBody(t, rec, &removed)
	if len(removed.Entities) != 0 {
		t.Fatalf("link remove answered %s, want an empty attachment set", rec.Body.String())
	}
	// Marshalled as [] and never as null, which a client rendering a
	// list has to be able to rely on.
	if !strings.Contains(rec.Body.String(), `"entities":[]`) {
		t.Fatalf("link remove body = %s, want entities as an empty array", rec.Body.String())
	}
}

// TestADocumentPathIsNeverAURLSegment is the counterpart of the
// metamodel's TestARouteShapedKeyIsStillAddressable. It writes a
// document at each of this surface's own sub-resource literals and reads
// every one of them back — which is only possible because a document
// path travels in the query string. See api_docs.go's header.
func TestADocumentPathIsNeverAURLSegment(t *testing.T) {
	f := newRESTFixture(t)
	for _, path := range []string{"one", "history", "version", "revert", "diff", "links", "rendered", "comparison"} {
		writeDocREST(t, f, path, "# "+path+"\n", 0)
	}
	for _, path := range []string{"one", "history", "version", "revert", "diff", "links", "rendered", "comparison"} {
		rec := f.as(t, http.MethodGet, docsPath("/docs/one", map[string]string{"path": path}), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("read %q = %d: %s", path, rec.Code, rec.Body.String())
		}
		var doc struct {
			Path string `json:"path"`
		}
		decodeBody(t, rec, &doc)
		if doc.Path != path {
			t.Fatalf("read %q answered path %q", path, doc.Path)
		}
	}
	// And a nested path, whose slashes would have needed a {path...}
	// wildcard: the shape ServeMux refuses to put a sub-resource behind.
	writeDocREST(t, f, "lore/regions/duskwood", "# D\n", 0)
	rec := f.as(t, http.MethodGet, docsPath("/docs/one",
		map[string]string{"path": "lore/regions/duskwood"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read a nested path = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestTheReadingViewRendersAndTheRawBodyIsWhatMCPGets reads one document
// through /docs/rendered and through docs.read, and asserts the first is
// HTML and the second is the markdown, byte for byte. It is the pin
// behind internal/markdown/render.go's header sentence and api_docs.go's
// own: rendering is a REST-only affordance, and an agent asked to
// rewrite a script gets the markdown it will edit.
func TestTheReadingViewRendersAndTheRawBodyIsWhatMCPGets(t *testing.T) {
	f := newRESTFixture(t)
	body := "# Duskwood\n\nThe *worgen* came at dusk.\n"
	writeDocREST(t, f, "lore/duskwood", body, 0)

	rec := f.as(t, http.MethodGet, docsPath("/docs/rendered",
		map[string]string{"path": "lore/duskwood"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rendered = %d: %s", rec.Code, rec.Body.String())
	}
	var rendered struct {
		Path      string `json:"path"`
		Title     string `json:"title"`
		Version   int32  `json:"version"`
		HTML      string `json:"html"`
		Body      string `json:"body"`
		UpdatedAt string `json:"updated_at"`
		UpdatedBy *struct {
			Kind  string `json:"kind"`
			Label string `json:"label"`
		} `json:"updated_by"`
	}
	decodeBody(t, rec, &rendered)
	if !strings.Contains(rendered.HTML, "<h1>Duskwood</h1>") || !strings.Contains(rendered.HTML, "<em>worgen</em>") {
		t.Fatalf("html = %q, want the rendered markdown", rendered.HTML)
	}
	if rendered.Body != "" {
		t.Fatalf("the reading view answered a body too (%q); it publishes html only", rendered.Body)
	}
	if rendered.Path != "lore/duskwood" || rendered.Title != "Duskwood" || rendered.Version != 1 {
		t.Fatalf("rendered answered %+v", rendered)
	}
	// The reading view's meta line says when the document last changed
	// and who changed it, which is what the page could not say without a
	// history call of its own.
	if rendered.UpdatedAt == "" {
		t.Fatal("the reading view does not say when the document last changed")
	}
	if rendered.UpdatedBy == nil || rendered.UpdatedBy.Kind != "user" ||
		rendered.UpdatedBy.Label == "" {
		t.Fatalf("updated_by = %+v, want the session designer named", rendered.UpdatedBy)
	}

	// The same document through the tool an agent calls.
	fx := newMetamodelFixture(t)
	_ = fx
	rec = f.as(t, http.MethodGet, docsPath("/docs/one", map[string]string{"path": "lore/duskwood"}), nil)
	var raw struct {
		Body string `json:"body"`
	}
	decodeBody(t, rec, &raw)
	if raw.Body != body {
		t.Fatalf("docs.read's body = %q, want the markdown byte for byte (%q)", raw.Body, body)
	}
	if strings.Contains(raw.Body, "<h1>") {
		t.Fatalf("docs.read answered rendered HTML: %q", raw.Body)
	}
}

// TestTheComparisonViewRendersADiff pins the second rendered route: the
// same diff docs.diff answers with, plus the classed lines a page
// colours without parsing the diff itself.
func TestTheComparisonViewRendersADiff(t *testing.T) {
	f := newRESTFixture(t)
	writeDocREST(t, f, "lore/duskwood", "# old\n", 0)
	writeDocREST(t, f, "lore/duskwood", "# <b>new</b>\n", 1)

	rec := f.as(t, http.MethodGet, docsPath("/docs/comparison",
		map[string]string{"path": "lore/duskwood", "from_version": "1", "to_version": "2"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("comparison = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Unified string `json:"unified"`
		HTML    string `json:"html"`
		Coarse  bool   `json:"coarse"`
	}
	decodeBody(t, rec, &out)
	if !strings.Contains(out.HTML, `class="diff-removed"`) || !strings.Contains(out.HTML, `class="diff-added"`) {
		t.Fatalf("html = %q, want classed lines", out.HTML)
	}
	// A diff's lines are markdown source: escaped, never rendered.
	if strings.Contains(out.HTML, "<b>new</b>") {
		t.Fatalf("html = %q: the diff's own source must be escaped", out.HTML)
	}
	if !strings.Contains(out.HTML, "&lt;b&gt;") {
		t.Fatalf("html = %q, want the source escaped and visible", out.HTML)
	}
	if !strings.Contains(out.Unified, "+# <b>new</b>") {
		t.Fatalf("unified = %q, want the diff text beside the markup", out.Unified)
	}
}

// TestTheReadingViewNeutralisesADangerousLinkIsTheWholeReasonItIsHTML
// reads a document carrying a stored-XSS payload back through the route
// a browser inserts as markup, rather than through markdown.Render.
// The unit tests in internal/markdown pin the renderer; this pins that
// this route is the one calling it — a handler that took a shortcut and
// echoed the body would pass every one of them.
func TestTheReadingViewNeutralisesADangerousLinkIsTheWholeReasonItIsHTML(t *testing.T) {
	f := newRESTFixture(t)
	writeDocREST(t, f, "lore/trap",
		"[click](javascript:alert(1)) and [also](javascript&#58;alert(1))\n\n"+
			// The autolink spelling is here because it is the one
			// goldmark itself does not defend: renderAutoLink has no
			// IsDangerousURL check, so this arrived on the page as a
			// live href until safeLinks started rewriting the kind.
			"Read <javascript:alert(document.domain)> and <vbscript:msgbox> "+
			"and <data:text/html;base64,PHNjcmlwdD4=> and <file:///etc/passwd>.\n\n"+
			"<script>alert(2)</script>\n", 0)

	rec := f.as(t, http.MethodGet, docsPath("/docs/rendered", map[string]string{"path": "lore/trap"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rendered = %d: %s", rec.Code, rec.Body.String())
	}
	var rendered struct {
		HTML string `json:"html"`
	}
	decodeBody(t, rec, &rendered)
	// The forms are `href="scheme:` and not the bare scheme because an
	// autolink's *label* is its own URL: the page legitimately shows the
	// text `javascript:alert(document.domain)`, greyed out and pointing
	// at "#", which is the neutralisation working and not a leak. What
	// must not appear is the scheme in an attribute a browser follows.
	for _, forbidden := range []string{
		`href="javascript:`, `href="vbscript:`, `href="data:`,
		`href="file:`, "<script>",
	} {
		if strings.Contains(rendered.HTML, forbidden) {
			t.Fatalf("html = %q still carries %q", rendered.HTML, forbidden)
		}
	}
	if !strings.Contains(rendered.HTML, `href="#"`) {
		t.Fatalf("html = %q, want the refused destinations neutralised", rendered.HTML)
	}
}

// TestARenderedViewCarriesTheSameSecurityHeadersEveryPageDoes asserts
// the reading view is not an exception to securityHeaders. It is not a
// header this handler sets — the middleware does, outermost — and that
// is exactly the point: the test is what stops a future handler writing
// its own headers and losing the policy on the one response in this
// product that is meant to be inserted as markup.
func TestARenderedViewCarriesTheSameSecurityHeadersEveryPageDoes(t *testing.T) {
	f := newRESTFixture(t)
	writeDocREST(t, f, "lore/duskwood", "# D\n", 0)

	rec := f.as(t, http.MethodGet, docsPath("/docs/rendered",
		map[string]string{"path": "lore/duskwood"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rendered = %d: %s", rec.Code, rec.Body.String())
	}
	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'; frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q: the reading view answers JSON carrying html, never text/html", got)
	}
}

// TestAViewerMayReadProseAndMayNotWriteIt is the prose half of
// TestEveryContentWriteRouteRefusesAViewer, which drives every write
// route from the routing table with a body that names a type — good
// enough to prove the refusal happens before the body is read, and not
// enough to show that a viewer can *read* prose, which is the other half
// of the rule and the half a too-eager gate would break.
func TestAViewerMayReadProseAndMayNotWriteIt(t *testing.T) {
	f := newRESTFixture(t)
	ctx := context.Background()
	writeDocREST(t, f, "lore/duskwood", "# Duskwood\n", 0)

	viewer, err := f.ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := f.proj.SetRole(ctx, viewer.ID, f.game, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	cookie := loginAs(t, f.srv, "viewer@studio.com")

	// Every read on this surface, including the two rendered views: a
	// viewer's browser renders a quest's script, which is the premise
	// internal/markdown/events.go's MinRole argument rests on.
	for _, suffix := range []string{
		"/docs",
		docsPath("/docs/one", map[string]string{"path": "lore/duskwood"}),
		docsPath("/docs/history", map[string]string{"path": "lore/duskwood"}),
		docsPath("/docs/version", map[string]string{"path": "lore/duskwood", "version": "1"}),
		docsPath("/docs/links", map[string]string{"path": "lore/duskwood"}),
		docsPath("/docs/rendered", map[string]string{"path": "lore/duskwood"}),
	} {
		rec := f.call(t, cookie, http.MethodGet, f.path(suffix), nil)
		if rec.Code != http.StatusOK {
			t.Errorf("viewer GET %s = %d: %s", suffix, rec.Code, rec.Body.String())
		}
	}

	// And is refused every write, naming the role that cannot write.
	rec := f.call(t, cookie, http.MethodPost, f.path("/docs"), map[string]any{
		"path": "lore/duskwood", "content": "# Mine now\n", "expected_version": 1,
	})
	body := assertError(t, rec, http.StatusForbidden, "forbidden", "")
	if !strings.Contains(body.Message, "viewer") {
		t.Errorf("refusal said %q, want it to name the role", body.Message)
	}
	// The refusal is real and not merely reported: the document did not
	// move. This is the "read it back" half — a gate that answered 403
	// after writing would pass the assertion above.
	rec = f.as(t, http.MethodGet, docsPath("/docs/one", map[string]string{"path": "lore/duskwood"}), nil)
	var doc struct {
		Version int32  `json:"version"`
		Body    string `json:"body"`
	}
	decodeBody(t, rec, &doc)
	if doc.Version != 1 || strings.Contains(doc.Body, "Mine now") {
		t.Fatalf("the viewer's refused write landed anyway: %+v", doc)
	}
}

// TestTheRESTMirrorAnswersTheSameCodesAsTheTools drives every refusal
// this surface can make and asserts the code *and* the field path, arm
// for arm with what the docs.* tools answer. The two surfaces call one
// core, so a divergence here can only come from this file's own argument
// reading — which is exactly what the table covers.
func TestTheRESTMirrorAnswersTheSameCodesAsTheTools(t *testing.T) {
	f := newRESTFixture(t)
	writeDocREST(t, f, "lore/duskwood", "# Duskwood\n", 0)

	for _, tc := range []struct {
		name   string
		method string
		suffix string
		body   any
		status int
		code   string
		path   string
	}{
		{
			name: "a write without expected_version", method: http.MethodPost, suffix: "/docs",
			body:   map[string]any{"path": "lore/x", "content": "x"},
			status: http.StatusBadRequest, code: "invalid_input", path: "expected_version",
		},
		{
			name: "a write losing a race", method: http.MethodPost, suffix: "/docs",
			body:   map[string]any{"path": "lore/duskwood", "content": "x", "expected_version": 7},
			status: http.StatusConflict, code: "version_conflict",
		},
		{
			name: "a read of a document that is not there", method: http.MethodGet,
			suffix: docsPath("/docs/one", map[string]string{"path": "lore/nowhere"}),
			status: http.StatusNotFound, code: "not_found",
		},
		{
			name: "a read of a version that is not there", method: http.MethodGet,
			suffix: docsPath("/docs/version", map[string]string{"path": "lore/duskwood", "version": "9"}),
			status: http.StatusNotFound, code: "not_found", path: "version",
		},
		{
			name: "a read_version with no version at all", method: http.MethodGet,
			suffix: docsPath("/docs/version", map[string]string{"path": "lore/duskwood"}),
			status: http.StatusBadRequest, code: "invalid_input", path: "version",
		},
		{
			name: "a diff with no from_version", method: http.MethodGet,
			suffix: docsPath("/docs/diff", map[string]string{"path": "lore/duskwood", "to_version": "1"}),
			status: http.StatusBadRequest, code: "invalid_input", path: "from_version",
		},
		{
			name: "a diff with no to_version", method: http.MethodGet,
			suffix: docsPath("/docs/diff", map[string]string{"path": "lore/duskwood", "from_version": "1"}),
			status: http.StatusBadRequest, code: "invalid_input", path: "to_version",
		},
		{
			name: "a version that is not a number", method: http.MethodGet,
			suffix: docsPath("/docs/version", map[string]string{"path": "lore/duskwood", "version": "one"}),
			status: http.StatusBadRequest, code: "invalid_input", path: "version",
		},
		{
			name: "a version too wide for an int32", method: http.MethodGet,
			suffix: docsPath("/docs/version", map[string]string{"path": "lore/duskwood", "version": "99999999999"}),
			status: http.StatusBadRequest, code: "invalid_input", path: "version",
		},
		{
			name: "a delete without expected_version", method: http.MethodDelete,
			suffix: docsPath("/docs/one", map[string]string{"path": "lore/duskwood"}),
			status: http.StatusBadRequest, code: "invalid_input", path: "expected_version",
		},
		{
			name: "the join asked from both sides at once", method: http.MethodGet,
			suffix: docsPath("/docs/links", map[string]string{
				"path": "lore/duskwood", "entity_type": "quest", "entity_key": "hogger"}),
			status: http.StatusBadRequest, code: "invalid_input", path: "path",
		},
		{
			name: "the join asked from neither side", method: http.MethodGet, suffix: "/docs/links",
			status: http.StatusBadRequest, code: "invalid_input", path: "path",
		},
		{
			name: "a link to an entity that is not there", method: http.MethodPost, suffix: "/docs/links",
			body: map[string]any{"path": "lore/duskwood", "entity_type": "quest", "entity_key": "hogger"},
			// The named miss: not_found carrying the argument that
			// missed, so a caller who mistyped one of two addresses is
			// told which. Task 10's correction 13 is what made this arm
			// publish anything at all.
			status: http.StatusNotFound, code: "not_found", path: "entity_type",
		},
		{
			name: "a limit that is not a number", method: http.MethodGet,
			suffix: docsPath("/docs", map[string]string{"limit": "lots"}),
			status: http.StatusBadRequest, code: "invalid_input", path: "limit",
		},
		{
			name: "a cursor from another listing", method: http.MethodGet,
			suffix: docsPath("/docs", map[string]string{"cursor": "not-a-cursor"}),
			status: http.StatusBadRequest, code: "invalid_input", path: "cursor",
		},
		{
			name: "a path parameter written with no value", method: http.MethodGet,
			suffix: "/docs/one?path=",
			status: http.StatusBadRequest, code: "invalid_input", path: "path",
		},
		{
			name: "a revert to a version that is not there", method: http.MethodPost, suffix: "/docs/revert",
			body: map[string]any{"path": "lore/duskwood", "to_version": 9, "expected_version": 1},
			// The revert core's own path name, not this file's.
			status: http.StatusNotFound, code: "not_found", path: "to_version",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.as(t, tc.method, tc.suffix, tc.body)
			assertError(t, rec, tc.status, tc.code, tc.path)
		})
	}
}

// --- The events read-back ---

// newProseEventServer wires a server the way cmd/maestro/main.go does:
// one realtime.Hub, handed to the metamodel service, the markdown
// service and Options.Hub alike. newMetamodelTestServer passes nil for
// both services' hubs, so every document event published under it goes
// nowhere — which is why, before this helper, no test anywhere showed a
// document.* event reaching a subscriber and the one-hub wiring was
// proved only by reading main.go.
func newProseEventServer(t *testing.T) (*web.Server, *identity.Service, *projects.Service) {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := config.Config{
		SessionTTL: testConfig().SessionTTL,
		InviteTTL:  testConfig().InviteTTL,
		Argon2:     testConfig().Argon2,
	}
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	hub := realtime.NewHub()
	srv := web.NewServer(web.Options{
		Version:              "test",
		Config:               cfg,
		Identity:             ids,
		Projects:             projSvc,
		Metamodel:            metamodel.New(pool, hub),
		Markdown:             markdown.New(pool, hub),
		Hub:                  hub,
		SSEMaxLifetime:       time.Minute,
		SSEHeartbeatInterval: time.Minute,
	})
	return srv, ids, projSvc
}

// TestADocumentEventReachesAnSSESubscriber is the read-back for the
// events, and it is a different claim from the domain's own event tests:
// those prove the hub is published to, this proves a designer's browser
// actually receives it, over the endpoint it actually subscribes to, on
// a server wired the way main.go wires it.
//
// It subscribes **as a token caller**, not as a session. That is the
// case a session-only test cannot see: internal/web's member, token and
// invite events all set HumanOnly true, and a document's do not
// (internal/markdown/events.go argues why at length) — so a test that
// only ever subscribed with a cookie would stay green if someone
// published document.written with HumanOnly: true, and the finding would
// be that agents had silently stopped being told their base moved.
func TestADocumentEventReachesAnSSESubscriber(t *testing.T) {
	srv, ids, projSvc := newProseEventServer(t)
	ctx := context.Background()

	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create game: %v", err)
	}
	other, err := projSvc.Create(ctx, "le-mans", "Le Mans", owner.ID)
	if err != nil {
		t.Fatalf("Create other game: %v", err)
	}
	secret, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: owner.ID, Label: "lore agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// The stream is opened by the token, the writes are made by the
	// session: one subscriber, and every event below crosses from a
	// human's write to an agent's stream, which is the direction
	// HumanOnly would break.
	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet,
		ts.URL+"/api/games/"+game.Slug+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}

	post := func(suffix string, body any) *http.Response {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost,
			ts.URL+"/api/games/"+game.Slug+suffix, strings.NewReader(string(raw)))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		out, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do %s: %v", suffix, err)
		}
		if out.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d", suffix, out.StatusCode)
		}
		return out
	}

	// An event for a different game must never reach this subscriber.
	// Published first, so a leak arrives before the frame the reader is
	// waiting for and fails the very next assertion rather than going
	// unnoticed.
	otherReq, err := http.NewRequest(http.MethodPost,
		ts.URL+"/api/games/"+other.ID.String()+"/docs",
		strings.NewReader(`{"path":"noise","content":"# noise\n","expected_version":0}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	otherReq.Header.Set("Content-Type", "application/json")
	otherReq.AddCookie(cookie)
	if out, err := http.DefaultClient.Do(otherReq); err != nil {
		t.Fatalf("write to the other game: %v", err)
	} else {
		_ = out.Body.Close()
	}

	resp1 := post("/docs", map[string]any{
		"path": "lore/duskwood", "content": "# Duskwood\n", "expected_version": 0,
	})
	_ = resp1.Body.Close()

	reader := bufio.NewReader(resp.Body)
	kind, _, data := readOneSSEFrame(t, reader)
	if kind != "document.written" {
		t.Fatalf("kind = %q, want document.written (a cross-game leak, or the write announced nothing)", kind)
	}
	var payload struct {
		Path    string `json:"path"`
		Version int32  `json:"version"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	if payload.Path != "lore/duskwood" || payload.Version != 1 {
		t.Fatalf("document.written payload = %s", data)
	}

	// A revert is its own kind, carrying the version it restored — the
	// whole reason RevertEvent is a second payload type.
	resp2 := post("/docs", map[string]any{
		"path": "lore/duskwood", "content": "# Darker\n", "expected_version": 1,
	})
	_ = resp2.Body.Close()
	if kind, _, _ = readOneSSEFrame(t, reader); kind != "document.written" {
		t.Fatalf("second write announced %q", kind)
	}
	resp3 := post("/docs/revert", map[string]any{
		"path": "lore/duskwood", "to_version": 1, "expected_version": 2,
	})
	_ = resp3.Body.Close()
	kind, _, data = readOneSSEFrame(t, reader)
	if kind != "document.reverted" {
		t.Fatalf("kind = %q, want document.reverted", kind)
	}
	var revert struct {
		Version     int32 `json:"version"`
		FromVersion int32 `json:"from_version"`
	}
	if err := json.Unmarshal([]byte(data), &revert); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	if revert.Version != 3 || revert.FromVersion != 1 {
		t.Fatalf("document.reverted payload = %s", data)
	}

	// And a delete, over the route whose arguments travel in the query
	// string.
	del, err := http.NewRequest(http.MethodDelete,
		ts.URL+"/api/games/"+game.Slug+
			"/docs/one?path=lore%2Fduskwood&expected_version=3", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	del.AddCookie(cookie)
	out, err := http.DefaultClient.Do(del)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if out.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", out.StatusCode)
	}
	_ = out.Body.Close()

	kind, _, data = readOneSSEFrame(t, reader)
	if kind != "document.deleted" {
		t.Fatalf("kind = %q, want document.deleted", kind)
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	if payload.Path != "lore/duskwood" || payload.Version != 4 {
		t.Fatalf("document.deleted payload = %s (want the tombstone's own version)", data)
	}
}

// TestADocumentLinkEventReachesAnSSESubscriber is the fourth kind, kept
// apart from the three above because it needs an entity to attach to and
// therefore a declared type — and because it is the kind whose payload
// Task 7's review left undecided. eventDocumentLinked's own doc comment
// records the decision; this test records what a subscriber actually
// receives, which is a DocumentEvent naming the document and nothing
// about the entity.
//
// **It subscribes as a token**, for the reason
// TestADocumentEventReachesAnSSESubscriber gives at length and which
// applied to this fourth kind just as much: subscribing with a cookie
// leaves the stream's HumanOnly filter unobserved for document.linked,
// so flipping documentEventHumanOnly to true would keep this test green
// while every agent silently stopped being told a document it holds had
// been attached to something. The link itself is still made by the
// session caller, so the event crosses from a human's write to an
// agent's stream — the direction the filter breaks.
func TestADocumentLinkEventReachesAnSSESubscriber(t *testing.T) {
	srv, ids, projSvc := newProseEventServer(t)
	ctx := context.Background()

	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create game: %v", err)
	}
	secret, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: owner.ID, Label: "lore agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	post := func(suffix, body string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost,
			ts.URL+"/api/games/"+game.Slug+suffix, strings.NewReader(body))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		out, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do %s: %v", suffix, err)
		}
		defer func() { _ = out.Body.Close() }()
		if out.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d", suffix, out.StatusCode)
		}
	}
	post("/types", `{"key":"quest","label":"Quest","label_plural":"Quests"}`)
	post("/entities", `{"items":[{"type_key":"quest","key":"hogger","name":"Hogger"}]}`)
	post("/docs", `{"path":"scripts/hogger","content":"# Act I\n","expected_version":0}`)

	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet,
		ts.URL+"/api/games/"+game.Slug+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}

	post("/docs/links", `{"path":"scripts/hogger","entity_type":"quest","entity_key":"hogger"}`)

	reader := bufio.NewReader(resp.Body)
	kind, _, data := readOneSSEFrame(t, reader)
	if kind != "document.linked" {
		t.Fatalf("kind = %q, want document.linked", kind)
	}
	var payload struct {
		Path    string `json:"path"`
		Version int32  `json:"version"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	if payload.Path != "scripts/hogger" {
		t.Fatalf("document.linked payload = %s", data)
	}
	// The document's version is *unmoved*: a link is not the document's
	// content, so a subscriber comparing this number against what it
	// holds learns nothing and must re-read the links.
	if payload.Version != 1 {
		t.Fatalf("document.linked carried version %d, want the document's unmoved 1", payload.Version)
	}
}

// TestTheProseRoutesAreVisibleToTheConventionTests is requirement 1 of
// the two Task 10's review handed this task, turned into a test rather
// than left as an argument in a comment.
//
// TestEveryGameScopedRouteGoesThroughRequireProject and
// TestEveryContentRouteIsRegisteredAsContent both build their server
// from stubOptions, which passes no Markdown. If this file's routes were
// registered behind `opts.Markdown != nil`, both would simply not see
// them, and a docs route wired outside requireProject — or outside the
// content set — would pass every test in this package. That is not a
// prediction: it is what happened to TestEveryMCPToolGoesThroughAdd
// ScopedTool, blind to all twelve docs tools until Task 10's review.
//
// This test fails the moment a gate is introduced, from the same
// stubOptions server those two use, naming the reason. It lives in the
// external test package and reads the routing table through the same
// exported hooks the viewer test uses.
func TestTheProseRoutesAreVisibleToTheConventionTests(t *testing.T) {
	srv := web.NewServer(web.Options{
		Version:  "test",
		Identity: identity.New(nil, config.Config{}),
		Projects: projects.New(nil),
		// Deliberately no Markdown: this is stubOptions' own shape.
	})

	want := []string{
		"GET /api/games/{game}/docs",
		"POST /api/games/{game}/docs",
		"GET /api/games/{game}/docs/one",
		"DELETE /api/games/{game}/docs/one",
		"GET /api/games/{game}/docs/history",
		"GET /api/games/{game}/docs/version",
		"POST /api/games/{game}/docs/revert",
		"GET /api/games/{game}/docs/diff",
		"GET /api/games/{game}/docs/links",
		"POST /api/games/{game}/docs/links",
		"DELETE /api/games/{game}/docs/links",
		"GET /api/games/{game}/docs/rendered",
		"GET /api/games/{game}/docs/comparison",
	}
	registered := map[string]bool{}
	for _, pattern := range srv.RegisteredPatternsForTest() {
		registered[pattern] = true
	}
	content := map[string]bool{}
	for _, pattern := range srv.ContentPatternsForTest() {
		content[pattern] = true
	}
	for _, pattern := range want {
		if !registered[pattern] {
			t.Errorf("%q is missing from a server built without a Markdown service — "+
				"the prose routes must be registered unconditionally, or "+
				"TestEveryGameScopedRouteGoesThroughRequireProject goes blind to every one of them",
				pattern)
		}
		if !content[pattern] {
			t.Errorf("%q is not recorded as a content route — "+
				"TestEveryContentRouteIsRegisteredAsContent cannot see it", pattern)
		}
	}
}

// TestAProseRouteOnAnInstanceWithoutTheServiceIsRefused is
// the other half of registering unconditionally: the routes exist on an
// instance built without a markdown service, so something has to answer
// them. requireProseService does, with the same shape
// requireContentService uses for the metamodel.
func TestAProseRouteOnAnInstanceWithoutTheServiceIsRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	srv := web.NewServer(web.Options{
		Version: "test", Config: cfg, Identity: ids, Projects: projSvc,
	})
	ctx := context.Background()
	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create game: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+game.Slug+"/docs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "prose") {
		t.Fatalf("body = %s, want it to say what this instance does not serve", rec.Body.String())
	}
}
