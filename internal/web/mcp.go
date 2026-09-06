package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/views"
)

// MCPDeps are the domain services the MCP tools need. Keeping them in one
// struct lets the tool functions be plain, directly testable functions
// that never touch an http.Request or the MCP SDK's own types — the same
// separation ProjectScope buys the REST handlers in api_projects.go.
type MCPDeps struct {
	Identity *identity.Service
	Projects *projects.Service

	// Metamodel is the game-content domain the tools in mcp_metamodel.go
	// serve. Optional in the sense that a Server built without one still
	// starts and still answers whoami and games.*: newMCPServer registers
	// the metamodel tools only when it is present, so a test that needs
	// nothing but the Core tools does not have to build a metamodel
	// service to get one.
	Metamodel *metamodel.Service

	// Markdown is the prose domain the tools in mcp_docs.go serve.
	// Optional in the same sense as Metamodel: a Server built without
	// one still starts and still answers every other tool, because
	// newMCPServer registers the docs tools only when it is present
	// (TestTheDocsToolsAreAbsentWithoutAMarkdownService). `search` reads
	// it too, and answers with entities alone when it is nil.
	// cmd/maestro always builds one.
	Markdown *markdown.Service

	// Views is the saved-view domain the tools in mcp_views.go serve.
	// Optional in the same sense as the two above: a Server built without
	// one still starts and still answers every other tool, because
	// newMCPServer registers the views tools only when it is present
	// (TestTheViewsToolsAreAbsentWithoutAViewsService). cmd/maestro
	// always builds one.
	Views *views.Service
}

// WhoamiOutput is the shape returned by the whoami tool. Its ProjectID
// stays uuid.UUID, not a wire-shadow string: the tool this task registers
// hand-writes its own OutputSchema (whoamiOutputSchema, below) rather
// than letting the SDK infer one from this type by reflection, and the
// SDK validates a tool's output against the *marshalled JSON*, not the
// Go value (issue #447, and confirmed against this SDK directly during
// this task's review — see this task's plan corrections for the
// wire-shadow-type approach this replaced). uuid.UUID already marshals
// to a JSON string; only the SDK's structural reflection ever thought
// otherwise.
type WhoamiOutput struct {
	UserID      uuid.UUID  `json:"user_id"`
	DisplayName string     `json:"display_name"`
	IsAdmin     bool       `json:"is_admin"`
	ProjectID   *uuid.UUID `json:"project_id,omitempty"`
	ProjectSlug string     `json:"project_slug,omitempty"`
	ProjectName string     `json:"project_name,omitempty"`
}

// GameOutput is the shape returned by the games.get tool and, nested in
// GamesListOutput, by games.list. See WhoamiOutput's own doc comment for
// why its ID field stays uuid.UUID.
type GameOutput struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

// GamesListOutput is games.list's result: the item list plus the
// pagination envelope every MCP list tool in this codebase returns —
// established here even though this particular tool can only ever
// return zero or one item (a token caller sees exactly the one game its
// token is bound to; nothing about this tool queries a real, growable
// list). NextCursor and Truncated exist so a future genuine list tool
// (entities.list, search, ...) copies this shape instead of inventing
// its own the first day it needs to bound a real result set — and so
// that tool pushes its limit into the domain query itself rather than
// fetching everything and slicing in Go, the discipline this envelope
// exists to make the default rather than an afterthought.
type GamesListOutput struct {
	Items      []GameOutput `json:"items"`
	NextCursor *string      `json:"next_cursor,omitempty"`
	Truncated  bool         `json:"truncated"`
}

