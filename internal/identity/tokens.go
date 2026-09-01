package identity

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// touchThrottle is how stale last_used_at must be before ResolveAPIToken
// bothers writing it again. It must match the "interval '5 minutes'"
// literal in TouchAPIToken's own SQL (identity.sql) — that WHERE clause,
// not this constant, is what actually makes the write safe under two
// concurrent resolves of the same token, since a plain Go time comparison
// has no way to serialize against a second goroutine doing the same
// check at the same moment. This constant exists so ResolveAPIToken can
// skip issuing the UPDATE at all in the overwhelmingly common case
// (already touched within the window), rather than sending a statement
// the database is guaranteed to match zero rows for on every single
// authenticated agent request.
const touchThrottle = 5 * time.Minute

// tokenChecksumRunes is the length, in base62 characters, of the checksum
// CreateAPIToken appends to a token's random body and ResolveAPIToken
// verifies before ever hashing or querying. crc32.ChecksumIEEE produces a
// 32-bit value; 62^6 exceeds 2^32, so six base62 characters always
// suffice with no risk of truncation.
const tokenChecksumRunes = 6

// tokenHintRunes is how much of a token's random body (before the
// checksum) CreateAPIToken stores, unencrypted, as TokenHint. It is
// deliberately short: enough for ListAPITokens to help an operator match
// a leaked value like "mst_xK9qP2af…" to the row it belongs to, nowhere
// near enough to reconstruct the token or meaningfully narrow a
// brute-force search of the remainder.
const tokenHintRunes = 8

// ErrTokenInvalid covers unknown and revoked bearer tokens, and a token
// whose checksum does not match its body (a mistyped or truncated
// paste — see verifyTokenChecksum). A revoked token is deliberately
// indistinguishable from one that never existed: GetLiveAPIToken's own
// WHERE clause excludes revoked rows from the lookup entirely, so
// ResolveAPIToken has no way to tell the two apart even if it wanted to,
// and it does not want to — the same reasoning ErrNoSession's doc comment
// (sessions.go) gives for collapsing an unknown token and an expired one
// into a single answer.
var ErrTokenInvalid = errors.New("api token is not valid")

// ErrTokenRequestInvalid means the request given to CreateAPIToken is
// malformed: an empty or over-long label, or a project or user id that
// does not exist (mapped from api_tokens' foreign key constraints, the
// same way projects.mapMembershipInsertError narrows a 23503 by
// constraint name).
var ErrTokenRequestInvalid = errors.New("token request is invalid")

// TokenPrefix marks a Maestro API token in logs and in the UI, and lets a
// human or a scanner recognise a leaked value on sight. It is inside the
// value ResolveAPIToken hashes (CreateAPIToken hashes TokenPrefix+body,
// not body alone), so changing it invalidates every token already
// issued — every stored hash was computed over the old prefix.
const TokenPrefix = "mst_"

// APITokenSummary is the identity domain's public view of an api_tokens
// row. It deliberately omits dbq.ApiToken's TokenHash, for the same
// reason User omits PasswordHash (users.go) and InviteSummary omits
// Invite's TokenHash (invites.go): nothing outside this package needs
// it, and nothing should be one careless writeJSON away from serving a
// value an attacker could brute-force offline against.
//
// LastUsedAt reflects the row as read at the start of ResolveAPIToken,
// before that call's own conditional touch (if any) is applied — so the
// value returned to whoever just made this exact call can be up to one
// touchThrottle interval stale relative to what the database holds
// immediately afterward. Callers that need the precise current value
// (an operator's listing, say) get it from ListAPITokens instead, which
// reads it fresh with no touch of its own.
//
// UserDisplayName is populated only by ListAPITokens, which joins users
// so an operator triaging a leaked token can see who minted it instead
// of a raw id; CreateAPIToken does not join users and always leaves it
// empty.
//
// UserIsAdmin is populated only by ResolveAPIToken, for a different
// reason than UserDisplayName: web.resolveBearerCaller needs the token
// creator's admin flag to build a Caller, and without it the middleware
// would need a second round trip (identity.UserByID) on every
// bearer-authenticated request to get one boolean — the exact
// last_used_at-style write Task 9 already fought to keep off this path,
// reintroduced as a read. GetLiveAPIToken's join exists for exactly this
// (see its own doc comment in identity.sql); CreateAPIToken, RevokeAPIToken
// and ListAPITokens do not join for this and always leave it false.
type APITokenSummary struct {
	ID              uuid.UUID
	ProjectID       uuid.UUID
	UserID          uuid.UUID
	Label           string
	TokenHint       string
	UserDisplayName string
	UserIsAdmin     bool
	CreatedAt       time.Time
	LastUsedAt      *time.Time
	RevokedAt       *time.Time
}

