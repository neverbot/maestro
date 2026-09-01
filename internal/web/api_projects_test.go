package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/identity"
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
	req := httptest.NewRequest(http.MethodPost, "/api/games/"+project.ID.String()+"/tokens", body)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestTokenIsReturnedOnceOnCreation(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	body := strings.NewReader(`{"label":"seed agent"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/games/"+project.ID.String()+"/tokens", body)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(created.Token, "mst_") {
		t.Fatalf("token = %q, want the mst_ prefix", created.Token)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/games/"+project.ID.String()+"/tokens", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if strings.Contains(listRec.Body.String(), created.Token) {
		t.Fatal("the listing leaked the clear token value")
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
	if err := projSvc.SetRole(ctx, viewer.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	cookie := loginAs(t, srv, "viewer@studio.com")
	body := strings.NewReader(`{"label":"sneaky"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/games/"+project.ID.String()+"/tokens", body)
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
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	viewer, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err := projSvc.SetRole(ctx, viewer.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	_, row, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	cookie := loginAs(t, srv, "viewer@studio.com")
	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.ID.String()+"/tokens/"+row.ID.String(), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
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

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.ID.String()+"/tokens", nil)
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
	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.ID.String()+"/members", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestListMembersNeverLeaksEmail(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.ID.String()+"/members", nil)
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
	if err := projSvc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if err := projSvc.SetRole(ctx, other.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	// An editor may not promote or demote anyone, including themselves.
	editorCookie := loginAs(t, srv, "editor@studio.com")
	body := strings.NewReader(`{"role":"owner"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/games/"+project.ID.String()+"/members/"+other.ID.String(), body)
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
	req = httptest.NewRequest(http.MethodPatch, "/api/games/"+project.ID.String()+"/members/"+editor.ID.String(), body)
	req.AddCookie(ownerCookie)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner changing a role: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestChangeRoleOnSoleOwnerReportsLastOwner(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	body := strings.NewReader(`{"role":"viewer"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/games/"+project.ID.String()+"/members/"+owner.ID.String(), body)
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
	if err := projSvc.SetRole(ctx, viewer.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	// A non-owner member may remove themselves.
	viewerCookie := loginAs(t, srv, "viewer@studio.com")
	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.ID.String()+"/members/"+viewer.ID.String(), nil)
	req.AddCookie(viewerCookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("viewer removing self: status = %d, want 204", rec.Code)
	}

	// The sole remaining owner may not remove themselves.
	ownerCookie := loginAs(t, srv, "owner@studio.com")
	req = httptest.NewRequest(http.MethodDelete, "/api/games/"+project.ID.String()+"/members/"+owner.ID.String(), nil)
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

func TestNonOwnerCannotRemoveAnotherMember(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	editor, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "editor@studio.com", DisplayName: "Editor", Password: "password12345"})
	viewer, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err := projSvc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if err := projSvc.SetRole(ctx, viewer.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	editorCookie := loginAs(t, srv, "editor@studio.com")
	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.ID.String()+"/members/"+viewer.ID.String(), nil)
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

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.ID.String()+"/members", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
