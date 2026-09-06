// Package web serves every HTTP surface of Maestro.
package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/analysis"
	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/views"
)

// Options carries everything the server needs from the outside.
type Options struct {
	Version  string
	Config   config.Config
	Identity *identity.Service
	Projects *projects.Service

	// Metamodel is the game-content domain. Optional: a Server built
	// without one serves every route and every MCP tool except the
	// game-content ones, which newMCPServer then does not register at
	// all — see MCPDeps.Metamodel. It is a field on Options for the same
	// reason Hub is: whoever builds the service has to hand this package
	// the same instance, not a second one over the same pool.
	Metamodel *metamodel.Service

	// Markdown is the prose domain. Optional in the same sense as
	// Metamodel and for the same reason it is a field here rather than
	// something NewServer builds: whoever constructs the service has to
	// hand this package the same instance, over the same pool and the
	// same hub. Today only the search tool reads it — see
	// MCPDeps.Markdown.
	Markdown *markdown.Service

	// Views is the saved-view domain. Optional in the same sense as
	// Metamodel and Markdown and for the same reason it is a field here:
	// whoever constructs the service has to hand this package the same
	// instance, over the same pool and the same hub. Today only the
	// background-asset routes (api_view_assets.go), the ten views.* MCP
	// tools (mcp_views.go) and their REST mirror (api_views.go) all read
	// it.
	Views *views.Service

	// Analysis is the analysis domain: the three read-only analyses, the
	// route CRUD and routes.check. Optional in the same sense as
	// Metamodel, Markdown and Views, and a field here for the same
	// reason: whoever constructs the service has to hand this package
	// the same instance, over the same pool and the same hub, or a
	// route.checked event would be published into a hub no SSE stream
	// reads from. The eight analysis.* / routes.* MCP tools
	// (mcp_analysis.go) and their REST mirror (api_analysis.go) read it.
	Analysis *analysis.Service

	// Hub is the realtime fan-out this instance publishes into and the
	// SSE endpoint (events.go) reads from. publish.go's own handlers
	// (Task 20) are this hub's first real publishers — game, membership,
	// token and invite mutations all reach it today — and the metamodel
	// plan's entity.* and relation.* events will too, from services this
	// package does not own, which is why this is a field on Options
	// instead of a value NewServer keeps entirely to itself: whoever
	// builds those services needs the same *realtime.Hub instance the
	// SSE handler is reading from, not a second, disconnected one.
	// Optional: a nil Hub gets a fresh realtime.NewHub(), same as every
	// other build did before this field existed.
	Hub *realtime.Hub

	// SSEMaxLifetime bounds how long GET /api/games/{game}/events keeps
	// one connection open before closing it and forcing the client to
	// reconnect. See events.go's own doc comment for why a bound exists
	// at all. Optional: zero gets defaultSSEMaxLifetime.
	SSEMaxLifetime time.Duration

	// SSEHeartbeatInterval overrides sseHeartbeatInterval (events.go) —
	// how often an open stream both pings and re-validates the caller's
	// access. It exists on Options, not only as a package constant,
	// purely so a test can shrink it far enough to observe a revoked
	// caller's stream actually close without waiting out the real
	// fifteen-second default; nothing else in this codebase has a
	// legitimate reason to run this off its default. Optional: zero (or
	// negative) gets sseHeartbeatInterval.
	SSEHeartbeatInterval time.Duration
}

