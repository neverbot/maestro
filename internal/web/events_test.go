package web_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/web"
)

// newTestServerWithHub wires a server the same way newTestServer does
// (auth_test.go), except the caller keeps its own reference to the
// *realtime.Hub the server publishes from — Options.Hub is exactly the
// injection point a real publisher, in production, arrives through from
// a service this package does not own; here it lets a test stand in for
// that publisher.
func newTestServerWithHub(t *testing.T, maxLifetime, heartbeatInterval time.Duration) (*web.Server, *identity.Service, *projects.Service, *realtime.Hub) {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := testConfig()
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	hub := realtime.NewHub()
	srv := web.NewServer(web.Options{
		Version:              "test",
		Config:               cfg,
		Identity:             ids,
		Projects:             projSvc,
		Hub:                  hub,
		SSEMaxLifetime:       maxLifetime,
		SSEHeartbeatInterval: heartbeatInterval,
	})
	return srv, ids, projSvc, hub
}

// readOneSSEFrame reads lines from r until it has collected one complete
// "id: ...\nevent: ...\ndata: ...\n\n" frame (skipping ": ping" heartbeat
// comment lines, which carry none of the three) or a read fails. id is ""
// for a frame that carried no id: line (the synthetic "resync" event
// never has one — see handleEvents's own doc comment).
func readOneSSEFrame(t *testing.T, r *bufio.Reader) (kind, id, data string) {
	t.Helper()
	var gotKind, gotID, gotData string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE stream: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "id: "):
			gotID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			gotKind = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			gotData = strings.TrimPrefix(line, "data: ")
		case line == "" && gotKind != "":
			return gotKind, gotID, gotData
		}
	}
}

func TestEventsStreamRequiresAuthentication(t *testing.T) {
	srv, _, _, _ := newTestServerWithHub(t, time.Minute, time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+uuid.New().String()+"/events", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
}

func TestEventsStreamRequiresMembership(t *testing.T) {
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "stranger@studio.com", DisplayName: "Stranger", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "stranger@studio.com")

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+project.ID.String()+"/events", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
}

// TestEventsStreamDeliversPublishedEvent drives the endpoint over a real
// listening socket (httptest.NewServer, not ServeHTTP against a
// Recorder): a Recorder runs the handler to completion synchronously, so
// a handler that blocks in its streaming loop would just hang the test
// rather than exercising it. It also asserts an event published for a
// different game never reaches this subscriber, which is the one thing
// internal/realtime's own tests cannot check — they never touch
// scope.ProjectID or hub.Subscribe as this handler actually wires them.
func TestEventsStreamDeliversPublishedEvent(t *testing.T) {
	srv, ids, projSvc, hub := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	other, err := projSvc.Create(ctx, "le-mans", "Le Mans", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, ts.URL+"/api/games/"+project.ID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	// Deterministic, not "probably fine because of the ordering": assert
	// the subscription actually exists before publishing anything,
	// rather than trusting that observing a 200 implies it (which is
	// true — see handleEvents's own doc comment on why Subscribe now
	// runs before headers are written — but a flake here is exactly what
	// someone would "fix" later by putting the sleep back).
	if got := hub.SubscriberCount(project.ID); got != 1 {
		t.Fatalf("SubscriberCount(project) = %d immediately after the 200, want 1", got)
	}

	hub.Publish(realtime.Event{ProjectID: other.ID, Kind: "noise"})
	hub.Publish(realtime.Event{ProjectID: project.ID, Kind: "project.updated", Payload: map[string]string{"slug": "azeroth"}})

	reader := bufio.NewReader(resp.Body)
	kind, id, data := readOneSSEFrame(t, reader)
	if kind != "project.updated" {
		t.Fatalf("Kind = %q, want project.updated (cross-game leak or missed the real event)", kind)
	}
	if id != "1" {
		t.Fatalf("id = %q, want 1 (this project's first-ever published event)", id)
	}
	if data != `{"slug":"azeroth"}` {
		t.Fatalf("data = %q", data)
	}
}

// TestEventsStreamSendsAnInitialConnectFrame pins the fix for a defect a
// review of this task found: with no event published and the heartbeat
// up to sseHeartbeatInterval away, a client had no way to tell "connected
// and waiting" from "stalled" until either one arrived. handleEvents now
// writes a ": connected\n\n" comment line immediately after the response
// headers, before entering its select loop — a comment, the same shape
// as the heartbeat's own ": ping\n\n", so it fires no "message" event on
// a real EventSource client and changes nothing about the data such a
// client receives; this test reads the raw bytes directly (not through
// readOneSSEFrame, which is built to skip past exactly this kind of
// comment line) specifically to assert the frame is there.
func TestEventsStreamSendsAnInitialConnectFrame(t *testing.T) {
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, ts.URL+"/api/games/"+project.ID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	line1, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read first line: %v", err)
	}
	if line1 != ": connected\n" {
		t.Fatalf("first line = %q, want %q", line1, ": connected\n")
	}
	line2, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read second line: %v", err)
	}
	if line2 != "\n" {
		t.Fatalf("second line = %q, want a blank line terminating the comment frame", line2)
	}
}

