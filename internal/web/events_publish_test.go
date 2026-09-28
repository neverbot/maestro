package web_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/realtime"
)

// openStream opens a real SSE connection (real socket, not a Recorder —
// see TestEventsStreamDeliversPublishedEvent's own doc comment for why)
// authenticated as cookie, past its initial ": connected\n\n" comment
// frame, and returns a reader positioned to read the next real event.
func openStream(t *testing.T, ts *httptest.Server, gameSlug, cookie string) *bufio.Reader {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/games/"+gameSlug+"/events", nil)
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.AddCookie(&http.Cookie{Name: "maestro_session", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Must(t, resp.StatusCode == http.StatusOK, "status = %d, want 200", resp.StatusCode)
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

// openTokenStream is openStream's bearer-token counterpart: the same
// real socket, past the same initial comment frame, authenticated as an
// API token rather than a browser session — the one subscriber shape
// TestChangeRolePublishesMemberUpdated and its siblings never exercised,
// which is exactly the gap that let a token subscriber receive another
// agent's token.minted event before this task's second review round.
func openTokenStream(t *testing.T, ts *httptest.Server, gameSlug, token string) *bufio.Reader {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/games/"+gameSlug+"/events", nil)
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Must(t, resp.StatusCode == http.StatusOK, "status = %d, want 200", resp.StatusCode)
	reader := bufio.NewReader(resp.Body)
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
// every human subscriber of the game regardless of role: MinRole is
// empty on eventMemberUpdated (publish.go) because handleListMembers,
// the REST endpoint this event mirrors, is open to a viewer already.
// The payload names only the member, never their new role — an
// invalidation, not a patch; see eventMemberUpdated's own doc comment
// for the concurrent-reordering hazard that forces a client to refetch
// instead of trusting a role carried on the wire.
func TestChangeRolePublishesMemberUpdated(t *testing.T) {
	t.Parallel()
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"})
	member, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "member@example.test", DisplayName: "Member", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	if _, err := projSvc.SetRole(ctx, member.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	ownerCookie := loginAs(t, srv, "owner@example.test")
	viewerCookie := loginAs(t, srv, "member@example.test")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	viewerReader := openStream(t, ts, project.Slug, viewerCookie.Value)

	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/games/"+project.Slug+"/members/"+member.ID.String(),
		strings.NewReader(`{"role":"editor"}`))
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	defer func() { _ = resp.Body.Close() }()
	assert.Must(t, resp.StatusCode == http.StatusOK, "status = %d, want 200", resp.StatusCode)

	kind, _, data := readOneSSEFrame(t, viewerReader)
	assert.Must(t, kind == "member.updated", "Kind = %q, want member.updated", kind)
	assert.Must(t, data == `{"user_id":"`+member.ID.String()+`"}`, "data = %q, want only user_id — no role field (an invalidation, not a patch)", data)
}

// TestRemoveMemberPublishesMemberRemoved pins the same thing for DELETE
// /api/games/{game}/members/{user}: MinRole empty, same reasoning as
// eventMemberUpdated.
func TestRemoveMemberPublishesMemberRemoved(t *testing.T) {
	t.Parallel()
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"})
	member, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "member@example.test", DisplayName: "Member", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	if _, err := projSvc.SetRole(ctx, member.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	ownerCookie := loginAs(t, srv, "owner@example.test")
	memberCookie := loginAs(t, srv, "member@example.test")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.Slug, ownerCookie.Value)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/games/"+project.Slug+"/members/"+member.ID.String(), nil)
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.AddCookie(memberCookie)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	defer func() { _ = resp.Body.Close() }()
	assert.Must(t, resp.StatusCode == http.StatusOK, "status = %d, want 200", resp.StatusCode)

	kind, _, data := readOneSSEFrame(t, ownerReader)
	assert.Must(t, kind == "member.removed", "Kind = %q, want member.removed", kind)
	assert.Must(t, strings.Contains(data, member.ID.String()), "data = %q, want it to name the removed member", data)
}

// TestDeleteGamePublishesGameDeleted pins that DELETE
// /api/games/{game}?confirm=<slug> publishes eventGameDeleted to every
// subscriber before answering 204 — see eventGameDeleted's own doc
// comment (publish.go) for why it carries no payload beyond the signal
// itself and why MinRole is empty.
func TestDeleteGamePublishesGameDeleted(t *testing.T) {
	t.Parallel()
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	ownerCookie := loginAs(t, srv, "owner@example.test")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.Slug, ownerCookie.Value)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/games/"+project.Slug+"?confirm=azeroth", nil)
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	defer func() { _ = resp.Body.Close() }()
	assert.Must(t, resp.StatusCode == http.StatusNoContent, "status = %d, want 204", resp.StatusCode)

	kind, _, _ := readOneSSEFrame(t, ownerReader)
	assert.Must(t, kind == "game.deleted", "Kind = %q, want game.deleted", kind)
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
	t.Parallel()
	srv, ids, projSvc, hub := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"})
	viewer, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "viewer@example.test", DisplayName: "Viewer", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	if _, err := projSvc.SetRole(ctx, viewer.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	ownerCookie := loginAs(t, srv, "owner@example.test")
	viewerCookie := loginAs(t, srv, "viewer@example.test")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.Slug, ownerCookie.Value)
	viewerReader := openStream(t, ts, project.Slug, viewerCookie.Value)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+project.Slug+"/tokens", strings.NewReader(`{"label":"agent"}`))
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	defer func() { _ = resp.Body.Close() }()
	assert.Must(t, resp.StatusCode == http.StatusCreated, "status = %d, want 201", resp.StatusCode)

	kind, _, _ := readOneSSEFrame(t, ownerReader)
	assert.Must(t, kind == "token.minted", "owner Kind = %q, want token.minted", kind)

	// Publish an unrelated, ungated marker event directly on the hub —
	// standing in for any other event this project might fire next — and
	// confirm it, not token.minted, is the *first* thing the viewer's
	// stream sees: proof the token event was filtered, not merely
	// delayed behind a slow consumer.
	hub.Publish(realtime.Event{ProjectID: project.ID, Kind: "marker"})

	kind, _, _ = readOneSSEFrame(t, viewerReader)
	assert.Must(t, kind != "token.minted", "viewer received token.minted; it should have been filtered by MinRole")
	assert.Must(t, kind == "marker", "viewer Kind = %q, want marker", kind)
}

// TestRevokeTokenPublishesTokenRevokedAboveViewerOnly is
// TestCreateTokenPublishesTokenMintedAboveViewerOnly's revoke
// counterpart.
func TestRevokeTokenPublishesTokenRevokedAboveViewerOnly(t *testing.T) {
	t.Parallel()
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	_, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	assert.Must(t, err == nil, "CreateAPIToken: %v", err)
	ownerCookie := loginAs(t, srv, "owner@example.test")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.Slug, ownerCookie.Value)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/games/"+project.Slug+"/tokens/"+tok.ID.String(), nil)
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	defer func() { _ = resp.Body.Close() }()
	assert.Must(t, resp.StatusCode == http.StatusNoContent, "status = %d, want 204", resp.StatusCode)

	kind, _, data := readOneSSEFrame(t, ownerReader)
	assert.Must(t, kind == "token.revoked", "Kind = %q, want token.revoked", kind)
	assert.Must(t, strings.Contains(data, tok.ID.String()), "data = %q, want it to name the revoked token", data)
}

// TestCreateProjectInvitePublishesInviteCreatedOwnerOnly pins
// eventInviteCreated's MinRole roles.Owner gate: an editor subscriber
// (below owner) never receives it, matching handleListProjectInvites'
// own owner-only gate on the REST read side.
func TestCreateProjectInvitePublishesInviteCreatedOwnerOnly(t *testing.T) {
	t.Parallel()
	srv, ids, projSvc, hub := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"})
	editor, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "editor@example.test", DisplayName: "Editor", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	if _, err := projSvc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	ownerCookie := loginAs(t, srv, "owner@example.test")
	editorCookie := loginAs(t, srv, "editor@example.test")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.Slug, ownerCookie.Value)
	editorReader := openStream(t, ts, project.Slug, editorCookie.Value)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+project.Slug+"/invites",
		strings.NewReader(`{"email":"new@example.test","role":"viewer"}`))
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	defer func() { _ = resp.Body.Close() }()
	assert.Must(t, resp.StatusCode == http.StatusCreated, "status = %d, want 201", resp.StatusCode)

	kind, _, data := readOneSSEFrame(t, ownerReader)
	assert.Must(t, kind == "invite.created", "owner Kind = %q, want invite.created", kind)
	assert.Must(t, strings.Contains(data, "new@example.test"), "data = %q, want it to name the invited email", data)

	// Publish an unrelated, ungated marker event directly on the hub, and
	// confirm it, not invite.created, is the first thing the editor's
	// stream sees — the same proof-of-filtering pattern
	// TestCreateTokenPublishesTokenMintedAboveViewerOnly uses.
	hub.Publish(realtime.Event{ProjectID: project.ID, Kind: "marker"})

	kind, _, _ = readOneSSEFrame(t, editorReader)
	assert.Must(t, kind != "invite.created", "editor received invite.created; it should have been filtered by MinRole")
	assert.Must(t, kind == "marker", "editor Kind = %q, want marker", kind)
}

