package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
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

// TestRunServesMigratesBootstrapsAndShutsDownCleanly drives run() as a
// real caller would: against a real TCP listener and a real (throwaway)
// Postgres database, not by calling web.NewServer's handler in-process.
// It exercises, end to end, everything this task's main() wiring is
// responsible for and nothing else in this codebase already covers at
// this level: migrations actually running before the server accepts a
// request, BootstrapFirstAdmin producing a login-able admin, and — the
// property Task 14's web.Server.Close exists for — a cancelled context
// making run() return promptly instead of hanging on an open connection.
func TestRunServesMigratesBootstrapsAndShutsDownCleanly(t *testing.T) {
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
	done := make(chan error, 1)
	go func() { done <- run(ctx, testGetenv(env)) }()

	base := "http://" + addr
	waitForHealthz(t, base, 5*time.Second)

	// The bootstrapped admin can actually log in — this is the property
	// that makes "a human logs in through the browser with a local
	// account" (the Core sub-project's own definition of done) true
	// against a running process, not only against identity's own unit
	// tests.
	loginBody, err := json.Marshal(map[string]string{
		"email":    "admin@studio.com",
		"password": "password12345",
	})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	resp, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("POST /api/auth/login: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("login response set no session cookie")
	}

	// Cancel exactly the way main() does on SIGINT/SIGTERM, and confirm
	// run() actually returns instead of blocking on Shutdown forever —
	// this is the regression Task 16's own plan section warns about:
	// http.Server.Shutdown alone waits on active handlers indefinitely,
	// and this test would hang (and eventually be killed by `go test`'s
	// own timeout) if webServer.Close() were not wired in ahead of it.
	cancel()

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