// Server routes all four surfaces: web UI, REST API, MCP and SSE.
type Server struct {
	mux     *http.ServeMux
	opts    Options
	handler http.Handler

	// mcp serves POST /mcp once mcpHandler has confirmed the caller holds
	// a live api token (see that method's own doc comment). It is built
	// from newMCPServer (mcp.go) with Stateless mode, so every tool call
	// is its own independently authenticated HTTP request rather than a
	// long-lived connection.
	mcp http.Handler

	// loginLimiter, loginIPLimiter and registerIPLimiter guard the two
	// unauthenticated endpoints that accept a secret to verify: a
	// password (POST /api/auth/login) and an invite token or a
	// self-service email (POST /api/auth/register), respectively. They
	// are separate Limiter instances, not one shared by key prefix, so a
	// burst of failed logins can never exhaust the budget registration
	// depends on, or vice versa.
	//
	// loginLimiter and loginIPLimiter are two independent budgets on the
	// same endpoint, deliberately never combined into one composite
	// email+ip key: loginLimiter alone (keyed on the normalized email)
	// caps how hard any one account can be attacked regardless of source,
	// but by itself it also lets someone who merely knows a colleague's
	// address lock that account out indefinitely at no cost to
	// themselves, from anywhere. loginIPLimiter adds a second, looser cap
	// keyed on source IP so a single origin cannot grind through many
	// accounts' budgets one at a time either. Composing them into one
	// "email+ip" key would defeat the per-account protection entirely —
	// an attacker rotating source IPs would get a fresh per-account
	// budget on every hop — so handleLogin checks and records both
	// independently instead.
	//
	// registerIPLimiter guards POST /api/auth/register as a whole — both
	// the invite-redemption branch and the domain_open self-service
	// branch, which share one budget rather than one each (see
	// handleRegister's own comment). It is keyed on source IP only, not
	// paired with a second key the way login is: unlike login, nothing
	// on this endpoint names an existing account an attacker could target
	// for a free lockout by spending someone else's budget — the
	// self-service branch creates a brand new account, and the invite
	// branch's actual credential is the token, never the email (Task 6,
	// Correction 10).
	loginLimiter      *identity.Limiter
	loginIPLimiter    *identity.Limiter
	registerIPLimiter *identity.Limiter

	// changePasswordLimiter and changePasswordFloodLimiter both guard
	// PATCH /api/me/password, keyed on the caller's own user id rather
	// than an IP — unlike every limiter above, this endpoint is only
	// ever reached by an already-authenticated caller, so there is no
	// "attacker picks their own key" concern a second, IP-keyed budget
	// would need to close. They serve different jobs, and — a Round 2
	// review found — must run in a specific order: changePasswordLimiter
	// (10/minute) is consulted only after a guess has already turned out
	// wrong, to decide whether that particular failure reports 401 or
	// 429; it must never gate entry before the password is checked,
	// because a design that does refuses a *correct* password once an
	// attacker with a stolen session (but not the password) has spent
	// the budget with wrong guesses — denying the account owner the one
	// remedy this endpoint exists to provide, with no password reset
	// anywhere in this product to fall back on. changePasswordFloodLimiter
	// (60/minute, deliberately looser) is what still gates entry, to
	// bound the argon2 cost a flood of requests can force regardless of
	// correctness, without being tight enough to plausibly deny a real
	// caller. See handleChangePassword's own doc comment for the full
	// reasoning and the order both limiters run in.
	changePasswordLimiter      *identity.Limiter
	changePasswordFloodLimiter *identity.Limiter

	// registeredPatterns and projectScopedPatterns exist for exactly one
	// reason: TestEveryGameScopedRouteGoesThroughRequireProject
	// (server_test.go). registeredPatterns records every pattern ever
	// passed to route/routeFunc, in registration order;
	// projectScopedPatterns records the subset registered through
	// registerProjectRoute. The test walks the first list and fails if
	// any pattern containing "{game}" is missing from the second — see
	// ProjectScope's own doc comment (api_projects.go) for why this test,
	// not the Go type system, is what actually enforces that every
	// project-scoped route resolves its scope through requireProject.
	registeredPatterns    []string
	projectScopedPatterns map[string]bool

	// contentPatterns and contentWritePatterns record the game-content
	// surface (api_metamodel.go) the way registeredPatterns records the
	// whole routing table, and for the same reason: a test, not
	// discipline, is what enforces the convention.
	// registerContentRoute records every content route here and
	// registerContentRoute's own write branch records the subset it
	// wrapped in requireEditor.
	// TestEveryContentRouteIsRegisteredAsContent walks the first list
	// and fails on a content path registered any other way;
	// TestEveryContentWriteRouteRefusesAViewer drives the second list
	// against a real viewer. See registerContentRoute for why the wrap
	// is decided there rather than in each handler.
	contentPatterns      []string
	contentWritePatterns []string

	// mcpScopedTools records every MCP tool registered through
	// addScopedTool (mcp.go), which is the only registration path that
	// enforces the caller's game binding.
	// TestEveryMCPToolGoesThroughAddScopedTool checks the tool list the
	// server actually serves against this map and fails on anything
	// registered any other way — the MCP counterpart of
	// TestEveryGameScopedRouteGoesThroughRequireProject above.
	mcpScopedTools map[string]bool
	// mcpToolDescriptions is the text each of those tools was registered
	// with. It exists so a test can read what an agent actually reads:
	// asserting that a description-generating function exists says
	// nothing about whether its output reached the wire, and the
	// generated renderer catalogue and operator table are contracts that
	// have to arrive rather than merely be available.
	mcpToolDescriptions map[string]string

	// hub, sseMaxLifetime and sseHeartbeatInterval back the SSE endpoint
	// (events.go). See Options.Hub, Options.SSEMaxLifetime and
	// Options.SSEHeartbeatInterval for what they do and why all three
	// are injectable rather than values this file keeps entirely to
	// itself.
	hub                  *realtime.Hub
	sseMaxLifetime       time.Duration
	sseHeartbeatInterval time.Duration

	// closing and closeOnce back Close, below: the lever a graceful
	// shutdown needs to end every open SSE stream instead of either
	// waiting on them or cutting them off mid-frame. See Close's own doc
	// comment for why this exists on Server rather than being left for
	// Task 16 to invent when it wires up http.Server.Shutdown.
	closing   chan struct{}
	closeOnce sync.Once
}