// TestTokenCallerStreamNeverReceivesHumanOnlyEvents is the regression
// test for the first critical this task's second review round found: an
// agent's own token subscribing to its game used to receive
// member.updated and token.minted — including another agent's
// token_hint — over a stream its own credential could not have read the
// equivalent REST listing through (handleListMembers and
// handleListTokens are both gated by requireHumanCaller). It mints a
// token, opens a stream with that same token, triggers a member update
// and a second token mint (both HumanOnly), and confirms the token
// stream's first actually-received event is the ungated game.deleted
// that follows — not delayed, filtered.
func TestTokenCallerStreamNeverReceivesHumanOnlyEvents(t *testing.T) {
	t.Parallel()
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"})
	other, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "other@example.test", DisplayName: "Other", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	if _, err := projSvc.SetRole(ctx, other.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	agentToken, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	assert.Must(t, err == nil, "CreateAPIToken: %v", err)
	ownerCookie := loginAs(t, srv, "owner@example.test")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	tokenReader := openTokenStream(t, ts, project.Slug, agentToken)

	// Trigger a member.updated (HumanOnly) via a real PATCH request.
	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/games/"+project.Slug+"/members/"+other.ID.String(),
		strings.NewReader(`{"role":"editor"}`))
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	_ = resp.Body.Close()
	assert.Must(t, resp.StatusCode == http.StatusOK, "PATCH members status = %d, want 200", resp.StatusCode)

	// Trigger a token.minted (HumanOnly, and MinRole editor which the
	// token subscriber's own Role would otherwise satisfy).
	req, err = http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+project.Slug+"/tokens", strings.NewReader(`{"label":"second agent"}`))
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ownerCookie)
	resp, err = http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	_ = resp.Body.Close()
	assert.Must(t, resp.StatusCode == http.StatusCreated, "POST tokens status = %d, want 201", resp.StatusCode)

	// Delete the game: eventGameDeleted is not HumanOnly, so this must
	// be the first thing the token stream actually receives.
	req, err = http.NewRequest(http.MethodDelete, ts.URL+"/api/games/"+project.Slug+"?confirm=azeroth", nil)
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.AddCookie(ownerCookie)
	resp, err = http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	_ = resp.Body.Close()
	assert.Must(t, resp.StatusCode == http.StatusNoContent, "DELETE game status = %d, want 204", resp.StatusCode)

	kind, _, _ := readOneSSEFrame(t, tokenReader)
	assert.Must(t, kind != "member.updated" && kind != "token.minted", "token stream received %q; HumanOnly events must never reach a token subscriber", kind)
	assert.Must(t, kind == "game.deleted", "token stream's first received event Kind = %q, want game.deleted", kind)
}

