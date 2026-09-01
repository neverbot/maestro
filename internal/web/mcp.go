package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
)

// MCPDeps are the domain services the MCP tools need. Keeping them in one
// struct lets the tool functions be plain, directly testable functions
// that never touch an http.Request or the MCP SDK's own types — the same
// separation ProjectScope buys the REST handlers in api_projects.go.
type MCPDeps struct {
	Identity *identity.Service
	Projects *projects.Service
}

// WhoamiOutput is the shape returned by the whoami tool.
type WhoamiOutput struct {
	UserID      uuid.UUID  `json:"user_id"`
	DisplayName string     `json:"display_name"`
	IsAdmin     bool       `json:"is_admin"`
	ProjectID   *uuid.UUID `json:"project_id,omitempty"`
	ProjectSlug string     `json:"project_slug,omitempty"`
	ProjectName string     `json:"project_name,omitempty"`
}

// GameOutput is the shape returned by the games.get tool and, wrapped in
// GamesListOutput, by games.list.
type GameOutput struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

// GamesListOutput is games.list's result. A named wrapper, not a bare
// slice, so the tool's structured output is a JSON object ({"games":
// [...]}) rather than a bare array — consistent with every REST list
// response in this package (handleListGames, handleListMembers,
// handleListTokens) and easier for a schema-driven client to extend later
// without a breaking shape change.
type GamesListOutput struct {
	Games []GameOutput `json:"games"`
}

// CallerForToken resolves a bearer token to a Caller, for the MCP
// transport (mcpHandler goes through the ordinary authenticate middleware
// instead — see that method's doc comment) and for tests that want a
// Caller without going through HTTP at all.
//
// This mirrors resolveBearerCaller (auth.go) rather than the plan's
// original draft, which called identity.UserByID a second time to learn
// IsAdmin: ResolveAPIToken's own row already carries UserIsAdmin (see
// APITokenSummary's doc comment in tokens.go), so a second round trip
// here would reintroduce exactly the per-request lookup Task 9 removed
// from the hot path. Keeping this in sync with resolveBearerCaller is
// deliberate: a caller resolved from the same token by either function
// must be identical, and there is now exactly one field mapping to keep
// in sync instead of two.
func CallerForToken(ctx context.Context, ids *identity.Service, token string) (Caller, error) {
	summary, err := ids.ResolveAPIToken(ctx, token)
	if err != nil {
		return Caller{}, err
	}
	return newTokenCaller(summary.UserID, summary.UserIsAdmin, summary.ID, summary.ProjectID), nil
}

// ErrScopeViolation is returned whenever an MCP tool call reaches outside
// the caller's own project. It carries errCodeScopeViolation on the wire
// (see mcpErrorFor) — the same code and the same message
// requireProject's own scope check (api_projects.go) uses, so an agent
// reading either surface's error sees one vocabulary.
var ErrScopeViolation = errors.New("scope_violation: this token is bound to another game")

// requireScope checks that caller may act on projectID. Built on
// Caller.ScopedProject, not the raw ProjectID field, so "an instance
// admin is not exempt from a token's binding" is enforced by the same
// method requireProject relies on for the REST surface (see
// ScopedProject's own doc comment in auth.go) — this task exists
// specifically to test that invariant here too, for a token minted by an
// admin, not to re-derive it.
//
// A session caller — ScopedProject reports "no project" for one — never
// satisfies this check: it has no project of its own to compare against,
// and mcpHandler refuses a session caller before any tool handler runs
// anyway (see that method's doc comment), so this case is only reachable
// from MCPGamesGet's and MCPGamesList's own direct-call tests, not from
// the mounted transport.
func requireScope(caller Caller, projectID uuid.UUID) error {
	scoped, ok := caller.ScopedProject()
	if !ok || scoped != projectID {
		return ErrScopeViolation
	}
	return nil
}