// NewServer builds the routing tree.
//
// Identity and Projects must both be non-nil: a Server with either unset
// would serve /healthz and every other public route just fine right up
// until the first request carrying a bearer token or a session cookie,
// which panics on a nil *identity.Service inside authenticate — a
// misconfigured process should refuse to start, not crash its first
// authenticated request (or, worse, its first unauthenticated one that
// merely happens to carry a stale cookie — see this task's plan
// corrections for how that was found).
func NewServer(opts Options) *Server {
	if opts.Identity == nil {
		panic("web: NewServer requires a non-nil Identity service")
	}
	if opts.Projects == nil {
		panic("web: NewServer requires a non-nil Projects service")
	}

	hub := opts.Hub
	if hub == nil {
		hub = realtime.NewHub()
	}
	sseMaxLifetime := opts.SSEMaxLifetime
	if sseMaxLifetime <= 0 {
		sseMaxLifetime = defaultSSEMaxLifetime
	}
	sseHeartbeatIntervalOpt := opts.SSEHeartbeatInterval
	if sseHeartbeatIntervalOpt <= 0 {
		sseHeartbeatIntervalOpt = sseHeartbeatInterval
	}

	s := &Server{
		mux:                        http.NewServeMux(),
		opts:                       opts,
		loginLimiter:               identity.NewLimiter(10, time.Minute),
		loginIPLimiter:             identity.NewLimiter(40, time.Minute),
		registerIPLimiter:          identity.NewLimiter(10, time.Minute),
		changePasswordLimiter:      identity.NewLimiter(10, time.Minute),
		changePasswordFloodLimiter: identity.NewLimiter(60, time.Minute),
		hub:                        hub,
		sseMaxLifetime:             sseMaxLifetime,
		sseHeartbeatInterval:       sseHeartbeatIntervalOpt,
		closing:                    make(chan struct{}),
	}
	s.routeFunc("GET /healthz", s.handleHealthz)
	s.route("GET /version", requireCaller(s.handleVersion))
	s.route("GET /api/me", requireCaller(s.handleMe))
	s.routeFunc("POST /api/auth/login", s.handleLogin)
	s.routeFunc("POST /api/auth/logout", s.handleLogout)
	s.routeFunc("POST /api/auth/register", s.handleRegister)
	s.routeFunc("GET /{$}", s.handleRoot)
	s.routeFunc("GET /login", func(w http.ResponseWriter, r *http.Request) { s.serveAsset(w, r, "login.html") })
	// The path variable here is deliberately {slug}, not {game}: the
	// plan's own snippet for this route used {game}, but
	// TestEveryGameScopedRouteGoesThroughRequireProject (server_test.go)
	// fails the build on any registered pattern containing "{game}" that
	// was not wired up through registerProjectRoute — its whole point is
	// to catch a project-scoped handler that bypassed requireProject.
	// This route resolves nothing server-side (Task 8's Round 2
	// Correction 12 — it only ever serves the static SPA shell; the
	// slug-to-id mapping happens client-side from GET /api/games), so it
	// is correctly registered outside that convention, and a differently
	// named path variable keeps the safety net from misreading it as a
	// bypass.
	s.routeFunc("GET /g/{slug}", func(w http.ResponseWriter, r *http.Request) { s.serveAsset(w, r, "game.html") })
	// The reading view, one document of one game. Same shell-only
	// contract as /g/{slug} above and the same {slug} spelling for the
	// same reason: this route resolves nothing server-side, and doc.js
	// discovers from GET /api/games whether the caller can reach the
	// game at all. The document's own path travels in the query string
	// (/g/{slug}/doc?path=…), mirroring the API's own shape — see
	// api_docs.go's header for why a document path never occupies a URL
	// segment.
	s.routeFunc("GET /g/{slug}/doc", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, r, "document.html")
	})
	// The seven remaining shells, from one table (shellRoutes, below).
	//
	// Every one of them resolves nothing server-side, exactly as
	// /g/{slug} and /g/{slug}/doc above do: the page reads the slug and
	// the keys out of the URL the browser already has, and the data
	// client asks for them by slug and by key. That is why they are
	// registered here as plain shells rather than through
	// registerProjectRoute, and why every path variable is spelled
	// {slug}, {key} or {typeKey} and never {game}.
	for _, shell := range shellRoutes {
		if shell.byHand {
			continue
		}
		file := shell.file
		s.routeFunc(shell.pattern, func(w http.ResponseWriter, r *http.Request) {
			s.serveAsset(w, r, file)
		})
	}
	s.route("GET /static/", s.staticFileServer())
	s.routeFunc("GET /api/config", s.handleConfig)
	s.route("GET /api/games", requireCaller(s.handleListGames))
	s.route("POST /api/games", requireCaller(s.handleCreateGame))
	s.route("POST /api/invites", requireCaller(s.handleCreateInstanceInvite))
	s.route("GET /api/invites", requireCaller(s.handleListInstanceInvites))
	s.route("DELETE /api/invites/{invite}", requireCaller(s.handleRevokeInstanceInvite))
	s.route("PATCH /api/me/password", requireCaller(s.handleChangePassword))
	s.route("PATCH /api/admins", requireCaller(s.handleSetAdmin))
	s.registerProjectRoute("GET /api/games/{game}/members", s.handleListMembers)
	s.registerProjectRoute("PATCH /api/games/{game}/members/{user}", s.handleChangeRole)
	s.registerProjectRoute("DELETE /api/games/{game}/members/{user}", s.handleRemoveMember)
	s.registerProjectRoute("POST /api/games/{game}/tokens", s.handleCreateToken)
	s.registerProjectRoute("GET /api/games/{game}/tokens", s.handleListTokens)
	s.registerProjectRoute("DELETE /api/games/{game}/tokens/{token}", s.handleRevokeToken)
	s.registerProjectRoute("GET /api/games/{game}/events", s.handleEvents)
	s.registerProjectRoute("DELETE /api/games/{game}", s.handleDeleteGame)
	s.registerProjectRoute("POST /api/games/{game}/invites", s.handleCreateProjectInvite)
	s.registerProjectRoute("GET /api/games/{game}/invites", s.handleListProjectInvites)
	s.registerProjectRoute("DELETE /api/games/{game}/invites/{invite}", s.handleRevokeProjectInvite)

	// The game-content surface (api_metamodel.go), mirroring the metamodel
	// MCP tools one for one. Every row addressed by key sits behind a
	// fixed by-key segment; see that file's header for why the obvious
	// /types/{key} shape was rejected.
	//
	// **There is no by-id segment left.** Metamodel 14 moved the four
	// removals and the relation listing's endpoint filters onto keys, so
	// the whole content surface now addresses a row exactly one way. An
	// edge has no key of its own, so its two by-address routes are
	// /relations/one with the same five query parameters on both — the
	// address relations.upsert writes it under.
	s.registerContentRoute("GET /api/games/{game}/types", s.handleListTypes)
	s.registerContentRoute("POST /api/games/{game}/types", s.handleUpsertType)
	// A rename is a POST with a body and not a PATCH on the key segment:
	// it names two keys, and only one of them can be in the path. Same
	// shape, and the same argument, as the document move above it in
	// this list.
	s.registerContentRoute("POST /api/games/{game}/types/rename", s.handleRenameType)
	s.registerContentRoute("GET /api/games/{game}/types/by-key/{key}", s.handleGetType)
	s.registerContentRoute("DELETE /api/games/{game}/types/by-key/{key}", s.handleRemoveType)
	s.registerContentRoute("GET /api/games/{game}/relation-types", s.handleListRelationTypes)
	s.registerContentRoute("POST /api/games/{game}/relation-types", s.handleUpsertRelationType)
	s.registerContentRoute("POST /api/games/{game}/relation-types/rename", s.handleRenameRelationType)
	s.registerContentRoute("GET /api/games/{game}/relation-types/by-key/{key}", s.handleGetRelationType)
	s.registerContentRoute("DELETE /api/games/{game}/relation-types/by-key/{key}", s.handleRemoveRelationType)
	s.registerContentRoute("GET /api/games/{game}/entities", s.handleListEntities)
	s.registerContentRoute("POST /api/games/{game}/entities", s.handleUpsertEntities)
	s.registerContentRoute("GET /api/games/{game}/entities/by-key/{type}/{key}", s.handleGetEntity)
	s.registerContentRoute("DELETE /api/games/{game}/entities/by-key/{type}/{key}", s.handleRemoveEntity)
	s.registerContentRoute("POST /api/games/{game}/entities/repair", s.handleRepairEntities)
	s.registerContentRoute("GET /api/games/{game}/relations", s.handleListRelations)
	s.registerContentRoute("GET /api/games/{game}/relations/one", s.handleGetRelation)
	s.registerContentRoute("POST /api/games/{game}/relations", s.handleUpsertRelations)
	s.registerContentRoute("DELETE /api/games/{game}/relations/one", s.handleRemoveRelation)
	s.registerContentRoute("POST /api/games/{game}/relations/repair", s.handleRepairRelations)
	s.registerContentRoute("GET /api/games/{game}/search", s.handleSearch)
	s.registerContentRoute("GET /api/games/{game}/summary", s.handleGameSummary)

	// The prose surface (api_docs.go), mirroring the twelve docs.* MCP
	// tools plus the two things an agent never needs: a rendered reading
	// view and a rendered comparison. A document path travels as a query
	// parameter and never as a URL segment — see api_docs.go's header
	// for why, and for what that buys over the metamodel's by-key/by-id
	// discriminators.
	//
	// Registered unconditionally, like the game-content block above and
	// unlike the MCP tools, which newMCPServer gates on
	// MCPDeps.Markdown. requireProseService's doc comment argues why the
	// difference is deliberate: a gate here would make every one of
	// these routes invisible to TestEveryGameScopedRouteGoesThrough
	// RequireProject and TestEveryContentRouteIsRegisteredAsContent,
	// both of which build their server from stubOptions.
	// The background-asset surface (api_view_assets.go). Registered
	// unconditionally, for the reason api_docs.go's header gives: a
	// registration gated on the service being present is invisible to
	// TestEveryContentRouteIsRegisteredAsContent and
	// TestEveryContentWriteRouteRefusesAViewer, both of which build
	// their server from stubOptions.
	//
	// The GET that serves an image is a content *read*, so a viewer
	// reaches it: looking at a picture is reading the game. The upload
	// and the delete are writes and are gated by registerContentRoute
	// from their own methods.
	s.registerContentRoute("GET /api/games/{game}/view-assets", s.handleListViewAssets)
	s.registerContentRoute("POST /api/games/{game}/view-assets", s.handleUploadViewAsset)
	s.registerContentRoute("GET /api/games/{game}/view-assets/{id}", s.handleServeViewAsset)
	s.registerContentRoute("DELETE /api/games/{game}/view-assets/{id}", s.handleRemoveViewAsset)
	// The saved-view surface (api_views.go), registered unconditionally
	// for the reason the two blocks around it are: a registration gated
	// on the service being present is invisible to
	// TestEveryContentRouteIsRegisteredAsContent and
	// TestEveryContentWriteRouteRefusesAViewer, both of which build their
	// server from stubOptions.
	//
	// The two GETs are reads and a viewer reaches them. Everything else
	// is a POST or a DELETE and is gated by registerContentRoute from its
	// own method — including `run`, which is a read spelled as a POST
	// because a query document does not fit in a URL. api_views.go's
	// header records what that costs and why the alternatives are worse.
	s.registerContentRoute("GET /api/games/{game}/views", s.handleListViews)
	s.registerContentRoute("POST /api/games/{game}/views", s.handleUpsertView)
	s.registerContentRoute("GET /api/games/{game}/views/by-key/{key}", s.handleGetView)
	s.registerContentRoute("DELETE /api/games/{game}/views/by-key/{key}", s.handleRemoveView)
	s.registerContentRoute("GET /api/games/{game}/views/renderers", s.handleListRenderers)
	s.registerContentRoute("POST /api/games/{game}/views/run", s.handleRunView)
	s.registerContentRoute("POST /api/games/{game}/views/validate", s.handleValidateView)
	s.registerContentRoute("POST /api/games/{game}/views/by-key/{key}/positions", s.handleSetViewPositions)
	s.registerContentRoute("POST /api/games/{game}/views/by-key/{key}/positions/clear", s.handleClearViewPositions)
	s.registerContentRoute("POST /api/games/{game}/views/by-key/{key}/background", s.handleSetViewBackground)
	// The analysis surface (api_analysis.go), registered unconditionally
	// for the reason every block around it is: a registration gated on
	// the service being present is invisible to
	// TestEveryContentRouteIsRegisteredAsContent and
	// TestEveryContentWriteRouteRefusesAViewer, both of which build their
	// server from stubOptions.
	//
	// The three analyses are POSTs and are still reads — their arguments
	// are a nested object that does not fit in a query string — so
	// registerContentRoute puts requireEditor in front of them. That is
	// the same cost `views.run` pays and api_analysis.go's header
	// records it as a finding against the pair rather than resolving it
	// for one domain only.
	s.registerContentRoute("POST /api/games/{game}/analysis/cycles", s.handleAnalysisCycles)
	s.registerContentRoute("POST /api/games/{game}/analysis/unreachable", s.handleAnalysisUnreachable)
	s.registerContentRoute("POST /api/games/{game}/analysis/orphans", s.handleAnalysisOrphans)
	s.registerContentRoute("GET /api/games/{game}/routes", s.handleListRoutes)
	s.registerContentRoute("POST /api/games/{game}/routes", s.handleUpsertRoute)
	s.registerContentRoute("GET /api/games/{game}/routes/by-key/{key}", s.handleGetRoute)
	s.registerContentRoute("DELETE /api/games/{game}/routes/by-key/{key}", s.handleRemoveRoute)
	s.registerContentRoute("POST /api/games/{game}/routes/by-key/{key}/check", s.handleCheckRoute)
	s.registerContentRoute("GET /api/games/{game}/docs", s.handleListDocs)
	s.registerContentRoute("POST /api/games/{game}/docs", s.handleWriteDoc)
	s.registerContentRoute("POST /api/games/{game}/docs/batch", s.handleWriteDocs)
	// A move is a POST with a body and not a PATCH on a path segment: a
	// document path never occupies a URL segment (api_docs.go's header),
	// and a move names two of them.
	s.registerContentRoute("POST /api/games/{game}/docs/move", s.handleMoveDoc)
	s.registerContentRoute("GET /api/games/{game}/docs/kinds", s.handleDocKinds)
	s.registerContentRoute("GET /api/games/{game}/docs/one", s.handleReadDoc)
	s.registerContentRoute("DELETE /api/games/{game}/docs/one", s.handleDeleteDoc)
	s.registerContentRoute("GET /api/games/{game}/docs/history", s.handleDocHistory)
	s.registerContentRoute("GET /api/games/{game}/docs/version", s.handleReadDocVersion)
	s.registerContentRoute("POST /api/games/{game}/docs/revert", s.handleRevertDoc)
	s.registerContentRoute("GET /api/games/{game}/docs/diff", s.handleDocDiff)
	s.registerContentRoute("GET /api/games/{game}/docs/links", s.handleListDocLinks)
	s.registerContentRoute("POST /api/games/{game}/docs/links", s.handleAddDocLink)
	s.registerContentRoute("DELETE /api/games/{game}/docs/links", s.handleRemoveDocLink)
	s.registerContentRoute("GET /api/games/{game}/docs/rendered", s.handleRenderDoc)
	s.registerContentRoute("GET /api/games/{game}/docs/comparison", s.handleCompareDoc)

	// The MCP tools (mcp.go) are built once, here, and mounted in
	// Stateless mode: no Mcp-Session-Id bookkeeping, and every tool call
	// is its own independently-authenticated HTTP request rather than a
	// session kept alive across many — see mcpHandler's own doc comment
	// for why that matters for a revoked token. getServer may return the
	// same *mcp.Server for every request (its own doc comment says so),
	// so the tool set is built exactly once rather than reconstructed on
	// every call.
	mcpServer := s.newMCPServer()
	s.mcp = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true})
	s.route("/mcp", s.mcpHandler())

	// Built once here, not per request in ServeHTTP: authenticate wraps
	// s.mux in a closure, and there is no reason to allocate a fresh one
	// for every single incoming request when the mux it wraps never
	// changes after construction.
	s.handler = s.securityHeaders(s.authenticate(s.mux))
	return s
}

