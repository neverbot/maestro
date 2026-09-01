package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
)

// stubOptions builds Options with non-nil, unconnected Identity and
// Projects services — enough to satisfy NewServer's construction guard
// (see its doc comment) without a database, for tests in this file that
// never send a credential and so never actually query either service. A
// test that does needs a real, migrated pool instead: see
// auth_test.go's newTestServer.
func stubOptions(version string) Options {
	return Options{
		Version:  version,
		Identity: identity.New(nil, config.Config{}),
		Projects: projects.New(nil),
	}
}

func TestHealthz(t *testing.T) {
	srv := NewServer(stubOptions(""))
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
	srv := NewServer(stubOptions("test-build"))
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestNotFound(t *testing.T) {
	srv := NewServer(stubOptions(""))
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHealthzMethodNotAllowed(t *testing.T) {
	srv := NewServer(stubOptions(""))
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestNewServerPanicsWithoutIdentity pins the construction guard directly:
// a misconfigured server must refuse to start, not serve /healthz
// successfully right up until the first request that dereferences a nil
// *identity.Service.
func TestNewServerPanicsWithoutIdentity(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewServer did not panic with a nil Identity service")
		}
	}()
	NewServer(Options{Projects: projects.New(nil)})
}

func TestNewServerPanicsWithoutProjects(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewServer did not panic with a nil Projects service")
		}
	}()
	NewServer(Options{Identity: identity.New(nil, config.Config{})})
}