// TestEventsStreamClosesAtMaxLifetime pins the bounded-lifetime design
// decision itself (see handleEvents's own doc comment): with no event
// ever published, the stream still ends once SSEMaxLifetime elapses,
// which is the only mechanism this design has for making an already-open
// stream stop trusting a caller whose access was revoked after the
// handshake.
func TestEventsStreamClosesAtMaxLifetime(t *testing.T) {
	srv, ids, projSvc, _ := newTestServerWithHub(t, 100*time.Millisecond, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/games/"+project.ID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 512)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				close(done)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not close at SSEMaxLifetime")
	}
}

// TestEventsStreamClosesOnTokenRevokedMidStream pins the heartbeat
// re-check itself (handleEvents's own doc comment): a token that
// resolved fine at admission but is revoked while the stream is open
// gets the stream closed within one heartbeat interval, not left open
// until SSEMaxLifetime. SSEHeartbeatInterval is shrunk to make this
// observable without waiting out the real fifteen-second default;
// SSEMaxLifetime is left long so the max-lifetime backstop (already
// pinned by TestEventsStreamClosesAtMaxLifetime) cannot be mistaken for
// what actually closed this stream.
func TestEventsStreamClosesOnTokenRevokedMidStream(t *testing.T) {
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, 20*time.Millisecond)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/games/"+project.ID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: project.ID, TokenID: tok.ID}); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 512)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				close(done)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream stayed open after its token was revoked")
	}
}

// TestEventsStreamClosesOnMembershipRemovedMidStream is
// TestEventsStreamClosesOnTokenRevokedMidStream's session-caller
// counterpart: a member removed from the game while their stream is
// open (a session credential that is still perfectly valid — the removal
// is a projects-layer change, not an identity-layer one) also gets cut
// off by the same heartbeat re-check, via resolveProjectScope rather
// than resolveSessionCaller.
func TestEventsStreamClosesOnMembershipRemovedMidStream(t *testing.T) {
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, 20*time.Millisecond)
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
	cookie := loginAs(t, srv, "member@studio.com")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/games/"+project.ID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if _, err := projSvc.RemoveMember(ctx, member.ID, project.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 512)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				close(done)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream stayed open after its member was removed from the game")
	}
}

