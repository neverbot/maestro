package web

import "net/http"

// The saved-view surface over REST: the browser's half of what
// mcp_views.go gives an agent.
//
// **Every handler here calls the same unexported core the MCP tool
// calls**, which is the split mcp_docs.go's header argues and this file
// inherits: requireScope asks "is this token bound to this game", which
// a session caller cannot answer, and requireProject asks the equivalent
// question of a session. One implementation of every tool, two admission
// checks — so a designer in a browser and an agent on /mcp cannot be
// answered differently about what a view is or what refused it.
//
// **Running is a read and saving is a write**, and the split is made by
// the route's own method rather than by any handler here:
// registerContentRoute puts requireEditor in front of every non-GET
// route on this surface (server.go). That is what makes "a viewer may
// open a view and may not change one" true rather than intended, and
// TestEveryContentWriteRouteRefusesAViewer drives every one of them.
//
// **views.run is a POST and is still a read**, which is the one place
// that rule costs something: a query document does not fit in a query
// string, and a POST is therefore the only honest spelling. What it
// means is that a viewer cannot run a view over REST while a viewer with
// a token can over MCP — recorded rather than papered over, because the
// alternatives are worse. Registering it through registerProjectRoute to
// dodge the editor gate would put a game-content route outside the one
// convention test that watches this surface, and a GET carrying a
// document in its URL would meet a server's own header bounds on the
// first interesting query. The browser client this surface exists for is
// sub-project 5's, and a viewer's read-only view runner is a decision for
// the change that builds one.

// ViewsRunBody is the POST body of the run route: exactly the MCP tool's
// input, so the two surfaces take one argument shape.
type ViewsRunBody = ViewsRunInput

func (s *Server) handleListViews(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	renderer, ok := queryString(w, r, "renderer")
	if !ok {
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
	out, err := viewsList(r.Context(), s.deps(), scope.ProjectID, ViewsListInput{
		Renderer: renderer, Cursor: cursor, Limit: limit,
	})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetView(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	out, err := viewsGet(r.Context(), s.deps(), scope.ProjectID,
		ViewsGetInput{Key: r.PathValue("key")})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpsertView answers 200 and not 201, for the reason
// handleUpsertType does: this route is an upsert idempotent by key, so
// the same request may create a view or replace one, and a status
// claiming "created" would be wrong half the time. The answer carries the
// row's version, which is what a client needs to know what happened and
// what to send next.
func (s *Server) handleUpsertView(w http.ResponseWriter, r *http.Request,
	caller Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	var in ViewsUpsertInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := viewsUpsert(r.Context(), s.deps(), caller, scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRemoveView(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	out, err := viewsRemove(r.Context(), s.deps(), scope.ProjectID,
		ViewsRemoveInput{Key: r.PathValue("key")})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRunView(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	var in ViewsRunBody
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := viewsRun(r.Context(), s.deps(), scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleValidateView(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	var in ViewsValidateInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	out, err := viewsValidate(r.Context(), s.deps(), scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetViewPositions(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	var in ViewsSetPositionsInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	in.Key = r.PathValue("key")
	out, err := viewsSetPositions(r.Context(), s.deps(), scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleClearViewPositions is a POST and not a DELETE, and the body is
// why: the difference between "clear these four nodes" and "clear the
// whole arrangement" is an absent member, which a request with no body
// cannot express and a query string can only express by convention.
func (s *Server) handleClearViewPositions(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	var in ViewsClearPositionsInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	in.Key = r.PathValue("key")
	out, err := viewsClearPositions(r.Context(), s.deps(), scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetViewBackground(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	var in ViewsSetBackgroundInput
	if !decodeContentBody(w, r, &in) || !checkStatedProject(w, scope, in) {
		return
	}
	in.Key = r.PathValue("key")
	out, err := viewsSetBackground(r.Context(), s.deps(), scope.ProjectID, in)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
