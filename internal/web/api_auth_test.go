package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/web"
)

// jsonRequest builds a request carrying the application/json Content-Type
// every handler in api_auth.go now requires (Task 11's second review
// pass); the handful of tests that specifically exercise the
// Content-Type gate build their own request instead of using this helper.
func jsonRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// domainOpenConfig adjusts a test config to RegistrationDomainOpen with a
// real, non-empty allowlist. config.Load itself refuses domain_open with
// an empty ALLOWED_EMAIL_DOMAINS, and — independently of that — an empty
// list makes Config.EmailAllowed permit everything, which would silently
// disable every domain_open test's actual subject (Task 11's second
// review pass, item 7: the earlier version of these tests built a
// configuration Load would reject and never exercised the domain gate at
// all).
func domainOpenConfig(cfg *config.Config) {
	cfg.RegistrationMode = config.RegistrationDomainOpen
	cfg.AllowedEmailDomains = []string{"studio.com"}
}

func TestLoginSetsSessionCookieAndLogoutClearsIt(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"designer@studio.com","password":"password12345"}`)
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

	req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"designer@studio.com","password":"nope"}`)
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

	wrongPassword := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"designer@studio.com","password":"wrongwrongwrong"}`)
	wrongPasswordRec := httptest.NewRecorder()
	srv.ServeHTTP(wrongPasswordRec, wrongPassword)

	unknownEmail := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"nobody@studio.com","password":"wrongwrongwrong"}`)
	unknownEmailRec := httptest.NewRecorder()
	srv.ServeHTTP(unknownEmailRec, unknownEmail)

	if wrongPasswordRec.Code != http.StatusUnauthorized || unknownEmailRec.Code != http.StatusUnauthorized {
		t.Fatalf("status codes = %d, %d, want both 401", wrongPasswordRec.Code, unknownEmailRec.Code)
	}
	if wrongPasswordRec.Body.String() != unknownEmailRec.Body.String() {
		t.Fatalf("response bodies differ: %q vs %q; login must not reveal which case applied", wrongPasswordRec.Body.String(), unknownEmailRec.Body.String())
	}
}

