package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// One image, over one unauthenticated signed URL, for an agent holding
// a command line and no browser session.
//
// **Why a signed URL rather than the authenticated route.** The browser
// reads an image through `/api/games/{game}/assets/{id}`, behind the
// session it already has. An agent has a bearer token and could send it
// too, but what the agent is handed is a URL it will pass to something
// else -- `curl`, a shell, whatever it has -- and a URL that only works
// with a header is a URL most of those get wrong. This is the same
// mechanism, the same key and the same arithmetic as the skill bundle's
// download (api_skill.go): possession of a live signature is the
// authorisation, and nothing else is consulted.
//
// **The game is in the path, so the read stays scoped.** GetAsset takes
// a project id and its own comment says why: an asset id is a value a
// previous answer handed back, so an unscoped read would serve one
// game's map to anybody holding an id from another. A signature is a
// good authorisation and a bad excuse to drop that. It is the project's
// id rather than its slug because that is what the scoped read wants
// and a slug would have to be resolved to it anyway; the signature
// covers the whole path, so neither half can be swapped.
const imagePath = "/image/"

// imageURLTTL is how long a signed image URL stays good. An hour, where
// the bundle's is five minutes, because the two are fetched on different
// clocks: the bundle is downloaded by the agent that just asked for it,
// and an image URL is read out of an entity's answer and may be acted on
// after the agent has read thirty more. Still short enough that a URL
// which leaks into a transcript is worthless by the time anybody reads
// it, which is the whole bargain.
const imageURLTTL = time.Hour

// signedImageURL is the whole download URL for one image: base (which
// may be empty, giving a path-only URL the agent resolves against the
// server it is already talking to) plus the path, the expiry and the
// signature.
func signedImageURL(key []byte, base string, projectID, id uuid.UUID, exp int64) string {
	path := imagePath + projectID.String() + "/" + id.String()
	return strings.TrimSuffix(base, "/") + path +
		"?exp=" + strconv.FormatInt(exp, 10) +
		"&sig=" + signDownloadURL(key, path, exp)
}

// handleImageDownload serves one image's bytes to a caller holding a
// live signature, and refuses everything else with 401 and no body.
func (s *Server) handleImageDownload(w http.ResponseWriter, r *http.Request) {
	if !s.signedURLIsLive(r) {
		// No body, for the reason the bundle's download gives: there is
		// nothing a caller can do with a description of why a signature
		// failed that they cannot do by reading the entity again for a
		// fresh URL.
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if s.opts.Views == nil {
		http.NotFound(w, r)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	projectID, err := uuid.Parse(r.PathValue("project"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	asset, err := s.opts.Views.ReadAsset(r.Context(), projectID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", asset.Mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Private and immutable, as the browser's own route sets: the bytes
	// behind one id never change, and the URL stops working long before
	// a year is up either way.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Length", strconv.Itoa(len(asset.Bytes)))
	_, _ = w.Write(asset.Bytes)
}
