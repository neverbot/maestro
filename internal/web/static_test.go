package web_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLoginPageIsServed(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "form") {
		t.Fatal("the login page has no form")
	}
}

func TestStylesheetIsServed(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/static/styles.css", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("Content-Type = %q, want text/css", ct)
	}
}

func TestAppScriptIsServed(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("Content-Type = %q, want text/javascript", ct)
	}
}

// TestGamePageIsServedForAnySlug pins that GET /g/{slug} always serves the
// same static shell regardless of the slug in the URL — there is no
// server-side slug resolution on this route (Task 8's Round 2 Correction
// 12), so a slug that does not exist, or one for a game this caller
// cannot reach, still gets the shell; app.js is what discovers, from GET
// /api/games, whether the caller can actually reach it.
func TestGamePageIsServedForAnySlug(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/g/does-not-exist", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "game-name") {
		t.Fatal("the game page has no #game-name element")
	}
}

// TestDocumentPageIsServedForAnySlug pins that GET /g/{slug}/doc serves
// the reading view's shell whatever the slug is, exactly as /g/{slug}
// serves the game page's: this route resolves nothing server-side
// either, and doc.js is what discovers from GET /api/games whether the
// caller can reach the game at all.
//
// The document's own path is not in the URL's path and is not read here:
// it travels in the query string (see internal/web/api_docs.go's header),
// so this route never sees it.
func TestDocumentPageIsServedForAnySlug(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/g/does-not-exist/doc?path=lore%2Fduskwood", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "doc-title") {
		t.Fatal("the document page has no #doc-title element")
	}
}

// TestDocumentScriptIsServed pins the second ES module this product
// ships. It is a separate file rather than more of app.js because it is
// the only one allowed to write markup — see
// TestTheDocumentScriptHasExactlyOneHTMLSink.
func TestDocumentScriptIsServed(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/static/doc.js", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("Content-Type = %q, want text/javascript", ct)
	}
}

func TestUnknownStaticAssetIsNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/static/does-not-exist.js", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestStaticDoesNotServeHTMLShellsAgain pins a correction a quality review
// found: http.FileServerFS(assets), with no filtering, would happily serve
// /static/index.html, /static/login.html and /static/game.html as a
// second URL for each page — one that bypasses handleRoot's redirect
// decision (GET /{$}) entirely, since a request straight at /static/
// never goes through it. staticFileServer (static.go) 404s any request
// under /static/ ending in .html for exactly this reason.
func TestStaticDoesNotServeHTMLShellsAgain(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, name := range []string{"index.html", "login.html", "game.html", "document.html"} {
		req := httptest.NewRequest(http.MethodGet, "/static/"+name, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /static/%s: status = %d, want 404", name, rec.Code)
		}
	}
}

// TestSecurityHeadersArePresentOnEveryResponse pins securityHeaders
// (server.go): it wraps the whole handler chain, outermost, specifically
// so these three headers show up on every response this server writes —
// a healthcheck included — not only on routes that happen to reach a
// specific handler. The CSP value includes frame-ancestors 'none'
// (Task 22): default-src does not back-fill it, so every page this
// server serves — the login form included — was embeddable cross-origin
// until it was added.
func TestSecurityHeadersArePresentOnEveryResponse(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}

	// The policy is built from the shipped assets (static.go's
	// importMapHashes), so it is asserted by its parts rather than by a
	// literal that would have to be re-typed on every vendored change.
	policy := rec.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'self'", "frame-ancestors 'none'", "script-src 'self' "} {
		if !strings.Contains(policy, directive) {
			t.Errorf("Content-Security-Policy = %q, which does not carry %q", policy, directive)
		}
	}
	if strings.Contains(policy, "unsafe-inline") {
		t.Errorf("Content-Security-Policy = %q: 'unsafe-inline' re-admits every injected script this policy exists to refuse", policy)
	}
}

// TestThePolicyAdmitsEveryShellsImportMap is the check that would have
// caught the defect Task 15 found by opening a page in a browser.
//
// An import map is an **inline** script. Under `default-src 'self'` with
// no hash, a browser refuses to apply it — and refusing to apply an
// import map produces no error anybody looks at: the element is still in
// the DOM, every existing test still passes, and the only symptom is
// that the first page to import a bare specifier loads nothing at all.
// Task 2 shipped the map, Task 15 mounted the first page that needs it,
// and four tasks' worth of components were unreachable in a browser in
// between.
//
// It hashes the map out of each shell **on disk** rather than asking
// static.go for the hashes it computed, because a test that asked the
// code under test for its own answer would agree with any answer.
func TestThePolicyAdmitsEveryShellsImportMap(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	policy := rec.Header().Get("Content-Security-Policy")

	shells, err := filepath.Glob(filepath.Join("static", "*.html"))
	if err != nil {
		t.Fatalf("glob shells: %v", err)
	}
	if len(shells) == 0 {
		t.Fatal("found no shell: this test would pass on an empty tree")
	}
	found := 0
	inline := regexp.MustCompile(`(?s)<script type="importmap">(.*?)</script>`)
	for _, shell := range shells {
		body, err := os.ReadFile(shell)
		if err != nil {
			t.Fatalf("read %s: %v", shell, err)
		}
		for _, match := range inline.FindAllSubmatch(body, -1) {
			found++
			sum := sha256.Sum256(match[1])
			source := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
			if !strings.Contains(policy, source) {
				t.Errorf("%s ships an import map the policy does not admit (%s); a browser silently "+
					"ignores it and every bare specifier on that page fails to resolve",
					filepath.Base(shell), source)
			}
		}
	}
	if found == 0 {
		t.Fatal("no shell declares an import map: this test would pass whatever the policy said")
	}
	t.Logf("the policy admits the import map of %d shell(s)", found)
}