// TestLoginWithDatabaseFailureIsInternalErrorNotUnauthorized pins Task
// 22's fix to handleLogin: it used to check `err != nil` with no regard
// to what Authenticate actually returned, so a genuine database failure
// (Authenticate's own fmt.Errorf("lookup user: %w", err), reached when
// GetUserByEmail fails for any reason other than pgx.ErrNoRows) was
// reported to the caller as "email or password is wrong" — the exact
// conflation the authentication middleware's own doc comment
// (auth.go, resolveSessionCaller/resolveBearerCaller) explains is wrong,
// and which api_password.go's handleChangePassword already avoided by
// matching identity.ErrInvalidCredentials explicitly. Left unfixed, a
// database blip told every caller their password was wrong, logged
// nothing an operator could act on, and spent both rate-limit budgets on
// a guess that was never actually evaluated — so the outage would go on
// locking people out for a further minute after the database itself had
// already recovered.
//
// Closing the pool after minting a real account forces Authenticate's
// GetUserByEmail to fail with a genuine connection error, not
// pgx.ErrNoRows, the same technique
// TestDatabaseErrorDuringSessionAuthenticationIsInternalError already
// uses for the authentication middleware (auth_test.go).
func TestLoginWithDatabaseFailureIsInternalErrorNotUnauthorized(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	ids := identity.New(pool, cfg)
	srv := web.NewServer(web.Options{Version: "test", Config: cfg, Identity: ids, Projects: projects.New(pool)})

	if _, err := ids.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	pool.Close()

	req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"designer@studio.com","password":"password12345"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a database failure, not 401 (indistinguishable from a wrong password); body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := decodeJSON(rec, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["error"] != "internal_error" {
		t.Fatalf("error = %q, want internal_error", body["error"])
	}

	// The other half of the bug, and the half that outlives the outage:
	// neither limiter may be charged for an attempt that was never
	// evaluated. loginLimiter allows 10 per normalized email per minute
	// and loginIPLimiter allows 40 per source IP per minute (server.go);
	// every request here shares both the one email and the one IP
	// httptest.NewRequest assigns. 41 more failed attempts through the
	// same dead pool cross both budgets, not just loginLimiter's tighter
	// one — a Task 22 re-review found that only twelve total attempts
	// (as this loop originally ran) proved loginLimiter alone: adding
	// back only `s.loginIPLimiter.Record(ip)` to the default branch left
	// this test green, since twelve attempts never approach 40. A
	// lockout here would persist for a further minute after the
	// database itself recovered, for a caller whose password was
	// correct all along. Every one of them must still be a 500.
	for i := range 41 {
		retry := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"designer@studio.com","password":"password12345"}`)
		retryRec := httptest.NewRecorder()
		srv.ServeHTTP(retryRec, retry)
		if retryRec.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d during the outage = 429: a database failure charged a rate limiter for an attempt that was never evaluated", i+2)
		}
		if retryRec.Code != http.StatusInternalServerError {
			t.Fatalf("attempt %d during the outage = %d, want 500; body = %s", i+2, retryRec.Code, retryRec.Body.String())
		}
	}
}

func TestLoginWithEmptyEmailIsBadRequest(t *testing.T) {
	// Rejected before either rate limiter is ever touched: an empty
	// normalized key would otherwise give every anonymous probe a single
	// shared "" bucket to spend against.
	srv, _, _ := newTestServer(t)
	req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"   ","password":"password12345"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestLoginIsRateLimitedPerNormalizedEmail(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	attempt := func(email string) int {
		req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"`+email+`","password":"nope"}`)
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
	req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"other@studio.com","password":"password12345"}`)
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
		req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"target@studio.com","password":"nope"}`)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d against target = %d, want 401", i, rec.Code)
		}
	}
	req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"target@studio.com","password":"password12345"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("target's own budget after 10 failures = %d, want 429", rec.Code)
	}

	// A colleague logging in correctly from the same source IP is
	// unaffected: only 10 of the IP's 40/min budget has been spent.
	req = jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"colleague@studio.com","password":"password12345"}`)
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
		req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"`+email+`","password":"nope"}`)
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
	req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"victim@studio.com","password":"password12345"}`)
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
		req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"designer@studio.com","password":"password12345"}`)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("login %d = %d, want 200 (a successful login must never spend rate-limit budget)", i, rec.Code)
		}
	}
}

func TestRegisterIsRejectedInInviteOnlyMode(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"new@studio.com","display_name":"New","password":"password12345"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 in invite_only mode", rec.Code)
	}
}

func TestRegisterSucceedsInDomainOpenMode(t *testing.T) {
	srv, _, _ := newTestServerWithConfig(t, domainOpenConfig)
	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"new@studio.com","display_name":"New","password":"password12345"}`)
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

func TestRegisterWithOffDomainEmailInDomainOpenModeIsForbidden(t *testing.T) {
	// The counterpart to TestRegisterSucceedsInDomainOpenMode: with a real
	// allowlist in place (see domainOpenConfig's own doc comment on why
	// the earlier version of these tests never actually exercised this),
	// an address outside it must be refused, not silently admitted.
	srv, _, _ := newTestServerWithConfig(t, domainOpenConfig)
	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"new@outside.com","display_name":"New","password":"password12345"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := decodeJSON(rec, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["error"] != "email_not_allowed" {
		t.Fatalf("error code = %q, want email_not_allowed", payload["error"])
	}
}

func TestRegisterWithTakenEmailInDomainOpenModeIsConflict(t *testing.T) {
	srv, _, _ := newTestServerWithConfig(t, domainOpenConfig)
	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"new@studio.com","display_name":"New","password":"password12345"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first register = %d, want 201", rec.Code)
	}

	req2 := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"new@studio.com","display_name":"Again","password":"password12345"}`)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second register = %d, want 409", rec2.Code)
	}
}

