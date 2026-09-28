package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

func TestUnknownStaticAssetIsNotFound(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/static/does-not-exist.js", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusNotFound, "status = %d, want 404", rec.Code)
}

// TestStaticDoesNotServeHTMLShellsAgain pins a correction a quality review
// found: http.FileServerFS(assets), with no filtering, would happily serve
// /static/index.html, /static/login.html and /static/game.html as a
// second URL for each page — one that bypasses handleRoot's redirect
// decision (GET /{$}) entirely, since a request straight at /static/
// never goes through it. staticFileServer (static.go) 404s any request
// under /static/ ending in .html for exactly this reason.
func TestStaticDoesNotServeHTMLShellsAgain(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)
	for _, name := range []string{"index.html", "login.html", "game.html", "document.html"} {
		req := httptest.NewRequest(http.MethodGet, "/static/"+name, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		assert.Should(t, rec.Code == http.StatusNotFound, "GET /static/%s: status = %d, want 404", name, rec.Code)
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
	t.Parallel()
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
		assert.Should(t, strings.Contains(policy, directive), "Content-Security-Policy = %q, which does not carry %q", policy, directive)
	}
	assert.Should(t, !strings.Contains(policy, "unsafe-inline"), "Content-Security-Policy = %q: 'unsafe-inline' re-admits every injected script this policy exists to refuse", policy)
}

// TestConfigEndpointIsPublicAndMinimal pins GET /api/config: reachable
// with no credential at all (an unauthenticated visitor is exactly who it
// exists for — see its own doc comment), and carrying nothing beyond the
// one field login.html actually reads. A future field added to
// config.Config landing on this response by reflex is exactly the drift
// this test exists to catch.
func TestConfigEndpointIsPublicAndMinimal(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusOK, "status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, ok := body["registration_mode"]; !ok {
		t.Fatal(`body has no "registration_mode" field`)
	}
	assert.Must(t, len(body) == 1, "body has %d fields, want exactly 1: %v", len(body), body)
}

// Every shell and asset a browser asks for on the way in: the status,
// the content type the browser decides what to do by, and one element
// each page would be useless without.
func TestEveryShellAndAssetIsServed(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)
	for _, tc := range []struct{ name, path, mediaType, holds string }{
		{"the login page", "/login", "text/html", "form"},
		{"a game page, for any slug", "/g/does-not-exist", "text/html", "game-name"},
		{"a document page, for any slug", "/g/does-not-exist/doc?path=lore%2Fduskwood", "text/html", "doc-title"},
		{"the stylesheet", "/static/styles.css", "text/css", ""},
		{"the app module", "/static/app.js", "text/javascript", ""},
		{"the document module", "/static/doc.js", "text/javascript", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			assert.Must(t, rec.Code == http.StatusOK, "status = %d, want 200", rec.Code)
			ct := rec.Header().Get("Content-Type")
			assert.Must(t, strings.HasPrefix(ct, tc.mediaType), "Content-Type = %q, want %s", ct, tc.mediaType)
			assert.Must(t, tc.holds == "" || strings.Contains(rec.Body.String(), tc.holds),
				"%s does not carry %q", tc.path, tc.holds)
		})
	}
}