// MCPWhoami implements the whoami tool: who the caller is, and the single
// game a token caller is bound to (nil for a session caller, which has
// none of its own).
func MCPWhoami(ctx context.Context, deps MCPDeps, caller Caller) (WhoamiOutput, error) {
	user, err := deps.Identity.UserByID(ctx, caller.UserID)
	if err != nil {
		return WhoamiOutput{}, err
	}
	out := WhoamiOutput{
		UserID:      user.ID,
		DisplayName: user.DisplayName,
		IsAdmin:     user.IsAdmin,
	}
	if projectID, ok := caller.ScopedProject(); ok {
		project, err := deps.Projects.ByID(ctx, projectID)
		if err != nil {
			return WhoamiOutput{}, err
		}
		out.ProjectID = &project.ID
		out.ProjectSlug = project.Slug
		out.ProjectName = project.Name
	}
	return out, nil
}

// MCPGamesGet implements the games.get tool. It refuses any project
// outside the caller's own scope — including one the caller's own user
// owns by a different route, and including one reached by an instance
// admin's token — before ever touching the Projects service; see
// requireScope's own doc comment for why an admin gets no exemption.
func MCPGamesGet(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID) (GameOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return GameOutput{}, err
	}
	project, err := deps.Projects.ByID(ctx, projectID)
	if err != nil {
		return GameOutput{}, err
	}
	return GameOutput{ID: project.ID, Slug: project.Slug, Name: project.Name}, nil
}

// MCPGamesList implements the games.list tool. A token caller sees
// exactly the one game its token is bound to — never zero, never more
// than one, and never a project it merely shares a route with (compare
// handleListGames in api_projects.go, which lists every game a *human*
// caller is a member of; that handler is also closed to token callers
// entirely, by requireHumanCaller, so this is the only "list games" a
// token ever sees). A session caller has no project of its own to list
// and is refused with ErrScopeViolation — unreachable through the
// mounted transport, since mcpHandler already closes /mcp to anything
// but a token caller, but MCPGamesList is tested directly too.
func MCPGamesList(ctx context.Context, deps MCPDeps, caller Caller) ([]GameOutput, error) {
	projectID, ok := caller.ScopedProject()
	if !ok {
		return nil, ErrScopeViolation
	}
	project, err := deps.Projects.ByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return []GameOutput{{ID: project.ID, Slug: project.Slug, Name: project.Name}}, nil
}

// mcpErrorResult builds the wire shape for a failed tool call: a single
// JSON object with a stable, machine-readable "error" code and a human
// "message" — the exact two fields writeError puts in a REST error body
// (auth.go), so an agent parses one error shape regardless of which
// surface answered it. Returning this as CallToolResult.Content with
// IsError set, rather than simply returning a Go error from the tool
// handler, is deliberate: ToolHandlerFor packs a plain error into
// unstructured text (its own doc comment says so), which would hand the
// agent prose with no stable code to switch on — exactly the failure
// mode this task's brief warns about ("how an error's shape reaches the
// agent, given the plan specifies stable machine-readable codes").
func mcpErrorResult(code, message string) *mcp.CallToolResult {
	body, err := json.Marshal(map[string]string{"error": code, "message": message})
	if err != nil {
		// Both arguments are plain strings; json.Marshal on a
		// map[string]string cannot fail. This branch exists only so a
		// future change to this function's inputs cannot ship a silently
		// empty error body.
		body = fmt.Appendf(nil, `{"error":%q,"message":"failed to encode the error"}`, errCodeInternal)
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}
}

// mcpErrorFor maps a domain error from an MCP tool implementation to a
// stable wire code, the same way each REST handler's own switch over
// errors.Is does for writeError. Anything unrecognised becomes
// errCodeInternal and is logged here — an MCP tool handler has no
// http.ResponseWriter-adjacent place to log from the way a REST handler
// does, so this is that path's one logging point, and the agent still
// only ever sees the generic code, never the underlying error text.
func mcpErrorFor(ctx context.Context, err error) *mcp.CallToolResult {
	switch {
	case errors.Is(err, ErrScopeViolation):
		return mcpErrorResult(errCodeScopeViolation, "this token is bound to another game")
	case errors.Is(err, projects.ErrProjectNotFound):
		return mcpErrorResult(errCodeNotFound, "no such game")
	default:
		slog.ErrorContext(ctx, "mcp tool call failed", "error", err)
		return mcpErrorResult(errCodeInternal, "internal error")
	}
}

