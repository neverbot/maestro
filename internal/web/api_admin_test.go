package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/identity"
)

// TestAdminCanPromoteAnotherUserToAdmin is this task's own acceptance
// test: the bootstrap admin, and only the bootstrap admin, is no longer
// the sole account that can ever hold instance-admin standing.
func TestAdminCanPromoteAnotherUserToAdmin(t *testing.T) {
	srv, ids, _, adminCookie := loginAsAdmin(t, nil)
	ctx := context.Background()
	colleague, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "colleague@studio.com", DisplayName: "Colleague", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/users/"+colleague.ID.String()+"/admin", strings.NewReader(`{"is_admin":true}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// The promotion is real, not just a 200: the new admin can now reach
	// the admin-only invite surface.
	colleagueCookie := loginAs(t, srv, "colleague@studio.com")
	inviteReq := httptest.NewRequest(http.MethodPost, "/api/invites", strings.NewReader(`{"email":"third@studio.com"}`))
	inviteReq.AddCookie(colleagueCookie)
	inviteReq.Header.Set("Content-Type", "application/json")
	inviteRec := httptest.NewRecorder()
	srv.ServeHTTP(inviteRec, inviteReq)
	if inviteRec.Code != http.StatusCreated {
		t.Fatalf("newly promoted admin POST /api/invites = %d, want 201: %s", inviteRec.Code, inviteRec.Body.String())
	}
}

// TestNonAdminCannotPromoteAnyone mirrors TestNonAdminCannotCreateAccountOnlyInvite.
func TestNonAdminCannotPromoteAnyone(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "plain@studio.com", DisplayName: "Plain", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser plain: %v", err)
	}
	target, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "target@studio.com", DisplayName: "Target", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser target: %v", err)
	}
	cookie := loginAs(t, srv, "plain@studio.com")

	req := httptest.NewRequest(http.MethodPatch, "/api/users/"+target.ID.String()+"/admin", strings.NewReader(`{"is_admin":true}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// TestTokenCallerCannotSetAdmin mirrors TestTokenCallerCannotManageInstanceInvites.
func TestTokenCallerCannotSetAdmin(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPatch, "/api/users/"+owner.ID.String()+"/admin", strings.NewReader(`{"is_admin":true}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// TestAdminCannotDemoteTheLastAdmin pins the guard that stops an
// instance from ever stranding itself again, including against the last
// admin demoting themselves.
func TestAdminCannotDemoteTheLastAdmin(t *testing.T) {
	srv, ids, _, adminCookie := loginAsAdmin(t, nil)
	ctx := context.Background()
	admin, err := ids.Authenticate(ctx, "admin@studio.com", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/users/"+admin.ID.String()+"/admin", strings.NewReader(`{"is_admin":false}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// TestAdminCanDemoteAnotherAdminWhenOneRemains confirms the guard only
// fires on the *last* admin, not on every demotion.
func TestAdminCanDemoteAnotherAdminWhenOneRemains(t *testing.T) {
	srv, ids, _, adminCookie := loginAsAdmin(t, nil)
	ctx := context.Background()
	colleague, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "colleague@studio.com", DisplayName: "Colleague", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := ids.SetAdmin(ctx, colleague.ID, true); err != nil {
		t.Fatalf("SetAdmin: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/users/"+colleague.ID.String()+"/admin", strings.NewReader(`{"is_admin":false}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// And the demoted colleague can no longer reach the admin surface.
	colleagueCookie := loginAs(t, srv, "colleague@studio.com")
	inviteReq := httptest.NewRequest(http.MethodPost, "/api/invites", strings.NewReader(`{"email":"third@studio.com"}`))
	inviteReq.AddCookie(colleagueCookie)
	inviteReq.Header.Set("Content-Type", "application/json")
	inviteRec := httptest.NewRecorder()
	srv.ServeHTTP(inviteRec, inviteReq)
	if inviteRec.Code != http.StatusForbidden {
		t.Fatalf("demoted colleague's POST /api/invites = %d, want 403", inviteRec.Code)
	}
}

func TestSetAdminUnknownUserIsNotFound(t *testing.T) {
	srv, _, _, adminCookie := loginAsAdmin(t, nil)

	req := httptest.NewRequest(http.MethodPatch, "/api/users/00000000-0000-0000-0000-000000000000/admin", strings.NewReader(`{"is_admin":true}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