// TestEventsStreamReCheckDoesNotSlideSessionExpiry is the point of
// resolveSessionCallerReadOnly (auth.go): the heartbeat re-check asks
// "is this session still valid" every 20ms here, and none of those asks
// may themselves count as the kind of activity that slides the session's
// expiry forward — an open browser tab is not a person present. The
// session is backdated past its renewal halfway point first, exactly the
// condition TestSessionRenewsPastHalfwayThroughItsLifetime
// (auth_test.go) proves *does* trigger a renewal on an ordinary request,
// so a regression that routed the re-check through resolveSessionCaller
// instead of the read-only variant would fail this test by renewing.
func TestEventsStreamReCheckDoesNotSlideSessionExpiry(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	hub := realtime.NewHub()
	srv := web.NewServer(web.Options{
		Version:              "test",
		Config:               cfg,
		Identity:             ids,
		Projects:             projSvc,
		Hub:                  hub,
		SSEMaxLifetime:       time.Minute,
		SSEHeartbeatInterval: 20 * time.Millisecond,
	})
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token, _, err := ids.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	pastHalfway := time.Now().Add(cfg.SessionTTL/2 - time.Minute)
	tokenHash := sha256.Sum256([]byte(token))
	if _, err := pool.Exec(ctx, `UPDATE sessions SET expires_at = $1 WHERE token_hash = $2`, pastHalfway, tokenHash[:]); err != nil {
		t.Fatalf("backdate expires_at: %v", err)
	}

	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/games/"+project.ID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Admission itself goes through the ordinary, sliding
	// resolveSessionCaller (auth.go's authenticate middleware runs
	// before handleEvents ever does) — exactly like any other
	// authenticated request, it renews this past-halfway session once,
	// before the 200 above is even received. That renewal is expected
	// and is not what this test is checking; it establishes the
	// baseline the heartbeat re-check must not move further.
	_, afterAdmission, err := ids.UserForSession(ctx, token)
	if err != nil {
		t.Fatalf("UserForSession: %v", err)
	}
	if afterAdmission.Equal(pastHalfway) {
		t.Fatal("admission itself did not renew the session — the precondition this test depends on did not hold")
	}

	// Let several heartbeat ticks (each a read-only re-check) pass.
	time.Sleep(150 * time.Millisecond)

	_, expiry, err := ids.UserForSession(ctx, token)
	if err != nil {
		t.Fatalf("UserForSession: %v", err)
	}
	if !expiry.Equal(afterAdmission) {
		t.Fatalf("expiry moved from %v to %v after admission's own renewal — the heartbeat re-check slid the session", afterAdmission, expiry)
	}
}

// TestEventsStreamMarshalsPayloadPreventingFrameForgery pins the wire
// bug realtime.Event.Payload's own doc comment describes: a raw string
// payload containing a newline used to end the "data:" line early and a
// second newline to end the frame, letting whatever came after —
// including a crafted "event:" line — be parsed by the client as a
// second, forged event. json.Marshal escapes every control character
// inside a string, so a payload built specifically to try this now
// arrives as one harmless, single-line JSON string instead.
func TestEventsStreamMarshalsPayloadPreventingFrameForgery(t *testing.T) {
	srv, ids, projSvc, hub := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/games/"+project.ID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := hub.SubscriberCount(project.ID); got != 1 {
		t.Fatalf("SubscriberCount(project) = %d immediately after the 200, want 1", got)
	}

	// A payload that, written raw into "data: %s\n\n", would end that
	// field after "malicious" and start a forged event.
	forgedAttempt := "malicious\n\nevent: forged.admin.action\ndata: nothing to see here"
	hub.Publish(realtime.Event{ProjectID: project.ID, Kind: "entity.updated", Payload: map[string]string{"note": forgedAttempt}})

	reader := bufio.NewReader(resp.Body)
	kind, _, data := readOneSSEFrame(t, reader)
	if kind != "entity.updated" {
		t.Fatalf("Kind = %q, want entity.updated (a forged event: line was parsed as its own frame)", kind)
	}
	var decoded struct {
		Note string `json:"note"`
	}
	if err := json.Unmarshal([]byte(data), &decoded); err != nil {
		t.Fatalf("data %q did not decode as the single JSON object it should be: %v", data, err)
	}
	if decoded.Note != forgedAttempt {
		t.Fatalf("decoded note = %q, want %q", decoded.Note, forgedAttempt)
	}
}

// TestEventsStreamClosesOnServerClose pins the shutdown lever itself
// (Server.Close, server.go): an open stream ends promptly once Close is
// called, well before its own bounded lifetime would otherwise end it —
// this is what lets a future graceful-shutdown sequence (Task 16) call
// Close before or alongside http.Server.Shutdown instead of Shutdown
// waiting on every open stream for up to its own lifetime.
func TestEventsStreamClosesOnServerClose(t *testing.T) {
	srv, ids, projSvc, _ := newTestServerWithHub(t, time.Minute, time.Minute)
	ctx := context.Background()

	owner, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/games/"+project.ID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	srv.Close()

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 512)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				close(done)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream stayed open after Server.Close")
	}

	// Close is safe to call more than once.
	srv.Close()
}
