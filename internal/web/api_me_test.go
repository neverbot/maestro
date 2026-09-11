package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neverbot/maestro/internal/identity"
)

// The account screen says who you are, and the header says it instead of
// a bare "Sign out". Both read this response, and before this test
// /api/me answered a user id and a boolean: two facts that name nobody.
//
// Mutation: delete either assignment in handleMe and this fails naming
// the missing field.
func TestMeNamesTheSessionCallerInWordsAPersonWouldUse(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "designer@studio.com", DisplayName: "A Designer", Password: "password12345",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/auth/login",
		`{"email":"designer@studio.com","password":"password12345"}`)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "maestro_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("login set no session cookie")
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(session)
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("/api/me = %d: %s", meRec.Code, meRec.Body.String())
	}

	var me struct {
		UserID      string `json:"user_id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		CreatedAt   string `json:"created_at"`
		IsAdmin     bool   `json:"is_admin"`
	}
	if err := json.Unmarshal(meRec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decoding /api/me: %v", err)
	}
	if me.Email != "designer@studio.com" {
		t.Errorf("email = %q, want the address they signed in with", me.Email)
	}
	if me.DisplayName != "A Designer" {
		t.Errorf("display_name = %q, want the name they were created with", me.DisplayName)
	}
	if me.CreatedAt == "" {
		t.Error("created_at is empty, so the account screen cannot say since when")
	}
	if me.UserID == "" {
		t.Error("user_id disappeared, and something still reads it")
	}
}

// A token is a key to one game. The person who minted it has an email
// address and a name, and neither is part of what the key opens: a token
// caller's answer is exactly what it was before the account screen
// existed.
//
// Mutation: drop the `!caller.IsToken()` guard in handleMe and this fails
// naming the field a token was told.
func TestMeTellsATokenCallerNothingAboutThePerson(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: project.ID, UserID: owner.ID, Label: "agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/me with a token = %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding /api/me: %v", err)
	}
	for _, field := range []string{"email", "display_name", "created_at"} {
		if _, told := payload[field]; told {
			t.Errorf("a token caller was told %q", field)
		}
	}
	if _, ok := payload["project_id"]; !ok {
		t.Error("a token caller lost project_id, which something reads")
	}
}
