package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/identity"
)

// TestAdminCanPromoteAnotherUserToAdmin is this task's own acceptance
// test: the bootstrap admin, and only the bootstrap admin, is no longer
// the sole account that can ever hold instance-admin standing.
func TestAdminCanPromoteAnotherUserToAdmin(t *testing.T) {
	t.Parallel()
	srv, ids, _, adminCookie := loginAsAdmin(t, nil)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "colleague@example.test", DisplayName: "Colleague", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"colleague@example.test","is_admin":true}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusOK, "status = %d, want 200: %s", rec.Code, rec.Body.String())

	// The promotion is real, not just a 200: the new admin can now reach
	// the admin-only invite surface.
	colleagueCookie := loginAs(t, srv, "colleague@example.test")
	inviteReq := httptest.NewRequest(http.MethodPost, "/api/invites", strings.NewReader(`{"email":"third@example.test"}`))
	inviteReq.AddCookie(colleagueCookie)
	inviteReq.Header.Set("Content-Type", "application/json")
	inviteRec := httptest.NewRecorder()
	srv.ServeHTTP(inviteRec, inviteReq)
	assert.Must(t, inviteRec.Code == http.StatusCreated, "newly promoted admin POST /api/invites = %d, want 201: %s", inviteRec.Code, inviteRec.Body.String())
}

// TestNonAdminCannotPromoteAnyone mirrors TestNonAdminCannotCreateAccountOnlyInvite.
func TestNonAdminCannotPromoteAnyone(t *testing.T) {
	t.Parallel()
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "plain@example.test", DisplayName: "Plain", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser plain: %v", err)
	}
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "target@example.test", DisplayName: "Target", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser target: %v", err)
	}
	cookie := loginAs(t, srv, "plain@example.test")

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"target@example.test","is_admin":true}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusForbidden, "status = %d, want 403: %s", rec.Code, rec.Body.String())
}

// TestTokenCallerCannotSetAdmin mirrors TestTokenCallerCannotManageInstanceInvites.
func TestTokenCallerCannotSetAdmin(t *testing.T) {
	t.Parallel()
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create project: %v", err)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	assert.Must(t, err == nil, "CreateAPIToken: %v", err)

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"owner@example.test","is_admin":true}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusForbidden, "status = %d, want 403: %s", rec.Code, rec.Body.String())
}

// TestLastAdminCannotDemoteThemselves is the last administrator choosing
// to step down, where the caller and the target are the same account.
func TestLastAdminCannotDemoteThemselves(t *testing.T) {
	t.Parallel()
	srv, _, _, adminCookie := loginAsAdmin(t, nil)

	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(adminCookie)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	assert.Must(t, meRec.Code == http.StatusOK, "/api/me = %d, want 200", meRec.Code)

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"admin@example.test","is_admin":false}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusConflict, "status = %d, want 409: %s", rec.Code, rec.Body.String())

	// Still an admin afterwards.
	stillMeReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	stillMeReq.AddCookie(adminCookie)
	stillMeRec := httptest.NewRecorder()
	srv.ServeHTTP(stillMeRec, stillMeReq)
	assert.Must(t, strings.Contains(stillMeRec.Body.String(), `"is_admin":true`), "admin should still be an admin after a refused self-demotion, /api/me body = %s", stillMeRec.Body.String())
}

// TestAdminCanDemoteAnotherAdminWhenOneRemains confirms the guard only
// fires on the *last* admin, not on every demotion.
func TestAdminCanDemoteAnotherAdminWhenOneRemains(t *testing.T) {
	t.Parallel()
	srv, ids, _, adminCookie := loginAsAdmin(t, nil)
	ctx := context.Background()
	colleague, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "colleague@example.test", DisplayName: "Colleague", Password: "password12345"})
	assert.Must(t, err == nil, "CreateUser: %v", err)
	if err := ids.SetAdmin(ctx, colleague.ID, true); err != nil {
		t.Fatalf("SetAdmin: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"colleague@example.test","is_admin":false}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusOK, "status = %d, want 200: %s", rec.Code, rec.Body.String())

	// And the demoted colleague can no longer reach the admin surface.
	colleagueCookie := loginAs(t, srv, "colleague@example.test")
	inviteReq := httptest.NewRequest(http.MethodPost, "/api/invites", strings.NewReader(`{"email":"third@example.test"}`))
	inviteReq.AddCookie(colleagueCookie)
	inviteReq.Header.Set("Content-Type", "application/json")
	inviteRec := httptest.NewRecorder()
	srv.ServeHTTP(inviteRec, inviteReq)
	assert.Must(t, inviteRec.Code == http.StatusForbidden, "demoted colleague's POST /api/invites = %d, want 403", inviteRec.Code)
}

// TestSetAdminMissingIsAdminIsBadRequest pins the bare-bool fix: an
// absent is_admin field must be refused outright, never silently treated
// as false (which would demote whoever the email named without the
// caller ever having asked for that).
func TestSetAdminMissingIsAdminIsBadRequest(t *testing.T) {
	t.Parallel()
	srv, ids, _, adminCookie := loginAsAdmin(t, nil)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "target@example.test", DisplayName: "Target", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"target@example.test"}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Must(t, rec.Code == http.StatusBadRequest, "status = %d, want 400: %s", rec.Code, rec.Body.String())

	// And the target's admin status is untouched — still not an admin,
	// not silently demoted (it never had the flag to begin with, but the
	// point is the request must not have been treated as is_admin:false).
	targetCookie := loginAs(t, srv, "target@example.test")
	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(targetCookie)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	assert.Must(t, strings.Contains(meRec.Body.String(), `"is_admin":false`), "target's /api/me body = %s, want is_admin:false unchanged", meRec.Body.String())
}

// What PATCH /api/admins refuses an administrator, and with which
// status: the screen tells these three apart by the code alone.
func TestSettingAnAdministratorRefusesABadRequest(t *testing.T) {
	t.Parallel()
	srv, _, _, adminCookie := loginAsAdmin(t, nil)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"demoting the last administrator", `{"email":"admin@example.test","is_admin":false}`, http.StatusConflict},
		{"an address nobody holds", `{"email":"nobody@example.test","is_admin":true}`, http.StatusNotFound},
		{"a body naming nobody", `{}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(tc.body))
			req.AddCookie(adminCookie)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			assert.Must(t, rec.Code == tc.status, "status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
		})
	}
}
