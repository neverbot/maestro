package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/identity"
)

// bearerRoundTripper adds an Authorization: Bearer header to every request
// it makes, so the MCP client transport authenticates the same way any
// real agent would.
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// TestMCPEndToEndOverHTTP exercises the actual mounted transport (Step 7
// of the plan), not just the plain functions the other tests in this
// package call directly: a real token authenticates over HTTP, whoami
// reports the bound game, and games.get on a foreign project comes back
// as a structured, machine-readable scope_violation error rather than a
// generic protocol failure — the two things the plan's own tests never
// exercise because they call MCPWhoami/MCPGamesGet in-process.
func TestMCPEndToEndOverHTTP(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "agent-owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	mine, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	theirs, _ := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID)
	token, _, _ := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: mine.ID, UserID: user.ID, Label: "agent"})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0.0.1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:             httpSrv.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: bearerRoundTripper{token: token}},
		DisableStandaloneSSE: true, // the server is mounted Stateless, which never accepts the client's GET stream.
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"whoami", "games.list", "games.get"} {
		if !names[want] {
			t.Fatalf("tool list %v is missing %q", names, want)
		}
	}

	whoami, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatalf("CallTool(whoami): %v", err)
	}
	if whoami.IsError {
		t.Fatalf("whoami reported an error: %+v", whoami.Content)
	}
	var whoamiOut struct {
		ProjectID   string `json:"project_id"`
		ProjectSlug string `json:"project_slug"`
	}
	decodeToolText(t, whoami, &whoamiOut)
	if whoamiOut.ProjectSlug != "azeroth" {
		t.Fatalf("whoami project_slug = %q, want azeroth", whoamiOut.ProjectSlug)
	}

	foreign, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "games.get",
		Arguments: map[string]any{"project_id": theirs.ID.String()},
	})
	if err != nil {
		t.Fatalf("CallTool(games.get): %v", err)
	}
	if !foreign.IsError {
		t.Fatal("games.get on a foreign project must report an error")
	}
	var wireErr struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	decodeToolText(t, foreign, &wireErr)
	if wireErr.Error != "scope_violation" {
		t.Fatalf("error code = %q, want scope_violation", wireErr.Error)
	}
}

// decodeToolText decodes the JSON text of a tool result's first content
// block into v. Both a successful ToolHandlerFor result (auto-populated
// from the typed Out value) and mcpErrorResult's own hand-built error
// body take this shape, so one helper covers both.
func decodeToolText(t *testing.T, result *mcp.CallToolResult, v any) {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", result.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), v); err != nil {
		t.Fatalf("decode tool content %q: %v", text.Text, err)
	}
}

// TestMCPRejectsAnUnauthenticatedRequestOverHTTP pins that a caller with
// no credential at all gets the ordinary JSON error body this package
// uses everywhere, not an MCP protocol-level response — confirming the
// gate mcpHandler's doc comment describes actually runs before the SDK's
// own transport, at the real routed address, not merely in the
// in-process unit test that calls mcpHandler() directly.
func TestMCPRejectsAnUnauthenticatedRequestOverHTTP(t *testing.T) {
	srv, _, _ := newTestServer(t)
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()

	resp, err := http.Post(httpSrv.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "unauthorized" {
		t.Fatalf("error = %q, want unauthorized", body["error"])
	}
}

// TestMCPRejectsARevokedTokenOverHTTP pins the distinction the plan's
// brief specifically calls out: what an agent sees for a revoked token
// (401, same as no credential at all) versus a token for another game
// (a structured scope_violation from the tool itself, see
// TestMCPEndToEndOverHTTP) — a revoked token never even reaches a tool
// handler.
func TestMCPRejectsARevokedTokenOverHTTP(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "revoked-owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	token, summary, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: project.ID, TokenID: summary.ID}); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, httpSrv.URL+"/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a revoked token", resp.StatusCode)
	}
}
