package web

import (
	"net/http"

	"github.com/neverbot/maestro/internal/views"
)

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

// --- The renderer catalogue ------------------------------------------

// RenderersOutput is the renderer catalogue as a browser reads it.
//
// **It exists so that one surface in this product can *offer* a renderer
// rather than check one.** Every other consumer of internal/views'
// catalogue asks it a yes-or-no question at save time; the "Save as"
// dialog has to paint a chooser, and a chooser needs the names, each
// renderer's knobs, each knob's kind and — for an enum — the exact
// spellings the server admits. A dialog that carried that list of its own
// would be a second copy of a table this repository generates its own
// prose from precisely so it cannot be copied, and the copy would live in
// JavaScript where no Go test reads it. Hence a route.
//
// It is a projection and not internal/views.Renderer itself, for this
// package's standing reason: Renderer carries `Requires`, a func, which
// cannot cross a wire, and the wire spelling of everything else is
// decided here rather than by Go's field names.
type RenderersOutput struct {
	Renderers []RendererOutput `json:"renderers"`
}

// RendererOutput is one entry of the catalogue.
//
// `requires` is the prose half of the contract — what a *query* must
// produce for this renderer to be saveable — and it is carried because
// the dialog changes the renderer of a query it may not edit: a designer
// who picks `nested` for a query with no containment edges is going to be
// refused, and the sentence that says why is worth reading before the
// refusal rather than after it.
type RendererOutput struct {
	Name            string                `json:"name"`
	Consumes        string                `json:"consumes"`
	Doc             string                `json:"doc"`
	Requires        string                `json:"requires"`
	ReadsBackground bool                  `json:"reads_background"`
	Params          []RendererParamOutput `json:"params"`
}

// RendererParamOutput is one knob.
//
// `values` is present for an enum and absent for every other kind, which
// is the catalogue's own rule
// (TestAnEnumParameterDeclaresItsValuesAndNothingElseDoes) carried onto
// the wire rather than restated: a control that offered spellings for a
// number would compose a document views.upsert refuses.
type RendererParamOutput struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Required bool     `json:"required"`
	Values   []string `json:"values,omitempty"`
	Doc      string   `json:"doc"`
}

// handleListRenderers answers the catalogue. It reads no row, takes no
// argument and cannot fail: the table is compiled in, and this is the
// same table CheckRenderer judges an upsert against.
//
// It is a content route rather than a public one because it is part of a
// game's surface and a caller with no business opening this game has no
// business reading what it could be drawn with. It is a GET, so
// registerContentRoute leaves it to a viewer as well as an editor: a
// viewer cannot save the copy, and a chooser they can read and not submit
// is better than a dialog that cannot explain itself.
func (s *Server) handleListRenderers(w http.ResponseWriter, _ *http.Request,
	_ Caller, _ ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	catalogue := views.RendererCatalogue()
	out := RenderersOutput{Renderers: make([]RendererOutput, 0, len(catalogue))}
	for _, r := range catalogue {
		entry := RendererOutput{
			Name:            r.Name,
			Consumes:        r.Consumes,
			Doc:             r.Doc,
			Requires:        r.RequiresDoc,
			ReadsBackground: r.ReadsBackground,
			Params:          make([]RendererParamOutput, 0, len(r.Params)),
		}
		for _, p := range r.Params {
			entry.Params = append(entry.Params, RendererParamOutput{
				Name:     p.Name,
				Kind:     string(p.Kind),
				Required: p.Required,
				Values:   p.Values,
				Doc:      p.Doc,
			})
		}
		out.Renderers = append(out.Renderers, entry)
	}
	writeJSON(w, http.StatusOK, out)
}
