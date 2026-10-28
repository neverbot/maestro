package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/testutil"
)

// reserveAddr opens and immediately closes a TCP listener on an
// OS-assigned port, returning the address it was bound to. run() below
// opens its own listener via http.Server.ListenAndServe, which offers no
// way to report back which port it chose, so this reserves one up front
// instead — the same "listen once to learn a free port, then hand the
// address to the thing that listens for real" pattern any test standing
// up its own server on port 0 uses. The small window between this
// listener closing and run's own bind is a real but negligible race on a
// CI runner or a developer machine; nothing else in this process binds
// to a TCP port to collide with it.
func reserveAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	assert.Must(t, err == nil, "reserve a free port: %v", err)
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close reserved listener: %v", err)
	}
	return addr
}

// testGetenv turns a plain map into the getenv func(string) string shape
// run() and config.Load both take, so this test can drive run() with an
// in-memory environment instead of mutating the real process environment
// (t.Setenv would work too, but a map keeps every test's environment
// fully independent when run in parallel).
func testGetenv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

// waitForRunningServer polls /healthz and watches run() at the same
// time, and it exists because the version that only polled reported the
// wrong thing.
func waitForRunningServer(t *testing.T, base string, done chan error, deadline time.Duration) {
	t.Helper()
	ready := make(chan error, 1)
	go func() { ready <- waitForHealthz(base, deadline) }()
	select {
	case err := <-done:
		// run() returned before the server ever answered. Whatever it
		// says is the real failure; put it back so the caller's own
		// shutdown assertion still finds a value rather than blocking.
		done <- err
		t.Fatalf("run() exited during startup instead of serving: %v", err)
	case err := <-ready:
		assert.Must(t, err == nil, "server never became healthy: %v", err)
	}
}

// waitForHealthz polls GET /healthz until it answers "ok" or deadline
// elapses, matching how a real caller (or an orchestrator's readiness
// probe) would wait for a process that has just been started in the
// background.
func waitForHealthz(base string, deadline time.Duration) error {
	client := &http.Client{Timeout: time.Second}
	giveUp := time.Now().Add(deadline)
	var lastErr error
	for time.Now().Before(giveUp) {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			body := make([]byte, 2)
			n, _ := resp.Body.Read(body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && string(body[:n]) == "ok" {
				return nil
			}
			lastErr = fmt.Errorf("status %d, body %q", resp.StatusCode, string(body[:n]))
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return lastErr
}

// startRunningServer starts run() against a fresh reserved address and
// throwaway database, waits for it to become healthy, and returns the
// base URL, the cancel func that triggers its shutdown (mirroring
// SIGINT/SIGTERM in main), and the channel run's own return value arrives
// on. Shared by every test in this file that needs a live process to
// drive HTTP requests against, rather than each duplicating the same
// start-up boilerplate.
func startRunningServer(t *testing.T) (base string, cancel context.CancelFunc, done chan error) {
	t.Helper()
	addr := reserveAddr(t)
	dbURL := testutil.NewDatabaseURL(t)

	env := map[string]string{
		"DATABASE_URL":         dbURL,
		"MAESTRO_ADDR":         addr,
		"FIRST_ADMIN_EMAIL":    "admin@example.test",
		"FIRST_ADMIN_PASSWORD": "password12345",
		"REGISTRATION_MODE":    "invite_only",
	}

	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- run(ctx, testGetenv(env)) }()

	base = "http://" + addr
	// **Thirty seconds, and it is not a claim about how fast this starts.**
	waitForRunningServer(t, base, done, 30*time.Second)
	return base, cancel, done
}

// loginAdmin logs the bootstrapped admin in over real HTTP and returns
// the session cookie the response set.
func loginAdmin(t *testing.T, base string) *http.Cookie {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"email":    "admin@example.test",
		"password": "password12345",
	})
	assert.Must(t, err == nil, "marshal login body: %v", err)
	resp, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(body))
	assert.Must(t, err == nil, "POST /api/auth/login: %v", err)
	defer func() { _ = resp.Body.Close() }()
	assert.Must(t, resp.StatusCode == http.StatusOK, "login status = %d, want 200", resp.StatusCode)
	cookies := resp.Cookies()
	assert.Must(t, len(cookies) != 0, "login response set no session cookie")
	return cookies[0]
}

