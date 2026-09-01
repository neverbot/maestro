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
	"github.com/neverbot/maestro/internal/web"
)

// loginAsAdmin bootstraps the configured first admin, logs in, and returns
// its session cookie — the one path in this test suite that reaches
// requireAdminCaller's true branch. Every other test in this file that
// needs "not an admin" uses loginAs (auth_test.go), which never sets
// FirstAdminEmail/Password and so never produces one.
func loginAsAdmin(t *testing.T, adjust func(*config.Config)) (*web.Server, *identity.Service, *projects.Service, *http.Cookie) {
	t.Helper()
	srv, ids, projSvc := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.FirstAdminEmail = "admin@studio.com"
		cfg.FirstAdminPassword = "password12345"
		if adjust != nil {
			adjust(cfg)
		}
	})
	if err := ids.BootstrapFirstAdmin(context.Background()); err != nil {
		t.Fatalf("BootstrapFirstAdmin: %v", err)
	}
	return srv, ids, projSvc, loginAs(t, srv, "admin@studio.com")
}

// TestAdminCanCreateAccountOnlyInvite is this task's own acceptance test:
// a fresh instance with nothing but its bootstrap admin and zero games can
// still mint a usable invite for its second human, with no game needing
// to exist first.
func TestAdminCanCreateAccountOnlyInvite(t *testing.T) {
	srv, _, _, cookie := loginAsAdmin(t, nil)

	body := strings.NewReader(`{"email":"newcomer@studio.com"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/invites", body)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID         string  `json:"id"`
		Email      *string `json:"email"`
		ProjectID  *string `json:"project_id"`
		Role       *string `json:"role"`
		Token      string  `json:"token"`
		RedeemPath string  `json:"redeem_path"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Token == "" {
		t.Fatal("token was empty on creation")
	}
	if created.RedeemPath != "/login#invite="+created.Token {
		t.Fatalf("redeem_path = %q, want /login#invite=%s", created.RedeemPath, created.Token)
	}
	if created.ProjectID != nil {
		t.Fatalf("project_id = %v, want nil for an account-only invite", *created.ProjectID)
	}
	if created.Role != nil {
		t.Fatalf("role = %v, want nil for an account-only invite", *created.Role)
	}

	// The link actually redeems, end to end, exactly the way an admin
	// pasting redeem_path after their instance's own address would use it.
	registerBody := strings.NewReader(`{"email":"newcomer@studio.com","display_name":"Newcomer","password":"password12345","invite_token":"` + created.Token + `"}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/api/auth/register", registerBody)
	registerReq.Header.Set("Content-Type", "application/json")
	registerRec := httptest.NewRecorder()
	srv.ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusCreated {
		t.Fatalf("register with invite: status = %d, want 201: %s", registerRec.Code, registerRec.Body.String())
	}

	// The redeemed user has no game membership at all — an account-only
	// invite only ever grants an account, matching the spec's "registering
	// grants no access to any game by itself".
	newcomerCookie := loginAs(t, srv, "newcomer@studio.com")
	gamesReq := httptest.NewRequest(http.MethodGet, "/api/games", nil)
	gamesReq.AddCookie(newcomerCookie)
	gamesRec := httptest.NewRecorder()
	srv.ServeHTTP(gamesRec, gamesReq)
	var games struct {
		Games []struct{} `json:"games"`
	}
	if err := json.NewDecoder(gamesRec.Body).Decode(&games); err != nil {
		t.Fatalf("decode games: %v", err)
	}
	if len(games.Games) != 0 {
		t.Fatalf("newcomer's games = %+v, want none from an account-only invite", games.Games)
	}
}

// TestNonAdminCannotCreateAccountOnlyInvite pins requireAdminCaller: an
// ordinary member, even an owner of their own game, has no standing over
// the instance-wide invite surface.
func TestNonAdminCannotCreateAccountOnlyInvite(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	req := httptest.NewRequest(http.MethodPost, "/api/invites", strings.NewReader(`{"email":"x@studio.com"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// TestTokenCallerCannotManageInstanceInvites mirrors
// TestTokenEndpointsRejectTokenCaller: a bearer token is not a browser
// session and has no business anywhere on this surface, admin-minted or
// not.
func TestTokenCallerCannotManageInstanceInvites(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/invites", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/api/invites", nil),
		httptest.NewRequest(http.MethodDelete, "/api/invites/"+uuid.NewString(), nil),
	} {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d, want 403: %s", req.Method, req.URL.Path, rec.Code, rec.Body.String())
		}
	}
}

// TestInstanceInviteListingExcludesProjectBound is the HTTP-level half of
// TestListOutstandingInvitesExcludesProjectBound (invites_test.go): an
// admin who has no membership in some other game must never see that
// game's pending invites through the instance-wide listing.
func TestInstanceInviteListingExcludesProjectBound(t *testing.T) {
	srv, ids, projSvc, cookie := loginAsAdmin(t, nil)
	ctx := context.Background()
	admin, err := ids.Authenticate(ctx, "admin@studio.com", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	other, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", other.ID)
	_, boundInvite, err := ids.CreateInvite(ctx, identity.InviteRequest{ProjectID: &project.ID, Role: "editor", CreatedBy: &other.ID})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	_ = admin

	req := httptest.NewRequest(http.MethodPost, "/api/invites", strings.NewReader(`{"email":"visible@studio.com"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, req)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create instance invite: status = %d, want 201: %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/invites", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: status = %d, want 200: %s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		Invites []struct {
			ID string `json:"id"`
		} `json:"invites"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&listed); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	sawOwn, sawForeign := false, false
	for _, inv := range listed.Invites {
		if inv.ID == created.ID {
			sawOwn = true
		}
		if inv.ID == boundInvite.ID.String() {
			sawForeign = true
		}
	}
	if !sawOwn {
		t.Fatalf("listing = %s, want it to contain the account-only invite %q", listRec.Body.String(), created.ID)
	}
	if sawForeign {
		t.Fatal("instance-wide listing leaked a project-bound invite for a game the admin is not a member of")
	}
}

// TestRevokeInstanceInviteIgnoresProjectBound is the HTTP-level half of
// TestRevokeInviteIgnoresProjectBound: DELETE /api/invites/{id} must be a
// no-op against a project-bound invite's id, never actually revoking it.
func TestRevokeInstanceInviteIgnoresProjectBound(t *testing.T) {
	srv, ids, projSvc, cookie := loginAsAdmin(t, nil)
	ctx := context.Background()
	other, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", other.ID)
	token, boundInvite, err := ids.CreateInvite(ctx, identity.InviteRequest{ProjectID: &project.ID, Role: "editor", CreatedBy: &other.ID})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/invites/"+boundInvite.ID.String(), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}

	// Still live.
	if _, err := ids.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "still.live@studio.com", DisplayName: "Still Live", Password: "password12345",
	}); err != nil {
		t.Fatalf("RedeemInvite after no-op instance revoke: %v", err)
	}
}

// TestOwnerCanCreateAndListProjectInvite covers the project-scoped
// surface end to end: an owner mints an invite bound to their own game
// and a role, and it appears in that game's own listing.
func TestOwnerCanCreateAndListProjectInvite(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	createReq := httptest.NewRequest(http.MethodPost, "/api/games/"+project.ID.String()+"/invites", strings.NewReader(`{"email":"designer@studio.com","role":"editor"}`))
	createReq.AddCookie(cookie)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, want 201: %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
		Role      string `json:"role"`
		Token     string `json:"token"`
	}
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ProjectID != project.ID.String() {
		t.Fatalf("project_id = %q, want %q", created.ProjectID, project.ID)
	}
	if created.Role != "editor" {
		t.Fatalf("role = %q, want editor", created.Role)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/games/"+project.ID.String()+"/invites", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: status = %d, want 200: %s", listRec.Code, listRec.Body.String())
	}
	if strings.Contains(listRec.Body.String(), created.Token) {
		t.Fatal("the listing leaked the clear token value")
	}
	var listed struct {
		Invites []struct {
			ID string `json:"id"`
		} `json:"invites"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&listed); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	found := false
	for _, inv := range listed.Invites {
		if inv.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("listing = %s, want it to contain id %q", listRec.Body.String(), created.ID)
	}

	// Redeeming it actually grants the named role.
	registerBody := strings.NewReader(`{"email":"designer@studio.com","display_name":"Designer","password":"password12345","invite_token":"` + created.Token + `"}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/api/auth/register", registerBody)
	registerReq.Header.Set("Content-Type", "application/json")
	registerRec := httptest.NewRecorder()
	srv.ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusCreated {
		t.Fatalf("register with invite: status = %d, want 201: %s", registerRec.Code, registerRec.Body.String())
	}
	designerCookie := loginAs(t, srv, "designer@studio.com")
	gamesReq := httptest.NewRequest(http.MethodGet, "/api/games", nil)
	gamesReq.AddCookie(designerCookie)
	gamesRec := httptest.NewRecorder()
	srv.ServeHTTP(gamesRec, gamesReq)
	var games struct {
		Games []struct {
			ID string `json:"id"`
		} `json:"games"`
	}
	if err := json.NewDecoder(gamesRec.Body).Decode(&games); err != nil {
		t.Fatalf("decode games: %v", err)
	}
	if len(games.Games) != 1 || games.Games[0].ID != project.ID.String() {
		t.Fatalf("designer's games = %+v, want just %s", games.Games, project.ID)
	}
}