// CallerForToken resolves a bearer token to a Caller. It is exported
// only so this package's own external test package (web_test — mcp_test.go
// and a handful of others) can build a Caller directly, without going
// through a real HTTP request, to unit-test what a given Caller shape is
// and is not allowed to do. No production code path calls this: the MCP
// transport goes through the ordinary authenticate middleware instead
// (mcpHandler's own doc comment explains why), and every other caller of
// a Caller in this package receives one the same way. A quality review
// asked, of this and every other export with no caller outside its own
// tests, to either unexport it behind a test shim or document plainly
// that it is test-only; this file chose the doc route over an internal
// test-only shim because mcp_test.go and its siblings already live in
// web_test, the external test package, and moving them into this
// package's own internal test files purely to reach an unexported
// function was a larger, unrelated churn than the export itself
// justifies — CallerForToken's surface area is small (one function,
// mirroring one already-reviewed piece of production logic below) and
// its risk is nil, since nothing calls it but tests that are themselves
// asserting what a Caller may do, not what constructs one.
//
// This mirrors resolveBearerCaller (auth.go) rather than calling
// identity.UserByID a second time to learn IsAdmin: ResolveAPIToken's own
// row already carries UserIsAdmin (see APITokenSummary's doc comment in
// tokens.go), so a second round trip here would reintroduce exactly the
// per-request lookup Task 9 removed from the hot path. Keeping this in
// sync with resolveBearerCaller is deliberate: a caller resolved from the
// same token by either function must be identical, and there is now
// exactly one field mapping to keep in sync instead of two.
func CallerForToken(ctx context.Context, ids *identity.Service, token string) (Caller, error) {
	summary, err := ids.ResolveAPIToken(ctx, token)
	if err != nil {
		return Caller{}, err
	}
	return newTokenCaller(summary.UserID, summary.UserIsAdmin, summary.ID, summary.ProjectID), nil
}

// ErrScopeViolation is returned whenever an MCP tool call reaches outside
// the caller's own project. It carries errCodeScopeViolation on the wire
// (mcpErrorFor unwraps it as an *MCPError) — the same code and the same
// message requireProject's own scope check (api_projects.go) uses, so an
// agent reading either surface's error sees one vocabulary.
var ErrScopeViolation = NewMCPError(errCodeScopeViolation, "this token is bound to another game")

// requireScope checks that caller may act on projectID. Built on
// Caller.ScopedProject, not the raw ProjectID field, so "an instance
// admin is not exempt from a token's binding" is enforced by the same
// method requireProject relies on for the REST surface (see
// ScopedProject's own doc comment in auth.go) — this task exists
// specifically to test that invariant here too, for a token minted by an
// admin, not to re-derive it.
func requireScope(caller Caller, projectID uuid.UUID) error {
	scoped, ok := caller.ScopedProject()
	if !ok || scoped != projectID {
		return ErrScopeViolation
	}
	return nil
}

// MCPWhoami implements the whoami tool: who the caller is, and the single
// game a token caller is bound to (nil for a session caller, which has
// none of its own — unreachable through the mounted transport, since
// mcpHandler already closes /mcp to anything but a token caller, but
// MCPWhoami is tested directly too).
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
//
// The wire registration (addScopedTool, below) never lets projectID here
// be anything other than the caller's own resolved binding — an agent
// cannot use this function's projectID parameter as a lookup key for an
// arbitrary game the way the plan's original games.get shape would have
// let it. requireScope's own check is kept regardless, as defence in
// depth and because this function is tested directly, without going
// through the wire wrapper, precisely to confirm that invariant here.
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
func MCPGamesList(ctx context.Context, deps MCPDeps, caller Caller) (GamesListOutput, error) {
	projectID, ok := caller.ScopedProject()
	if !ok {
		return GamesListOutput{}, ErrScopeViolation
	}
	project, err := deps.Projects.ByID(ctx, projectID)
	if err != nil {
		return GamesListOutput{}, err
	}
	return GamesListOutput{
		Items:     []GameOutput{{ID: project.ID, Slug: project.Slug, Name: project.Name}},
		Truncated: false,
	}, nil
}

// ScopedArgs is embedded in every scoped tool's input type. `game` is
// optional: omitted, the tool acts on the caller's own bound game exactly
// as if it had not been asked; supplied, it must name that binding or
// the call fails with scope_violation before the tool's own handler ever
// runs — it is never used to select which project to query. This gives
// an agent juggling two tokens for two games a way to state which one it
// meant to use and be told immediately when it guessed wrong (Nottario
// requires an equivalent field for the same reason), without turning a
// caller-supplied name into a lookup key the way this task's first draft
// of games.get did — see addScopedTool's own doc comment for why that
// shape was the actual risk a quality review flagged, not merely a
// missing check.
type ScopedArgs struct {
	// Game is the game's slug, as a confirmation and never as a
	// selector: the caller's game comes from its token binding, and this
	// says which game the caller *believes* it is working in.
	//
	// **It was `project_id` and took a uuid.** It changed with the REST
	// routes, in the same change and for the same reason: a game is
	// addressed by its slug now, and leaving one field on one surface
	// still demanding a uuid would have made this the only place in the
	// product where a caller had to learn one — the rule established and
	// not carried one step. The uuid is still in every answer; it is
	// simply not what a caller types.
	Game *string `json:"game,omitempty"`
}