// createGame creates a game as the given session caller and returns its
// slug — the address every route under /api/games/{game} takes, and the
// address /g/{game} shows a designer. The id comes back in the same
// answer and nothing here needs it.
func createGame(t *testing.T, base string, session *http.Cookie, slug, name string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"slug": slug, "name": name})
	assert.Must(t, err == nil, "marshal create game body: %v", err)
	req, err := http.NewRequest(http.MethodPost, base+"/api/games", bytes.NewReader(body))
	assert.Must(t, err == nil, "NewRequest POST /api/games: %v", err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(session)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "POST /api/games: %v", err)
	defer func() { _ = resp.Body.Close() }()
	assert.Must(t, resp.StatusCode == http.StatusCreated, "create game status = %d, want 201", resp.StatusCode)
	var out struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create game response: %v", err)
	}
	assert.Must(t, out.Slug != "", "create game answered no slug, and the slug is the address: %+v", out)
	return out.Slug
}

// mintToken creates an API token for a game, addressed by slug, as the given session caller
// and returns its clear (bearer) value.
func mintToken(t *testing.T, base string, session *http.Cookie, gameSlug string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"label": "test-agent"})
	assert.Must(t, err == nil, "marshal create token body: %v", err)
	req, err := http.NewRequest(http.MethodPost, base+"/api/games/"+gameSlug+"/tokens", bytes.NewReader(body))
	assert.Must(t, err == nil, "NewRequest POST tokens: %v", err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(session)
	resp, err := http.DefaultClient.Do(req)
	assert.Must(t, err == nil, "POST tokens: %v", err)
	defer func() { _ = resp.Body.Close() }()
	assert.Must(t, resp.StatusCode == http.StatusCreated, "create token status = %d, want 201", resp.StatusCode)
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create token response: %v", err)
	}
	return out.Token
}

// TestRunServesMigratesAndBootstraps drives run() as a real caller would:
// against a real TCP listener and a real (throwaway) Postgres database,
// not by calling web.NewServer's handler in-process. It exercises,
// end to end, migrations actually running before the server accepts a
// request and BootstrapFirstAdmin producing a login-able admin — the
// property that makes "a human logs in through the browser with a local
// account" (the Core sub-project's own definition of done) true against a
// running process, not only against identity's own unit tests.
func TestRunServesMigratesAndBootstraps(t *testing.T) {
	base, cancel, done := startRunningServer(t)
	loginAdmin(t, base)
	cancel()
	select {
	case err := <-done:
		assert.Must(t, err == nil, "run() returned an error on shutdown: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s of ctx being cancelled")
	}
}

