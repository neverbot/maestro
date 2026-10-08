package web

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/views"
)

// The images attached to an entity, as a browser drives them.
//
// **There is no MCP mirror of the writes, and that is the feature.** An
// image arrives by somebody choosing a file, which is a thing a person
// does at a screen; an agent is told what is attached when it reads the
// entity and is given a URL it can fetch. The read half of this file is
// mirrored in mcp_metamodel.go's entity answer, the write half is not
// mirrored anywhere, and routes.go's own guard is what keeps that
// deliberate rather than forgotten.
//
// The entity is addressed the way every other entity route addresses
// one, by its type's key and its own, and the image by its id: an image
// has no key, being a file somebody uploaded rather than a thing the
// game declared.

// EntityImageOutput is one attached image, without its bytes.
type EntityImageOutput struct {
	ID       uuid.UUID `json:"id"`
	Filename string    `json:"filename"`
	Mime     string    `json:"mime"`
	Width    int32     `json:"width"`
	Height   int32     `json:"height"`
	// URL is where the bytes are, on this same server and behind the same
	// session or token the caller already holds.
	URL string `json:"url"`
}

// EntityImagesOutput is everything attached to one entity.
type EntityImagesOutput struct {
	Items []EntityImageOutput `json:"items"`
}

// attachInput is the one argument attaching takes: which image.
type attachInput struct {
	AssetID string `json:"asset_id"`
}

func (s *Server) requireAttachments(w http.ResponseWriter) bool {
	if s.opts.Views == nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "this instance keeps no image library")
		return false
	}
	return true
}

// entityImageOutputs is the one place the browser's shape is built, so
// the listing and the answer to an attach cannot disagree about it.
func entityImageOutputs(gameSlug string, rows []views.Attachment) EntityImagesOutput {
	out := EntityImagesOutput{Items: make([]EntityImageOutput, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, EntityImageOutput{
			ID: row.ID, Filename: row.Filename, Mime: row.Mime,
			Width: row.Width, Height: row.Height,
			URL: assetPath(gameSlug, row.ID),
		})
	}
	return out
}

// assetPath is where one image's bytes are served from. It is written
// once here because three surfaces say it and a second spelling is a
// broken picture nobody notices until a page is opened.
func assetPath(gameSlug string, id uuid.UUID) string {
	return "/api/games/" + gameSlug + "/assets/" + id.String()
}

func (s *Server) handleListEntityImages(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) || !s.requireAttachments(w) {
		return
	}
	entity, err := s.opts.Metamodel.EntityByKey(r.Context(), scope.ProjectID,
		r.PathValue("type"), r.PathValue("key"))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	rows, err := s.opts.Views.Attachments(r.Context(), scope.ProjectID, entity.ID)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entityImageOutputs(scope.Slug, rows))
}

func (s *Server) handleAttachEntityImage(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) || !s.requireAttachments(w) {
		return
	}
	var in attachInput
	if !decodeJSONBody(w, r, &in) {
		return
	}
	assetID, err := parseID("asset_id", in.AssetID)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	entity, err := s.opts.Metamodel.EntityByKey(r.Context(), scope.ProjectID,
		r.PathValue("type"), r.PathValue("key"))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	if err := s.opts.Views.Attach(r.Context(), scope.ProjectID, entity.ID, assetID, actorOf(caller)); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	// The whole listing back, not the one row: attaching is the only
	// thing that changes this list, and a caller that has it does not
	// need a second call to draw the result.
	rows, err := s.opts.Views.Attachments(r.Context(), scope.ProjectID, entity.ID)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entityImageOutputs(scope.Slug, rows))
}

func (s *Server) handleDetachEntityImage(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !s.requireContentService(w) || !s.requireAttachments(w) {
		return
	}
	assetID, err := parseID("image", r.PathValue("image"))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	entity, err := s.opts.Metamodel.EntityByKey(r.Context(), scope.ProjectID,
		r.PathValue("type"), r.PathValue("key"))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	if err := s.opts.Views.Detach(r.Context(), scope.ProjectID, entity.ID, assetID); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	rows, err := s.opts.Views.Attachments(r.Context(), scope.ProjectID, entity.ID)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entityImageOutputs(scope.Slug, rows))
}