// TestRevokeProjectInvitePublishesInviteRevoked is
// TestCreateProjectInvitePublishesInviteCreatedOwnerOnly's revoke
// counterpart — eventInviteRevoked had no test at all before this task's
// second review round.
func TestRevokeProjectInvitePublishesInviteRevoked(t *testing.T) {
	t.Parallel()
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	ownerCookie := loginAs(t, srv, "owner@example.test")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.Slug, ownerCookie.Value)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+project.Slug+"/invites",
		strings.NewReader(`{"email":"third@example.test","role":"viewer"}`))
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	_ = resp.Body.Close()
	assert.Must(t, resp.StatusCode == http.StatusCreated, "status = %d, want 201", resp.StatusCode)
	// Drain the invite.created frame this create just published.
	if kind, _, _ := readOneSSEFrame(t, ownerReader); kind != "invite.created" {
		t.Fatalf("Kind = %q, want invite.created", kind)
	}

	req, err = http.NewRequest(http.MethodDelete, ts.URL+"/api/games/"+project.Slug+"/invites/"+created.ID, nil)
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.AddCookie(ownerCookie)
	resp, err = http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	defer func() { _ = resp.Body.Close() }()
	assert.Must(t, resp.StatusCode == http.StatusNoContent, "status = %d, want 204", resp.StatusCode)

	kind, _, data := readOneSSEFrame(t, ownerReader)
	assert.Must(t, kind == "invite.revoked", "Kind = %q, want invite.revoked", kind)
	assert.Must(t, strings.Contains(data, created.ID), "data = %q, want it to name the revoked invite", data)
}