// mcpUnauthenticated is returned by a tool handler that finds no Caller
// on its context. mcpHandler already refuses any request reaching the
// transport without a token caller (see its own doc comment), so this is
// unreachable in production; it exists only so a tool handler never
// trusts an absent Caller into a nil-pointer panic if that invariant is
// ever loosened.
func mcpUnauthenticated() *mcp.CallToolResult {
	return mcpErrorResult(errCodeUnauthorized, "authentication required")
}

// gamesGetInput is games.get's argument shape: the one field an agent
// supplies, the project id it wants to look up. It is deliberately named
// project_id on the wire, not game_id: every REST and domain type in
// this codebase (Caller.ProjectID, ProjectScope.ProjectID,
// projects.Project.ID) calls this same value a project id, and "game" is
// reserved for the human-facing vocabulary (game slugs in URLs, the
// {game} path parameter) — see ProjectScope's own doc comment in
// api_projects.go. The tool name still reads "games.get" because that is
// the noun an agent reasons about; the identifier it passes is a
// project id like everywhere else this task's own types are used.
//
// ProjectID is a string, not a uuid.UUID, deliberately: uuid.UUID's
// underlying type is [16]byte, and the SDK's schema inference
// (jsonschema-go) reflects the Go type structurally rather than
// consulting its MarshalText/MarshalJSON methods, so a uuid.UUID field
// gets a generated schema of "array of 16 integers" while the value it
// actually marshals to on the wire is a string — a real mismatch this
// task's own end-to-end test caught: AddTool validates a tool's output
// (and input) against its inferred schema before ever handing it to a
// client, so a uuid.UUID-typed field here made every one of these tools
// fail its own schema validation on every call, whoami included. Every
// wire-facing MCP type in this file (this one and gameWire/whoamiWire
// below) uses string ids for exactly this reason; the plain domain
// functions above (MCPWhoami, MCPGamesGet, MCPGamesList) keep
// uuid.UUID, matching the plan and the tests that call them directly —
// only the boundary between the two is where the conversion happens.
type gamesGetInput struct {
	ProjectID string `json:"project_id"`
}

// gameWire is GameOutput's wire shape for the MCP transport — see
// gamesGetInput's own doc comment for why string, not uuid.UUID.
type gameWire struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func gameToWire(g GameOutput) gameWire {
	return gameWire{ID: g.ID.String(), Slug: g.Slug, Name: g.Name}
}

// gamesListWire is GamesListOutput's wire shape.
type gamesListWire struct {
	Games []gameWire `json:"games"`
}

// whoamiWire is WhoamiOutput's wire shape — see gamesGetInput's own doc
// comment for why string, not uuid.UUID. ProjectID stays a *string,
// omitted entirely for a session caller, matching WhoamiOutput's own
// omitempty pointer.
type whoamiWire struct {
	UserID      string  `json:"user_id"`
	DisplayName string  `json:"display_name"`
	IsAdmin     bool    `json:"is_admin"`
	ProjectID   *string `json:"project_id,omitempty"`
	ProjectSlug string  `json:"project_slug,omitempty"`
	ProjectName string  `json:"project_name,omitempty"`
}

func whoamiToWire(w WhoamiOutput) whoamiWire {
	out := whoamiWire{
		UserID:      w.UserID.String(),
		DisplayName: w.DisplayName,
		IsAdmin:     w.IsAdmin,
		ProjectSlug: w.ProjectSlug,
		ProjectName: w.ProjectName,
	}
	if w.ProjectID != nil {
		id := w.ProjectID.String()
		out.ProjectID = &id
	}
	return out
}