// TestEditorCannotCreateOrListOrRevokeProjectInvite pins this task's own
// "should granting owner require an owner" decision at the ceiling, not
// the floor: an editor is refused even when the invite they attempt names
// no more than their own role (viewer/editor), because the same endpoint
// also has the power to grant owner and this package gates the whole
// endpoint, not a per-request role check.
func TestEditorCannotCreateOrListOrRevokeProjectInvite(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	editor, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "editor@studio.com", DisplayName: "Editor", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if _, err := projSvc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	editorID := editor.ID
	_, existing, err := ids.CreateInvite(ctx, identity.InviteRequest{ProjectID: &project.ID, Role: "viewer", CreatedBy: &editorID})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	cookie := loginAs(t, srv, "editor@studio.com")

	createReq := httptest.NewRequest(http.MethodPost, "/api/games/"+project.ID.String()+"/invites", strings.NewReader(`{"role":"viewer"}`))
	createReq.AddCookie(cookie)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusForbidden {
		t.Fatalf("create: status = %d, want 403: %s", createRec.Code, createRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/games/"+project.ID.String()+"/invites", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusForbidden {
		t.Fatalf("list: status = %d, want 403: %s", listRec.Code, listRec.Body.String())
	}

	revokeReq := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.ID.String()+"/invites/"+existing.ID.String(), nil)
	revokeReq.AddCookie(cookie)
	revokeRec := httptest.NewRecorder()
	srv.ServeHTTP(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusForbidden {
		t.Fatalf("revoke: status = %d, want 403: %s", revokeRec.Code, revokeRec.Body.String())
	}
}

// TestOwnerCanInviteAsOwner pins the positive side of the same decision:
// an owner may mint an invite that grants owner, since only an owner ever
// reaches this handler at all.
func TestOwnerCanInviteAsOwner(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	req := httptest.NewRequest(http.MethodPost, "/api/games/"+project.ID.String()+"/invites", strings.NewReader(`{"email":"cofounder@studio.com","role":"owner"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Role != "owner" {
		t.Fatalf("role = %q, want owner", created.Role)
	}
}

// TestRevokingAnUnknownOrForeignProjectInviteIsANoop mirrors
// TestRevokingAnUnknownOrForeignTokenIsANoop for invites: an id that does
// not exist, or belongs to a different game, is a silent 204, never a
// 404 that would let an owner of one game probe another's invite ids.
func TestRevokingAnUnknownOrForeignProjectInviteIsANoop(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	azeroth, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	leMans, _ := projSvc.Create(ctx, "le-mans", "Le Mans", owner.ID)
	ownerID := owner.ID
	foreignToken, foreignInvite, err := ids.CreateInvite(ctx, identity.InviteRequest{ProjectID: &leMans.ID, Role: "editor", CreatedBy: &ownerID})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	for name, inviteID := range map[string]string{"unknown": uuid.NewString(), "foreign": foreignInvite.ID.String()} {
		req := httptest.NewRequest(http.MethodDelete, "/api/games/"+azeroth.ID.String()+"/invites/"+inviteID, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s invite id: status = %d, want 204: %s", name, rec.Code, rec.Body.String())
		}
	}

	// The foreign invite must still be live.
	if _, err := ids.RedeemInvite(ctx, foreignToken, identity.CreateUserRequest{
		Email: "still.live@studio.com", DisplayName: "Still Live", Password: "password12345",
	}); err != nil {
		t.Fatalf("RedeemInvite after no-op cross-game revoke: %v", err)
	}
}

// TestNonMemberCannotSeeOrTouchProjectInvites mirrors
// TestNonMemberCannotDeleteGameAndLearnsNothing: a caller with no
// standing at all gets the same 403 as any other project-scoped route,
// not a 404 that would confirm the game id is real.
func TestNonMemberCannotSeeOrTouchProjectInvites(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	outsider, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "outsider@studio.com", DisplayName: "Outsider", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "outsider@studio.com")
	_ = outsider

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.ID.String()+"/invites", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// TestProjectInviteCreationRejectsInvalidRole exercises
// identity.ErrInviteRequestInvalid's HTTP mapping.
func TestProjectInviteCreationRejectsInvalidRole(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	cookie := loginAs(t, srv, "owner@studio.com")

	req := httptest.NewRequest(http.MethodPost, "/api/games/"+project.ID.String()+"/invites", strings.NewReader(`{"role":"superadmin"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}
