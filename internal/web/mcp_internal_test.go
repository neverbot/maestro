package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// TestRequireScopeRefusesASessionCaller pins that a session caller —
// which carries no project of its own (newSessionCaller, auth.go) — can
// never satisfy requireScope for any project id, not even the zero
// value. MCPGamesGet and MCPGamesList both build on this.
func TestRequireScopeRefusesASessionCaller(t *testing.T) {
	caller := newSessionCaller(uuid.New(), false)
	if err := requireScope(caller, uuid.New()); err == nil {
		t.Fatal("requireScope must refuse a session caller, which has no project of its own")
	}
}

// TestRequireScopeIgnoresAdmin pins the invariant this task exists to
// protect at the unit level, without a database: an admin's token is
// still refused for any project other than the one it is bound to.
func TestRequireScopeIgnoresAdmin(t *testing.T) {
	mine, theirs := uuid.New(), uuid.New()
	caller := newTokenCaller(uuid.New(), true, uuid.New(), mine)

	if err := requireScope(caller, theirs); err == nil {
		t.Fatal("an admin's token must not be exempt from its own project scope")
	}
	if err := requireScope(caller, mine); err != nil {
		t.Fatalf("requireScope refused the caller's own project: %v", err)
	}
}

// TestMCPHandlerRefusesAnUnauthenticatedRequest pins that mcpHandler
// rejects a request with no Caller on its context at all, before the MCP
// transport ever runs — the transport's own SDK is never reached, so an
// anonymous prober learns nothing about the protocol version or the tool
// list by hitting this route with no credential.
func TestMCPHandlerRefusesAnUnauthenticatedRequest(t *testing.T) {
	srv := NewServer(stubOptions("test"))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rec := httptest.NewRecorder()

	srv.mcpHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestMCPHandlerRefusesASessionCaller pins that the MCP surface is
// closed to a browser session even when authenticate has resolved one
// onto the request: mcpHandler requires a token caller specifically
// (caller.IsToken()), not merely "any Caller" — the mirror image of
// requireHumanCaller closing the REST surface to token callers
// (api_projects.go). The Caller is injected directly onto the request
// context here, bypassing authenticate entirely, so this test does not
// need a database to exercise the gate.
func TestMCPHandlerRefusesASessionCaller(t *testing.T) {
	srv := NewServer(stubOptions("test"))
	caller := newSessionCaller(uuid.New(), false)
	ctx := context.WithValue(context.Background(), callerKey{}, caller)
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	srv.mcpHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a session caller on /mcp", rec.Code)
	}
}
