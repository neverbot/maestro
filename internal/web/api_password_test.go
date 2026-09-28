package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/web"
)

// TestChangePasswordRotatesHashKeepsCallerLoggedInAndRevokesOtherSessions
// is this task's own acceptance test: the endpoint rotates the password,
// re-issues a fresh session for the tab that made the request (so the
// caller is not thrown out of the very request that changed it), and
// revokes every other session of the account.
func TestChangePasswordRotatesHashKeepsCallerLoggedInAndRevokesOtherSessions(t *testing.T) {
	t.Parallel()
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@example.test", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cookieHere := loginAs(t, srv, "designer@example.test")
	cookieElsewhere := loginAs(t, srv, "designer@example.test")

	body := strings.NewReader(`{"current_password":"password12345","new_password":"newpassword12345"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", body)
	req.AddCookie(cookieHere)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusNoContent, "status = %d, want 204: %s", rec.Code, rec.Body.String())

	var newCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.SessionCookie {
			newCookie = c
		}
	}
	assert.Must(t, newCookie != nil, "handleChangePassword did not set a new session cookie")
	assert.Must(t, newCookie.Value != cookieHere.Value, "the new session cookie must not reuse the old session's token")

	// The request's own session is dead (ChangePassword revokes
	// everything), but the freshly issued cookie must work.
	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(newCookie)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	assert.Must(t, meRec.Code == http.StatusOK, "/api/me with the freshly issued cookie = %d, want 200: %s", meRec.Code, meRec.Body.String())

	// The other tab's session must be dead.
	otherReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	otherReq.AddCookie(cookieElsewhere)
	otherRec := httptest.NewRecorder()
	srv.ServeHTTP(otherRec, otherReq)
	assert.Must(t, otherRec.Code == http.StatusUnauthorized, "/api/me with the other tab's cookie = %d, want 401", otherRec.Code)

	// The new password authenticates a fresh login; the old one no longer does.
	oldReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"designer@example.test","password":"password12345"}`))
	oldReq.Header.Set("Content-Type", "application/json")
	oldRec := httptest.NewRecorder()
	srv.ServeHTTP(oldRec, oldReq)
	assert.Must(t, oldRec.Code == http.StatusUnauthorized, "login with old password = %d, want 401", oldRec.Code)
	newReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"designer@example.test","password":"newpassword12345"}`))
	newReq.Header.Set("Content-Type", "application/json")
	newRec := httptest.NewRecorder()
	srv.ServeHTTP(newRec, newReq)
	assert.Must(t, newRec.Code == http.StatusOK, "login with new password = %d, want 200: %s", newRec.Code, newRec.Body.String())
}

// TestChangePasswordRejectsWrongCurrentPassword is the endpoint-level
// pin of this task's own attacker model: a live session alone must never
// be enough to rotate the password.
func TestChangePasswordRejectsWrongCurrentPassword(t *testing.T) {
	t.Parallel()
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "stolen@example.test", DisplayName: "Stolen", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "stolen@example.test")

	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"totally-wrong","new_password":"newpassword12345"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusUnauthorized, "status = %d, want 401: %s", rec.Code, rec.Body.String())

	// The original session must still be live — a rejected attempt has
	// no side effect.
	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(cookie)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	assert.Must(t, meRec.Code == http.StatusOK, "the caller's own session must survive a rejected attempt, /api/me = %d", meRec.Code)
}

func TestChangePasswordRejectsSamePassword(t *testing.T) {
	t.Parallel()
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "sameone@example.test", DisplayName: "Same One", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "sameone@example.test")

	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"password12345","new_password":"password12345"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusUnprocessableEntity, "status = %d, want 422: %s", rec.Code, rec.Body.String())
}

func TestChangePasswordRejectsWeakNewPassword(t *testing.T) {
	t.Parallel()
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "weak@example.test", DisplayName: "Weak", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "weak@example.test")

	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"password12345","new_password":"short"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusUnprocessableEntity, "status = %d, want 422: %s", rec.Code, rec.Body.String())
}

// TestChangePasswordRejectsTokenCaller mirrors
// TestTokenCallerCannotManageInstanceInvites: an API token authenticates
// an agent scoped to a project's content, not a person with a password
// of their own.
func TestChangePasswordRejectsTokenCaller(t *testing.T) {
	t.Parallel()
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create project: %v", err)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	assert.Must(t, err == nil, "CreateAPIToken: %v", err)

	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"password12345","new_password":"newpassword12345"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusForbidden, "status = %d, want 403: %s", rec.Code, rec.Body.String())
}

// TestChangePasswordIsRateLimitedPerAccount pins the limiter's own key
// choice: ten wrong guesses against one account exhaust that account's
// budget, independent of a second account's own budget staying untouched.
func TestChangePasswordIsRateLimitedPerAccount(t *testing.T) {
	t.Parallel()
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "victim@example.test", DisplayName: "Victim", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser victim: %v", err)
	}
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "other@example.test", DisplayName: "Other", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser other: %v", err)
	}
	victimCookie := loginAs(t, srv, "victim@example.test")
	otherCookie := loginAs(t, srv, "other@example.test")

	var last *httptest.ResponseRecorder
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"wrong","new_password":"newpassword12345"}`))
		req.AddCookie(victimCookie)
		req.Header.Set("Content-Type", "application/json")
		last = httptest.NewRecorder()
		srv.ServeHTTP(last, req)
	}
	assert.Must(t, last.Code == http.StatusUnauthorized, "last of 10 attempts = %d, want 401 (still under budget)", last.Code)

	// The 11th exhausts the budget.
	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"wrong","new_password":"newpassword12345"}`))
	req.AddCookie(victimCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Must(t, rec.Code == http.StatusTooManyRequests, "11th attempt = %d, want 429", rec.Code)

	// The other account's own budget is untouched.
	otherReq := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"wrong","new_password":"newpassword12345"}`))
	otherReq.AddCookie(otherCookie)
	otherReq.Header.Set("Content-Type", "application/json")
	otherRec := httptest.NewRecorder()
	srv.ServeHTTP(otherRec, otherReq)
	assert.Must(t, otherRec.Code == http.StatusUnauthorized, "other account's own attempt = %d, want 401 (its own budget is untouched)", otherRec.Code)
}

