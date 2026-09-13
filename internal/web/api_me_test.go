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
		UserID             string `json:"user_id"`
		Email              string `json:"email"`
		DisplayName        string `json:"display_name"`
		CreatedAt          string `json:"created_at"`
		SkillBundleVersion string `json:"skill_bundle_version"`
		IsAdmin            bool   `json:"is_admin"`
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
	// The bundle's first instruction tells an agent to read the bundle's
	// version from this call. The MCP tool has always answered it and
	// this mirror did not.
	if me.SkillBundleVersion == "" {
		t.Error("no skill_bundle_version: an agent following the bundle over REST cannot tell " +
			"whether the copy it holds is the one this server would serve")
	}
}

// TestMeAnswersATokenCallerTheAddressOfItsGame pins the other half of the
// same repair.
//
// `skill.md` §1 tells an agent to call whoami and take the game's
// **address** from the answer, because every route on both surfaces is
// addressed by slug. The MCP tool answers `project_slug`; this mirror
// answered a `project_id` and nothing else, so an agent that followed
// the bundle's first instruction over REST could not address its second
// call. A mirror that answers a different question from the tool it
// mirrors is not a mirror.
//
// Mutation: remove the project block from handleMe and this fails.
func TestMeAnswersATokenCallerTheAddressOfItsGame(t *testing.T) {
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

	var me struct {
		ProjectSlug        string `json:"project_slug"`
		ProjectName        string `json:"project_name"`
		SkillBundleVersion string `json:"skill_bundle_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decoding /api/me: %v", err)
	}
	if me.ProjectSlug != "azeroth" {
		t.Errorf("project_slug = %q, want the address every other route takes", me.ProjectSlug)
	}
	if me.ProjectName != "Azeroth" {
		t.Errorf("project_name = %q, want the game's own name", me.ProjectName)
	}
	if me.SkillBundleVersion == "" {
		t.Error("no skill_bundle_version for a token caller either")
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
