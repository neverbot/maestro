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

	for _, path := range []string{"/static/styles.css", "/login", "/g/some-slug"} {
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

func TestIsPublicPath(t *testing.T) {
	cases := map[string]bool{
		"/static/styles.css": true,
		"/static/":           true,
		"/login":             true,
		"/g/azeroth":         true,
		"/":                  false,
		"/api/games":         false,
		"/loginish":          false,
	}
	for path, want := range cases {
		if got := isPublicPath(path); got != want {
			t.Errorf("isPublicPath(%q) = %v, want %v", path, got, want)
		}
	}
}