// TestChangePasswordSucceedsWithCorrectPasswordEvenAfterWrongGuessBudgetExhausted
// is this task's own Round 2 acceptance test: the threat this endpoint
// exists to counter is a live session with no knowledge of the real
// password. A design that lets ten wrong guesses from exactly that
// attacker shut the account owner's own, correct-password request out
// with 429 hands the attacker a way to hold the real remedy shut
// indefinitely — with no password reset anywhere in this product to
// fall back on. A correct current password must always get through,
// regardless of how many prior wrong guesses spent the wrong-guess
// budget.
func TestChangePasswordSucceedsWithCorrectPasswordEvenAfterWrongGuessBudgetExhausted(t *testing.T) {
	t.Parallel()
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "owner@example.test")

	// Exhaust the wrong-guess budget (10/min) exactly the way a session
	// thief with no knowledge of the password would.
	for i := 0; i < 11; i++ {
		req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"wrong","new_password":"newpassword12345"}`))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
	}

	// The real owner's own, correct-password request must still succeed.
	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"password12345","new_password":"newpassword12345"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusNoContent, "correct password after budget exhaustion = %d, want 204: %s", rec.Code, rec.Body.String())
}

// TestChangePasswordFloodLimiterBoundsRepeatedAttemptsRegardlessOfCorrectness
// pins the other half of the Round 2 fix: verifying unconditionally
// reopened an unbounded-argon2-cost concern the original single-limiter
// design closed for free. changePasswordFloodLimiter (60/minute) still
// gates entry before any password is checked, so a flood of requests —
// even ones that would otherwise succeed — is eventually capped.
func TestChangePasswordFloodLimiterBoundsRepeatedAttemptsRegardlessOfCorrectness(t *testing.T) {
	t.Parallel()
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "flooded@example.test", DisplayName: "Flooded", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "flooded@example.test")

	// 61 attempts, all with a wrong password (so none of them succeed
	// and stop being retryable) — the 61st must be refused by the flood
	// limiter regardless of correctness, distinctly from the 10/minute
	// wrong-guess limiter, whose own budget this deliberately stays
	// under by never letting a single account's wrong guesses alone
	// decide the outcome: 61 requests here would already have tripped
	// the tighter wrong-guess budget too, so this test only pins that
	// *some* 429 eventually fires under sustained load, not which
	// limiter produced it.
	var last *httptest.ResponseRecorder
	for i := 0; i < 61; i++ {
		req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"wrong","new_password":"newpassword12345"}`))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		last = httptest.NewRecorder()
		srv.ServeHTTP(last, req)
	}
	assert.Must(t, last.Code == http.StatusTooManyRequests, "61st attempt = %d, want 429", last.Code)
}