// shellRoutes is every HTML shell this server serves, with the route it
// is served at.
//
// **It is a table so that a shell and its route cannot come apart.**
// internal/web/static_pages_test.go enumerates the shells on disk and
// asserts each has an entry here, and then drives a real request at each
// pattern and asserts the bytes that come back are that shell's — so a
// shell added without a route fails in a test rather than 404ing in a
// browser, and a route pointing at the wrong shell fails too.
//
// game.html and document.html are registered by hand above rather than
// from here, because each has more to say than a file name: the game
// page's own comment records why its path variable is {slug} and not
// {game}, and the document page's records why a document path travels in
// the query string. Their entries are here as well, with no handler, so
// the enumeration below covers every shell rather than every shell this
// loop happens to register — a list that covered only what it registered
// would be a list that agrees with itself.
var shellRoutes = []struct {
	pattern string
	file    string
	// byHand says this pattern is registered elsewhere in newServer, with
	// an argument of its own beside it.
	byHand bool
	// dispatches says the route does something before it serves — it may
	// redirect, or serve a different page — so "GET this pattern and
	// compare the bytes" is not a test of it. Exactly one route is like
	// that: handleRoot decides between the picker, a single game and the
	// sign-in page, which is the whole reason it is a handler and not a
	// file.
	dispatches bool
}{
	{pattern: "GET /{$}", file: "index.html", byHand: true, dispatches: true},
	// The picker's own address, and the one route in this product that
	// serves a shell a second route also serves. "/" is a *shortcut* —
	// handleRoot sends a caller with exactly one game straight into it,
	// and the picker's script sends a caller with a remembered game
	// straight back into that one — so "/" cannot double as the way out
	// of a game: it is the way back in. /games serves the same shell
	// with neither shortcut applying (app.js takes the remembered-game
	// redirect only at "/"), so the header's game switcher has somewhere
	// to point that always means "all of my games", and the create-game
	// form has somewhere to live that an account with one game can
	// reach.
	{pattern: "GET /games", file: "index.html"},
	{pattern: "GET /login", file: "login.html", byHand: true},
	{pattern: "GET /g/{slug}", file: "game.html", byHand: true},
	{pattern: "GET /g/{slug}/doc", file: "document.html", byHand: true},
	{pattern: "GET /g/{slug}/views", file: "views.html"},
	{pattern: "GET /g/{slug}/v/{key}", file: "view.html"},
	{pattern: "GET /g/{slug}/types", file: "types.html"},
	{pattern: "GET /g/{slug}/t/{typeKey}", file: "type.html"},
	{pattern: "GET /g/{slug}/e/{typeKey}/{key}", file: "entity.html"},
	{pattern: "GET /g/{slug}/assets", file: "assets.html"},
}