func (a ScopedArgs) requestedGame() *string { return a.Game }

// statedGameProblem is the one place either surface decides what a
// stated `game` means. It answers with the wire code a refusal carries,
// or "" when there is nothing to refuse: absent is silence, an empty
// string names no game at all and is therefore the caller's own
// malformed argument, and any other spelling that is not the game the
// caller is working in is a scope violation.
//
// It is a shared function rather than two matching switches because the
// REST mirror claims to enforce "the rule ScopedArgs states", and a
// review found it enforcing a different one: it reported an unparseable
// id as scope_violation ("names a different game than the URL", which is
// false — it names none), and accepted an empty string outright. Each
// surface still writes its own message: "this token is bound to another
// game" is true of a token and meaningless to a designer with a session
// cookie. The decision is shared; the wording is local.
//
// **The comparison folds case**, matching projects_slug_key and matching
// the path segment on the REST routes: a caller that confirmed "Azeroth"
// while working in "azeroth" has confirmed the right game, and refusing
// it would make this field harder to satisfy than the address beside it.
//
// Where the uuid version of this had three shapes — absent, unparseable,
// wrong — a slug has two, because there is no spelling of a slug that is
// malformed rather than simply wrong. The empty string is kept as its
// own refusal for the reason the review that added it gave: silently
// accepting `""` is how the field stops being a confirmation at all.
func statedGameProblem(stated *string, boundSlug string) string {
	if stated == nil {
		return ""
	}
	if *stated == "" {
		return errCodeBadRequest
	}
	if !strings.EqualFold(*stated, boundSlug) {
		return errCodeScopeViolation
	}
	return ""
}

// scopedInput is what addScopedTool requires of its In type parameter:
// the ability to report the optional `game` confirmation ScopedArgs
// carries. Embedding ScopedArgs satisfies this automatically, by Go's
// usual method promotion.
type scopedInput interface {
	requestedGame() *string
}

type whoamiInput struct{ ScopedArgs }
type gamesListInput struct{ ScopedArgs }
type gamesGetInput struct{ ScopedArgs }

