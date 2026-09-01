package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPublicPathsSkipAuthentication pins the requirement this task's own
// plan entry called out by name: a request under /static/, /login or
// /g/{slug} must never trigger a session-cookie lookup, since a browser
// sends its cookie on every same-origin request regardless of whether the
// handler ever reads CallerFrom, and none of these three handlers do.
//
// stubOptions (server_test.go) builds an Identity service over a nil
// pool, so if authenticate ever attempted resolveSessionCaller for one of
// these paths, the resulting query would panic on the nil pool and this
// test would fail loudly instead of quietly passing — a request that
// completes normally is the proof the lookup was never attempted.
func TestPublicPathsSkipAuthentication(t *testing.T) {
	srv := NewServer(stubOptions("test"))

	for _, path := range []string{"/static/styles.css", "/login", "/g/some-slug", "/api/config"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(&http.Cookie{Name: SessionCookie, Value: "whatever-this-is-never-looked-up"})
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
		})
	}
}

// TestIsPublicPath pins the exact boundary of the public-path allowlist,
// including the near-misses a reviewer manually probing this endpoint
// would try first: a prefix lookalike that shares a few characters but
// not the trailing slash the check requires ("/staticfoo", "/loginish"),
// a bare directory name missing that same slash ("/static", "/g"), and
// "/api/config" itself both matching (exact) and not matching a
// same-prefix cousin ("/api/configish") — isPublicPath does string
// prefix/equality checks only, never a regex or a path-segment split, so
// each of these is deterministic and needs no URL decoding to reason
// about.
func TestIsPublicPath(t *testing.T) {
	cases := map[string]bool{
		"/static/styles.css": true,
		"/static/":           true,
		"/static":            false, // no trailing slash: not a prefix match
		"/staticfoo":         false, // shares a prefix, but not "/static/"
		"/login":             true,
		"/loginish":          false, // isPublicPath("/login") is exact, not a prefix
		"/g/azeroth":         true,
		"/g/":                true,
		"/g":                 false, // no trailing slash: not a prefix match
		"/api/config":        true,
		"/api/configish":     false, // isPublicPath("/api/config") is exact, not a prefix
		"/":                  false,
		"/api/games":         false,
		// A traversal segment inside the request path (e.g.
		// "/static/../api/games") is intentionally still true here: this
		// check only decides whether authenticate skips its own cookie
		// lookup, never whether the mux actually dispatches to a
		// handler. net/http's ServeMux cleans an unclean path and
		// 301-redirects to the cleaned target rather than serving it in
		// the same request, so the redirected, re-authenticated request
		// is what actually reaches /api/games — this function does not
		// need to, and must not try to, out-guess that.
		"/static/../api/games": true,
	}
	for path, want := range cases {
		if got := isPublicPath(path); got != want {
			t.Errorf("isPublicPath(%q) = %v, want %v", path, got, want)
		}
	}
}
