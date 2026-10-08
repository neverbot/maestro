package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/assert"
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
	t.Parallel()
	srv := NewServer(stubOptions(""))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusOK, "status = %d, want 200", rec.Code)
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

// TestNewServerPanicsWithoutIdentity pins the construction guard directly:
// a misconfigured server must refuse to start, not serve /healthz
// successfully right up until the first request that dereferences a nil
// *identity.Service.
func TestNewServerPanicsWithoutIdentity(t *testing.T) {
	t.Parallel()
	defer func() {
		assert.Must(t, recover() != nil, "NewServer did not panic with a nil Identity service")
	}()
	NewServer(Options{Projects: projects.New(nil)})
}

func TestNewServerPanicsWithoutProjects(t *testing.T) {
	t.Parallel()
	defer func() {
		assert.Must(t, recover() != nil, "NewServer did not panic with a nil Projects service")
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
	t.Parallel()
	s := NewServer(stubOptions("test"))

	assert.Must(t, len(s.projectScopedPatterns) != 0, "no project-scoped patterns were recorded — this test would pass vacuously")
	for _, pattern := range s.registeredPatterns {
		assert.Should(t, !strings.Contains(pattern, "{game}") || s.projectScopedPatterns[pattern], "pattern %q contains {game} but was not registered through registerProjectRoute", pattern)
	}
}

// **The signed routes are the third kind, and they are named here rather
// than inferred.** Two routes on this server carry a game's data and go
// through neither requireProject nor any caller check: the skill bundle
// and one image, each admitted by an HMAC this process minted over the
// path. That is a real authorisation and it is not the one the test
// above enforces, so a route that quietly stopped checking its signature
// would look exactly like one that never had to. This list is what makes
// adding a third a decision somebody makes on purpose.
func TestEverySignedRouteChecksItsSignature(t *testing.T) {
	t.Parallel()
	s := NewServer(stubOptions("test"))

	signed := map[string]bool{
		"GET " + skillZipPath:                 true,
		"GET " + imagePath + "{project}/{id}": true,
	}
	for pattern := range signed {
		found := false
		for _, registered := range s.registeredPatterns {
			if registered == pattern {
				found = true
			}
		}
		assert.Should(t, found, "%q is listed as a signed route and is not registered: this guard is reading a route that moved", pattern)
	}
	// Every signed route refuses an unsigned request, which is the thing
	// the list exists to keep true.
	for pattern := range signed {
		path := strings.TrimPrefix(pattern, "GET ")
		path = strings.ReplaceAll(path, "{project}", uuid.New().String())
		path = strings.ReplaceAll(path, "{id}", uuid.New().String())
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Should(t, rec.Code == http.StatusUnauthorized,
			"%s answered %d to a request carrying no signature, want 401", path, rec.Code)
	}
}

// TestEveryContentRouteIsRegisteredAsContent is the other half of
// registerContentRoute's guarantee. That function makes the editor check
// impossible to forget *for the routes registered through it*; this test
// is what stops a game-content route being registered through
// registerProjectRoute instead, which would compile, serve, and let a
// viewer write.
func TestEveryContentRouteIsRegisteredAsContent(t *testing.T) {
	t.Parallel()
	s := NewServer(stubOptions("test"))

	content := map[string]bool{}
	for _, pattern := range s.contentPatterns {
		content[pattern] = true
	}
	assert.Must(t, len(content) != 0, "no content patterns were recorded — this test would pass vacuously")

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
		assert.Should(t, content[pattern], "pattern %q sits under /api/games/{game}/ and names none of this instance's standing sub-resources, "+
			"so it is game content and must be registered through registerContentRoute", pattern)
	}
	assert.Must(t, checked != 0, "matched no game-content pattern — every route under /api/games/{game}/ was read as a standing sub-resource")
}

// TestOnlyRouteTouchesTheMux closes the last way a route can reach this
// server without either convention test above ever seeing it.
func TestOnlyRouteTouchesTheMux(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	assert.Must(t, err == nil, "read package directory: %v", err)
	found := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		assert.Must(t, err == nil, "read %s: %v", name, err)
		for i, line := range strings.Split(string(source), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if !strings.Contains(code, "s.mux.Handle") {
				continue
			}
			found++
			assert.Should(t, name == "server.go" && strings.Contains(code, "s.mux.Handle(pattern, h)"), "%s:%d registers on the mux directly: %s\n"+
				"every route goes through route(), which is what records it for the two convention tests above; "+
				"a route registered here is invisible to both", name, i+1, strings.TrimSpace(line))
		}
	}
	assert.Should(t, found == 1, "found %d direct mux registrations, want exactly the one inside route() — "+
		"if route() was renamed or restructured, this test has to be taught the new shape", found)
}

// TestStatusForCodeDefaultsToUnprocessable pins the choice
// statusForCode's own doc comment argues: an *MCPError is by
// construction a refusal this server chose to make about the caller's
// request, so an unmapped code must not be reported as a server fault.
// Every code the parsing layer produces today is mapped explicitly, so
// the default arm is unreachable through a request — which is exactly
// why it needs pinning here rather than through one.
func TestStatusForCodeDefaultsToUnprocessable(t *testing.T) {
	t.Parallel()
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

// What the bare mux answers a request carrying nothing.
func TestTheMuxAnswersAnUncredentialedRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, method, path string
		status             int
	}{
		{"the version, which is not public", http.MethodGet, "/version", http.StatusUnauthorized},
		{"an address nobody serves", http.MethodGet, "/nope", http.StatusNotFound},
		{"a write to the health check", http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(stubOptions(""))
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			assert.Must(t, rec.Code == tc.status, "status = %d, want %d", rec.Code, tc.status)
		})
	}
}