// route registers pattern on the mux and records it in
// s.registeredPatterns — see that field's own doc comment for why.
func (s *Server) route(pattern string, h http.Handler) {
	s.registeredPatterns = append(s.registeredPatterns, pattern)
	s.mux.Handle(pattern, h)
}

// routeFunc is route for a plain handler function, matching
// http.ServeMux.HandleFunc's own shape.
func (s *Server) routeFunc(pattern string, h http.HandlerFunc) {
	s.route(pattern, h)
}

// registerContentRoute registers one route of the game-content surface
// (api_metamodel.go). It is registerProjectRoute plus the one thing a
// route on this surface must never forget: a write goes through
// requireEditor.
//
// That check used to be a line each write handler wrote for itself, and
// a review proved what that costs — the check was stripped from five of
// the eight write handlers and the whole package's tests stayed green,
// because the one test that covered it exercised three routes by hand.
// Deciding it here, from the pattern's own method, makes forgetting
// impossible rather than merely tested: a route registered through this
// function is gated because it is a write, not because whoever wrote the
// handler remembered. The paired convention test
// (TestEveryContentRouteIsRegisteredAsContent) closes the other half —
// registering a content route through registerProjectRoute directly.
//
// GET is the only method read as a read. A pattern that names no method
// panics rather than being guessed at: an unmethoded pattern matches
// every method, so it would silently register writes with no gate.
func (s *Server) registerContentRoute(pattern string, h func(http.ResponseWriter, *http.Request, Caller, ProjectScope)) {
	method, _, ok := strings.Cut(pattern, " ")
	if !ok || method == "" {
		panic("web: a game-content route pattern must name its method: " + pattern)
	}
	s.contentPatterns = append(s.contentPatterns, pattern)
	if method != http.MethodGet {
		s.contentWritePatterns = append(s.contentWritePatterns, pattern)
		write := h
		h = func(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
			if !requireEditor(w, scope) {
				return
			}
			write(w, r, caller, scope)
		}
	}
	s.registerProjectRoute(pattern, h)
}

