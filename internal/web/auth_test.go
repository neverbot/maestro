package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/web"
)

func decodeJSON(rec *httptest.ResponseRecorder, v any) error {
	return json.NewDecoder(rec.Body).Decode(v)
}

func testConfig() config.Config {
	return config.Config{
		SessionKey: "0123456789abcdef0123456789abcdef",
		SessionTTL: 24 * time.Hour,
		InviteTTL:  24 * time.Hour,
		Argon2:     config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
	}
}

// newTestServer wires a server against an ephemeral database.
func newTestServer(t *testing.T) (*web.Server, *identity.Service, *projects.Service) {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := testConfig()
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	srv := web.NewServer(web.Options{
		Version:  "test",
		Config:   cfg,
		Identity: ids,
		Projects: projSvc,
	})
	return srv, ids, projSvc
}

func TestBearerTokenIdentifiesCaller(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	token, _, _ := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		UserID    string `json:"user_id"`
		IsAdmin   bool   `json:"is_admin"`
		ProjectID string `json:"project_id"`
	}
	if err := decodeJSON(rec, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.UserID != user.ID.String() {
		t.Fatalf("user_id = %q, want %q", body.UserID, user.ID.String())
	}
	if body.ProjectID != project.ID.String() {
		t.Fatalf("project_id = %q, want %q", body.ProjectID, project.ID.String())
	}
}

func TestMissingCredentialsAreUnauthorized(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestInvalidBearerTokenIsUnauthorized(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer mst_not-a-real-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRevokedTokenIsUnauthorized(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	token, row, _ := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: project.ID, TokenID: row.ID}); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestExpelledMemberTokenIsUnauthorized pins the property Task 9 closed and
// this middleware must not reopen: a token stays bound to the project it
// was minted for only as long as its creator is still a member of that
// project. RemoveMember revokes the departing member's tokens for that
// project as part of the same removal; this exercises that through the
// full authentication path, not just the identity package directly.
func TestExpelledMemberTokenIsUnauthorized(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	agent, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "agent@studio.com", DisplayName: "Agent", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err := projSvc.SetRole(ctx, agent.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: agent.ID, Label: "agent token"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	if err := projSvc.RemoveMember(ctx, agent.ID, project.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an expelled member's token; body = %s", rec.Code, rec.Body.String())
	}
}

func TestSessionCookieIdentifiesCaller(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	token, _, err := ids.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		UserID    string `json:"user_id"`
		ProjectID string `json:"project_id"`
	}
	if err := decodeJSON(rec, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.UserID != user.ID.String() {
		t.Fatalf("user_id = %q, want %q", body.UserID, user.ID.String())
	}
	if body.ProjectID != "" {
		t.Fatalf("project_id = %q, want empty: a session caller carries no project", body.ProjectID)
	}
}

func TestExpiredSessionCookieIsUnauthorized(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	token, _, err := ids.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	if err := ids.RevokeSession(ctx, token); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestInvalidBearerDoesNotFallBackToCookie pins a deliberate choice: an
// Authorization header, even a broken one, takes the bearer path and stays
// there. It never falls through to a session cookie the same request also
// happens to carry, so an attacker who can only inject a header (not steal
// the cookie itself) can never use a malformed bearer value to make a
// server that would otherwise ignore the header instead honour the cookie
// under different, unexpected precedence rules.
func TestInvalidBearerDoesNotFallBackToCookie(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	token, _, err := ids.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer garbage")
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: an invalid bearer must not fall back to a valid cookie", rec.Code)
	}
}

func TestHealthzStaysPublic(t *testing.T) {
	srv, _, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz: status = %d, want 200 with no credentials", rec.Code)
	}
}

func TestVersionRejectsAnonymousRequests(t *testing.T) {
	srv, _, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/version: status = %d, want 401 with no credentials", rec.Code)
	}
}

// TestVersionReturnsBuildVersionToAnAuthenticatedCaller is /version's
// success path: any authenticated caller (no admin requirement) can read
// the build version once past the gate TestVersionRejectsAnonymousRequests
// pins. The unauthenticated case doesn't need a database, so it lives in
// server_test.go's TestVersionRequiresAuthentication instead; this one
// needs a real session, hence the DB-backed newTestServer here.
func TestVersionReturnsBuildVersionToAnAuthenticatedCaller(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	token, _, err := ids.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/version: status = %d, want 200 for an authenticated caller; body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(rec, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version != "test" {
		t.Fatalf("version = %q, want %q", body.Version, "test")
	}
}

// TestSessionRenewsPastHalfwayThroughItsLifetime pins the sliding-session
// policy resolveSessionCaller implements: a session used after crossing
// the halfway point of its SESSION_TTL gets pushed back out to a full
// SESSION_TTL from now, so a caller in continued use is never logged out
// mid-session. A short SessionTTL makes "past halfway" reachable with a
// short sleep instead of a mocked clock.
func TestSessionRenewsPastHalfwayThroughItsLifetime(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.SessionTTL = 200 * time.Millisecond
	ids := identity.New(pool, cfg)
	srv := web.NewServer(web.Options{Version: "test", Config: cfg, Identity: ids, Projects: projects.New(pool)})
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	token, originalExpiry, err := ids.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	time.Sleep(120 * time.Millisecond) // past the 100ms halfway point of a 200ms TTL

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	_, newExpiry, err := ids.UserForSession(ctx, token)
	if err != nil {
		t.Fatalf("UserForSession: %v", err)
	}
	if !newExpiry.After(originalExpiry) {
		t.Fatalf("expiry = %v, want later than the original %v: a session past halfway should renew", newExpiry, originalExpiry)
	}
	if extended := newExpiry.Sub(originalExpiry); extended < 80*time.Millisecond {
		t.Fatalf("expiry moved forward by only %v, want close to a full %v renewal", extended, cfg.SessionTTL)
	}
}

// TestSessionDoesNotRenewBeforeHalfway pins the other half of the same
// policy: a fresh session, nowhere near its expiry, is not written to on
// every request. Renewing unconditionally would put a write on the
// hottest session-authenticated path in the product for no behavioural
// difference most requests would ever need.
func TestSessionDoesNotRenewBeforeHalfway(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	token, originalExpiry, err := ids.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	_, expiryAfter, err := ids.UserForSession(ctx, token)
	if err != nil {
		t.Fatalf("UserForSession: %v", err)
	}
	if !expiryAfter.Equal(originalExpiry) {
		t.Fatalf("expiry changed from %v to %v for a fresh session well before halfway", originalExpiry, expiryAfter)
	}
}
