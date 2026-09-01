package web_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/realtime"
)

// openStream opens a real SSE connection (real socket, not a Recorder —
// see TestEventsStreamDeliversPublishedEvent's own doc comment for why)
// authenticated as cookie, past its initial ": connected\n\n" comment
// frame, and returns a reader positioned to read the next real event.
func openStream(t *testing.T, ts *httptest.Server, projectID, cookie string) *bufio.Reader {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/games/"+projectID+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "maestro_session", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	// Skip the initial ": connected\n\n" comment frame.
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read connect frame: %v", err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read connect frame: %v", err)
	}
	return reader
}

// TestChangeRolePublishesMemberUpdated pins that a real PATCH
// /api/games/{game}/members/{user} request — not a direct
// projects.SetRole call, which the hub never sees, since this task's
// whole point is that the HTTP mutation path is what publishes — reaches
// every subscriber of the game regardless of role: MinRole is empty on
// eventMemberUpdated (publish.go) because handleListMembers, the REST
// endpoint this event mirrors, is open to a viewer already.
func TestChangeRolePublishesMemberUpdated(t *testing.T) {
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	member, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "member@studio.com", DisplayName: "Member", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := projSvc.SetRole(ctx, member.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	ownerCookie := loginAs(t, srv, "owner@studio.com")
	viewerCookie := loginAs(t, srv, "member@studio.com")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	viewerReader := openStream(t, ts, project.ID.String(), viewerCookie.Value)

	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/games/"+project.ID.String()+"/members/"+member.ID.String(),
		strings.NewReader(`{"role":"editor"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	kind, _, data := readOneSSEFrame(t, viewerReader)
	if kind != "member.updated" {
		t.Fatalf("Kind = %q, want member.updated", kind)
	}
	if !strings.Contains(data, member.ID.String()) || !strings.Contains(data, `"role":"editor"`) {
		t.Fatalf("data = %q, want it to name the member and their new role", data)
	}
}

// TestRemoveMemberPublishesMemberRemoved pins the same thing for DELETE
// /api/games/{game}/members/{user}: MinRole empty, same reasoning as
// eventMemberUpdated.
func TestRemoveMemberPublishesMemberRemoved(t *testing.T) {
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	member, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "member@studio.com", DisplayName: "Member", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := projSvc.SetRole(ctx, member.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	ownerCookie := loginAs(t, srv, "owner@studio.com")
	memberCookie := loginAs(t, srv, "member@studio.com")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.ID.String(), ownerCookie.Value)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/games/"+project.ID.String()+"/members/"+member.ID.String(), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(memberCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	kind, _, data := readOneSSEFrame(t, ownerReader)
	if kind != "member.removed" {
		t.Fatalf("Kind = %q, want member.removed", kind)
	}
	if !strings.Contains(data, member.ID.String()) {
		t.Fatalf("data = %q, want it to name the removed member", data)
	}
}

// TestDeleteGamePublishesGameDeleted pins that DELETE
// /api/games/{game}?confirm=<slug> publishes eventGameDeleted to every
// subscriber before answering 204 — see eventGameDeleted's own doc
// comment (publish.go) for why it carries no payload beyond the signal
// itself and why MinRole is empty.
func TestDeleteGamePublishesGameDeleted(t *testing.T) {
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ownerCookie := loginAs(t, srv, "owner@studio.com")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.ID.String(), ownerCookie.Value)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/games/"+project.ID.String()+"?confirm=azeroth", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	kind, _, _ := readOneSSEFrame(t, ownerReader)
	if kind != "game.deleted" {
		t.Fatalf("Kind = %q, want game.deleted", kind)
	}
}

// TestCreateTokenPublishesTokenMintedAboveViewerOnly pins the deliberate
// role gate eventTokenMinted's own doc comment (publish.go) argues for: a
// viewer subscriber never receives it, even though it is only metadata a
// viewer could already read via GET /api/games/{game}/tokens, while an
// editor-or-above subscriber does. Proven by reading the viewer's *next*
// frame after the mint and asserting it is the unrelated member.updated
// this test triggers afterward, not token.minted — a filtered event, not
// merely a delayed one.
func TestCreateTokenPublishesTokenMintedAboveViewerOnly(t *testing.T) {
	srv, ids, projSvc, hub := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	viewer, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := projSvc.SetRole(ctx, viewer.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	ownerCookie := loginAs(t, srv, "owner@studio.com")
	viewerCookie := loginAs(t, srv, "viewer@studio.com")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.ID.String(), ownerCookie.Value)
	viewerReader := openStream(t, ts, project.ID.String(), viewerCookie.Value)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+project.ID.String()+"/tokens", strings.NewReader(`{"label":"agent"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	kind, _, _ := readOneSSEFrame(t, ownerReader)
	if kind != "token.minted" {
		t.Fatalf("owner Kind = %q, want token.minted", kind)
	}

	// Publish an unrelated, ungated marker event directly on the hub —
	// standing in for any other event this project might fire next — and
	// confirm it, not token.minted, is the *first* thing the viewer's
	// stream sees: proof the token event was filtered, not merely
	// delayed behind a slow consumer.
	hub.Publish(realtime.Event{ProjectID: project.ID, Kind: "marker"})

	kind, _, _ = readOneSSEFrame(t, viewerReader)
	if kind == "token.minted" {
		t.Fatal("viewer received token.minted; it should have been filtered by MinRole")
	}
	if kind != "marker" {
		t.Fatalf("viewer Kind = %q, want marker", kind)
	}
}

// TestRevokeTokenPublishesTokenRevokedAboveViewerOnly is
// TestCreateTokenPublishesTokenMintedAboveViewerOnly's revoke
// counterpart.
func TestRevokeTokenPublishesTokenRevokedAboveViewerOnly(t *testing.T) {
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	ownerCookie := loginAs(t, srv, "owner@studio.com")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.ID.String(), ownerCookie.Value)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/games/"+project.ID.String()+"/tokens/"+tok.ID.String(), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	kind, _, data := readOneSSEFrame(t, ownerReader)
	if kind != "token.revoked" {
		t.Fatalf("Kind = %q, want token.revoked", kind)
	}
	if !strings.Contains(data, tok.ID.String()) {
		t.Fatalf("data = %q, want it to name the revoked token", data)
	}
}

// TestCreateProjectInvitePublishesInviteCreatedOwnerOnly pins
// eventInviteCreated's MinRole roles.Owner gate: an editor subscriber
// (below owner) never receives it, matching handleListProjectInvites'
// own owner-only gate on the REST read side.
func TestCreateProjectInvitePublishesInviteCreatedOwnerOnly(t *testing.T) {
	srv, ids, projSvc, hub := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	editor, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "editor@studio.com", DisplayName: "Editor", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := projSvc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	ownerCookie := loginAs(t, srv, "owner@studio.com")
	editorCookie := loginAs(t, srv, "editor@studio.com")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.ID.String(), ownerCookie.Value)
	editorReader := openStream(t, ts, project.ID.String(), editorCookie.Value)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+project.ID.String()+"/invites",
		strings.NewReader(`{"email":"new@studio.com","role":"viewer"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	kind, _, data := readOneSSEFrame(t, ownerReader)
	if kind != "invite.created" {
		t.Fatalf("owner Kind = %q, want invite.created", kind)
	}
	if !strings.Contains(data, "new@studio.com") {
		t.Fatalf("data = %q, want it to name the invited email", data)
	}

	// Publish an unrelated, ungated marker event directly on the hub, and
	// confirm it, not invite.created, is the first thing the editor's
	// stream sees — the same proof-of-filtering pattern
	// TestCreateTokenPublishesTokenMintedAboveViewerOnly uses.
	hub.Publish(realtime.Event{ProjectID: project.ID, Kind: "marker"})

	kind, _, _ = readOneSSEFrame(t, editorReader)
	if kind == "invite.created" {
		t.Fatal("editor received invite.created; it should have been filtered by MinRole")
	}
	if kind != "marker" {
		t.Fatalf("editor Kind = %q, want marker", kind)
	}
}