// addScopedTool registers a tool whose handler needs the caller's own
// resolved project id, not a raw Caller: it resolves the caller once,
// enforces game isolation once (both the token's own binding and, if the
// agent supplied one, that its stated `game` agrees with it), maps a
// domain error once, and suppresses structured output on every error
// path once (see mcpErrorResult's own doc comment, and this task's plan
// corrections, for why a fabricated success payload riding along with an
// error result was a real defect a quality review caught here, not a
// hypothetical one). This is the same "one place, not nine copies"
// reasoning ProjectScope brings to the REST surface
// (api_projects.go) — a metamodel tool that reaches for an entity or
// relation by id registers through this and receives an
// already-resolved, already-checked project id, never a bare Caller it
// could forget to scope-check itself.
//
// Every tool registered here is recorded in s.mcpScopedTools, which is
// what TestEveryMCPToolGoesThroughAddScopedTool checks the *served* tool
// list against: it connects a real client, calls ListTools, and fails if
// the server exposes a tool this function never saw. That is the MCP
// half of TestEveryGameScopedRouteGoesThroughRequireProject (server_test.go)
// and it exists for the same reason — the Go type system cannot stop
// someone calling mcp.AddTool directly, and a tool registered that way
// would answer with no scope check at all.
//
// handler's signature deliberately does not take a Caller: if it needs
// more than the resolved project id (whoami needs the caller's own user
// id and admin flag, for instance), it reads CallerFrom(ctx) itself —
// ctx still carries it, since addScopedTool already required a valid
// token caller to reach this point — but the *scope* decision is never
// its own to make; that already happened before handler was called.
//
// On success, handler's Out value is returned as-is; addScopedTool
// registers the tool with the SDK's Out type parameter fixed to `any`
// and passes the caller-visible output schema and structured value
// through by hand (see newMCPServer's tool definitions, which set
// mcp.Tool.OutputSchema explicitly) rather than letting AddTool infer
// one from Out — the mechanism that lets a hand-written schema validate
// correctly regardless of what Go type the domain function actually
// returns.
func addScopedTool[In scopedInput, Out any](s *Server, srv *mcp.Server, deps MCPDeps, tool *mcp.Tool, handler func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in In) (Out, error)) {
	if s.mcpScopedTools == nil {
		s.mcpScopedTools = map[string]bool{}
		s.mcpToolDescriptions = map[string]string{}
	}
	s.mcpScopedTools[tool.Name] = true
	// The description is recorded beside the name for the same reason the
	// name is recorded at all: it is the half of a tool an agent actually
	// reads, and the only way a test can assert that a generated table
	// reached the wire — rather than that a function which generates one
	// exists — is to read what was registered.
	s.mcpToolDescriptions[tool.Name] = tool.Description
	mcp.AddTool(srv, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		caller, ok := CallerFrom(ctx)
		if !ok {
			return mcpUnauthenticated(), nil, nil
		}
		projectID, ok := caller.ScopedProject()
		if !ok {
			return mcpErrorResult(errCodeScopeViolation, "this caller has no game binding", nil), nil, nil
		}
		// The bound game's slug is read only when the caller stated one,
		// so the confirmation field costs a query exactly when it is
		// used and nothing at all otherwise. The REST mirror pays
		// nothing either way: requireProject resolved the game *by* its
		// slug, so ProjectScope already carries it.
		if stated := in.requestedGame(); stated != nil {
			bound, err := deps.Projects.ByID(ctx, projectID)
			if err != nil {
				return mcpErrorFor(ctx, tool.Name, caller, err), nil, nil
			}
			switch statedGameProblem(stated, bound.Slug) {
			case errCodeBadRequest:
				return mcpErrorResult(errCodeBadRequest,
					"game must name a game: pass this game's slug, or leave it out", nil), nil, nil
			case errCodeScopeViolation:
				return mcpErrorResult(errCodeScopeViolation, fmt.Sprintf(
					"this token is bound to the game %q, and this request names %q",
					bound.Slug, *stated), nil), nil, nil
			}
		}
		out, err := handler(ctx, deps, projectID, in)
		if err != nil {
			return mcpErrorFor(ctx, tool.Name, caller, err), nil, nil
		}
		return nil, out, nil
	})
}

// readOnlyTool marks annotations every tool in this task carries: none
// of whoami, games.list or games.get change anything, and calling any of
// them again with the same arguments has no additional effect. Setting
// this now, while there are three tools and the habit is free, is
// cheaper than retrofitting it once a write tool exists and the
// distinction actually matters to a client deciding whether a call is
// safe to retry.
func readOnlyTool() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
}

// newMCPServer builds the mcp.Server that mcpHandler mounts: the three
// tools this task adds, each registered through addScopedTool so game
// isolation is enforced in exactly one place regardless of how many
// tools this file eventually holds.
//
// Built once in NewServer, not per request: mcp.NewStreamableHTTPHandler
// accepts a getServer function precisely so the same *mcp.Server instance
// can be returned for every request (its own doc comment says so) — there
// is no per-caller state on this value to isolate between requests, since
// every tool handler reads its Caller from the request context, never
// from anything captured in the closure below.
func (s *Server) newMCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "maestro", Version: s.opts.Version}, nil)
	deps := s.deps()

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "whoami",
		Description: "Report the calling identity: user, admin flag, and the single game this " +
			"token is bound to. **project_slug is that game's address** — it is what the " +
			"REST routes take in /api/games/{game}/…, what /g/{game} shows a designer, and " +
			"the value the optional `game` argument on every other tool is checked against. " +
			"project_id is returned too and is not an address: nothing on either surface " +
			"takes it.",
		OutputSchema: whoamiOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, _ uuid.UUID, _ whoamiInput) (WhoamiOutput, error) {
		caller, _ := CallerFrom(ctx) // guaranteed present: addScopedTool already required it.
		return MCPWhoami(ctx, deps, caller)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name:         "games.list",
		Description:  "List the games visible to the caller. A token caller always sees exactly the one game it is bound to.",
		OutputSchema: gamesListOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, _ uuid.UUID, _ gamesListInput) (GamesListOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPGamesList(ctx, deps, caller)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "games.get",
		Description: "Look up the caller's own game: its slug, its name and its id. Refuses " +
			"any game outside the caller's own scope, instance admins included. The slug " +
			"is the address — see whoami.",
		OutputSchema: gameOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, _ gamesGetInput) (GameOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPGamesGet(ctx, deps, caller, projectID)
	})

	// The game-content tools, registered only when this instance was
	// built with a metamodel service — see MCPDeps.Metamodel.
	if deps.Metamodel != nil {
		s.addMetamodelTools(srv, deps)
	}

	// The prose tools, on the same terms — see MCPDeps.Markdown.
	if deps.Markdown != nil {
		s.addDocsTools(srv, deps)
	}

	// The saved-view tools, on the same terms — see MCPDeps.Views.
	if deps.Views != nil {
		s.addViewsTools(srv, deps)
	}

	return srv
}