// TestNoShippedAssetCarriesInlineStyleThePolicyBlocks is the second half
// of the guard TestThePolicyAdmitsEveryShellsImportMap opens, and it
// exists because the import map was not the only thing `default-src
// 'self'` was silently switching off.
//
// **An inline style is refused exactly as silently as an inline script.**
// A `<style>` element — written into a shell, or built in script and
// appended to a shadow root, which is the same thing to a policy —
// carries no hash here, so a browser leaves it in the tree with its text
// intact and applies none of it: `style.sheet` is null and nothing
// anywhere throws. The canvas shipped that way from Task 7 until Task 15
// mounted a view and read the sheet back: the host laid out `static`
// instead of `absolute` and the surface drew at an SVG's default
// 300x150. A `style` attribute set from script is refused the same way
// and was confirmed the same way, in the same browser.
//
// So the rule this test keeps is that **this product's own assets ship
// no inline style at all**, and the way to give a component a stylesheet
// is `adoptedStyleSheets`, which no policy governs — which is also why
// every Lit component was unaffected while the one hand-rolled element
// was not. There is no allowance for "guarded by a feature check": a
// fallback this policy cannot execute is a mechanism nothing reads.
//
// vendor/ is exempt and named as such: it is third-party code this
// repository does not write, and Lit's own `<style>` path is reached
// only when `adoptedStyleSheets` is missing, which no browser this
// product targets is.
func TestNoShippedAssetCarriesInlineStyleThePolicyBlocks(t *testing.T) {
	forbidden := []struct {
		pattern *regexp.Regexp
		why     string
	}{
		{regexp.MustCompile(`(?i)<style[\s>]`), "an inline <style> element is refused by default-src 'self' and applies nothing"},
		{regexp.MustCompile(`(?i)\bstyle\s*=\s*["']`), "an inline style attribute is refused by default-src 'self'"},
		{regexp.MustCompile(`createElement\(\s*["']style["']\s*\)`), "a <style> built in script is inline style to a policy; adopt a constructible stylesheet instead"},
		{regexp.MustCompile(`setAttribute\(\s*["']style["']`), "a style attribute written from script is refused; use a class and a stylesheet"},
	}

	scanned := 0
	err := filepath.Walk("static", func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(name)
		if ext != ".html" && ext != ".js" {
			return nil
		}
		body, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		scanned++
		// A JS file is read with its comments stripped, by the same
		// helper static_layout_test.go's source-shape guard uses: the
		// reason a component does not build a style element is written
		// directly above the code that does not build one, and a scan
		// that read its own explanation as a violation would push that
		// explanation out of the file. A shell is read whole — its only
		// comment syntax is `<!-- -->`, no shell carries one, and the
		// line-comment stripper would eat the rest of any line holding a
		// `//` in a URL, which in markup is most of them.
		code := string(body)
		if ext == ".js" {
			code = withoutComments(code)
		}
		for _, rule := range forbidden {
			if loc := rule.pattern.FindStringIndex(code); loc != nil {
				t.Errorf("%s carries %q: %s", name, code[loc[0]:loc[1]], rule.why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static: %v", err)
	}
	if scanned < 10 {
		t.Fatalf("scanned only %d asset(s): this test would pass on a tree it never read", scanned)
	}
	t.Logf("%d shipped asset(s) carry no inline style", scanned)
}

// TestConfigEndpointIsPublicAndMinimal pins GET /api/config: reachable
// with no credential at all (an unauthenticated visitor is exactly who it
// exists for — see its own doc comment), and carrying nothing beyond the
// one field login.html actually reads. A future field added to
// config.Config landing on this response by reflex is exactly the drift
// this test exists to catch.
func TestConfigEndpointIsPublicAndMinimal(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, ok := body["registration_mode"]; !ok {
		t.Fatal(`body has no "registration_mode" field`)
	}
	if len(body) != 1 {
		t.Fatalf("body has %d fields, want exactly 1: %v", len(body), body)
	}
}
