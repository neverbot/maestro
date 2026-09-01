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
		"SESSION_KEY":          "0123456789abcdef0123456789abcdef",
		"FIRST_ADMIN_EMAIL":    "admin@studio.com",
		"FIRST_ADMIN_PASSWORD": "password12345",
		"REGISTRATION_MODE":    "invite_only",
	}

	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- run(ctx, testGetenv(env)) }()

	base = "http://" + addr
	waitForHealthz(t, base, 5*time.Second)
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

// createGame creates a game as the given session caller and returns its id.
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
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create game response: %v", err)
	}
	return out.ID
}

// mintToken creates an API token for gameID as the given session caller
// and returns its clear (bearer) value.
func mintToken(t *testing.T, base string, session *http.Cookie, gameID string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"label": "test-agent"})
	if err != nil {
		t.Fatalf("marshal create token body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/games/"+gameID+"/tokens", bytes.NewReader(body))
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
	gameID := createGame(t, base, session, "azeroth", "Azeroth")
	token := mintToken(t, base, session, gameID)

	// Open one long-lived SSE stream ahead of the burst below, exactly the
	// shape web.Server.Close exists to end promptly on shutdown.
	sseReq, err := http.NewRequest(http.MethodGet, base+"/api/games/"+gameID+"/events", nil)
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
	// A single one of these logins measured ~80ms on this machine
	// (argon2 dominates); waiting a fraction of that before cancelling
	// gives every goroutine above time to dial, send its request and
	// have the server start verifying its password — genuinely inside
	// Authenticate, not merely queued to connect — before shutdown
	// begins, without waiting long enough for any of them to finish
	// first. Without this, cancel() fired essentially at t=0 mostly
	// raced dialing itself: most goroutines had not yet reached the
	// server when the listener closed, so the failures observed were
	// "connection refused" on requests that never really started,
	// not the pool-closed-under-an-active-handler bug this test exists
	// to catch.
	time.Sleep(25 * time.Millisecond)
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