func TestRegisterWithInvalidEmailIsUnprocessable(t *testing.T) {
	srv, _, _ := newTestServerWithConfig(t, domainOpenConfig)
	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"a","display_name":"New","password":"password12345"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterWithEmptyDisplayNameIsUnprocessable(t *testing.T) {
	srv, _, _ := newTestServerWithConfig(t, domainOpenConfig)
	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"new@studio.com","display_name":"","password":"password12345"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := decodeJSON(rec, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["error"] != "display_name_invalid" {
		t.Fatalf("error code = %q, want display_name_invalid", payload["error"])
	}
}

func TestRegisterWithShortPasswordIsUnprocessable(t *testing.T) {
	srv, _, _ := newTestServerWithConfig(t, domainOpenConfig)
	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"new@studio.com","display_name":"New","password":"short"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := decodeJSON(rec, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["error"] != "password_invalid" {
		t.Fatalf("error code = %q, want password_invalid", payload["error"])
	}
}

func TestRegisterWithInviteTokenWinsOverInviteOnlyMode(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	token, _, err := ids.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"`+token+`"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterWithInviteTokenWinsOverDomainOpenMode(t *testing.T) {
	// The invite branch is checked before the mode branch regardless of
	// which mode is configured: domain_open must not change which branch
	// runs, only what happens when no token is given at all.
	srv, ids, _ := newTestServerWithConfig(t, domainOpenConfig)
	ctx := context.Background()
	token, _, err := ids.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"`+token+`"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
}

// TestRegisterWithInviteTokenAndLiveSessionGrantsExistingAccountMembership
// is this task's own Round 2 acceptance test: a designer who already has
// an account, and is signed in, clicking a project invite link grants
// membership to *that* account rather than attempting (and failing) to
// create a brand-new one. Before this existed, RedeemInvite always tried
// to create an account, so a pre-existing email hit ErrEmailTaken,
// mapped to the generic "invite is not valid" — a designer's account
// could never be granted a second game at all.
func TestRegisterWithInviteTokenAndLiveSessionGrantsExistingAccountMembership(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	existing, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "existing@studio.com", DisplayName: "Existing", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "existing@studio.com")

	// A fresh project existing is not already a member of, created by a
	// different owner through the real HTTP surface (see
	// createTestProjectForRegisterTest), so redeeming the invite below is
	// a real, visible second membership rather than a lateral no-op on a
	// game existing already owns.
	ownerCookie := loginAsFreshOwner(t, srv, ids, "owner-of-second-game@studio.com")
	secondProjectID := createTestProjectForRegisterTest(t, srv, ownerCookie, "clicked-invite-game")
	inviteToken, _, err := ids.CreateInvite(ctx, identity.InviteRequest{ProjectID: &secondProjectID, Role: "editor"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"invite_token":"`+inviteToken+`"}`)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (granted to the existing account, nothing created); body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		UserID    string `json:"user_id"`
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.UserID != existing.ID.String() {
		t.Fatalf("user_id = %q, want the existing account's id %q", body.UserID, existing.ID.String())
	}

	// No new session was issued — the same cookie must still work, and
	// no second cookie appears in the response.
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.SessionCookie {
			t.Fatal("granting membership to an existing, logged-in caller must not issue a new session cookie")
		}
	}
	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(cookie)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("the original session must still work, /api/me = %d", meRec.Code)
	}
}

// TestRegisterWithInviteTokenAndLiveSessionRejectsBoundInviteForAnotherEmail
// pins the binding check end to end: an invite naming one address must
// not grant membership to whichever account happens to be logged in when
// the link is clicked.
func TestRegisterWithInviteTokenAndLiveSessionRejectsBoundInviteForAnotherEmail(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "bystander@studio.com", DisplayName: "Bystander", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "bystander@studio.com")

	ownerCookie := loginAsFreshOwner(t, srv, ids, "owner-of-bound-game@studio.com")
	projectID := createTestProjectForRegisterTest(t, srv, ownerCookie, "bound-invite-game")
	token, _, err := ids.CreateInvite(ctx, identity.InviteRequest{Email: "someone-else@studio.com", ProjectID: &projectID, Role: "viewer"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"invite_token":"`+token+`"}`)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (invite_invalid); body = %s", rec.Code, rec.Body.String())
	}
}

// TestRegisterWithInviteTokenAndLiveSessionRejectsAccountOnlyInvite pins
// that an account-only invite has nothing to grant an account that
// already exists.
func TestRegisterWithInviteTokenAndLiveSessionRejectsAccountOnlyInvite(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "already-has-account@studio.com", DisplayName: "Already", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "already-has-account@studio.com")

	token, _, err := ids.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"invite_token":"`+token+`"}`)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (invite_invalid); body = %s", rec.Code, rec.Body.String())
	}
}

// loginAsFreshOwner creates a brand-new account and logs it in, purely
// so a test in this file can mint a project through the real HTTP
// surface without that project's ownership colliding with the account
// under test.
func loginAsFreshOwner(t *testing.T, srv *web.Server, ids *identity.Service, email string) *http.Cookie {
	t.Helper()
	if _, err := ids.CreateUser(context.Background(), identity.CreateUserRequest{Email: email, DisplayName: "Owner", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser %s: %v", email, err)
	}
	return loginAs(t, srv, email)
}

// createTestProjectForRegisterTest creates a game as the given session
// caller through the real HTTP surface and returns its id, so tests in
// this file that need a project-bound invite do not have to reach into
// internal/projects directly.
func createTestProjectForRegisterTest(t *testing.T, srv *web.Server, cookie *http.Cookie, slug string) uuid.UUID {
	t.Helper()
	req := jsonRequest(http.MethodPost, "/api/games", `{"slug":"`+slug+`","name":"`+slug+`"}`)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create game status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode create game response: %v", err)
	}
	id, err := uuid.Parse(body.ID)
	if err != nil {
		t.Fatalf("parse project id: %v", err)
	}
	return id
}

func TestRegisterWithOffDomainUnboundInviteIsForbidden(t *testing.T) {
	// An *unbound* invite (no email attached at creation) only ever said
	// "whoever holds this link gets in" — it names no domain, so
	// ALLOWED_EMAIL_DOMAINS is still the only statement anyone has made
	// about who may hold an account here, and it must still apply. Task
	// 11's third review pass: an earlier version of this fix skipped the
	// allowlist for every invite redemption regardless of shape, which
	// let any unbound invite's holder register with any address at all —
	// this test pins the corrected, narrower behavior instead.
	srv, ids, _ := newTestServerWithConfig(t, domainOpenConfig)
	ctx := context.Background()
	token, _, err := ids.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"contractor@outside.com","display_name":"Contractor","password":"password12345","invite_token":"`+token+`"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := decodeJSON(rec, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["error"] != "email_not_allowed" {
		t.Fatalf("error code = %q, want email_not_allowed (there is no attacker to protect here, so the person deserves to know why)", payload["error"])
	}
}

func TestRegisterWithOffDomainBoundInviteSucceeds(t *testing.T) {
	// A *bound* invite is different: the admin typed this exact address
	// when they created it, which is the actual contractor case this
	// fix (Task 11's third review pass) preserves. CreateInvite itself
	// still applies ALLOWED_EMAIL_DOMAINS when an invite is bound (Task
	// 5, Correction 8, unchanged), so to isolate "redemption does not
	// re-apply the allowlist to an already-bound address" from
	// "CreateInvite would have refused to mint this invite under its own
	// config", the invite here is minted through one identity.Service
	// with no domain restriction and redeemed through the HTTP server's
	// own service, wired with a strict allowlist that would reject the
	// address on the open self-service path — the two share one
	// database, only their config differs, the same way an operator
	// tightening ALLOWED_EMAIL_DOMAINS after minting an invite would
	// not retroactively break it.
	pool := testutil.NewPool(t)

	permissiveCfg := testConfig()
	creator := identity.New(pool, permissiveCfg)
	token, _, err := creator.CreateInvite(context.Background(), identity.InviteRequest{Email: "contractor@outside.com"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	// The HTTP server's own identity.Service shares the same pool as
	// creator above, but is wired with the strict allowlist domain_open
	// registration would otherwise apply — the invite must still bypass
	// it, since it is bound.
	strictCfg := testConfig()
	domainOpenConfig(&strictCfg)
	ids := identity.New(pool, strictCfg)
	projSvc := projects.New(pool)
	srv := web.NewServer(web.Options{
		Version:  "test",
		Config:   strictCfg,
		Identity: ids,
		Projects: projSvc,
	})

	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"contractor@outside.com","display_name":"Contractor","password":"password12345","invite_token":"`+token+`"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterWithInvalidInviteTokenIsForbidden(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"not-a-real-token"}`)
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

