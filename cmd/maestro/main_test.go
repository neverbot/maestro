package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
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
//
// run() is started in a goroutine whose only output is the done
// channel. When it fails at startup — a port taken between reserveAddr
// closing its listener and run() binding it, a migration that will not
// apply, a database that refuses the connection — it returns
// immediately and that error sits in the channel unread, while the
// poller spends its whole budget dialling a port nobody is listening
// on and then reports "server never became healthy: connection
// refused". That sentence is true and it names a symptom: it is what
// *the test* saw, not what went wrong. Two runs of the full suite were
// diagnosed from it as a timeout, which is the one thing it does not
// prove.
//
// Selecting on both means a failed start is reported as itself, and a
// genuinely slow start is still reported as a timeout — and the two
// stop being indistinguishable.
func waitForRunningServer(t *testing.T, base string, done chan error, deadline time.Duration) {
	t.Helper()
	ready := make(chan struct{})
	go func() {
		defer close(ready)
		waitForHealthz(t, base, deadline)
	}()
	select {
	case err := <-done:
		// run() returned before the server ever answered. Whatever it
		// says is the real failure; put it back so the caller's own
		// shutdown assertion still finds a value rather than blocking.
		done <- err
		t.Fatalf("run() exited during startup instead of serving: %v", err)
	case <-ready:
	}
}

// waitForHealthz polls GET /healthz until it answers "ok" or deadline
// elapses, matching how a real caller (or an orchestrator's readiness
// probe) would wait for a process that has just been started in the
// background.
func waitForHealthz(t *testing.T, base string, deadline time.Duration) {
	t.Helper()
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
				return
			}
			lastErr = fmt.Errorf("status %d, body %q", resp.StatusCode, string(body[:n]))
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never became healthy: %v", lastErr)
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
		"FIRST_ADMIN_EMAIL":    "admin@studio.com",
		"FIRST_ADMIN_PASSWORD": "password12345",
		"REGISTRATION_MODE":    "invite_only",
	}

	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- run(ctx, testGetenv(env)) }()

	base = "http://" + addr
	waitForRunningServer(t, base, done, 5*time.Second)
	return base, cancel, done
}

// loginAdmin logs the bootstrapped admin in over real HTTP and returns
// the session cookie the response set.
func loginAdmin(t *testing.T, base string) *http.Cookie {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"email":    "admin@studio.com",
		"password": "password12345",
	})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	resp, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/auth/login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("login response set no session cookie")
	}
	return cookies[0]
}

// createGame creates a game as the given session caller and returns its
// slug — the address every route under /api/games/{game} takes, and the
// address /g/{game} shows a designer. The id comes back in the same
// answer and nothing here needs it.
func createGame(t *testing.T, base string, session *http.Cookie, slug, name string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"slug": slug, "name": name})
	if err != nil {
		t.Fatalf("marshal create game body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/games", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest POST /api/games: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/games: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create game status = %d, want 201", resp.StatusCode)
	}
	var out struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create game response: %v", err)
	}
	if out.Slug == "" {
		t.Fatalf("create game answered no slug, and the slug is the address: %+v", out)
	}
	return out.Slug
}

// mintToken creates an API token for a game, addressed by slug, as the given session caller
// and returns its clear (bearer) value.
func mintToken(t *testing.T, base string, session *http.Cookie, gameSlug string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"label": "test-agent"})
	if err != nil {
		t.Fatalf("marshal create token body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/games/"+gameSlug+"/tokens", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest POST tokens: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST tokens: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token status = %d, want 201", resp.StatusCode)
	}
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
		if err != nil {
			t.Fatalf("run() returned an error on shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s of ctx being cancelled")
	}
}

