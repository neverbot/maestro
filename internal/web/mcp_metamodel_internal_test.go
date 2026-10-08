package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/comments"
	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/views"
)

// TestEveryMCPToolGoesThroughAddScopedTool is the MCP counterpart of
// TestEveryGameScopedRouteGoesThroughRequireProject (server_test.go), and
// it exists for the same reason: nothing in the Go type system stops
// someone registering a tool with mcp.AddTool directly, and a tool
// registered that way would answer with no game-scope check at all — the
// caller's token binding would never be consulted, and one game's agent
// would be reading another game's content.
func TestEveryMCPToolGoesThroughAddScopedTool(t *testing.T) {
	t.Parallel()
	srv, session := everyToolOverTheWire(t)
	ctx := context.Background()

	tools, err := session.ListTools(ctx, nil)
	assert.Must(t, err == nil, "ListTools: %v", err)
	assert.Must(t, len(tools.Tools) != 0, "the server served no tools at all; this test would pass vacuously")
	for _, tool := range tools.Tools {
		assert.Must(t, srv.mcpScopedTools[tool.Name], "tool %q is served but was not registered through addScopedTool, "+
			"so nothing checks the caller's game binding before it runs", tool.Name)
	}
	// And the recorded set is not larger than what is served: a name in
	// the map that no client can see would mean the map had drifted into
	// a wish list rather than a record.
	assert.Must(t, len(srv.mcpScopedTools) == len(tools.Tools), "addScopedTool recorded %d tools but %d are served",
		len(srv.mcpScopedTools), len(tools.Tools))
}

// everyToolOverTheWire stands up the build carrying every tool this
// product has and connects an agent's own client to it. It is a helper
// rather than a block inside one test because the second guard that
// needs it would otherwise have copied this Options literal, and a
// second copy is how a domain goes missing from one of them.
func everyToolOverTheWire(t *testing.T) (*Server, *mcp.ClientSession) {
	t.Helper()
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
		// most tools on it and the docs.* twelve must be inside the
		// comparison: a docs tool registered with mcp.AddTool directly
		// would otherwise never be seen here.
		Markdown: markdown.New(pool, nil),
		// And a views service, for the same reason and one domain along.
		// **This is the shape of the defect this test exists to catch,
		// arriving in the test itself**: the comment above says "the
		// build with the most tools on it", and a domain added later
		// makes that false in silence — the ten views.* tools would have
		// been outside the comparison entirely, and one of them
		// registered with mcp.AddTool directly would have been invisible
		// here while every sentence in this file claimed otherwise.
		// Every new domain service belongs in this Options literal in the
		// commit that adds it.
		Views: views.New(pool, nil),
		// **And the comment above went false the day after it was
		// written.** The comments domain shipped with three tools and
		// did not reach this literal, so comments.* was outside every
		// comparison built on "the build with the most tools on it" —
		// which is how comments.remove reached the wire advertising an
		// argument nothing could send.
		Comments: comments.New(pool, metamodel.New(pool, nil)),
	})

	ctx := context.Background()
	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "convention@example.test", DisplayName: "Designer", Password: "password12345",
	})
	assert.Must(t, err == nil, "CreateUser: %v", err)
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: project.ID, UserID: user.ID, Label: "agent",
	})
	assert.Must(t, err == nil, "CreateAPIToken: %v", err)

	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "convention-test", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpSrv.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: tokenRoundTripper{token: token}},
		DisableStandaloneSSE: true,
	}, nil)
	assert.Must(t, err == nil, "Connect: %v", err)
	t.Cleanup(func() { _ = session.Close() })
	return srv, session
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
