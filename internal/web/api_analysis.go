package web

import (
	"net/http"
)

// The analysis surface over REST: the browser's half of what
// mcp_analysis.go gives an agent.
//
// Every handler calls the same unexported core the MCP tool calls, with
// a different admission check in front — requireScope asks whether a
// token is bound to this game, requireProject asks the equivalent of a
// session — so the two surfaces cannot answer differently about what a
// cycle is or what refused a route.
//
// An analysis is a read spelled as a POST: its arguments are a nested
// object that does not fit in a query string. registerContentRoute puts
// requireEditor in front of every non-GET route here, so a **viewer
// cannot run an analysis over REST** while a viewer with a token can
// over MCP. That is a known gap, shared with `views.run`, and it belongs
// to the change that builds a read-only runner rather than to one
// domain.

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
