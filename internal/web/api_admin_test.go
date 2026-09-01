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
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "colleague@studio.com", DisplayName: "Colleague", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"colleague@studio.com","is_admin":true}`))
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
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "target@studio.com", DisplayName: "Target", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser target: %v", err)
	}
	cookie := loginAs(t, srv, "plain@studio.com")

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"target@studio.com","is_admin":true}`))
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

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"owner@studio.com","is_admin":true}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// TestAdminCannotDemoteTheLastAdminByDemotingSomeoneElse pins the guard
// against a second party stranding the instance.
func TestAdminCannotDemoteTheLastAdminByDemotingSomeoneElse(t *testing.T) {
	srv, _, _, adminCookie := loginAsAdmin(t, nil)

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"admin@studio.com","is_admin":false}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// TestLastAdminCannotDemoteThemselves is the HTTP-level pin for the case
// this task's own decision explicitly argues for — the last admin
// choosing, themselves, to step down — and that TestAdminCannotDemoteTheLastAdminByDemotingSomeoneElse
// does not cover, since there the caller and the target are different
// accounts. Here they are the same one.
func TestLastAdminCannotDemoteThemselves(t *testing.T) {
	srv, _, _, adminCookie := loginAsAdmin(t, nil)

	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(adminCookie)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("/api/me = %d, want 200", meRec.Code)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"admin@studio.com","is_admin":false}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}

	// Still an admin afterwards.
	stillMeReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	stillMeReq.AddCookie(adminCookie)
	stillMeRec := httptest.NewRecorder()
	srv.ServeHTTP(stillMeRec, stillMeReq)
	if !strings.Contains(stillMeRec.Body.String(), `"is_admin":true`) {
		t.Fatalf("admin should still be an admin after a refused self-demotion, /api/me body = %s", stillMeRec.Body.String())
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

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"colleague@studio.com","is_admin":false}`))
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

func TestSetAdminUnknownEmailIsNotFound(t *testing.T) {
	srv, _, _, adminCookie := loginAsAdmin(t, nil)

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"nobody@studio.com","is_admin":true}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestSetAdminMissingIsAdminIsBadRequest pins the bare-bool fix: an
// absent is_admin field must be refused outright, never silently treated
// as false (which would demote whoever the email named without the
// caller ever having asked for that).
func TestSetAdminMissingIsAdminIsBadRequest(t *testing.T) {
	srv, ids, _, adminCookie := loginAsAdmin(t, nil)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "target@studio.com", DisplayName: "Target", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{"email":"target@studio.com"}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// And the target's admin status is untouched — still not an admin,
	// not silently demoted (it never had the flag to begin with, but the
	// point is the request must not have been treated as is_admin:false).
	targetCookie := loginAs(t, srv, "target@studio.com")
	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(targetCookie)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	if !strings.Contains(meRec.Body.String(), `"is_admin":false`) {
		t.Fatalf("target's /api/me body = %s, want is_admin:false unchanged", meRec.Body.String())
	}
}

func TestSetAdminEmptyBodyIsBadRequest(t *testing.T) {
	srv, _, _, adminCookie := loginAsAdmin(t, nil)

	req := httptest.NewRequest(http.MethodPatch, "/api/admins", strings.NewReader(`{}`))
	req.AddCookie(adminCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
