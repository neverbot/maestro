package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// TestEveryGameScopedRouteGoesThroughRequireProject is the actual
// enforcement behind ProjectScope's guarantee, not the type system: Go
// cannot stop a same-package handler from parsing r.PathValue("game")
// itself and skipping requireProject entirely (a quality review proved
// this by writing exactly such a handler and watching it compile and
// serve real data to a non-member). What this test can do instead is
// inspect the routing table this server actually built and fail if any
// pattern containing "{game}" was not registered through
// registerProjectRoute — turning "someone might forget" into a failing
// test instead of a comment nobody re-reads.
func TestEveryGameScopedRouteGoesThroughRequireProject(t *testing.T) {
	s := NewServer(stubOptions("test"))

	if len(s.projectScopedPatterns) == 0 {
		t.Fatal("no project-scoped patterns were recorded — this test would pass vacuously")
	}
	for _, pattern := range s.registeredPatterns {
		if strings.Contains(pattern, "{game}") && !s.projectScopedPatterns[pattern] {
			t.Errorf("pattern %q contains {game} but was not registered through registerProjectRoute", pattern)
		}
	}
}

// TestEveryContentRouteIsRegisteredAsContent is the other half of
// registerContentRoute's guarantee. That function makes the editor check
// impossible to forget *for the routes registered through it*; this test
// is what stops a game-content route being registered through
// registerProjectRoute instead, which would compile, serve, and let a
// viewer write.
//
// The rule is inverted from the one this test used to apply, and the
// inversion is the point. It used to hold a hand-written list of the
// segments that name game content and check only those, so a route
// registered through the wrong function under a segment nobody had
// added to the list — `views`, which api_metamodel.go's own header and
// app.js both already name as the next thing built on this surface —
// passed both convention tests and let a viewer write. A review proved
// exactly that. The `checked == 0` guard below catches a list gone
// wholly stale, never a single missing entry.
//
// So the allowlist is the other set, and that one is closed: under
// /api/games/{game}/ the product has four standing sub-resources of its
// own — members, tokens, invites, events — gated by owner/admin checks
// of their own rather than by requireEditor, plus the bare game itself,
// which carries no trailing segment and so never reaches the check.
// Everything else under that prefix is game content by default and must
// go through registerContentRoute. A new sub-resource that genuinely is
// not game content is added to notContent here, deliberately, in the
// same commit that registers it.
func TestEveryContentRouteIsRegisteredAsContent(t *testing.T) {
	s := NewServer(stubOptions("test"))

	content := map[string]bool{}
	for _, pattern := range s.contentPatterns {
		content[pattern] = true
	}
	if len(content) == 0 {
		t.Fatal("no content patterns were recorded — this test would pass vacuously")
	}

	notContent := map[string]bool{
		"members": true, "tokens": true, "invites": true, "events": true,
	}
	checked := 0
	for _, pattern := range s.registeredPatterns {
		_, path, ok := strings.Cut(pattern, " ")
		if !ok {
			continue
		}
		rest, ok := strings.CutPrefix(path, "/api/games/{game}/")
		if !ok {
			continue
		}
		segment, _, _ := strings.Cut(rest, "/")
		if notContent[segment] {
			continue
		}
		checked++
		if !content[pattern] {
			t.Errorf("pattern %q sits under /api/games/{game}/ and names none of this instance's standing sub-resources, "+
				"so it is game content and must be registered through registerContentRoute", pattern)
		}
	}
	if checked == 0 {
		t.Fatal("matched no game-content pattern — every route under /api/games/{game}/ was read as a standing sub-resource")
	}
}

// TestOnlyRouteTouchesTheMux closes the last way a route can reach this
// server without either convention test above ever seeing it.
//
// Both of those tests walk s.registeredPatterns, and only route()
// appends to it. A handler registered straight on s.mux — one line,
// compiles, serves — is invisible to both, so the guarantee that a
// game-content write cannot forget requireEditor held only for routes
// that went through the front door. This test is the front door's lock:
// in this package's own source, s.mux.Handle and s.mux.HandleFunc may
// appear exactly once, inside route().
//
// A source-grep test is a blunt instrument and this one is deliberately
// the narrowest form of it: it does not parse Go, it does not know what
// a route is, and it will fire if route() is ever renamed or the mux
// field is. That is the whole cost, and it is paid in a test that fails
// loudly with an explanation rather than in a surface that lets a viewer
// write. The alternative — an exported accessor, or a mux type that
// refuses direct registration — buys the same property at the price of
// indirection in the thing this file is trying to keep readable.
func TestOnlyRouteTouchesTheMux(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	found := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(source), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if !strings.Contains(code, "s.mux.Handle") {
				continue
			}
			found++
			if name != "server.go" || !strings.Contains(code, "s.mux.Handle(pattern, h)") {
				t.Errorf("%s:%d registers on the mux directly: %s\n"+
					"every route goes through route(), which is what records it for the two convention tests above; "+
					"a route registered here is invisible to both", name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if found != 1 {
		t.Errorf("found %d direct mux registrations, want exactly the one inside route() — "+
			"if route() was renamed or restructured, this test has to be taught the new shape", found)
	}
}

// TestStatusForCodeDefaultsToUnprocessable pins the choice
// statusForCode's own doc comment argues: an *MCPError is by
// construction a refusal this server chose to make about the caller's
// request, so an unmapped code must not be reported as a server fault.
// Every code the parsing layer produces today is mapped explicitly, so
// the default arm is unreachable through a request — which is exactly
// why it needs pinning here rather than through one.
func TestStatusForCodeDefaultsToUnprocessable(t *testing.T) {
	for code, want := range map[string]int{
		errCodeInvalidInput:    http.StatusBadRequest,
		errCodeScopeViolation:  http.StatusForbidden,
		errCodeNotFound:        http.StatusNotFound,
		errCodeUnauthorized:    http.StatusUnauthorized,
		"a_code_no_spec_names": http.StatusUnprocessableEntity,
	} {
		if got := statusForCode(code); got != want {
			t.Errorf("statusForCode(%q) = %d, want %d", code, got, want)
		}
	}
}