// TestRedeemInviteViaRegisterPublishesMemberUpdatedAndInviteRedeemed
// pins the second critical this task's second review round found:
// redeeming a project-bound invite through POST /api/auth/register (an
// unauthenticated, project-less handler) is a second producer of project
// membership besides handleChangeRole, and it used to publish nothing at
// all — an owner watching an invite get created would see the invitee
// redeem it and become a member with no signal on the stream at either
// end. handleRegister now publishes both eventMemberUpdated (the new
// member, as an invalidation) and eventInviteRedeemed (the consumed
// invite) once identity.RedeemInvite reports the project id it granted
// membership in.
func TestRedeemInviteViaRegisterPublishesMemberUpdatedAndInviteRedeemed(t *testing.T) {
	t.Parallel()
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@example.test", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	ownerCookie := loginAs(t, srv, "owner@example.test")

	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })

	ownerReader := openStream(t, ts, project.Slug, ownerCookie.Value)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+project.Slug+"/invites",
		strings.NewReader(`{"email":"","role":"viewer"}`))
	assert.Must(t, err == nil, "NewRequest: %v", err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ownerCookie)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "Do: %v", err)
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	_ = resp.Body.Close()
	assert.Must(t, resp.StatusCode == http.StatusCreated, "status = %d, want 201", resp.StatusCode)
	if kind, _, _ := readOneSSEFrame(t, ownerReader); kind != "invite.created" {
		t.Fatalf("Kind = %q, want invite.created", kind)
	}

	registerBody := `{"email":"newbie@example.test","display_name":"Newbie","password":"password12345","invite_token":"` + created.Token + `"}`
	regReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/auth/register", strings.NewReader(registerBody))
	assert.Must(t, err == nil, "NewRequest: %v", err)
	regReq.Header.Set("Content-Type", "application/json")
	regResp, err := http.DefaultClient.Do(regReq)
	assert.Must(t, err == nil, "Do: %v", err)
	defer func() { _ = regResp.Body.Close() }()
	assert.Must(t, regResp.StatusCode == http.StatusCreated, "register status = %d, want 201", regResp.StatusCode)

	kind, _, data := readOneSSEFrame(t, ownerReader)
	assert.Must(t, kind == "member.updated", "first post-redeem Kind = %q, want member.updated", kind)
	assert.Must(t, !strings.Contains(data, "role"), "member.updated data = %q, must carry no role field", data)

	kind, _, data = readOneSSEFrame(t, ownerReader)
	assert.Must(t, kind == "invite.redeemed", "second post-redeem Kind = %q, want invite.redeemed", kind)
	assert.Must(t, strings.Contains(data, created.ID), "invite.redeemed data = %q, want it to name the consumed invite", data)
}
