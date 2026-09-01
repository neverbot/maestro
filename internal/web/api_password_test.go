package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/web"
)

// TestChangePasswordRotatesHashKeepsCallerLoggedInAndRevokesOtherSessions
// is this task's own acceptance test: the endpoint rotates the password,
// re-issues a fresh session for the tab that made the request (so the
// caller is not thrown out of the very request that changed it), and
// revokes every other session of the account.
func TestChangePasswordRotatesHashKeepsCallerLoggedInAndRevokesOtherSessions(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cookieHere := loginAs(t, srv, "designer@studio.com")
	cookieElsewhere := loginAs(t, srv, "designer@studio.com")

	body := strings.NewReader(`{"current_password":"password12345","new_password":"newpassword12345"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", body)
	req.AddCookie(cookieHere)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}

	var newCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.SessionCookie {
			newCookie = c
		}
	}
	if newCookie == nil {
		t.Fatal("handleChangePassword did not set a new session cookie")
	}
	if newCookie.Value == cookieHere.Value {
		t.Fatal("the new session cookie must not reuse the old session's token")
	}

	// The request's own session is dead (ChangePassword revokes
	// everything), but the freshly issued cookie must work.
	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(newCookie)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("/api/me with the freshly issued cookie = %d, want 200: %s", meRec.Code, meRec.Body.String())
	}

	// The other tab's session must be dead.
	otherReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	otherReq.AddCookie(cookieElsewhere)
	otherRec := httptest.NewRecorder()
	srv.ServeHTTP(otherRec, otherReq)
	if otherRec.Code != http.StatusUnauthorized {
		t.Fatalf("/api/me with the other tab's cookie = %d, want 401", otherRec.Code)
	}

	// The new password authenticates a fresh login; the old one no longer does.
	oldReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"designer@studio.com","password":"password12345"}`))
	oldReq.Header.Set("Content-Type", "application/json")
	oldRec := httptest.NewRecorder()
	srv.ServeHTTP(oldRec, oldReq)
	if oldRec.Code != http.StatusUnauthorized {
		t.Fatalf("login with old password = %d, want 401", oldRec.Code)
	}
	newReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"designer@studio.com","password":"newpassword12345"}`))
	newReq.Header.Set("Content-Type", "application/json")
	newRec := httptest.NewRecorder()
	srv.ServeHTTP(newRec, newReq)
	if newRec.Code != http.StatusOK {
		t.Fatalf("login with new password = %d, want 200: %s", newRec.Code, newRec.Body.String())
	}
}

// TestChangePasswordRejectsWrongCurrentPassword is the endpoint-level
// pin of this task's own attacker model: a live session alone must never
// be enough to rotate the password.
func TestChangePasswordRejectsWrongCurrentPassword(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "stolen@studio.com", DisplayName: "Stolen", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "stolen@studio.com")

	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"totally-wrong","new_password":"newpassword12345"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}

	// The original session must still be live — a rejected attempt has
	// no side effect.
	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(cookie)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("the caller's own session must survive a rejected attempt, /api/me = %d", meRec.Code)
	}
}

func TestChangePasswordRejectsSamePassword(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "sameone@studio.com", DisplayName: "Same One", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "sameone@studio.com")

	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"password12345","new_password":"password12345"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

func TestChangePasswordRejectsWeakNewPassword(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "weak@studio.com", DisplayName: "Weak", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "weak@studio.com")

	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"password12345","new_password":"short"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// TestChangePasswordRejectsTokenCaller mirrors
// TestTokenCallerCannotManageInstanceInvites: an API token authenticates
// an agent scoped to a project's content, not a person with a password
// of their own.
func TestChangePasswordRejectsTokenCaller(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"password12345","new_password":"newpassword12345"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// TestChangePasswordIsRateLimitedPerAccount pins the limiter's own key
// choice: ten wrong guesses against one account exhaust that account's
// budget, independent of a second account's own budget staying untouched.
func TestChangePasswordIsRateLimitedPerAccount(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "victim@studio.com", DisplayName: "Victim", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser victim: %v", err)
	}
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "other@studio.com", DisplayName: "Other", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser other: %v", err)
	}
	victimCookie := loginAs(t, srv, "victim@studio.com")
	otherCookie := loginAs(t, srv, "other@studio.com")

	var last *httptest.ResponseRecorder
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"wrong","new_password":"newpassword12345"}`))
		req.AddCookie(victimCookie)
		req.Header.Set("Content-Type", "application/json")
		last = httptest.NewRecorder()
		srv.ServeHTTP(last, req)
	}
	if last.Code != http.StatusUnauthorized {
		t.Fatalf("last of 10 attempts = %d, want 401 (still under budget)", last.Code)
	}

	// The 11th exhausts the budget.
	req := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"wrong","new_password":"newpassword12345"}`))
	req.AddCookie(victimCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th attempt = %d, want 429", rec.Code)
	}

	// The other account's own budget is untouched.
	otherReq := httptest.NewRequest(http.MethodPatch, "/api/me/password", strings.NewReader(`{"current_password":"wrong","new_password":"newpassword12345"}`))
	otherReq.AddCookie(otherCookie)
	otherReq.Header.Set("Content-Type", "application/json")
	otherRec := httptest.NewRecorder()
	srv.ServeHTTP(otherRec, otherReq)
	if otherRec.Code != http.StatusUnauthorized {
		t.Fatalf("other account's own attempt = %d, want 401 (its own budget is untouched)", otherRec.Code)
	}
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
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

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

	if rec.Code != http.StatusNoContent {
		t.Fatalf("correct password after budget exhaustion = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

// TestChangePasswordFloodLimiterBoundsRepeatedAttemptsRegardlessOfCorrectness
// pins the other half of the Round 2 fix: verifying unconditionally
// reopened an unbounded-argon2-cost concern the original single-limiter
// design closed for free. changePasswordFloodLimiter (60/minute) still
// gates entry before any password is checked, so a flood of requests —
// even ones that would otherwise succeed — is eventually capped.
func TestChangePasswordFloodLimiterBoundsRepeatedAttemptsRegardlessOfCorrectness(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "flooded@studio.com", DisplayName: "Flooded", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "flooded@studio.com")

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
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("61st attempt = %d, want 429", last.Code)
	}
}
