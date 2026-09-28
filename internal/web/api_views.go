package web

import (
	"net/http"

	"github.com/neverbot/maestro/internal/views"
)

// The saved-view surface over REST: the browser's half of what
// mcp_views.go gives an agent.
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
type RenderersOutput struct {
	Renderers []RendererOutput `json:"renderers"`
}

// RendererOutput is one entry of the catalogue.
type RendererOutput struct {
	Name            string                `json:"name"`
	Consumes        string                `json:"consumes"`
	Doc             string                `json:"doc"`
	Requires        string                `json:"requires"`
	ReadsBackground bool                  `json:"reads_background"`
	Params          []RendererParamOutput `json:"params"`
}

// RendererParamOutput is one knob.
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
