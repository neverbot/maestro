package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/web"
)

func TestLoginSetsSessionCookieAndLogoutClearsIt(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	body := strings.NewReader(`{"email":"designer@studio.com","password":"password12345"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == web.SessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("login did not set a session cookie")
	}
	if !session.HttpOnly {
		t.Fatal("the session cookie must be HttpOnly")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Fatal("the session cookie must be SameSite=Lax")
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(session)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("/api/me with the session cookie = %d, want 200", meRec.Code)
	}

	outReq := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	outReq.AddCookie(session)
	outRec := httptest.NewRecorder()
	srv.ServeHTTP(outRec, outReq)
	if outRec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", outRec.Code)
	}

	afterReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	afterReq.AddCookie(session)
	afterRec := httptest.NewRecorder()
	srv.ServeHTTP(afterRec, afterReq)
	if afterRec.Code != http.StatusUnauthorized {
		t.Fatalf("/api/me after logout = %d, want 401", afterRec.Code)
	}
}

func TestLogoutWithoutCookieIsNoContent(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout without a cookie = %d, want 204", rec.Code)
	}
}

func TestLoginWithWrongPasswordIsUnauthorized(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	if _, err := ids.CreateUser(context.Background(), identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	body := strings.NewReader(`{"email":"designer@studio.com","password":"nope"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestLoginWithUnknownEmailIsUnauthorizedWithSameBody(t *testing.T) {
	// A designer typing the wrong password and a designer typing an email
	// that has no account must be indistinguishable: otherwise the
	// endpoint becomes an account-enumeration oracle.
	srv, ids, _ := newTestServer(t)
	if _, err := ids.CreateUser(context.Background(), identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	wrongPassword := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"designer@studio.com","password":"wrongwrongwrong"}`))
	wrongPasswordRec := httptest.NewRecorder()
	srv.ServeHTTP(wrongPasswordRec, wrongPassword)

	unknownEmail := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"nobody@studio.com","password":"wrongwrongwrong"}`))
	unknownEmailRec := httptest.NewRecorder()
	srv.ServeHTTP(unknownEmailRec, unknownEmail)

	if wrongPasswordRec.Code != http.StatusUnauthorized || unknownEmailRec.Code != http.StatusUnauthorized {
		t.Fatalf("status codes = %d, %d, want both 401", wrongPasswordRec.Code, unknownEmailRec.Code)
	}
	if wrongPasswordRec.Body.String() != unknownEmailRec.Body.String() {
		t.Fatalf("response bodies differ: %q vs %q; login must not reveal which case applied", wrongPasswordRec.Body.String(), unknownEmailRec.Body.String())
	}
}

func TestLoginIsRateLimitedPerNormalizedEmail(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	attempt := func(email string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"`+email+`","password":"nope"}`))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	// Exhaust the budget (max 10/min, from server.go) using varying
	// capitalization: the limiter key is normalized the same way
	// Authenticate normalizes its lookup, so this must still count
	// against one shared budget rather than resetting per variant.
	variants := []string{
		"designer@studio.com", "Designer@studio.com", " designer@studio.com ",
		"DESIGNER@STUDIO.COM", "designer@studio.com", "designer@studio.com",
		"designer@studio.com", "designer@studio.com", "designer@studio.com",
		"designer@studio.com",
	}
	for i, v := range variants {
		if code := attempt(v); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d (%q) = %d, want 401", i, v, code)
		}
	}
	if code := attempt("designer@studio.com"); code != http.StatusTooManyRequests {
		t.Fatalf("attempt after budget exhausted = %d, want 429", code)
	}

	// A different account's budget must be untouched: an attacker who
	// knows a legitimate user's email must not be able to lock them out
	// of logging in by spending their budget from elsewhere.
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "other@studio.com", DisplayName: "Other", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"other@studio.com","password":"password12345"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("a different account's login = %d, want 200 (its own budget must be untouched)", rec.Code)
	}
}

func TestLoginIPLimiterDoesNotBlockOtherAccountsUntilExhausted(t *testing.T) {
	// The friendly side of loginIPLimiter: a burst of failed attempts
	// against one account must not, by itself, block a *different*
	// account logging in correctly from the same source IP — the IP
	// budget (40/min) is deliberately looser than the per-email budget
	// (10/min) so a shared office NAT keeps working for everyone else
	// while one account is under attack.
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "target@studio.com", DisplayName: "Target", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "colleague@studio.com", DisplayName: "Colleague", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Exhaust target@studio.com's own 10/min budget (all from the same
	// default httptest source IP).
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"target@studio.com","password":"nope"}`))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d against target = %d, want 401", i, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"target@studio.com","password":"password12345"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("target's own budget after 10 failures = %d, want 429", rec.Code)
	}

	// A colleague logging in correctly from the same source IP is
	// unaffected: only 10 of the IP's 40/min budget has been spent.
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"colleague@studio.com","password":"password12345"}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("colleague's login from the same IP = %d, want 200", rec.Code)
	}
}