// TestGracefulShutdownDrainsSSEAndInFlightRequests asserts the two
// halves of a graceful shutdown: an open SSE stream ends promptly, and a
// request already being served still completes with 200 rather than
// losing the pool mid-handler.
func TestGracefulShutdownDrainsSSEAndInFlightRequests(t *testing.T) {
	base, cancel, done := startRunningServer(t)

	session := loginAdmin(t, base)
	gameSlug := createGame(t, base, session, "azeroth", "Azeroth")
	token := mintToken(t, base, session, gameSlug)

	// Open one long-lived SSE stream ahead of the burst below, exactly the
	// shape web.Server.Close exists to end promptly on shutdown.
	sseReq, err := http.NewRequest(http.MethodGet, base+"/api/games/"+gameSlug+"/events", nil)
	assert.Must(t, err == nil, "NewRequest GET events: %v", err)
	sseReq.Header.Set("Authorization", "Bearer "+token)
	sseResp, err := http.DefaultClient.Do(sseReq)
	assert.Must(t, err == nil, "GET events: %v", err)
	defer func() { _ = sseResp.Body.Close() }()
	assert.Must(t, sseResp.StatusCode == http.StatusOK, "events status = %d, want 200", sseResp.StatusCode)
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		_, _ = io.Copy(io.Discard, sseResp.Body)
	}()

	// **The client owns the window instead of racing the clock for it.**
	// The login goes out in two halves with Expect: 100-continue, so the
	// server's 100 is proof that the handler is inside the request before
	// anything is cancelled, and the connection is active rather than idle
	// — an idle one Shutdown closes at once, which reaches the client as
	// EOF and reads exactly like the bug under test. No sleep, no measured
	// latency and no assumption about how loaded the machine is.
	body, pipe := io.Pipe()
	loginReq, err := http.NewRequest(http.MethodPost, base+"/api/auth/login", body)
	assert.Must(t, err == nil, "NewRequest POST login: %v", err)
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Expect", "100-continue")
	reading := make(chan struct{})
	loginReq = loginReq.WithContext(httptrace.WithClientTrace(loginReq.Context(), &httptrace.ClientTrace{
		Got100Continue: func() { close(reading) },
	}))
	// DisableKeepAlives keeps this off the idle connections the requests
	// above left behind; ExpectContinueTimeout has to be non-zero or the
	// transport sends the body immediately and never waits for the 100.
	client := &http.Client{Transport: &http.Transport{
		DisableKeepAlives:     true,
		ExpectContinueTimeout: 10 * time.Second,
	}}
	defer client.CloseIdleConnections()

	var loginResp *http.Response
	var loginErr error
	landed := make(chan struct{})
	go func() {
		defer close(landed)
		loginResp, loginErr = client.Do(loginReq)
	}()

	select {
	case <-reading:
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not start reading the login body within 10s")
	}
	cancel()
	// Only now is the request finished, so the handler reaches argon2 and
	// the database with shutdown already under way.
	_, err = io.WriteString(pipe, `{"email":"admin@example.test","password":"password12345"}`)
	assert.Must(t, err == nil, "write login body: %v", err)
	assert.Must(t, pipe.Close() == nil, "close login body")

	select {
	case <-landed:
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatal("the in-flight login neither completed nor failed")
	}
	assert.Must(t, loginErr == nil, "in-flight login across shutdown: transport error instead of completing: %v", loginErr)
	defer func() { _ = loginResp.Body.Close() }()
	assert.Must(t, loginResp.StatusCode == http.StatusOK, "in-flight login across shutdown: status = %d, want 200", loginResp.StatusCode)

	select {
	case <-streamDone:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE stream did not terminate within 5s of shutdown")
	}

	select {
	case err := <-done:
		assert.Must(t, err == nil, "run() returned an error on shutdown: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s of ctx being cancelled")
	}

	// The listener is actually gone, not merely idle: any connection-level
	// failure here is the expected outcome; only a successful response
	// would mean the server kept accepting connections after run()
	// already returned.
	if _, err := http.Get(base + "/healthz"); err == nil {
		t.Fatal("server still accepted a connection after shutdown")
	} else {
		t.Logf("post-shutdown request failed as expected: %v", err)
	}
}

