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
// **One size bound, in one place, and the plan's second one is not
// here.** Task 14 prescribed wrapping the request body in
// http.MaxBytesReader; measured, that wrapper can never fire. The domain
// reads through an io.LimitReader of views.MaxAssetBytes+1 in the same
// read path, so it stops one byte past the cap and the transport's
// limiter — set at the same cap — is never asked for the byte that would
// trip it. A guard that cannot fire is the mechanism-nothing-reads this
// plan refuses everywhere else.
//
// Setting the transport's limit one byte *tighter* would make it fire,
// and that is the version that was rejected: it wins the race, and what
// it wins with is `http: request body too large` in place of a sentence
// telling a designer to scale the image down or save it at a lower
// quality. The refusal a designer reads is worth more than a second copy
// of a bound that is already applied while reading.
//
// The protection is unchanged either way — nothing here reads more than
// eight megabytes and one byte off the socket, whichever object stops
// it. internal/views' TestAnOversizeAssetIsRefusedBeforeItIsRead counts
// the bytes; TestAnOversizeUploadIsRefusedOverTheWire asserts that the
// refusal reaches a browser as a 400 naming /bytes.
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
//
// **It is paged, and it was not.** The listing answered with every asset
// a game held, in a product where every other listing takes a cursor and
// a limit; `cursor` and `limit` are read here exactly as api_docs.go and
// api_metamodel.go read them, and next_cursor is answered under the same
// name, so a client that can page one listing can page this one.
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
//
// **A GET, so a viewer reaches it**: looking at a picture is reading the
// game, and registerContentRoute gates only the writes.
//
// Three headers, each for its own reason:
//
//   - Content-Type is the *stored* mime, which is the mime a decoder
//     agreed with when the bytes were accepted. It is never derived from
//     the filename and never guessed here.
//
//   - X-Content-Type-Options: nosniff. securityHeaders already sets it
//     on every response this server writes, and it is set again here
//     rather than relied on, because this is the one route whose safety
//     depends on it: a browser that sniffs its own type out of bytes a
//     designer uploaded is the whole of the risk the closed mime list
//     exists to bound. Moving or narrowing the global middleware must
//     not silently take it away from here.
//
//     **That claim is now assertable, and for a while it was not.**
//     TestAnAssetIsServedWithANoSniffHeaderAndItsOwnContentType goes
//     through the full server, where securityHeaders sets the same
//     header outermost — so deleting this line left the whole web suite
//     green, and a line whose own comment calls it load-bearing could be
//     removed in silence. TestTheServingRoutesNoSniffHeaderIsItsOwn
//     calls this handler directly, with no middleware in front of it,
//     which is the only place in this package that can tell the two
//     sources apart.
//
//   - Cache-Control, long and immutable, because an asset's bytes never
//     change: there is no update path, and a new image is a new asset
//     with a new id. It is private rather than public — the bytes are a
//     game's own content behind an authenticated route, and a shared
//     cache has no business holding them.
//
//     **This overrides requireCaller's Cache-Control: no-store**, which
//     is deliberate and is the same override events.go makes for its own
//     reason. no-store exists because a response computed for one
//     identity must not be replayed to another; `private` says exactly
//     that to every cache that is not this browser, and requireCaller's
//     `Vary: Cookie, Authorization` — which this handler leaves alone —
//     is what keys the browser's own copy. What is bought for it is a
//     map that is not re-downloaded on every pan of a graph. A viewer
//     who later loses access keeps whatever their own browser cached,
//     which is true of every image any authenticated site serves and is
//     what `private` scopes to one machine.
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
