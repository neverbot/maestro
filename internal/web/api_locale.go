package web

import (
	"errors"
	"io/fs"
	"net/http"
	"sort"
	"strings"

	"github.com/neverbot/maestro/internal/identity"
)

// shippedLocales is every language a catalogue ships for, read once from
// the files the browser fetches.
//
// **The list is the directory, not a constant beside it.** A second
// spelling of "which languages exist" is the half that goes stale: a
// catalogue added to static/i18n and not to a Go slice would be offered
// by the browser and refused by this endpoint.
var shippedLocales = func() map[string]bool {
	out := map[string]bool{}
	entries, err := fs.ReadDir(assets, "i18n")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			out[strings.TrimSuffix(e.Name(), ".json")] = true
		}
	}
	return out
}()

// LocaleNames lists them, sorted, for a message that has to name them.
func LocaleNames() []string {
	out := make([]string, 0, len(shippedLocales))
	for tag := range shippedLocales {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// handleSetLocale records the language the caller reads this product in.
// The empty string clears the choice, which is not the same as choosing
// English: an account with no choice follows the browser's languages.
func (s *Server) handleSetLocale(w http.ResponseWriter, r *http.Request, caller Caller) {
	if caller.IsToken() {
		writeError(w, http.StatusForbidden, errCodeForbidden,
			"a token speaks for a game's content and has no language of its own")
		return
	}
	var body struct {
		Locale string `json:"locale"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	tag := strings.ToLower(strings.TrimSpace(body.Locale))
	if tag != "" && !shippedLocales[tag] {
		writeError(w, http.StatusUnprocessableEntity, errCodeBadRequest,
			"this instance reads "+strings.Join(LocaleNames(), ", "))
		return
	}
	user, err := s.opts.Identity.SetLocale(r.Context(), caller.UserID, tag)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			writeError(w, http.StatusNotFound, errCodeNotFound, "that account no longer exists")
			return
		}
		writeUnmappedError(w, r, err, "set locale failed", "could not save that", "user_id", caller.UserID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"locale": user.Locale})
}