// newMCPServer builds the mcp.Server that mcpHandler mounts: the three
// tools this task adds, each a thin wrapper that reads the Caller
// already resolved onto its context (by authenticate, the same
// middleware every other surface in this package goes through) and
// calls the plain, directly-tested MCPWhoami/MCPGamesList/MCPGamesGet
// functions above.
//
// Built once in NewServer, not per request: mcp.NewStreamableHTTPHandler
// accepts a getServer function precisely so the same *mcp.Server instance
// can be returned for every request (its own doc comment says so) — there
// is no per-caller state on this value to isolate between requests, since
// every tool handler reads its Caller from the request context, never
// from anything captured in the closure below.
func (s *Server) newMCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "maestro", Version: s.opts.Version}, nil)
	deps := MCPDeps{Identity: s.opts.Identity, Projects: s.opts.Projects}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "whoami",
		Description: "Report the calling identity: user, admin flag, and the single game this token is bound to.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, whoamiWire, error) {
		caller, ok := CallerFrom(ctx)
		if !ok {
			return mcpUnauthenticated(), whoamiWire{}, nil
		}
		out, err := MCPWhoami(ctx, deps, caller)
		if err != nil {
			return mcpErrorFor(ctx, err), whoamiWire{}, nil
		}
		return nil, whoamiToWire(out), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "games.list",
		Description: "List the games visible to the caller. A token caller always sees exactly the one game it is bound to.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, gamesListWire, error) {
		caller, ok := CallerFrom(ctx)
		if !ok {
			return mcpUnauthenticated(), gamesListWire{}, nil
		}
		games, err := MCPGamesList(ctx, deps, caller)
		if err != nil {
			return mcpErrorFor(ctx, err), gamesListWire{}, nil
		}
		wire := make([]gameWire, len(games))
		for i, g := range games {
			wire[i] = gameToWire(g)
		}
		return nil, gamesListWire{Games: wire}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "games.get",
		Description: "Look up one game by id. Refuses any game outside the caller's own scope, instance admins included.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gamesGetInput) (*mcp.CallToolResult, gameWire, error) {
		caller, ok := CallerFrom(ctx)
		if !ok {
			return mcpUnauthenticated(), gameWire{}, nil
		}
		projectID, err := uuid.Parse(in.ProjectID)
		if err != nil {
			return mcpErrorResult(errCodeBadRequest, "project_id must be a valid uuid"), gameWire{}, nil
		}
		out, err := MCPGamesGet(ctx, deps, caller, projectID)
		if err != nil {
			return mcpErrorFor(ctx, err), gameWire{}, nil
		}
		return nil, gameToWire(out), nil
	})

	return srv
}

// mcpHandler serves the MCP endpoint. Every request must carry a bearer
// token that resolves to a live token caller — not merely "any Caller",
// which would also admit a session cookie. The REST surface is
// deliberately closed to token callers (requireHumanCaller,
// api_projects.go); this is the opposite gate, deliberately closing the
// agent surface to a browser session: a person's session cookie proves
// they are logged in as themselves, not that they are entitled to act as
// an unscoped agent, and every tool this task adds assumes exactly one
// bound project on the caller, which only a token caller ever carries
// (Caller.ScopedProject, auth.go). This check happens before the SDK's
// own transport ever runs, so an unauthenticated or session-authenticated
// request gets our ordinary JSON error body and never reaches the MCP
// framing at all — it cannot discover the protocol version, the tool
// list or anything else about the server's capabilities by probing this
// route without a token.
//
// authenticate resolves the Caller once, at the start of every HTTP
// request, exactly as it does for every other route (see its own doc
// comment); it is not evaluated again for the rest of that request's
// lifetime. That is not a gap specific to this handler: newMCPServer is
// mounted with StreamableHTTPOptions.Stateless, so — unlike Task 14's SSE
// stream, which the plan called out as needing an explicit bounded
// lifetime for exactly this reason — every single tool call an agent
// makes is its own independent POST to /mcp, gated by this same
// mcpHandler and re-authenticated by authenticate from scratch. A token
// revoked between two tool calls blocks the next call immediately; there
// is no long-lived connection here for a revoked token to keep working
// under.
func (s *Server) mcpHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, ok := CallerFrom(r.Context())
		if !ok || !caller.IsToken() {
			w.Header().Set("WWW-Authenticate", `Bearer realm="maestro"`)
			writeError(w, http.StatusUnauthorized, errCodeUnauthorized, "an api token is required on /mcp")
			return
		}
		setNoStoreHeaders(w)
		s.mcp.ServeHTTP(w, r)
	})
}