// registerProjectRoute registers pattern through
// requireCaller(s.requireProject(h)) and records pattern as
// project-scoped, so TestEveryGameScopedRouteGoesThroughRequireProject
// can confirm it. This is the only call in this file that is allowed to
// wire up a route whose pattern contains "{game}" — see ProjectScope's
// own doc comment (api_projects.go) for what that guarantees and, just
// as importantly, what it does not.
func (s *Server) registerProjectRoute(pattern string, h func(http.ResponseWriter, *http.Request, Caller, ProjectScope)) {
	if s.projectScopedPatterns == nil {
		s.projectScopedPatterns = map[string]bool{}
	}
	s.projectScopedPatterns[pattern] = true
	s.route(pattern, requireCaller(s.requireProject(h)))
}

// ServeHTTP runs every request through authentication first. authenticate
// never rejects a request on its own — it only attaches a Caller to the
// context when one can be resolved — so a route not wrapped in
// requireCaller is unaffected by wrapping the whole mux here; only
// handlers wrapped in requireCaller actually enforce anything.
//
// /healthz stays public: liveness is genuinely information-free (it says
// nothing beyond "the process accepted this TCP connection and can
// answer"), and a load balancer's or orchestrator's health probe needs to
// reach it without carrying credentials.
//
// /version does not: unlike /healthz, it hands back the exact commit an
// instance is running, which is fingerprinting material, not a health
// signal. An unauthenticated caller could scan for it and match the
// returned SHA against whatever was patched afterward, at zero cost — the
// repository being public makes that matching easier, not harmless, since
// it hands the attacker a precise diff of what the instance is missing.
// /version is behind requireCaller for that reason, even though nothing
// else about it is sensitive.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// Close signals every open SSE stream (internal/web/events.go's
// handleEvents, the one long-lived handler in this package) to end, and
// returns immediately — it does not wait for them to actually stop.
//
// It exists because http.Server.Shutdown, on its own, is not enough for
// a handler like this one: Shutdown waits for active handlers to return
// but never cancels their request contexts itself, so a stream with no
// bounded lifetime would keep Shutdown waiting indefinitely, and even
// this stream's own bounded sseMaxLifetime could still leave Shutdown
// waiting minutes past every ordinary request having finished — or, if
// whatever wraps Shutdown in a timeout fires first, every open stream
// gets cut off mid-frame instead of a chance to close cleanly. Call
// Close once, before or alongside Shutdown; handleEvents selects on the
// channel this closes and returns promptly once it does, the same way
// it already reacts to r.Context().Done() or its own deadline.
//
// Wired into cmd/maestro/main.go's signal-handling shutdown goroutine
// (Task 16): it calls Close before srv.Shutdown, exactly as this doc
// comment describes, so every open SSE stream gets a chance to close
// cleanly before the ordinary HTTP shutdown starts waiting on it as a
// normal in-flight request. This package still does not glue the rest
// of that sequence together on its own — main.go owns process lifecycle,
// this method only owns the lever.
//
// Safe to call more than once (sync.Once); a second call is a no-op.
func (s *Server) Close() {
	s.closeOnce.Do(func() { close(s.closing) })
}