// TestRegisterWithExpiredInviteReportsExpired.
//
// **This test used to sleep, and the sleep was the bug.** It minted an
// invite with a one-millisecond TTL and slept ten milliseconds, so the
// assertion held only while the database server's clock was no more than
// nine milliseconds behind this process' — a margin nothing bounds, and
// one the two containers a real deployment runs in have no reason to
// respect. Under load it failed, and the failure looked like the
// registration path accepting an expired invite.
//
// The invite is now expired by *statement*, on the same clock every
// expiry check uses, with an hour of margin instead of nine
// milliseconds. Nothing here waits for anything, and the production
// half of the same defect — expires_at written from Go's clock and
// judged against Postgres' — is fixed in identity.sql rather than
// papered over here.
func TestRegisterWithExpiredInviteReportsExpired(t *testing.T) {
	srv, ids, _, pool := newTestServerWithPool(t)
	ctx := context.Background()
	token, invite, err := ids.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE invites SET expires_at = now() - interval '1 hour' WHERE id = $1`,
		invite.ID); err != nil {
		t.Fatalf("expire the invite: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"`+token+`"}`)
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

func TestDoubleRedemptionOverHTTPFailsTheSecondTime(t *testing.T) {
	// The single most likely real-world failure per Task 11's second
	// review pass: an invite link pasted into a shared channel gets
	// clicked twice. The second attempt — whether from the same person
	// double-submitting or a second person who found the same link —
	// must fail cleanly rather than create a second account or silently
	// re-authenticate.
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	token, _, err := ids.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	first := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"`+token+`"}`)
	firstRec := httptest.NewRecorder()
	srv.ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusCreated {
		t.Fatalf("first redemption = %d, want 201; body = %s", firstRec.Code, firstRec.Body.String())
	}

	second := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"invited@studio.com","display_name":"Invited Again","password":"password12345","invite_token":"`+token+`"}`)
	secondRec := httptest.NewRecorder()
	srv.ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusForbidden {
		t.Fatalf("second redemption = %d, want 403; body = %s", secondRec.Code, secondRec.Body.String())
	}
	var payload map[string]string
	if err := decodeJSON(secondRec, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["error"] != "invite_invalid" {
		t.Fatalf("error code = %q, want invite_invalid", payload["error"])
	}
}