func TestLoginIPLimiterBlocksAcrossAccountsWhenExhausted(t *testing.T) {
	// The hostile side of the same property: an attacker who knows (or
	// guesses) several addresses at one studio and spreads failed
	// attempts across them, staying under each account's own 10/min cap,
	// must still be stopped once the shared IP's 40/min budget runs out
	// — and, critically, a *correct* password submitted after that must
	// still come back 429, never a 200, so budget exhaustion cannot be
	// used to distinguish a right password from a wrong one (the same
	// oracle Authenticate's sentinel hash exists to close).
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "victim@studio.com", DisplayName: "Victim", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	emails := []string{"a@studio.com", "b@studio.com", "c@studio.com", "d@studio.com", "e@studio.com"}
	blocked := false
	for i := 0; i < 45; i++ {
		email := emails[i%len(emails)]
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"`+email+`","password":"nope"}`))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		switch rec.Code {
		case http.StatusUnauthorized:
			// Still within both budgets.
		case http.StatusTooManyRequests:
			blocked = true
		default:
			t.Fatalf("attempt %d (%s) = %d, want 401 or 429", i, email, rec.Code)
		}
	}
	if !blocked {
		t.Fatal("spreading failed attempts across five accounts from one IP never hit the shared IP budget")
	}

	// A correct password for an entirely different, untouched account,
	// from the same now-exhausted IP, must still be refused — not
	// silently let through because the guard only ever intended to
	// throttle wrong passwords.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"victim@studio.com","password":"password12345"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("correct password from an IP-exhausted source = %d, want 429 (must not leak a right password via a 200)", rec.Code)
	}
}

func TestSuccessfulLoginDoesNotSpendRateLimitBudget(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"designer@studio.com","password":"password12345"}`))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("login %d = %d, want 200 (a successful login must never spend rate-limit budget)", i, rec.Code)
		}
	}
}

func TestRegisterIsRejectedInInviteOnlyMode(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := strings.NewReader(`{"email":"new@studio.com","display_name":"New","password":"password12345"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 in invite_only mode", rec.Code)
	}
}

func TestRegisterSucceedsInDomainOpenMode(t *testing.T) {
	srv := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.RegistrationMode = config.RegistrationDomainOpen
	})
	body := strings.NewReader(`{"email":"new@studio.com","display_name":"New","password":"password12345"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == web.SessionCookie {
			found = true
		}
	}
	if !found {
		t.Fatal("register did not set a session cookie")
	}
}

func TestRegisterWithTakenEmailInDomainOpenModeIsConflict(t *testing.T) {
	srv := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.RegistrationMode = config.RegistrationDomainOpen
	})
	body := strings.NewReader(`{"email":"new@studio.com","display_name":"New","password":"password12345"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first register = %d, want 201", rec.Code)
	}

	body2 := strings.NewReader(`{"email":"new@studio.com","display_name":"Again","password":"password12345"}`)
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/register", body2)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second register = %d, want 409", rec2.Code)
	}
}

func TestRegisterWithInvalidEmailIsUnprocessable(t *testing.T) {
	srv := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.RegistrationMode = config.RegistrationDomainOpen
	})
	body := strings.NewReader(`{"email":"a","display_name":"New","password":"password12345"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterWithInviteTokenWinsOverInviteOnlyMode(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	token, _, err := ids.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	body := strings.NewReader(`{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"` + token + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterWithInvalidInviteTokenIsForbidden(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := strings.NewReader(`{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"not-a-real-token"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := decodeJSON(rec, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["error"] != "invite_invalid" {
		t.Fatalf("error code = %q, want invite_invalid", payload["error"])
	}
}

func TestRegisterWithExpiredInviteReportsExpired(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	token, _, err := ids.CreateInvite(ctx, identity.InviteRequest{ExpiresIn: time.Millisecond})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	body := strings.NewReader(`{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"` + token + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := decodeJSON(rec, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["error"] != "invite_expired" {
		t.Fatalf("error code = %q, want invite_expired", payload["error"])
	}
}

func TestInviteRedemptionIsRateLimitedByIPNotByToken(t *testing.T) {
	srv, _, _ := newTestServer(t)

	attempt := func(remoteAddr string) int {
		body := strings.NewReader(`{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"guess-` + remoteAddr + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/register", body)
		req.RemoteAddr = remoteAddr + ":12345"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	// Ten distinct, never-repeating token guesses from the same source IP
	// must still share one budget: keying on the token itself would give
	// every one of these its own fresh, unlimited allowance.
	for i := 0; i < 10; i++ {
		if code := attempt("203.0.113.1"); code != http.StatusForbidden {
			t.Fatalf("attempt %d = %d, want 403 (invalid invite)", i, code)
		}
	}
	if code := attempt("203.0.113.1"); code != http.StatusTooManyRequests {
		t.Fatalf("attempt after budget exhausted = %d, want 429", code)
	}

	// A different source IP is a different budget.
	if code := attempt("198.51.100.7"); code != http.StatusForbidden {
		t.Fatalf("a different source IP = %d, want 403 (its own budget must be untouched)", code)
	}
}

func TestOversizedRegisterBodyIsRejected(t *testing.T) {
	srv, _, _ := newTestServer(t)
	huge := strings.Repeat("a", 1<<20) // 1 MiB, far past the 16KiB bound.
	body := strings.NewReader(`{"email":"new@studio.com","display_name":"New","password":"` + huge + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an oversized body", rec.Code)
	}
}

func TestLoginRejectsMalformedJSON(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{not json`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestLoginResponseNeverLeaksPasswordHash(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	if _, err := ids.CreateUser(context.Background(), identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"designer@studio.com","password":"password12345"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "argon2") {
		t.Fatalf("login response leaks the password hash: %s", rec.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := decodeJSON(rec, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := payload["password_hash"]; ok {
		t.Fatal("login response must not include a password_hash field")
	}
}
