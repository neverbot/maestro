package web_test

import (
	"context"
	"crypto/sha256"
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

// newTestServerWithConfig wires a server the same way newTestServer does,
// but lets the caller adjust the config before the server and identity
// service are built from it — e.g. to switch REGISTRATION_MODE to
// domain_open for a specific test without affecting every other test's
// default invite_only config.
func newTestServerWithConfig(t *testing.T, adjust func(*config.Config)) (*web.Server, *identity.Service, *projects.Service) {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := testConfig()
	adjust(&cfg)
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
	token, tok, _ := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})

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
		TokenID   string `json:"token_id"`
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
	if body.TokenID != tok.ID.String() {
		t.Fatalf("token_id = %q, want %q", body.TokenID, tok.ID.String())
	}
	if body.IsAdmin {
		t.Fatalf("is_admin = true, want false for a non-admin user's token")
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
	if _, err := projSvc.SetRole(ctx, agent.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: agent.ID, Label: "agent token"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	if _, err := projSvc.RemoveMember(ctx, agent.ID, project.ID); err != nil {
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
// the halfway point of its SESSION_TTL gets pushed back out to exactly a
// full SESSION_TTL from now, so a caller in continued use is never logged
// out mid-session.
//
// This backdates sessions.expires_at directly with the pool, the same
// technique TestExpiredSessionIsRejectedAndPruned and
// TestResolveAPITokenThrottlesLastUsedAtWrites (internal/identity) use,
// rather than sleeping inside a short TTL: a sleep leaves little margin
// for user creation, an HTTP round trip and two queries before an
// overshoot flips the session from "past halfway" to "expired", which
// fails hard (a 401, not a soft miss) — the worst kind of flake to
// debug. An injectable fake clock would be worse, not better: expiry is
// enforced in SQL by expires_at > now(), so a Go-side clock would
// desynchronise from the database and this would end up testing a
// fiction instead of the real comparison ExtendSession and
// UserForSession both make. Backdating the column keeps Postgres' own
// now() as the only clock in play.
//
// The assertion brackets now()+SessionTTL around the request instead of
// checking "extended by at least N": a renewal that landed at the wrong
// offset (half the TTL, say, or the pre-cap value before LEAST applies)
// would pass a loose lower bound but fails this bracket.
func TestSessionRenewsPastHalfwayThroughItsLifetime(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	ids := identity.New(pool, cfg)
	srv := web.NewServer(web.Options{Version: "test", Config: cfg, Identity: ids, Projects: projects.New(pool)})
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	token, _, err := ids.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	pastHalfway := time.Now().Add(cfg.SessionTTL/2 - time.Minute)
	tokenHash := sha256.Sum256([]byte(token))
	if _, err := pool.Exec(ctx, `UPDATE sessions SET expires_at = $1 WHERE token_hash = $2`, pastHalfway, tokenHash[:]); err != nil {
		t.Fatalf("backdate expires_at: %v", err)
	}

	before := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	after := time.Now()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	_, newExpiry, err := ids.UserForSession(ctx, token)
	if err != nil {
		t.Fatalf("UserForSession: %v", err)
	}
	// clockSkewSlack allows for the target being computed by Postgres'
	// own now(), not the Go process' — see sessions_test.go's identical
	// constant for why a tight, zero-slack bracket flaked here.
	const clockSkewSlack = 2 * time.Second
	wantMin, wantMax := before.Add(cfg.SessionTTL-clockSkewSlack), after.Add(cfg.SessionTTL+clockSkewSlack)
	if newExpiry.Before(wantMin) || newExpiry.After(wantMax) {
		t.Fatalf("expiry = %v, want between %v and %v (now + SessionTTL, bracketed around the request)", newExpiry, wantMin, wantMax)
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

// TestAdminTokenReturnsProjectAndIsAdmin pins the admin half of the
// invariant this task exists to protect: an admin's token is still
// bound to exactly the project it was minted for, not exempted into an
// unscoped caller. Before this test, no test in this package (or
// package web's own internal tests before caller_test.go) ever created
// an admin and asserted on IsAdmin — it was decoded and never checked.
func TestAdminTokenReturnsProjectAndIsAdmin(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "admin@studio.com"
	cfg.FirstAdminPassword = "password12345"
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	srv := web.NewServer(web.Options{Version: "test", Config: cfg, Identity: ids, Projects: projSvc})
	ctx := context.Background()

	if err := ids.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("BootstrapFirstAdmin: %v", err)
	}
	admin, err := ids.Authenticate(ctx, cfg.FirstAdminEmail, cfg.FirstAdminPassword)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !admin.IsAdmin {
		t.Fatal("bootstrapped user is not an admin")
	}

	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", admin.ID)
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: admin.ID, Label: "admin token"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		IsAdmin   bool   `json:"is_admin"`
		ProjectID string `json:"project_id"`
	}
	if err := decodeJSON(rec, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.IsAdmin {
		t.Fatal("is_admin = false, want true for an admin's token")
	}
	if body.ProjectID != project.ID.String() {
		t.Fatalf("project_id = %q, want %q: an admin's token stays bound to its own project, not exempted", body.ProjectID, project.ID.String())
	}
}

// TestBearerTakesPrecedenceOverCookieForADifferentUser is where the
// bearer-over-cookie precedence decision (TestInvalidBearerDoesNotFallBackToCookie
// above) actually matters: both credentials here are valid, for two
// different users. Getting the precedence wrong would resolve the wrong
// identity for a valid request — a privilege issue, not merely a 401.
func TestBearerTakesPrecedenceOverCookieForADifferentUser(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	tokenUser, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "token-user@studio.com", DisplayName: "Token User", Password: "password12345"})
	cookieUser, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "cookie-user@studio.com", DisplayName: "Cookie User", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", tokenUser.ID)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: tokenUser.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	sessionToken, _, err := ids.IssueSession(ctx, cookieUser.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: sessionToken})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		UserID string `json:"user_id"`
	}
	if err := decodeJSON(rec, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.UserID != tokenUser.ID.String() {
		t.Fatalf("user_id = %q, want the bearer token's user %q, not the cookie's user %q", body.UserID, tokenUser.ID.String(), cookieUser.ID.String())
	}
}

// TestDatabaseErrorDuringBearerAuthenticationIsInternalError and its
// session counterpart below pin the 500 path in the tree rather than
// leaving it proven only once by hand: closing the pool after minting a
// live credential forces ResolveAPIToken/UserForSession to fail with a
// genuine connection error, not ErrTokenInvalid/ErrNoSession, so
// authenticate must answer 500, not 401 — the exact distinction
// Correction 1 (this task's plan notes) exists to preserve. Nothing else
// in this file exercises this path in CI; without it, that split could be
// collapsed back without any test noticing.
func TestDatabaseErrorDuringBearerAuthenticationIsInternalError(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	srv := web.NewServer(web.Options{Version: "test", Config: cfg, Identity: ids, Projects: projSvc})
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	pool.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a database failure, not 401 (indistinguishable from a bad credential); body = %s", rec.Code, rec.Body.String())
	}
}

func TestDatabaseErrorDuringSessionAuthenticationIsInternalError(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	ids := identity.New(pool, cfg)
	srv := web.NewServer(web.Options{Version: "test", Config: cfg, Identity: ids, Projects: projects.New(pool)})
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	token, _, err := ids.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	pool.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a database failure, not 401; body = %s", rec.Code, rec.Body.String())
	}
}