func TestInviteRedemptionIsRateLimitedByIPNotByToken(t *testing.T) {
	srv, _, _ := newTestServer(t)

	attempt := func(remoteAddr string) int {
		req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"guess-`+remoteAddr+`"}`)
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

func TestRegisterInDomainOpenModeIsRateLimitedByIP(t *testing.T) {
	// Item 2 of Task 11's second review pass: the self-service
	// (domain_open) branch reaches identity.CreateUser — a full argon2
	// derivation, plus an account-existence oracle via 201/409/403/422 —
	// completely unthrottled before this fix, since registerIPLimiter
	// only ever guarded the invite branch. It now guards the endpoint as
	// a whole, so ten failing self-service attempts from one IP exhaust
	// the same budget invite redemption does.
	srv, _, _ := newTestServerWithConfig(t, domainOpenConfig)

	attempt := func(email string) int {
		req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"`+email+`","display_name":"New","password":"short"}`)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < 10; i++ {
		if code := attempt("probe@studio.com"); code != http.StatusUnprocessableEntity {
			t.Fatalf("attempt %d = %d, want 422 (invalid password)", i, code)
		}
	}
	if code := attempt("probe@studio.com"); code != http.StatusTooManyRequests {
		t.Fatalf("attempt after budget exhausted = %d, want 429", code)
	}
}

func TestOversizedRegisterBodyIsRejectedWith413(t *testing.T) {
	srv, _, _ := newTestServer(t)
	huge := strings.Repeat("a", 1<<20) // 1 MiB, far past the 16KiB bound.
	req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"new@studio.com","display_name":"New","password":"`+huge+`"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for an oversized body", rec.Code)
	}
}