// TestGracefulShutdownDrainsSSEAndInFlightRequests is the regression test
// for the defect a review of this task found: run's original shutdown
// goroutine called srv.Shutdown but returned from run() (running its
// deferred pool.Close()) the instant srv.ListenAndServe unblocked — which
// happens the moment Shutdown is *called*, with http.ErrServerClosed, not
// when Shutdown's own wait for active handlers finishes. Forty concurrent
// requests measured against that version came back empty every time: the
// pool closed under handlers that were still using it. A prior version of
// this test only asserted that run() returned promptly after an SSE
// stream was left open — with no SSE stream actually opened, and with
// nothing in run() able to hang on shutdown once Shutdown is reached
// regardless of this bug, so it passed whether or not web.Server.Close
// was wired in and said nothing about draining. This test instead:
//  1. opens a real SSE stream and confirms it terminates once shutdown
//     signals it (the property web.Server.Close exists for), and
//  2. fires a burst of concurrent, DB-touching, project-scoped requests
//     and cancels the server's context at essentially the same instant,
//     asserting every one of them still completes with 200 rather than a
//     transport-level failure — the property this bug actually broke.
//
// The DB pool (internal/db/pool.go) caps MaxConns at 10, so a burst well
// above that count guarantees several requests are genuinely still
// waiting on — or holding — a pool connection at the moment shutdown
// begins, rather than relying on wall-clock timing alone to create the
// overlap.
func TestGracefulShutdownDrainsSSEAndInFlightRequests(t *testing.T) {
	base, cancel, done := startRunningServer(t)

	session := loginAdmin(t, base)
	gameSlug := createGame(t, base, session, "azeroth", "Azeroth")
	token := mintToken(t, base, session, gameSlug)

	// Open one long-lived SSE stream ahead of the burst below, exactly the
	// shape web.Server.Close exists to end promptly on shutdown.
	sseReq, err := http.NewRequest(http.MethodGet, base+"/api/games/"+gameSlug+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest GET events: %v", err)
	}
	sseReq.Header.Set("Authorization", "Bearer "+token)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer func() { _ = sseResp.Body.Close() }()
	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d, want 200", sseResp.StatusCode)
	}
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		_, _ = io.Copy(io.Discard, sseResp.Body)
	}()

	// Measure one login's real latency on this machine, right now, rather
	// than assuming a fixed number: argon2's cost is deliberately
	// noticeable (tens of milliseconds on a fast machine) but the actual
	// figure moves with CPU speed and, more importantly, with whatever
	// else is contending for it — a `go test ./...` run sharing the
	// machine with every other package's own tests is measurably slower
	// than this test running alone, which a fixed sleep tuned on a quiet
	// machine cannot account for. warmupLatency calibrates the settle
	// delay below to this run's own conditions instead.
	warmupStart := time.Now()
	warmupBody, err := json.Marshal(map[string]string{
		"email":    "admin@studio.com",
		"password": "password12345",
	})
	if err != nil {
		t.Fatalf("marshal warm-up login body: %v", err)
	}
	warmupResp, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(warmupBody))
	if err != nil {
		t.Fatalf("warm-up login: %v", err)
	}
	_ = warmupResp.Body.Close()
	warmupLatency := time.Since(warmupStart)

	// Fire a burst of concurrent logins against the bootstrapped admin's
	// own credentials and cancel the server's context essentially
	// concurrently with launching them, so several are still mid-request
	// — inside Authenticate's own argon2 password verification, which
	// takes tens of milliseconds by design (Config.Argon2), long enough
	// to guarantee real overlap with the shutdown that follows a few
	// microseconds later — when shutdown begins. Login, not a cheap
	// bearer-token lookup, is deliberately the request under test here:
	// this is the same slow-request-plus-concurrent-shutdown shape the
	// review that found this bug used (forty concurrent logins,
	// SIGTERM at 300ms), and a fast handler leaves too small a window to
	// reliably catch the race this test exists to catch. The burst stays
	// at 8, under loginLimiter's ten-per-minute-per-email budget
	// (server.go), so every one of them is expected to succeed with 200
	// rather than 429 — a rate-limited response would still be a
	// well-formed one, but asserting exactly 200 keeps this test from
	// quietly accepting "some requests got a low-effort answer instead of
	// being served" as a pass.
	const burst = 8
	var wg sync.WaitGroup
	statuses := make([]int, burst)
	errs := make([]error, burst)
	wg.Add(burst)
	for i := 0; i < burst; i++ {
		go func(i int) {
			defer wg.Done()
			body, err := json.Marshal(map[string]string{
				"email":    "admin@studio.com",
				"password": "password12345",
			})
			if err != nil {
				errs[i] = err
				return
			}
			resp, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(body))
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			_, _ = io.Copy(io.Discard, resp.Body)
			statuses[i] = resp.StatusCode
		}(i)
	}
	// Waiting a fraction of the warm-up login's own latency before
	// cancelling gives every goroutine above time to dial, send its
	// request and have the server start verifying its password —
	// genuinely inside Authenticate, not merely queued to connect —
	// before shutdown begins, without waiting long enough for any of
	// them to finish first. Without this, cancel() fired essentially at
	// t=0 mostly raced dialing itself: most goroutines had not yet
	// reached the server when the listener closed, so the failures
	// observed were "connection refused" on requests that never really
	// started, not the pool-closed-under-an-active-handler bug this test
	// exists to catch. A floor of 10ms keeps this meaningful even if
	// warmupLatency comes back implausibly small.
	settle := warmupLatency / 3
	if settle < 10*time.Millisecond {
		settle = 10 * time.Millisecond
	}
	time.Sleep(settle)
	cancel()
	wg.Wait()

	failures := 0
	for i := 0; i < burst; i++ {
		if errs[i] != nil {
			failures++
			t.Errorf("login %d: transport error instead of completing: %v", i, errs[i])
			continue
		}
		if statuses[i] != http.StatusOK {
			failures++
			t.Errorf("login %d: status = %d, want 200", i, statuses[i])
		}
	}
	if failures > 0 {
		t.Fatalf("%d/%d in-flight logins did not complete cleanly across shutdown", failures, burst)
	}

	select {
	case <-streamDone:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE stream did not terminate within 5s of shutdown")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() returned an error on shutdown: %v", err)
		}
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
		Email:       "prune-target@studio.com",
		DisplayName: "Prune Target",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

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
	if countSessions() != 1 || countInvites() != 1 {
		t.Fatal("test setup did not actually insert the expired rows")
	}

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
//
// internal/web's own tests all build their Server directly, with a
// metamodel service they construct themselves, so every one of them would
// keep passing if the line in main.go that supplies one were deleted —
// newMCPServer would simply register the Core three and say nothing.
// This test connects to the real binary's real endpoint with a real
// token and asks for the tool list.
//
// It also seeds one entity through it, because a tool that appears in the
// list and cannot reach a database is a different failure with the same
// symptom in a list-only assertion: the *pool* the metamodel service
// holds has to be the same live one the rest of the process uses.
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
	if err != nil {
		t.Fatalf("connect to /mcp: %v", err)
	}
	defer func() { _ = mcpSession.Close() }()

	tools, err := mcpSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"types.upsert", "entities.upsert", "entities.list", "search"} {
		if !names[want] {
			t.Fatalf("the running binary serves no %q tool; the metamodel service is not wired into web.NewServer", want)
		}
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
		if err != nil {
			t.Fatalf("CallTool(%s): %v", call.Name, err)
		}
		if result.IsError {
			t.Fatalf("CallTool(%s) failed against the running binary: %+v", call.Name, result.Content)
		}
	}

	found, err := mcpSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "search", Arguments: map[string]any{"query": "hogger"},
	})
	if err != nil {
		t.Fatalf("CallTool(search): %v", err)
	}
	if found.IsError {
		t.Fatalf("search failed against the running binary: %+v", found.Content)
	}
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
	if err != nil {
		t.Fatalf("marshal search result: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode search result %s: %v", raw, err)
	}
	if len(out.Items) != 1 || out.Items[0].Kind != "entity" ||
		out.Items[0].Entity == nil || out.Items[0].Entity.Key != "hogger" {
		t.Fatalf("search found %+v, want the entity just seeded through the same binary", out.Items)
	}
}

// bearerTransport authenticates every request with an API token.
type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(req)
}
