package web

import (
	"net/http"
)

// The analysis surface over REST: the browser's half of what
// mcp_analysis.go gives an agent.
//
// **Every handler here calls the same unexported core the MCP tool
// calls**, which is the split mcp_docs.go's header argues and this file
// inherits from api_views.go: requireScope asks "is this token bound to
// this game", which a session caller cannot answer, and requireProject
// asks the equivalent question of a session. One implementation of every
// tool, two admission checks — so a designer in a browser and an agent
// on /mcp cannot be answered differently about what a cycle is or what
// refused a route.
//
// **The prefix is `/api/games/{game}/…`, which is what shipped, and not
// the `/api/g/<slug>/…` the analysis spec's §9 writes.** The spec is
// wrong about it; there is no second family of URLs on this server and
// inventing one here would make this the only domain a client addresses
// differently.
//
// **An analysis is a read spelled as a POST, and that costs something.**
// Its arguments are a nested object — a seed set, a gating choice, three
// type filters — which does not fit in a query string, so a POST is the
// only honest spelling; and registerContentRoute puts requireEditor in
// front of every non-GET route on this surface (server.go), so a viewer
// cannot run an analysis over REST while a viewer with a token can over
// MCP.
//
// **api_views.go met this exactly and did not resolve it**, for
// `views.run`, and recorded what it costs and why the alternatives are
// worse: registering through registerProjectRoute to dodge the editor
// gate would put a game-content route outside the one convention test
// that watches this surface, and a GET carrying a document in its URL
// would meet a server's own header bounds on the first interesting
// query. This file follows that file, which is the instruction — and the
// finding stands and is recorded rather than papered over: a **viewer
// cannot run a read-only analysis over REST**. It is a finding against
// api_views.go's resolution and not against this one, because the two
// have the same shape and one answer between them; a viewer's read-only
// runner is a decision for the change that builds the browser client,
// and building it here for one domain would leave `views.run` as the
// only read on the surface a viewer cannot make.
// TestAViewerCanRunAnAnalysisAndCannotUpsertARoute asserts what actually
// ships, over both surfaces, rather than what would be nicer.

func (s *Server) handleAnalysisCycles(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireAnalysisService(w) {
		return
	}
	var in AnalysisCyclesInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := analysisCycles(r.Context(), s.deps(), scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAnalysisUnreachable(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireAnalysisService(w) {
		return
	}
	var in AnalysisUnreachableInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := analysisUnreachable(r.Context(), s.deps(), scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAnalysisOrphans(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireAnalysisService(w) {
		return
	}
	var in AnalysisOrphansInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := analysisOrphans(r.Context(), s.deps(), scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireAnalysisService(w) {
		return
	}
	cursor, ok := queryString(w, r, "cursor")
	if !ok {
		return
	}
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	out, err := routesList(r.Context(), s.deps(), scope.ProjectID,
		RoutesListInput{Cursor: cursor, Limit: limit})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetRoute(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireAnalysisService(w) {
		return
	}
	out, err := routesGet(r.Context(), s.deps(), scope.ProjectID,
		RoutesGetInput{Key: r.PathValue("key")})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpsertRoute answers 200 and not 201, for the reason
// handleUpsertView does: this route is an upsert idempotent by key, so
// the same request may create a route or replace one, and a status
// claiming "created" would be wrong half the time.
func (s *Server) handleUpsertRoute(w http.ResponseWriter, r *http.Request,
	caller Caller, scope ProjectScope,
) {
	if !s.requireAnalysisService(w) {
		return
	}
	var in RoutesUpsertInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := routesUpsert(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRemoveRoute is a DELETE that reads its expected_version from the
// query string, because a DELETE with a body is a shape half the HTTP
// stack in the world drops.
//
// **A missing or unparseable version is not defaulted here.** The domain
// refuses a removal with no version and says why, so this handler passes
// nil through and lets that refusal be the one the caller reads —
// inventing a zero here would turn "you must say which version you saw"
// into a version_conflict about a number nobody sent.
func (s *Server) handleRemoveRoute(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireAnalysisService(w) {
		return
	}
	expected, ok := queryVersion(w, r, "expected_version")
	if !ok {
		return
	}
	out, err := routesRemove(r.Context(), s.deps(), scope.ProjectID,
		RoutesRemoveInput{Key: r.PathValue("key"), ExpectedVersion: expected})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCheckRoute(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireAnalysisService(w) {
		return
	}
	out, err := routesCheck(r.Context(), s.deps(), scope.ProjectID,
		RoutesCheckInput{Key: r.PathValue("key")})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// requireAnalysisService refuses an analysis route on an instance built
// without an analysis service. That shape is supported deliberately
// (Options.Analysis) and every core above would panic on a nil service,
// so the guard is here rather than in each of them.
func (s *Server) requireAnalysisService(w http.ResponseWriter) bool {
	if s.opts.Analysis == nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "this instance serves no analyses")
		return false
	}
	return true
}