// securityHeaders sets the three response headers every response in this
// product can carry, and the policy is built from the assets rather than
// written out here: `default-src 'self'` rules out every injected-script
// and injected-stylesheet exfiltration path a future XSS bug could reach
// for, and it also — silently — switches off the one inline script this
// product does ship, the import map. static.go's importMapHashes is the
// exemption, one SHA-256 source per distinct map, computed from the bytes
// that are served; see its doc comment for why a hash and not a nonce or
// 'unsafe-inline', and for what opening a page in a browser found. X-Content-Type-Options: nosniff stops a browser from guessing a
// different content type than the one this package already sets on every
// response it writes (serveAsset, static.go, and writeJSON, this file);
// and Referrer-Policy: no-referrer is the same protection login.html's own
// <meta name="referrer"> gives that one page, applied to every response
// this server writes rather than left to the one page a quality review
// happened to check by hand — a query parameter on any other page (a
// return path, say) deserves the same treatment an invite token got.
//
// frame-ancestors 'none' is a fourth directive on the same policy, added
// by a Task 22 review: default-src governs what a page loads, not
// whether the page itself may be loaded inside another site's frame, so
// every page this server serves — the login form included — was
// embeddable cross-origin until this was added, which is exactly the
// precondition a clickjacking attack over the login form needs. 'none'
// rather than 'self': nothing in this product embeds one of its own
// pages inside another of its own pages either, so there is no
// same-origin framing use to preserve.
//
// Wraps the whole handler chain, outermost, so it applies uniformly
// including to a 404 or a panic recovery, not only to routes that
// happen to reach a specific handler.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request, _ Caller) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.opts.Version})
}

