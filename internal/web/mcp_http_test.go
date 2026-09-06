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

// TestMCPEndToEndOverHTTP exercises the actual mounted transport, not
// just the plain functions the other tests in this package call
// directly: a real token authenticates over HTTP, whoami's successful
// structured output round-trips correctly, and games.get on a foreign
// project comes back as a structured, machine-readable scope_violation
// error — with no fabricated structured payload riding along with it.
//
// That last assertion is the one a quality review found this test
// originally missed: it only ever read Content[0], never
// StructuredContent, so it could not have caught the SDK marshalling the
// zero-value Out on every error path regardless of IsError (a real
// defect — see this task's plan corrections). addScopedTool registers
// every tool with Out=any and returns a literal nil on every error path
// specifically so StructuredContent stays absent here; this test pins
// that this actually holds through the real transport, not merely in
// the Go source.
func TestMCPEndToEndOverHTTP(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "agent-owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mine, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create mine: %v", err)
	}
	theirs, err := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID)
	if err != nil {
		t.Fatalf("Create theirs: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: mine.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	session := connectMCP(t, httpSrv.URL, token)

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
	if whoami.StructuredContent == nil {
		t.Fatal("a successful whoami call must carry StructuredContent")
	}
	var whoamiOut struct {
		UserID      string `json:"user_id"`
		ProjectID   string `json:"project_id"`
		ProjectSlug string `json:"project_slug"`
	}
	// Read from StructuredContent directly, not Content[0] — this is the
	// field the phantom-payload defect actually populated, and the field
	// a conforming client is told to prefer (see this function's own doc
	// comment); decodeToolText below (used by the error-path assertions)
	// reads Content[0] instead, which every result — success or error —
	// still carries.
	decodeStructured(t, whoami, &whoamiOut)
	if whoamiOut.UserID != user.ID.String() {
		t.Fatalf("whoami user_id = %q, want %q", whoamiOut.UserID, user.ID.String())
	}
	if whoamiOut.ProjectID != mine.ID.String() {
		t.Fatalf("whoami project_id = %q, want %q", whoamiOut.ProjectID, mine.ID.String())
	}
	if whoamiOut.ProjectSlug != "azeroth" {
		t.Fatalf("whoami project_slug = %q, want azeroth", whoamiOut.ProjectSlug)
	}

	foreign, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "games.get",
		Arguments: map[string]any{"game": theirs.Slug},
	})
	if err != nil {
		t.Fatalf("CallTool(games.get): %v", err)
	}
	if !foreign.IsError {
		t.Fatal("games.get on a foreign project must report an error")
	}
	if foreign.StructuredContent != nil {
		t.Fatalf("an error result must carry no StructuredContent at all, got %#v", foreign.StructuredContent)
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

// TestMCPGamesGetAcceptsAMatchingGameConfirmation pins the
// "optional but checked" middle position ScopedArgs implements: an agent
// that states the game it expects, and states it correctly, is not
// refused for stating it.
func TestMCPGamesGetAcceptsAMatchingGameConfirmation(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "confirm-owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mine, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create mine: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: mine.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	session := connectMCP(t, httpSrv.URL, token)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "games.get",
		Arguments: map[string]any{"game": mine.Slug},
	})
	if err != nil {
		t.Fatalf("CallTool(games.get): %v", err)
	}
	if result.IsError {
		t.Fatalf("games.get refused a game matching the token's own binding: %+v", result.Content)
	}
}

// TestMCPInputValidationFailuresAreProseNotACode pins the one documented
// exception to this surface's error vocabulary: the SDK rejects a
// malformed argument against a tool's input schema before any handler in
// this package ever runs, and reports it as plain prose with no "error"
// code — see mcpErrorResult's own doc comment. A comment that claimed
// every error on this surface carried a code would be wrong; this test
// is what makes that claim checked instead of just asserted.
func TestMCPInputValidationFailuresAreProseNotACode(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "malformed-owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	session := connectMCP(t, httpSrv.URL, token)

	// `game`'s schema (inferred from ScopedArgs.Game's Go type) is a
	// string; a number is a schema violation caught before
	// addScopedTool's own wrapper — or any tool handler — ever runs.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "games.get",
		Arguments: map[string]any{"game": 12345},
	})
	if err != nil {
		t.Fatalf("CallTool(games.get): %v", err)
	}
	if !result.IsError {
		t.Fatal("a malformed game must be refused")
	}
	if len(result.Content) == 0 {
		t.Fatal("no content on the validation failure")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", result.Content[0])
	}
	var wireErr map[string]string
	if err := json.Unmarshal([]byte(text.Text), &wireErr); err == nil {
		t.Fatalf("expected prose, not this package's coded error shape, got %q", text.Text)
	}
}

// connectMCP builds an MCP client session against baseURL, authenticated
// with token, with the standalone SSE stream disabled — the server is
// mounted Stateless, which never accepts the client's GET stream (see
// mcpHandler's own doc comment on why Stateless was chosen).
func connectMCP(t *testing.T, baseURL, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0.0.1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:             baseURL + "/mcp",
		HTTPClient:           &http.Client{Transport: bearerRoundTripper{token: token}},
		DisableStandaloneSSE: true,
	}
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// decodeToolText decodes the JSON text of a tool result's first content
// block into v. Every result, success or error, carries this — see
// decodeStructured for the field that is only present on success.
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

// decodeStructured decodes result.StructuredContent into v by
// round-tripping it through JSON — the client only ever hands this back
// as an untyped any, decoded from the wire, so this is the same
// marshal/unmarshal any real client-side consumer would do.
func decodeStructured(t *testing.T, result *mcp.CallToolResult, v any) {
	t.Helper()
	if result.StructuredContent == nil {
		t.Fatal("tool result has no StructuredContent")
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode StructuredContent %s: %v", raw, err)
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
	defer func() { _ = resp.Body.Close() }()

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

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "revoked-owner@studio.com", DisplayName: "Owner", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a revoked token", resp.StatusCode)
	}
}
