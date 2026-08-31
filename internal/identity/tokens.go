package identity

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// Bounds on an API token's label. It is free text an admin chooses to
// recognise a token in a list (ListAPITokens) — "seed agent",
// "nightly export" — not an identifier anything parses, so the same
// trim-and-rune-count treatment projects.validateName applies to a
// project's name applies here too.
const (
	minLabelRunes = 1
	maxLabelRunes = 200
)

// ErrTokenInvalid covers unknown and revoked bearer tokens. A revoked
// token is deliberately indistinguishable from one that never existed:
// GetLiveAPIToken's own WHERE clause excludes revoked rows from the
// lookup entirely, so ResolveAPIToken has no way to tell the two apart
// even if it wanted to, and it does not want to — the same reasoning
// ErrNoSession's doc comment (sessions.go) gives for collapsing an
// unknown token and an expired one into a single answer.
var ErrTokenInvalid = errors.New("api token is not valid")

// ErrTokenRequestInvalid means the label given to CreateAPIToken is
// malformed: empty, or longer than maxLabelRunes.
var ErrTokenRequestInvalid = errors.New("token request is invalid")

// TokenPrefix marks a Maestro API token in logs and in the UI, and lets a
// human or a scanner recognise a leaked value on sight.
const TokenPrefix = "mst_"

// APIToken is the identity domain's public view of an api_tokens row. It
// deliberately omits dbq.ApiToken's TokenHash, for the same reason User
// omits PasswordHash (users.go) and InviteSummary omits Invite's
// TokenHash (invites.go): nothing outside this package needs it, and
// nothing should be one careless writeJSON away from serving a value an
// attacker could brute-force offline against.
type APIToken struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	UserID     uuid.UUID
	Label      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

func apiTokenFrom(t dbq.ApiToken) APIToken {
	out := APIToken{
		ID:        t.ID,
		ProjectID: t.ProjectID,
		UserID:    t.UserID,
		Label:     t.Label,
		CreatedAt: t.CreatedAt.Time,
	}
	if t.LastUsedAt.Valid {
		lastUsed := t.LastUsedAt.Time
		out.LastUsedAt = &lastUsed
	}
	if t.RevokedAt.Valid {
		revoked := t.RevokedAt.Time
		out.RevokedAt = &revoked
	}
	return out
}

// CreateAPITokenRequest is the input to CreateAPIToken. It exists for the
// same reason CreateUserRequest (users.go) and InviteRequest (invites.go)
// do: ProjectID and UserID are both uuid.UUID, and a positional signature
// would let a transposed argument compile fine while silently minting a
// token bound to the wrong project — the exact isolation invariant this
// type of token exists to enforce.
type CreateAPITokenRequest struct {
	ProjectID uuid.UUID
	UserID    uuid.UUID
	Label     string
}

// CreateAPIToken mints a bearer token bound to exactly one project. The
// clear value is returned once and never stored; nothing in this package
// ever holds it again. Nothing in this package ever updates project_id
// once a token is created — a token's project binding is fixed at
// creation and there is no query that could change it.
func (s *Service) CreateAPIToken(ctx context.Context, req CreateAPITokenRequest) (string, APIToken, error) {
	label := strings.TrimSpace(req.Label)
	if n := utf8.RuneCountInString(label); n < minLabelRunes || n > maxLabelRunes {
		return "", APIToken{}, fmt.Errorf("%w: label must be between %d and %d characters", ErrTokenRequestInvalid, minLabelRunes, maxLabelRunes)
	}

	raw, err := randomToken()
	if err != nil {
		return "", APIToken{}, err
	}
	clear := TokenPrefix + raw
	sum := sha256.Sum256([]byte(clear))

	row, err := s.q.CreateAPIToken(ctx, dbq.CreateAPITokenParams{
		TokenHash: sum[:],
		ProjectID: req.ProjectID,
		UserID:    req.UserID,
		Label:     label,
	})
	if err != nil {
		return "", APIToken{}, fmt.Errorf("create api token: %w", err)
	}
	return clear, apiTokenFrom(row), nil
}

// ResolveAPIToken maps a bearer value to its live token and records use.
// "Live" means not revoked; GetLiveAPIToken's own WHERE clause is the only
// thing that decides that, so there is exactly one place that answers
// "is this token still good".
func (s *Service) ResolveAPIToken(ctx context.Context, clear string) (APIToken, error) {
	sum := sha256.Sum256([]byte(clear))
	row, err := s.q.GetLiveAPIToken(ctx, sum[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APIToken{}, ErrTokenInvalid
		}
		return APIToken{}, fmt.Errorf("lookup api token: %w", err)
	}
	if err := s.q.TouchAPIToken(ctx, row.ID); err != nil {
		return APIToken{}, fmt.Errorf("touch api token: %w", err)
	}
	return apiTokenFrom(row), nil
}

// RevokeAPITokenRequest is the input to RevokeAPIToken. It exists for the
// same reason CreateAPITokenRequest above does: ProjectID and TokenID are
// both uuid.UUID.
type RevokeAPITokenRequest struct {
	ProjectID uuid.UUID
	TokenID   uuid.UUID
}

// RevokeAPIToken revokes a token, scoped to the project that owns it.
// Revoking an unknown token id, an already-revoked one, or one that
// belongs to a different project is not an error: the caller's goal (no
// live token under this id in this project) is already satisfied, the
// same convention RevokeSession and RevokeInvite already establish. The
// project scope in the query's own WHERE clause is what makes this safe
// to call with a project id taken from the caller's own request path
// without a separate ownership check: a caller can never revoke a token
// it does not administer, and never learns whether the id it named
// belongs to some other project instead.
func (s *Service) RevokeAPIToken(ctx context.Context, req RevokeAPITokenRequest) error {
	if err := s.q.RevokeAPIToken(ctx, dbq.RevokeAPITokenParams{ID: req.TokenID, ProjectID: req.ProjectID}); err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}
	return nil
}

// ListAPITokens lists every token of one project, newest first, without
// their values or hashes — the only way an operator has to notice a
// token that should have been revoked (an unrecognised label, a
// last-used time that doesn't match the agent it's supposed to be, one
// still live long after the work it was minted for finished).
func (s *Service) ListAPITokens(ctx context.Context, projectID uuid.UUID) ([]APIToken, error) {
	rows, err := s.q.ListAPITokens(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	out := make([]APIToken, len(rows))
	for i, row := range rows {
		out[i] = apiTokenFrom(dbq.ApiToken{
			ID:         row.ID,
			ProjectID:  row.ProjectID,
			UserID:     row.UserID,
			Label:      row.Label,
			CreatedAt:  row.CreatedAt,
			LastUsedAt: row.LastUsedAt,
			RevokedAt:  row.RevokedAt,
		})
	}
	return out, nil
}