// handleMe reports the authenticated caller's own identity: which user,
// whether they are an instance admin, and — for a token caller only — the
// single project that token is bound to and the token's own id. A session
// caller's project_id and token_id are both absent (ScopedProject and
// TokenID both report "none"); it resolves a project from the URL and its
// own membership on project-scoped routes instead, never from a
// caller-supplied parameter. token_id has no consumer yet — it is exposed
// here for a client that wants to show "you are using token X" or
// self-revoke it later (Task 12's token endpoints); nothing reads it back
// out of a response today.
func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request, caller Caller) {
	payload := map[string]any{
		"user_id":  caller.UserID,
		"is_admin": caller.IsAdmin,
	}
	if projectID, ok := caller.ScopedProject(); ok {
		payload["project_id"] = projectID
	}
	if caller.TokenID != nil {
		payload["token_id"] = *caller.TokenID
	}
	writeJSON(w, http.StatusOK, payload)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		slog.Error("encode json response", "error", err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		// Built from errCodeInternal rather than spelled out, the same
		// way mcpErrorResult's own encode-failure fallback does
		// (mcp_errors.go): auth.go's error-code block claims every call
		// site in this package uses a name from it, and a literal here
		// — the one place a response body is assembled without going
		// through writeError — was the last thing making that claim
		// false.
		_, _ = w.Write(fmt.Appendf(nil, `{"error":%q,"message":"failed to encode response"}`, errCodeInternal))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
