package web

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/neverbot/maestro/internal/views"
)

// The background-asset surface: upload, list, serve, delete.
func (s *Server) requireViewsService(w http.ResponseWriter) bool {
	if s.opts.Views == nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "this instance serves no views")
		return false
	}
	return true
}

// ViewAssetOutput is one asset as a picker reads it.
type ViewAssetOutput struct {
	ID        string    `json:"id"`
	Filename  string    `json:"filename"`
	Mime      string    `json:"mime"`
	Width     int32     `json:"width"`
	Height    int32     `json:"height"`
	CreatedAt time.Time `json:"created_at"`
	// URL is where the bytes are, spelled by the server rather than
	// assembled by every client that wants to draw one.
	URL string `json:"url"`
}

// viewAssetOutput takes the game from the request path rather than from
// ProjectScope, which carries no slug: the URL a client is handed has to
// be the URL it asked through, or a token caller and a session caller
// would be told different addresses for one image.
func viewAssetOutput(game string, a views.Asset) ViewAssetOutput {
	return ViewAssetOutput{
		ID: a.ID.String(), Filename: a.Filename, Mime: a.Mime,
		Width: a.Width, Height: a.Height, CreatedAt: a.CreatedAt,
		URL: fmt.Sprintf("/api/games/%s/view-assets/%s", game, a.ID),
	}
}

// handleUploadViewAsset stores one background image.
func (s *Server) handleUploadViewAsset(w http.ResponseWriter, r *http.Request,
	caller Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	filename, ok := queryString(w, r, "filename")
	if !ok {
		return
	}
	// r.Body is handed over unwrapped: the size bound is the domain's and
	// is applied while reading — see this file's header for why the
	// second wrapper the plan asked for was measured and left out.
	asset, err := s.opts.Views.CreateAsset(r.Context(), scope.ProjectID,
		actorOf(caller), filename, r.Body)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewAssetOutput(r.PathValue("game"), asset))
}

// handleListViewAssets lists one page of this game's assets.
func (s *Server) handleListViewAssets(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
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
	page, err := s.opts.Views.ListAssets(r.Context(), scope.ProjectID,
		views.AssetFilter{Cursor: cursor, Limit: limit})
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	out := make([]ViewAssetOutput, 0, len(page.Assets))
	for _, a := range page.Assets {
		out = append(out, viewAssetOutput(r.PathValue("game"), a))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"assets": out, "next_cursor": page.NextCursor,
	})
}

// handleServeViewAsset writes one asset's bytes.
func (s *Server) handleServeViewAsset(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	id, err := parseID("id", r.PathValue("id"))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	asset, err := s.opts.Views.ReadAsset(r.Context(), scope.ProjectID, id)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", asset.Mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Length", strconv.Itoa(len(asset.Bytes)))
	_, _ = w.Write(asset.Bytes)
}

// handleRemoveViewAsset deletes one asset.
func (s *Server) handleRemoveViewAsset(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	id, err := parseID("id", r.PathValue("id"))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	if err := s.opts.Views.RemoveAsset(r.Context(), scope.ProjectID, id); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}