func TestLoginRejectsMalformedJSON(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := jsonRequest(http.MethodPost, "/api/auth/login", `{not json`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestLoginRejectsNonJSONContentType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`email=x&password=y`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestLoginRejectsMissingContentType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"a@b.com","password":"password12345"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestClientIPIgnoresXForwardedForByDefault(t *testing.T) {
	// TrustedProxyCount defaults to zero (a directly exposed instance):
	// X-Forwarded-For must be ignored entirely, so every request sharing
	// the real RemoteAddr shares one rate-limit bucket regardless of
	// what a caller claims in the header. Task 11's second review pass,
	// item 1.
	srv, _, _ := newTestServer(t)

	attempt := func(forwardedFor string) int {
		req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"guess-`+forwardedFor+`"}`)
		req.Header.Set("X-Forwarded-For", forwardedFor)
		req.RemoteAddr = "192.0.2.9:4242"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < 10; i++ {
		// A different claimed X-Forwarded-For on every attempt; with the
		// header ignored, this must not grant a fresh budget each time.
		if code := attempt("203.0.113." + string(rune('0'+i))); code != http.StatusForbidden {
			t.Fatalf("attempt %d = %d, want 403 (invalid invite)", i, code)
		}
	}
	if code := attempt("203.0.113.99"); code != http.StatusTooManyRequests {
		t.Fatalf("attempt after budget exhausted = %d, want 429 (X-Forwarded-For must not grant a fresh budget)", code)
	}
}

func TestClientIPHonorsXForwardedForBehindTrustedProxy(t *testing.T) {
	// With TRUSTED_PROXY_COUNT=1, the rightmost X-Forwarded-For entry is
	// the real client — even though every request here shares the same
	// RemoteAddr (the proxy itself), distinct claimed clients must get
	// independent budgets. Task 11's second review pass, item 1.
	srv, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.TrustedProxyCount = 1
	})

	attempt := func(forwardedFor string) int {
		req := jsonRequest(http.MethodPost, "/api/auth/register", `{"email":"invited@studio.com","display_name":"Invited","password":"password12345","invite_token":"guess"}`)
		req.Header.Set("X-Forwarded-For", forwardedFor)
		req.RemoteAddr = "10.0.0.1:9999" // the trusted proxy's own address, identical on every request
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < 10; i++ {
		if code := attempt("203.0.113.5"); code != http.StatusForbidden {
			t.Fatalf("attempt %d against 203.0.113.5 = %d, want 403", i, code)
		}
	}
	if code := attempt("203.0.113.5"); code != http.StatusTooManyRequests {
		t.Fatalf("attempt after 203.0.113.5's budget exhausted = %d, want 429", code)
	}

	// A distinct claimed client, still through the same proxy RemoteAddr,
	// has its own untouched budget.
	if code := attempt("198.51.100.9"); code != http.StatusForbidden {
		t.Fatalf("a distinct claimed client = %d, want 403 (its own budget must be untouched)", code)
	}
}

func TestSessionCookieNotSecureOverForwardedProtoWithoutTrustedProxy(t *testing.T) {
	// The counterpart to the clientIP tests above, for the other header
	// this instance only trusts once TrustedProxyCount says a proxy is
	// really there: with the default of zero, a claimed
	// X-Forwarded-Proto: https from a plain HTTP connection must not
	// mark the session cookie Secure — the cookie would otherwise still
	// be sent by the browser over a later plaintext connection. Task 11's
	// second review pass, item 6.
	srv, ids, _ := newTestServer(t)
	if _, err := ids.CreateUser(context.Background(), identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"designer@studio.com","password":"password12345"}`)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.SessionCookie && c.Secure {
			t.Fatal("session cookie is Secure from an untrusted X-Forwarded-Proto claim")
		}
	}
}

func TestSessionCookieSecureOverForwardedProtoBehindTrustedProxy(t *testing.T) {
	srv, ids, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.TrustedProxyCount = 1
	})
	if _, err := ids.CreateUser(context.Background(), identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"designer@studio.com","password":"password12345"}`)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.SessionCookie {
			found = true
			if !c.Secure {
				t.Fatal("session cookie is not Secure despite a trusted X-Forwarded-Proto: https")
			}
		}
	}
	if !found {
		t.Fatal("login did not set a session cookie")
	}
}

func TestLoginResponseNeverLeaksPasswordHash(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	if _, err := ids.CreateUser(context.Background(), identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	req := jsonRequest(http.MethodPost, "/api/auth/login", `{"email":"designer@studio.com","password":"password12345"}`)
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
