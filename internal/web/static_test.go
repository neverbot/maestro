package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	for _, name := range []string{"index.html", "login.html", "game.html"} {
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
// specific handler.
func TestSecurityHeadersArePresentOnEveryResponse(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
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
