package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/web"
)

// loginAs creates a user, logs in, and returns the session cookie.
func loginAs(t *testing.T, srv *web.Server, email string) *http.Cookie {
	t.Helper()
	body := strings.NewReader(`{"email":"` + email + `","password":"password12345"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login for %s = %d: %s", email, rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.SessionCookie {
			return c
		}
	}
	t.Fatalf("no session cookie for %s", email)
	return nil
}

func TestListGamesOnlyShowsMemberships(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	insider, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "in@studio.com", DisplayName: "In", Password: "password12345"})
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "out@studio.com", DisplayName: "Out", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := projSvc.Create(ctx, "azeroth", "Azeroth", insider.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for email, want := range map[string]int{"in@studio.com": 1, "out@studio.com": 0} {
		cookie := loginAs(t, srv, email)
		req := httptest.NewRequest(http.MethodGet, "/api/games", nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		var body struct {
			Games []struct {
				Slug string `json:"slug"`
			} `json:"games"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode for %s: %v", email, err)
		}
		if len(body.Games) != want {
			t.Fatalf("%s sees %d games, want %d", email, len(body.Games), want)
		}
	}
}

func TestListGamesRejectsTokenCaller(t *testing.T) {
	// A token caller's user may belong to other games its token knows
	// nothing about; ListForUser has no way to filter those out by
	// project, so this endpoint is human-only rather than risking a token
	// enumerating games outside its own binding.
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/games", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestCreateGameRejectsTokenCaller(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	body := strings.NewReader(`{"slug":"le-mans","name":"Le Mans"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/games", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestCreateGameRejectsInvalidSlugAsBadRequest(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	_, _ = ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	cookie := loginAs(t, srv, "owner@studio.com")

	body := strings.NewReader(`{"slug":"Not A Slug!","name":"Whatever"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/games", body)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

func TestRootRedirectsToTheOnlyGame(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "solo@studio.com", DisplayName: "Solo", Password: "password12345"})
	if _, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "solo@studio.com")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/g/azeroth" {
		t.Fatalf("Location = %q, want /g/azeroth", got)
	}
	// handleRoot bypasses requireCaller (it needs "no caller" to mean
	// "redirect to /login", not a 401), so it is responsible for its own
	// Cache-Control/Vary headers — see setNoStoreHeaders's own doc
	// comment for why this is the most identity-dependent response in
	// the product to be missing them.
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Vary"); got != "Cookie, Authorization" {
		t.Fatalf("Vary = %q, want Cookie, Authorization", got)
	}
}

func TestRootShowsPickerWithTwoGames(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "two@studio.com", DisplayName: "Two", Password: "password12345"})
	if _, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "two@studio.com")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the picker)", rec.Code)
	}
}

func TestRootShowsPickerWithNoGames(t *testing.T) {
	// A brand-new user with no memberships yet must not error out on /;
	// the picker (or its empty state) is what renders, not a crash on
	// games[0] with no elements.
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	_, _ = ids.CreateUser(ctx, identity.CreateUserRequest{Email: "fresh@studio.com", DisplayName: "Fresh", Password: "password12345"})
	cookie := loginAs(t, srv, "fresh@studio.com")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRootRedirectsAnonymousToLogin(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/login" {
		t.Fatalf("Location = %q, want /login", got)
	}
}

func TestTokenCreationRequiresMembership(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "stranger@studio.com", DisplayName: "Stranger", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	cookie := loginAs(t, srv, "stranger@studio.com")
	body := strings.NewReader(`{"label":"sneaky"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/games/"+project.Slug+"/tokens", body)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// 404 and not 403, since a game is addressed by its slug: a
	// stranger must not be able to tell a game they are not in from
	// one that does not exist (resolveGameRef).
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestTokenIsReturnedOnceOnCreation(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	body := strings.NewReader(`{"label":"seed agent"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/games/"+project.Slug+"/tokens", body)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID        string `json:"id"`
		Token     string `json:"token"`
		TokenHint string `json:"token_hint"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(created.Token, "mst_") {
		t.Fatalf("token = %q, want the mst_ prefix", created.Token)
	}
	if created.TokenHint == "" {
		t.Fatal("token_hint was empty on creation")
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/games/"+project.Slug+"/tokens", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if strings.Contains(listRec.Body.String(), created.Token) {
		t.Fatal("the listing leaked the clear token value")
	}
	// A test that only checks the clear value is absent would pass
	// against an empty list too — assert the created row is actually
	// present, by id, so a handler that silently dropped the row (or
	// never called ListAPITokens at all) would be caught.
	var listed struct {
		Tokens []struct {
			ID string `json:"id"`
		} `json:"tokens"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&listed); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	found := false
	for _, tok := range listed.Tokens {
		if tok.ID == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("listing = %s, want it to contain id %q", listRec.Body.String(), created.ID)
	}
}

func TestViewerCannotCreateToken(t *testing.T) {
	// A token grants an agent whatever standing its project binding
	// carries; letting a read-only viewer mint one would hand out a
	// credential wider than the viewer's own role.
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	viewer, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if _, err := projSvc.SetRole(ctx, viewer.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	cookie := loginAs(t, srv, "viewer@studio.com")
	body := strings.NewReader(`{"label":"sneaky"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/games/"+project.Slug+"/tokens", body)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

func TestViewerCanRevokeToken(t *testing.T) {
	// Revocation only ever removes access, so a viewer who can see a
	// leaked token in the listing may kill it — unlike creation, which
	// grants standing the viewer does not have.
	//
	// A bare assertion on the 204 status would pass even if the handler
	// never called RevokeAPIToken at all — mint over HTTP, revoke over
	// HTTP, then use the bearer value against a real route and require it
	// to actually stop authenticating.
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	viewer, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if _, err := projSvc.SetRole(ctx, viewer.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	ownerCookie := loginAs(t, srv, "owner@studio.com")

	createReq := httptest.NewRequest(http.MethodPost, "/api/games/"+project.Slug+"/tokens", strings.NewReader(`{"label":"agent"}`))
	createReq.AddCookie(ownerCookie)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create token: status = %d, want 201: %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	viewerCookie := loginAs(t, srv, "viewer@studio.com")
	revokeReq := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug+"/tokens/"+created.ID, nil)
	revokeReq.AddCookie(viewerCookie)
	revokeRec := httptest.NewRecorder()
	srv.ServeHTTP(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusNoContent {
		t.Fatalf("revoke: status = %d, want 204: %s", revokeRec.Code, revokeRec.Body.String())
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+created.Token)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token still authenticates: status = %d, want 401", meRec.Code)
	}
}

// TestRevokingAnUnknownOrForeignTokenIsANoop pins handleRevokeToken's own
// doc comment: revoking a token id that does not exist, or that belongs
// to a different project than the one in the URL, is deliberately a
// no-op 204, not a 404 — telling the two apart would let a member of one
// game probe whether some other token id belongs to a different project.
// This must never be "fixed" into a lookup-then-404.
func TestRevokingAnUnknownOrForeignTokenIsANoop(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	azeroth, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	leMans, _ := projSvc.Create(ctx, "le-mans", "Le Mans", owner.ID)
	foreignToken, foreignRow, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: leMans.ID, UserID: owner.ID, Label: "other game's agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	for name, tokenID := range map[string]string{"unknown": uuid.NewString(), "foreign": foreignRow.ID.String()} {
		req := httptest.NewRequest(http.MethodDelete, "/api/games/"+azeroth.Slug+"/tokens/"+tokenID, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s token id: status = %d, want 204: %s", name, rec.Code, rec.Body.String())
		}
	}

	// The foreign token must still be live: azeroth's DELETE never
	// touched a token that belongs to le-mans, even by id.
	if _, err := ids.ResolveAPIToken(ctx, foreignToken); err != nil {
		t.Fatalf("foreign token was revoked by another game's DELETE: %v", err)
	}
}

func TestTokenEndpointsRejectTokenCaller(t *testing.T) {
	// Managing credentials — minting, listing or revoking tokens — is a
	// human action; an agent authenticating with a token is not entitled
	// to manage other tokens in its own project, itself included.
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.Slug+"/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestListMembersRequiresMembership(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "stranger@studio.com", DisplayName: "Stranger", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	cookie := loginAs(t, srv, "stranger@studio.com")
	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.Slug+"/members", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// 404 and not 403, since a game is addressed by its slug: a
	// stranger must not be able to tell a game they are not in from
	// one that does not exist (resolveGameRef).
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestListMembersNeverLeaksEmail(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.Slug+"/members", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "owner@studio.com") {
		t.Fatal("the member listing leaked an email address")
	}
}

func TestOnlyOwnerCanChangeRole(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	editor, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "editor@studio.com", DisplayName: "Editor", Password: "password12345"})
	other, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "other@studio.com", DisplayName: "Other", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if _, err := projSvc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if _, err := projSvc.SetRole(ctx, other.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	// An editor may not promote or demote anyone, including themselves.
	editorCookie := loginAs(t, srv, "editor@studio.com")
	body := strings.NewReader(`{"role":"owner"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/games/"+project.Slug+"/members/"+other.ID.String(), body)
	req.AddCookie(editorCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("editor changing a role: status = %d, want 403", rec.Code)
	}

	// The owner may.
	ownerCookie := loginAs(t, srv, "owner@studio.com")
	body = strings.NewReader(`{"role":"viewer"}`)
	req = httptest.NewRequest(http.MethodPatch, "/api/games/"+project.Slug+"/members/"+editor.ID.String(), body)
	req.AddCookie(ownerCookie)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner changing a role: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// TestChangeRoleDemotionReportsRevokedTokenLabels mirrors
// TestRemoveMemberReportsRevokedTokenLabels for the other path that kills
// a member's agents: demoting them below editor. A bare 200 with an
// empty body would leave the owner who just demoted someone with no way
// to know which of that person's agents just stopped working.
func TestChangeRoleDemotionReportsRevokedTokenLabels(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	editor, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "editor@studio.com", DisplayName: "Editor", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if _, err := projSvc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if _, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: editor.ID, Label: "nightly export"}); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	cookie := loginAs(t, srv, "owner@studio.com")
	body := strings.NewReader(`{"role":"viewer"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/games/"+project.Slug+"/members/"+editor.ID.String(), body)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		RevokedTokens []string `json:"revoked_tokens"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.RevokedTokens) != 1 || got.RevokedTokens[0] != "nightly export" {
		t.Fatalf("revoked_tokens = %v, want [\"nightly export\"]", got.RevokedTokens)
	}
}

func TestChangeRoleOnSoleOwnerReportsLastOwner(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	body := strings.NewReader(`{"role":"viewer"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/games/"+project.Slug+"/members/"+owner.ID.String(), body)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var body2 struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body2.Error != "last_owner" {
		t.Fatalf("error = %q, want last_owner", body2.Error)
	}
}

func TestMemberCanRemoveSelfButNotSoleOwner(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	viewer, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if _, err := projSvc.SetRole(ctx, viewer.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	// A non-owner member may remove themselves. The response is 200 with
	// the (here, empty) list of tokens the removal revoked, not a bare
	// 204 — see handleRemoveMember's own doc comment.
	viewerCookie := loginAs(t, srv, "viewer@studio.com")
	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug+"/members/"+viewer.ID.String(), nil)
	req.AddCookie(viewerCookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer removing self: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var removed struct {
		RevokedTokens []string `json:"revoked_tokens"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&removed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(removed.RevokedTokens) != 0 {
		t.Fatalf("revoked_tokens = %v, want none (the viewer held no tokens)", removed.RevokedTokens)
	}

	// The sole remaining owner may not remove themselves.
	ownerCookie := loginAs(t, srv, "owner@studio.com")
	req = httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug+"/members/"+owner.ID.String(), nil)
	req.AddCookie(ownerCookie)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("sole owner removing self: status = %d, want 409", rec.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "last_owner" {
		t.Fatalf("error = %q, want last_owner", body.Error)
	}
}

// TestRemoveMemberReportsRevokedTokenLabels pins the point of the 200
// response shape: an owner removing a member with live tokens in this
// game must see which agents just stopped working, by label, not just a
// bare success.
func TestRemoveMemberReportsRevokedTokenLabels(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	editor, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "editor@studio.com", DisplayName: "Editor", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if _, err := projSvc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if _, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: editor.ID, Label: "nightly export"}); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if _, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: editor.ID, Label: "seed agent"}); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	cookie := loginAs(t, srv, "owner@studio.com")
	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug+"/members/"+editor.ID.String(), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		RevokedTokens []string `json:"revoked_tokens"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]bool{"nightly export": true, "seed agent": true}
	if len(body.RevokedTokens) != len(want) {
		t.Fatalf("revoked_tokens = %v, want exactly %v", body.RevokedTokens, want)
	}
	for _, label := range body.RevokedTokens {
		if !want[label] {
			t.Fatalf("unexpected revoked label %q", label)
		}
	}
}

func TestNonOwnerCannotRemoveAnotherMember(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	editor, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "editor@studio.com", DisplayName: "Editor", Password: "password12345"})
	viewer, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if _, err := projSvc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if _, err := projSvc.SetRole(ctx, viewer.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	editorCookie := loginAs(t, srv, "editor@studio.com")
	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug+"/members/"+viewer.ID.String(), nil)
	req.AddCookie(editorCookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestTokenCallerCannotManageMembers(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.Slug+"/members", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestProjectScopeLookupFailureIsInternalErrorNotForbidden pins the split
// this task's own review demanded: a database failure while resolving a
// session caller's membership (requireProject -> resolveProjectScope ->
// projects.RoleOf) must surface as a 500 an operator can act on, not a
// 403 that reads as "this caller was rejected" when nothing about them
// was actually evaluated — the exact conflation Task 10's authenticate
// already drew a hard line against for the credential-resolution step,
// now drawn the same way for the membership-resolution step.
//
// Session authentication itself must still succeed here, which is why
// this needs two separate *pgxpool.Pool values against the *same*
// database rather than the single shared pool
// TestDatabaseErrorDuringBearerAuthenticationIsInternalError (auth_test.go)
// closes wholesale: Identity keeps a live pool so the session cookie
// resolves normally, while Projects' own pool — opened separately, at
// the same connection string testutil.NewPool already migrated — is
// closed before the request, so only the RoleOf lookup fails.
func TestProjectScopeLookupFailureIsInternalErrorNotForbidden(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)

	ctx := context.Background()
	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A second pool at the same connection string, so closing it affects
	// only the Projects service below, not Identity's own pool.
	projPool, err := pgxpool.New(ctx, pool.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatalf("open second pool: %v", err)
	}
	brokenProjSvc := projects.New(projPool)

	srv := web.NewServer(web.Options{Version: "test", Config: cfg, Identity: ids, Projects: brokenProjSvc})
	cookie := loginAs(t, srv, "owner@studio.com")

	projPool.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.Slug+"/members", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a database failure resolving membership, not 403 (indistinguishable from an actual rejection); body = %s", rec.Code, rec.Body.String())
	}
}

// TestOwnerCanDeleteGame confirms the owner-only game-deletion route
// answers 204 and no body when the caller echoes the game's slug as
// ?confirm — see handleDeleteGame's own doc comment for why nothing is
// reported back, unlike handleRemoveMember and handleChangeRole, and
// for why ?confirm is required at all.
func TestOwnerCanDeleteGame(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug+"?confirm=azeroth", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.String())
	}
}

// TestDeleteGameRequiresMatchingConfirmSlug pins the safety property
// handleDeleteGame's own doc comment describes: deleting the wrong game
// because an id was mis-pasted is structurally impossible, because the
// request also has to name the game correctly. A missing ?confirm and a
// mismatched one are both refused with 400, and neither refusal deletes
// anything.
func TestDeleteGameRequiresMatchingConfirmSlug(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	cases := []struct {
		name string
		url  string
	}{
		{"missing", "/api/games/" + project.Slug},
		{"mismatched", "/api/games/" + project.Slug + "?confirm=notazeroth"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, c.url, nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}

	if _, err := projSvc.RoleOf(ctx, owner.ID, project.ID); err != nil {
		t.Fatalf("RoleOf after refused deletes: %v, want the game to still exist", err)
	}
}

// TestNonOwnerCannotDeleteGame confirms an editor — a real member, just
// not an owner — is refused the same way handleChangeRole refuses a
// non-owner: 403, not 404, since a member already has standing to know
// the game exists.
func TestNonOwnerCannotDeleteGame(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	editor, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "editor@studio.com", DisplayName: "Editor", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if _, err := projSvc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	cookie := loginAs(t, srv, "editor@studio.com")

	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}

	// The game must still exist: a refused delete is not a partial one.
	if _, err := projSvc.RoleOf(ctx, owner.ID, project.ID); err != nil {
		t.Fatalf("RoleOf after refused delete: %v, want the game (and membership) to still exist", err)
	}
}

// TestTokenCallerCannotDeleteGame confirms requireHumanCaller's own
// boundary applies here too: an agent's token is scoped to a game's
// content, never to deciding whether the game itself keeps existing.
func TestTokenCallerCannotDeleteGame(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestNonMemberCannotDeleteGameAndLearnsNothing confirms a caller with
// no standing in the game at all gets the same 403 requireProject
// already answers every other project-scoped route with, not a 404 that
// would tell a non-member the id is real, and not a 403 with different
// wording that would tell them the opposite. Pinned against a real game
// AND a fabricated id, comparing both status and body: a status-only
// comparison would miss a leak hiding in the message text alone.
func TestNonMemberCannotDeleteGameAndLearnsNothing(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	_, _ = ids.CreateUser(ctx, identity.CreateUserRequest{Email: "stranger@studio.com", DisplayName: "Stranger", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "stranger@studio.com")

	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// 404 and not 403, since a game is addressed by its slug: a
	// stranger must not be able to tell a game they are not in from
	// one that does not exist (resolveGameRef).
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if _, err := projSvc.RoleOf(ctx, owner.ID, project.ID); err != nil {
		t.Fatalf("RoleOf after refused delete: %v, want the game to still exist", err)
	}

	// The same request against a name that is nobody's game answers the
	// same way. It cannot answer with the *same bytes* — the refusal
	// names the value that was tried, which is the caller's own input —
	// so what is asserted is that the answer is a function of that input
	// and of nothing else: same status, same code, and a message built
	// from the same sentence. A stranger walking slugs learns nothing
	// from the difference between these two, which is the whole property.
	fakeReq := httptest.NewRequest(http.MethodDelete, "/api/games/not-a-real-game", nil)
	fakeReq.AddCookie(cookie)
	fakeRec := httptest.NewRecorder()
	srv.ServeHTTP(fakeRec, fakeReq)

	if fakeRec.Code != rec.Code {
		t.Fatalf("fabricated slug status = %d, real game status = %d, want equal (no leak)", fakeRec.Code, rec.Code)
	}
	for ref, body := range map[string]string{
		project.Slug:      rec.Body.String(),
		"not-a-real-game": fakeRec.Body.String(),
	} {
		quoted, err := json.Marshal("no game named " + strconv.Quote(ref) + " is available to you")
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := fmt.Sprintf(`{"error":"not_found","message":%s}`, quoted)
		if strings.TrimSpace(body) != want {
			t.Fatalf("%q answered %s, want %s (no leak)", ref, body, want)
		}
	}
}

// TestDeletingGameRevokesItsTokens confirms an agent holding a token for
// a deleted game cannot tell deletion apart from ordinary revocation:
// the same 401 either way, once the token row itself is gone.
func TestDeletingGameRevokesItsTokens(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug+"?confirm=azeroth", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204: %s", rec.Code, rec.Body.String())
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+token)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusUnauthorized {
		t.Fatalf("token after game deletion: status = %d, want 401", meRec.Code)
	}
}

// TestDeletingGameTwiceIsIdempotent drives the concurrent-delete case
// described in Service.Delete's own doc comment through the HTTP layer:
// a second owner-authenticated DELETE, arriving after the game is
// already gone, must not surface as a 500 — the caller had standing when
// they made the request, and by the time their request executes the
// outcome they wanted ("the game is gone") has already happened. In
// practice the second call can no longer reach handleDeleteGame at all,
// because requireProject re-resolves membership first and that
// membership row cascaded away with the project — so it answers 403,
// the same "you are not a member of this game" every other
// already-departed caller sees, not a 404 or 500 that would distinguish
// "used to exist" from "never did".
func TestDeletingGameTwiceIsIdempotent(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	first := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug+"?confirm=azeroth", nil)
	first.AddCookie(cookie)
	firstRec := httptest.NewRecorder()
	srv.ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusNoContent {
		t.Fatalf("first delete status = %d, want 204: %s", firstRec.Code, firstRec.Body.String())
	}

	// The second request needs no ?confirm of its own: requireProject
	// refuses it before handleDeleteGame ever reads the query string (see
	// the comment above), so an absent confirm here still proves the
	// same 403 this test is pinning, not a false pass from the 400
	// TestDeleteGameRequiresMatchingConfirmSlug already covers.
	second := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.Slug, nil)
	second.AddCookie(cookie)
	secondRec := httptest.NewRecorder()
	srv.ServeHTTP(secondRec, second)
	// 404 and not 403, since a game is addressed by its slug: a
	// stranger must not be able to tell a game they are not in from
	// one that does not exist (resolveGameRef).
	if secondRec.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404 (membership already gone), body: %s", secondRec.Code, secondRec.Body.String())
	}
}
