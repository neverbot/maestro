package web

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/neverbot/maestro/internal/views"
)

// The background-asset surface: upload, list, serve, delete.
//
// **This is the one REST surface in Maestro with no MCP twin, and that
// is the decision rather than an omission.** Pushing megabytes of base64
// through a tool call to save a human from opening the UI is the wrong
// trade, so an agent references an asset that already exists —
// views.list_assets tells it which — and the bytes arrive from a
// browser. api_docs.go's rendered views are the other REST-only routes,
// for a related reason: a surface exists on the wire an agent can
// actually use it from.
//
// **The upload takes the image as the raw request body, not as
// multipart.** A multipart parser is a second parser over hostile bytes,
// with its own boundary scanning and its own temporary files, for a
// request that carries exactly one file; a browser sends a File object
// as a body with one line of fetch. The filename rides in the query
// string, where it is prose and nothing else — it is stored so a
// designer recognises the image, is never consulted for the format, and
// is never echoed into a response header.
//
// **Two bounds, one constant.** http.MaxBytesReader stops the request
// body at views.MaxAssetBytes+1 so the server never reads a four-gigabyte
// upload into anything, and internal/views bounds the reader it is
// handed for the same reason one layer down. They are the same bound
// twice rather than two bounds: the constant is the domain's, and this
// file reads it rather than declaring its own.
// TestAnOversizeUploadIsRefusedByTheTransportToo drives the transport
// half; internal/views' TestAnOversizeAssetIsRefusedBeforeItIsRead
// drives the other and counts the bytes.
//
// **Everything else about the bytes is decided in internal/views**, and
// deliberately not here: the mime is sniffed there, the dimensions are
// decoded there, SVG is refused there. A transport that judged a format
// would be a second judge, and the stored mime — which is what the
// serving route below answers with — must be the one the decoder agreed
// with.

// requireViewsService refuses a views route on an instance built without
// a views service, the way requireProseService does for prose. The
// routes are registered unconditionally for the reason that file's
// comment gives: a registration gated on the service being present is
// invisible to the convention tests, which build their server from
// stubOptions.
func (s *Server) requireViewsService(w http.ResponseWriter) bool {
	if s.opts.Views == nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "this instance serves no views")
		return false
	}
	return true
}

// ViewAssetOutput is one asset as a picker reads it.
//
// It carries width, height and mime because those are decoded and
// sniffed rather than given, and a client placing a background needs the
// pixel size to compute a scale. It carries no bytes: the listing would
// otherwise be megabytes a caller throws away.
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
	// The transport's own bound, over the domain's constant. +1 so that
	// a body of exactly the cap is readable and one byte more is not,
	// which is the same off-by-one internal/views' readBounded makes and
	// for the same reason: reading exactly the cap cannot tell a file of
	// that size from the front of a larger one.
	body := http.MaxBytesReader(w, r.Body, views.MaxAssetBytes+1)
	asset, err := s.opts.Views.CreateAsset(r.Context(), scope.ProjectID,
		actorOf(caller), filename, body)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewAssetOutput(r.PathValue("game"), asset))
}

// handleListViewAssets lists this game's assets.
func (s *Server) handleListViewAssets(w http.ResponseWriter, r *http.Request,
	_ Caller, scope ProjectScope,
) {
	if !s.requireViewsService(w) {
		return
	}
	assets, err := s.opts.Views.ListAssets(r.Context(), scope.ProjectID)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	out := make([]ViewAssetOutput, 0, len(assets))
	for _, a := range assets {
		out = append(out, viewAssetOutput(r.PathValue("game"), a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"assets": out})
}

// handleServeViewAsset writes one asset's bytes.
//
// **A GET, so a viewer reaches it**: looking at a picture is reading the
// game, and registerContentRoute gates only the writes.
//
// Three headers, each for its own reason:
//
//   - Content-Type is the *stored* mime, which is the mime a decoder
//     agreed with when the bytes were accepted. It is never derived from
//     the filename and never guessed here.
//   - X-Content-Type-Options: nosniff. securityHeaders already sets it
//     on every response this server writes, and it is set again here
//     rather than relied on, because this is the one route whose safety
//     depends on it: a browser that sniffs its own type out of bytes a
//     designer uploaded is the whole of the risk the closed mime list
//     exists to bound. TestAnAssetIsServedWithANoSniffHeaderAndItsOwnContentType
//     asserts it on this response, so moving or narrowing the global
//     middleware cannot silently take it away from here.
//   - Cache-Control, long and immutable, because an asset's bytes never
//     change: there is no update path, and a new image is a new asset
//     with a new id. It is private rather than public — the bytes are a
//     game's own content behind an authenticated route, and a shared
//     cache has no business holding them.
//
// **No Content-Disposition.** Nothing echoes the caller-supplied
// filename into a response header, so there is no header-injection or
// download-name surface to bound in the first place.
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
//
// Every view drawn over it keeps its row and loses its background, which
// is 0008_views.sql's ON DELETE SET NULL rather than anything this
// handler does.
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