// mcpHandler serves the MCP endpoint. Every request must carry a bearer
// token that resolves to a live token caller — not merely "any Caller",
// which would also admit a session cookie. The REST routes that decide
// who holds standing in the product — creating and listing games,
// membership, tokens, invites — are deliberately closed to token callers
// (requireHumanCaller, api_projects.go); Task 8's game-*content* routes
// (api_metamodel.go) are not, because a token is exactly a credential
// for one game's content and refusing it there would deny over REST what
// the same token already does over MCP. This is the opposite gate,
// deliberately closing the agent surface to a browser session: a person's session cookie proves
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
//
// maxMCPRequestBodyBytes bounds the body this handler will let the SDK's
// own transport read, the same decodeJSONBody discipline api_auth.go's
// doc comment describes applied explicitly here rather than left to the
// SDK's own internal default (StreamableHTTPOptions.MaxRequestBodyBytes,
// currently 4MiB) to define on our behalf. It is deliberately far larger
// than maxAuthRequestBodyBytes: a bulk upsert (the metamodel plan's
// atomic/partial writes over many entities at once) needs real headroom,
// not the 16KiB a login form needs — but it is still a fixed, named
// bound an operator can find and change in one place, not an implicit
// library default.
const maxMCPRequestBodyBytes = 4 << 20 // 4 MiB

func (s *Server) mcpHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, ok := CallerFrom(r.Context())
		if !ok || !caller.IsToken() {
			w.Header().Set("WWW-Authenticate", `Bearer realm="maestro"`)
			writeError(w, http.StatusUnauthorized, errCodeUnauthorized, "an api token is required on /mcp")
			return
		}
		setNoStoreHeaders(w)
		r.Body = http.MaxBytesReader(w, r.Body, maxMCPRequestBodyBytes)
		s.mcp.ServeHTTP(w, r)
	})
}

// --- Hand-written output schemas ---
//
// Every schema below is written by hand rather than inferred by the SDK
// from the Go output type. This is not a style preference: the SDK
// validates against the *marshalled JSON* of a tool's output (see
// WhoamiOutput's own doc comment), and its own reflection-based inference
// gets that JSON shape wrong for any type whose marshalling comes from a
// method (uuid.UUID's MarshalText, here) rather than its literal Go
// structure — a mismatch that surfaces only when the tool is actually
// called, never at registration, and that this task's own end-to-end
// test caught the hard way (see this task's plan corrections). Writing
// the schema by hand sidesteps the inference entirely; it is validated
// against the JSON either way, so a hand-written "string" for a
// uuid.UUID field is exactly as correct as the JSON it produces.

func stringSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "string"} }
func boolSchema() *jsonschema.Schema   { return &jsonschema.Schema{Type: "boolean"} }

var gameOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"id", "slug", "name"},
	Properties: map[string]*jsonschema.Schema{
		"id":   stringSchema(),
		"slug": stringSchema(),
		"name": stringSchema(),
	},
}

var gamesListOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"items", "truncated"},
	Properties: map[string]*jsonschema.Schema{
		"items":       {Type: "array", Items: gameOutputSchema},
		"next_cursor": stringSchema(),
		"truncated":   boolSchema(),
	},
}

var whoamiOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"user_id", "display_name", "is_admin"},
	Properties: map[string]*jsonschema.Schema{
		"user_id":      stringSchema(),
		"display_name": stringSchema(),
		"is_admin":     boolSchema(),
		"project_id":   stringSchema(),
		"project_slug": stringSchema(),
		"project_name": stringSchema(),
	},
}
