package web

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/roles"
)

type createTokenRequest struct {
	Label string `json:"label"`
}

// handleCreateToken mints a token bound to scope.ProjectID. Editor or
// owner only — see ProjectScope's own doc comment for the reasoning: a
// token grants an agent whatever access its project binding carries, for
// as long as the token lives, independent of the person who minted it;
// letting a read-only viewer mint one would hand out a credential wider
// than the viewer's own role, a privilege escalation dressed as a
// convenience. Listing (metadata and hint only, never a live value) and
// revoking (which only ever removes access) stay open to any member; see
// handleListTokens and handleRevokeToken.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireHumanCaller(w, caller) {
		return
	}
	if !roles.AtLeast(roles.Role(scope.Role), roles.Editor) {
		writeError(w, http.StatusForbidden, errCodeForbidden, "only an editor or owner may create tokens")
		return
	}
	var req createTokenRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Label == "" {
		req.Label = "unnamed token"
	}

	clear, row, err := s.opts.Identity.CreateAPIToken(r.Context(), identity.CreateAPITokenRequest{ProjectID: scope.ProjectID, UserID: caller.UserID, Label: req.Label})
	switch {
	case errors.Is(err, identity.ErrTokenRequestInvalid):
		writeError(w, http.StatusUnprocessableEntity, errCodeLabelInvalid, "that label is not usable")
	case err != nil:
		slog.ErrorContext(r.Context(), "create api token failed", "project_id", scope.ProjectID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not create the token")
	default:
		// Published with the same fields apiTokenResponse exposes to
		// handleListTokens — never the clear value, which appears in
		// this handler's own response below and nowhere else, ever — at
		// MinRole roles.Editor and HumanOnly true; see eventTokenMinted's
		// own doc comment (publish.go) for why both gates are needed (a
		// token caller's own Role is always Editor, so MinRole alone
		// would not have excluded it).
		s.publish(scope.ProjectID, eventTokenMinted, roles.Editor, true, map[string]any{
			"id": row.ID, "label": row.Label, "token_hint": row.TokenHint,
		})
		// The clear value appears here and nowhere else, ever.
		// token_hint is returned here too, not just from the listing: an
		// operator who copies the clear value into an agent's config
		// wants the fingerprint they will later match a leaked value
		// against available at mint time, without a second round trip to
		// the listing to find the row they just created.
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":         row.ID,
			"label":      row.Label,
			"token":      clear,
			"token_hint": row.TokenHint,
		})
	}
}

// apiTokenResponse is the wire shape of one row in handleListTokens'
// response. A quality review found the handler used to pass
// identity.APITokenSummary straight to writeJSON: that type's own doc
// comment says nothing should be one careless writeJSON away from
// serving a field like the token hash, and this was exactly that call,
// saved only by the hash already being absent from the struct — every
// *other* field (PascalCase, UserIsAdmin included) still went out
// verbatim, unlike every sibling handler in this file, which builds its
// own snake_case shape by hand. This type is that shape for tokens:
// UserIsAdmin is dropped entirely (a token listing has no business
// reporting whether its minter is an instance admin), and every field
// that stays has a name matching the rest of this package's JSON, not
// whatever Go's exporting convention happened to produce.
type apiTokenResponse struct {
	ID         uuid.UUID  `json:"id"`
	Label      string     `json:"label"`
	TokenHint  string     `json:"token_hint"`
	MintedBy   string     `json:"minted_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func apiTokenResponseFrom(t identity.APITokenSummary) apiTokenResponse {
	return apiTokenResponse{
		ID:         t.ID,
		Label:      t.Label,
		TokenHint:  t.TokenHint,
		MintedBy:   t.UserDisplayName,
		CreatedAt:  t.CreatedAt,
		LastUsedAt: t.LastUsedAt,
		RevokedAt:  t.RevokedAt,
	}
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireHumanCaller(w, caller) {
		return
	}
	rows, err := s.opts.Identity.ListAPITokens(r.Context(), scope.ProjectID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list api tokens failed", "project_id", scope.ProjectID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not list tokens")
		return
	}
	tokens := make([]apiTokenResponse, len(rows))
	for i, row := range rows {
		tokens[i] = apiTokenResponseFrom(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

// handleRevokeToken revokes one token by id, scoped to scope.ProjectID.
// Revoking an id that does not exist, or belongs to a different project,
// is deliberately answered the same 204 as a successful revoke — the
// same no-op convention identity.RevokeAPIToken's own doc comment
// establishes — and not a 404: telling the two apart here would let a
// caller who already holds standing in this project probe whether some
// other token id belongs to a different project by watching for 404
// versus 204, exactly the cross-project inference requireProject's own
// scope check exists to prevent everywhere else in this file. Do not
// "fix" this into a lookup-then-404; TestRevokingAnUnknownOrForeignTokenIsANoop
// pins it.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireHumanCaller(w, caller) {
		return
	}
	tokenID, err := uuid.Parse(r.PathValue("token"))
	if err != nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such token")
		return
	}
	if err := s.opts.Identity.RevokeAPIToken(r.Context(), identity.RevokeAPITokenRequest{ProjectID: scope.ProjectID, TokenID: tokenID}); err != nil {
		slog.ErrorContext(r.Context(), "revoke api token failed", "project_id", scope.ProjectID, "token_id", tokenID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not revoke the token")
		return
	}
	// Published even for the no-op case (an unknown or foreign token id
	// — this handler's own doc comment above explains why that answers
	// the same 204 as a real revoke): a subscriber cannot tell the two
	// apart from this event alone either, which is fine, since a client
	// reacts to token.revoked by dropping a row matching this id from
	// its own list, a no-op if it never had one.
	s.publish(scope.ProjectID, eventTokenRevoked, roles.Editor, true, map[string]any{"id": tokenID})
	w.WriteHeader(http.StatusNoContent)
}
