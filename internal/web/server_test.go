package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	srv := NewServer(Options{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

// TestVersionRequiresAuthentication pins /version's auth gate: unlike
// /healthz, it hands back the exact commit an instance is running, which is
// fingerprinting material for an attacker matching the SHA against known
// patches, not a health signal — see the doc comment on ServeHTTP. The
// success path (a real caller getting the version back) needs a
// DB-backed identity service and lives in auth_test.go's
// TestVersionReturnsBuildVersionToAnAuthenticatedCaller instead.
func TestVersionRequiresAuthentication(t *testing.T) {
	srv := NewServer(Options{Version: "test-build"})
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestNotFound(t *testing.T) {
	srv := NewServer(Options{})
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHealthzMethodNotAllowed(t *testing.T) {
	srv := NewServer(Options{})
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