func apiTokenSummaryFrom(t dbq.ApiToken) APITokenSummary {
	out := APITokenSummary{
		ID:        t.ID,
		ProjectID: t.ProjectID,
		UserID:    t.UserID,
		Label:     t.Label,
		TokenHint: t.TokenHint,
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

// apiTokenSummaryFromLive is apiTokenSummaryFrom's counterpart for
// GetLiveAPITokenRow, the joined shape ResolveAPIToken reads (see that
// query's own doc comment in identity.sql). It is a separate function,
// not an overload of apiTokenSummaryFrom, because the two input types
// share no common struct to convert generically — dbq.ApiToken and
// dbq.GetLiveAPITokenRow are independently generated by sqlc from
// different SELECT lists.
func apiTokenSummaryFromLive(t dbq.GetLiveAPITokenRow) APITokenSummary {
	out := APITokenSummary{
		ID:          t.ID,
		ProjectID:   t.ProjectID,
		UserID:      t.UserID,
		Label:       t.Label,
		TokenHint:   t.TokenHint,
		UserIsAdmin: t.UserIsAdmin,
		CreatedAt:   t.CreatedAt.Time,
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
// token value is returned once and never stored; nothing in this package
// ever holds it again. Nothing in this package ever updates project_id
// once a token is created — a token's project binding is fixed at
// creation and there is no query that could change it.
func (s *Service) CreateAPIToken(ctx context.Context, req CreateAPITokenRequest) (string, APITokenSummary, error) {
	label := strings.TrimSpace(req.Label)
	if n := utf8.RuneCountInString(label); n < minLabelRunes || n > maxLabelRunes {
		return "", APITokenSummary{}, fmt.Errorf("%w: label must be between %d and %d characters", ErrTokenRequestInvalid, minLabelRunes, maxLabelRunes)
	}

	body, err := newTokenBody()
	if err != nil {
		return "", APITokenSummary{}, err
	}
	token := TokenPrefix + body
	sum := sha256.Sum256([]byte(token))
	hint := body
	if n := utf8.RuneCountInString(hint); n > tokenHintRunes {
		hint = string([]rune(hint)[:tokenHintRunes])
	}

	row, err := s.q.CreateAPIToken(ctx, dbq.CreateAPITokenParams{
		TokenHash: sum[:],
		ProjectID: req.ProjectID,
		UserID:    req.UserID,
		Label:     label,
		TokenHint: hint,
	})
	if err != nil {
		if merr := mapAPITokenInsertError(err); merr != nil {
			return "", APITokenSummary{}, merr
		}
		return "", APITokenSummary{}, fmt.Errorf("create api token: %w", err)
	}
	return token, apiTokenSummaryFrom(row), nil
}

// mapAPITokenInsertError narrows a foreign-key violation from
// CreateAPIToken's insert to ErrTokenRequestInvalid, the same way
// projects.mapMembershipInsertError narrows one on memberships: checked
// by constraint name, not just SQLSTATE 23503, so a future unrelated
// foreign key on this table cannot be misreported as "bad request".
// Returns nil when err is not a foreign-key violation on either
// constraint, so the caller falls through to its own generic wrap.
func mapAPITokenInsertError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return nil
	}
	switch pgErr.ConstraintName {
	case "api_tokens_project_id_fkey", "api_tokens_user_id_fkey":
		return fmt.Errorf("%w: project or user does not exist", ErrTokenRequestInvalid)
	default:
		return nil
	}
}

// ResolveAPIToken maps a bearer value to its live token and records use.
// "Live" means not revoked; GetLiveAPIToken's own WHERE clause is the only
// thing that decides that, so there is exactly one place that answers
// "is this token still good".
//
// ResolveAPIToken answers authentication, not authorization: it proves
// the caller holds a value that hashes to a live row, and the ProjectID
// on the row it returns names what that token is bound to. It does not
// re-check whether the token's creator is still a member of that
// project — a caller that treats ProjectID as permission to act on that
// project must satisfy itself the binding is still one the product
// wants to honour. Today that guarantee comes from
// projects.RemoveMember revoking a departing member's tokens for that
// project in the same transaction as the membership deletion (see its
// own doc comment); nothing in this method re-derives that from
// memberships on every call, so a caller relying on ProjectID for
// authorization is trusting that revocation path, not re-verifying it.
func (s *Service) ResolveAPIToken(ctx context.Context, token string) (APITokenSummary, error) {
	if !verifyTokenChecksum(token) {
		// A mistyped or truncated paste never matches its own checksum,
		// so this is rejected without a database round trip at all — the
		// same reason the checksum exists in the first place (see
		// newTokenBody's doc comment).
		return APITokenSummary{}, ErrTokenInvalid
	}

	sum := sha256.Sum256([]byte(token))
	row, err := s.q.GetLiveAPIToken(ctx, sum[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return APITokenSummary{}, ErrTokenInvalid
		}
		return APITokenSummary{}, fmt.Errorf("lookup api token: %w", err)
	}

	// Pre-checked in Go before ever issuing TouchAPIToken: GetLiveAPIToken
	// already returned last_used_at in row, so whether the write is even
	// worth attempting is known without a second statement. This is an
	// optimisation only — TouchAPIToken's own WHERE clause (identity.sql)
	// is what actually makes the write safe under two concurrent
	// resolves of the same token; it is left exactly as it was and is
	// still evaluated by Postgres regardless of this check.
	if !row.LastUsedAt.Valid || time.Since(row.LastUsedAt.Time) >= touchThrottle {
		if err := s.q.TouchAPIToken(ctx, row.ID); err != nil {
			// last_used_at is an audit convenience, not part of the
			// authentication decision: a statement timeout, a lock wait,
			// a read-only replica, or a cancelled context here must not
			// turn an otherwise-valid, already-authenticated token into a
			// hard failure on the hottest path in the product.
			slog.ErrorContext(ctx, "touch api token failed; continuing with the resolved token",
				"token_id", row.ID, "error", err)
		}
	}
	return apiTokenSummaryFromLive(row), nil
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

// ListAPITokens lists every token of one project, newest first, revoked
// ones included — this is the audit surface an operator uses to notice a
// token that should have been revoked (an unrecognised label, a
// last-used time that doesn't match the agent it's supposed to be, one
// still live long after the work it was minted for finished) and to see
// what happened to one that already was. It never returns a token's
// value or hash. As this list grows, a filter (live-only by default) or
// a periodic prune of long-revoked rows will likely be wanted; neither
// exists yet.
func (s *Service) ListAPITokens(ctx context.Context, projectID uuid.UUID) ([]APITokenSummary, error) {
	rows, err := s.q.ListAPITokens(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	out := make([]APITokenSummary, len(rows))
	for i, row := range rows {
		out[i] = APITokenSummary{
			ID:              row.ID,
			ProjectID:       row.ProjectID,
			UserID:          row.UserID,
			Label:           row.Label,
			TokenHint:       row.TokenHint,
			UserDisplayName: row.UserDisplayName,
			CreatedAt:       row.CreatedAt.Time,
		}
		if row.LastUsedAt.Valid {
			lastUsed := row.LastUsedAt.Time
			out[i].LastUsedAt = &lastUsed
		}
		if row.RevokedAt.Valid {
			revoked := row.RevokedAt.Time
			out[i].RevokedAt = &revoked
		}
	}
	return out, nil
}

// newTokenBody generates the random, non-prefix portion of a bearer
// token: randomTokenBytes of entropy (the same shared helper
// sessions.go's IssueSession and invites.go's CreateInvite use),
// base64url-encoded, followed by a tokenChecksumRunes-long base62 CRC32
// of that encoded string.
//
// The checksum is not a security control — CRC32 is not remotely
// collision-resistant against a determined attacker, and none of this
// package's authorization guarantees rest on it. What it buys is two
// things that only matter for a value with no attacker in the loop: a
// bare base64 body carries no distinctive structure, so a secret
// scanner's regex for "mst_" tokens either overmatches on unrelated
// base64 or has to fall back to entropy heuristics; appending a
// checksum a scanner can verify collapses that false-positive rate
// towards zero. And a human who mistypes or truncates a paste gets
// ErrTokenInvalid straight out of verifyTokenChecksum, with no database
// round trip, instead of a lookup miss indistinguishable from "this
// value was never valid at all".
func newTokenBody() (string, error) {
	raw, err := randomToken()
	if err != nil {
		return "", err
	}
	return raw + tokenChecksum(raw), nil
}

// verifyTokenChecksum reports whether token has TokenPrefix and a body
// ending in a checksum that matches the body's own random portion. It is
// the first thing ResolveAPIToken checks, before any hashing or
// database access.
func verifyTokenChecksum(token string) bool {
	body, ok := strings.CutPrefix(token, TokenPrefix)
	if !ok || len(body) <= tokenChecksumRunes {
		return false
	}
	random := body[:len(body)-tokenChecksumRunes]
	checksum := body[len(body)-tokenChecksumRunes:]
	return tokenChecksum(random) == checksum
}

// tokenChecksum returns the tokenChecksumRunes-long base62 encoding of
// random's CRC32-IEEE checksum.
func tokenChecksum(random string) string {
	return base62Encode(crc32.ChecksumIEEE([]byte(random)), tokenChecksumRunes)
}

// base62EncodeAlphabet is ordered digits-then-uppercase-then-lowercase so
// two encodings of nearby inputs do not casually resemble each other
// (not a security property — see newTokenBody — just a readability
// choice for the humans this hint and checksum exist for).
const base62EncodeAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// base62Encode encodes n as a fixed-width, zero-left-padded base62
// string of exactly width characters. width is always large enough to
// hold any uint32 for this file's one caller (tokenChecksum, with
// width == tokenChecksumRunes == 6; 62^6 > 2^32), so this never
// truncates significant digits.
func base62Encode(n uint32, width int) string {
	buf := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		buf[i] = base62EncodeAlphabet[n%62]
		n /= 62
	}
	return string(buf)
}
