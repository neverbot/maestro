package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
)

// TestEveryMCPToolGoesThroughAddScopedTool is the MCP counterpart of
// TestEveryGameScopedRouteGoesThroughRequireProject (server_test.go), and
// it exists for the same reason: nothing in the Go type system stops
// someone registering a tool with mcp.AddTool directly, and a tool
// registered that way would answer with no game-scope check at all — the
// caller's token binding would never be consulted, and one game's agent
// would be reading another game's content.
//
// It compares the tool list the server *actually serves*, read back over
// the real transport by a real client, against the set addScopedTool
// recorded. A tool that reaches the first list without appearing in the
// second is exactly the bypass this test exists to catch, and it is a
// bypass no amount of reading mcp.go can rule out.
//
// It runs against a server built with a metamodel service, because that
// is the build with the most tools on it; without one, newMCPServer
// registers only the Core three (MCPDeps.Metamodel).
func TestEveryMCPToolGoesThroughAddScopedTool(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := config.Config{
		SessionTTL: 24 * time.Hour,
		InviteTTL:  24 * time.Hour,
		Argon2:     config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
	}
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	srv := NewServer(Options{
		Version:   "test",
		Config:    cfg,
		Identity:  ids,
		Projects:  projSvc,
		Metamodel: metamodel.New(pool, nil),
		// And a markdown service, because this is the build with the
		// most tools on it and the docs.* eleven must be inside the
		// comparison: a docs tool registered with mcp.AddTool directly
		// would otherwise never be seen here.
		Markdown: markdown.New(pool, nil),
	})

	ctx := context.Background()
	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "convention@studio.com", DisplayName: "Designer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: project.ID, UserID: user.ID, Label: "agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "convention-test", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpSrv.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: tokenRoundTripper{token: token}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("the server served no tools at all; this test would pass vacuously")
	}
	for _, tool := range tools.Tools {
		if !srv.mcpScopedTools[tool.Name] {
			t.Fatalf("tool %q is served but was not registered through addScopedTool, "+
				"so nothing checks the caller's game binding before it runs", tool.Name)
		}
	}
	// And the recorded set is not larger than what is served: a name in
	// the map that no client can see would mean the map had drifted into
	// a wish list rather than a record.
	if len(srv.mcpScopedTools) != len(tools.Tools) {
		t.Fatalf("addScopedTool recorded %d tools but %d are served",
			len(srv.mcpScopedTools), len(tools.Tools))
	}
}

// tokenRoundTripper authenticates every request with a bearer token, the
// way a real agent's transport does. web_test has its own copy for its
// own tests; this one is here because an internal test cannot see it.
type tokenRoundTripper struct{ token string }

func (rt tokenRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return http.DefaultTransport.RoundTrip(req)
}