// TestStartPruneLoopSweepsImmediatelyAtStartup pins the fix for the
// defect a review of this task found: time.NewTicker's first tick does
// not fire until a full pruneInterval has elapsed, so a naive
// ticker-only loop never prunes anything on a process that restarts more
// often than that — ordinary deploy churn, on most real deployments. It
// creates one already-expired session and one already-expired unbound
// invite directly (bypassing IssueSession/CreateInvite, both of which
// refuse to mint something already expired), starts startPruneLoop, and
// asserts both rows are gone well inside pruneInterval — proving the
// start-up sweep actually ran rather than only the ticked ones this
// package's own test suite would otherwise have to wait an hour to see.
func TestStartPruneLoopSweepsImmediatelyAtStartup(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := config.Config{
		RegistrationMode: config.RegistrationInviteOnly,
		Argon2:           config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		SessionTTL:       24 * time.Hour,
		InviteTTL:        24 * time.Hour,
	}
	ids := identity.New(pool, cfg)
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "prune-target@example.test",
		DisplayName: "Prune Target",
		Password:    "password12345",
	})
	assert.Must(t, err == nil, "CreateUser: %v", err)

	// A session already expired an hour ago: IssueSession refuses to
	// mint one like this, so the row is inserted directly.
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, now() - interval '1 hour')`,
		[]byte("test-prune-session-hash"), user.ID,
	); err != nil {
		t.Fatalf("insert expired session: %v", err)
	}

	// An already-expired, unbound (no email, no project/role) invite,
	// for the same reason.
	if _, err := pool.Exec(ctx,
		`INSERT INTO invites (token_hash, expires_at) VALUES ($1, now() - interval '1 hour')`,
		[]byte("test-prune-invite-hash"),
	); err != nil {
		t.Fatalf("insert expired invite: %v", err)
	}

	countSessions := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE token_hash = $1`, []byte("test-prune-session-hash")).Scan(&n); err != nil {
			t.Fatalf("count sessions: %v", err)
		}
		return n
	}
	countInvites := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM invites WHERE token_hash = $1`, []byte("test-prune-invite-hash")).Scan(&n); err != nil {
			t.Fatalf("count invites: %v", err)
		}
		return n
	}
	assert.Must(t, countSessions() == 1 && countInvites() == 1, "test setup did not actually insert the expired rows")

	loopCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startPruneLoop(loopCtx, ids)

	// pruneInterval is an hour; this only has to outlast the immediate
	// start-up sweep, not a real tick.
	giveUp := time.Now().Add(5 * time.Second)
	for time.Now().Before(giveUp) {
		if countSessions() == 0 && countInvites() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expired session/invite still present after 5s (sessions=%d, invites=%d); startPruneLoop did not sweep at start-up", countSessions(), countInvites())
}

// TestTheRunningBinaryServesTheGameContentTools is the only test that
// covers the wiring in run() itself: that this binary builds a metamodel
// service, hands it to web.NewServer, and therefore actually serves the
// game-content MCP tools an agent needs.
func TestTheRunningBinaryServesTheGameContentTools(t *testing.T) {
	base, cancel, done := startRunningServer(t)
	defer func() {
		cancel()
		<-done
	}()

	session := loginAdmin(t, base)
	gameSlug := createGame(t, base, session, "azeroth", "Azeroth")
	token := mintToken(t, base, session, gameSlug)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "wiring-test", Version: "0.0.1"}, nil)
	mcpSession, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             base + "/mcp",
		HTTPClient:           &http.Client{Transport: bearerTransport{token: token}},
		DisableStandaloneSSE: true,
	}, nil)
	assert.Must(t, err == nil, "connect to /mcp: %v", err)
	defer func() { _ = mcpSession.Close() }()

	tools, err := mcpSession.ListTools(ctx, nil)
	assert.Must(t, err == nil, "ListTools: %v", err)
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"types.upsert", "entities.upsert", "entities.list", "search"} {
		assert.Must(t, names[want], "the running binary serves no %q tool; the metamodel service is not wired into web.NewServer", want)
	}

	for _, call := range []*mcp.CallToolParams{
		{Name: "types.upsert", Arguments: map[string]any{
			"key": "quest", "label": "Quest", "label_plural": "Quests",
		}},
		{Name: "entities.upsert", Arguments: map[string]any{
			"items": []any{map[string]any{"type_key": "quest", "key": "hogger", "name": "Wanted: Hogger"}},
		}},
	} {
		result, err := mcpSession.CallTool(ctx, call)
		assert.Must(t, err == nil, "CallTool(%s): %v", call.Name, err)
		assert.Must(t, !result.IsError, "CallTool(%s) failed against the running binary: %+v", call.Name, result.Content)
	}

	found, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "search", Arguments: map[string]any{"query": "hogger"},
	})
	assert.Must(t, err == nil, "CallTool(search): %v", err)
	assert.Must(t, !found.IsError, "search failed against the running binary: %+v", found.Content)
	// The hit's shape as the running binary actually emits it: each hit
	// is labelled by kind, and an entity's own fields live under
	// `entity`, since a document hit has none of them.
	var out struct {
		Items []struct {
			Kind   string `json:"kind"`
			Entity *struct {
				Key string `json:"key"`
			} `json:"entity"`
		} `json:"items"`
	}
	raw, err := json.Marshal(found.StructuredContent)
	assert.Must(t, err == nil, "marshal search result: %v", err)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode search result %s: %v", raw, err)
	}
	assert.Must(t, len(out.Items) == 1 && out.Items[0].Kind == "entity" && out.Items[0].Entity != nil && out.Items[0].Entity.Key == "hogger", "search found %+v, want the entity just seeded through the same binary", out.Items)
}

// bearerTransport authenticates every request with an API token.
type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(req)
}
