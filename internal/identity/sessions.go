package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// ErrNoSession means the cookie carried no live session: the token is
// unknown, was revoked, or its session has expired. GetSessionUser's query
// already filters on expires_at > now(), so an expired row and a missing
// row are indistinguishable here by construction — exactly as desired,
// since neither should let the caller in.
var ErrNoSession = errors.New("no active session")

// randomTokenBytes is the amount of raw entropy in a token minted by
// randomToken, before base64 encoding. Named for the shared helper, not
// for sessions specifically: invites.go's CreateInvite calls the same
// randomToken and depends on this same constant, so a name that read
// "sessionTokenBytes" invited someone tuning session token length to
// change invite token length without knowing it. 32 bytes (256 bits) is
// far beyond what is guessable; encoding expands this to a longer string,
// so a length check on the encoded token is not itself a check on this
// constant — see the tests.
const randomTokenBytes = 32

// maxSessionLifetime is the absolute cap on how long a session can live,
// no matter how much sliding renewal (ExtendSession) extends it. Without
// a ceiling, a session in continuous use never expires and a stolen
// cookie exercised even once a fortnight stays valid forever — renewal
// alone answers "should an active session survive its SESSION_TTL" but
// says nothing about "for how long, ultimately", which is a separate
// policy decision this instance makes explicitly rather than by omission.
// 90 days is long enough that a designer who opens their laptop weekly is
// never forced to log back in, and short enough that a credential nobody
// has used in three months stops being a standing risk on its own.
//
// This constant exists for documentation and cross-reference only —
// ExtendSession's own SQL (identity.sql) is what actually enforces the
// cap, via a LEAST(..., created_at + interval '90 days') that must keep
// matching the "90 days" here. Nothing in Go re-derives or re-checks it.
//
//nolint:unused // documentation-only cross-reference to the SQL-enforced cap, see above.
const maxSessionLifetime = 90 * 24 * time.Hour

// IssueSession mints a session token and stores only its hash. The token
// itself is returned once, for the caller to set as an httpOnly cookie
// value; nothing else in this package ever holds it again. The expiry that
// was actually written is returned alongside it, so the HTTP layer's
// cookie code sets its Expires field from this value instead of
// recomputing time.Now().Add(s.cfg.SessionTTL) itself — a second
// computation of the same policy that could silently drift from what the
// database holds, and that gains a `.Add` truncation or a slightly later
// `time.Now()` for free the moment anyone touches either call site.
func (s *Service) IssueSession(ctx context.Context, userID uuid.UUID) (token string, expiresAt time.Time, err error) {
	token, err = randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt = time.Now().Add(s.cfg.SessionTTL)
	sum := sha256.Sum256([]byte(token))
	if err := s.q.CreateSession(ctx, dbq.CreateSessionParams{
		TokenHash: sum[:],
		UserID:    userID,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return token, expiresAt, nil
}

// UserForSession resolves a session token to its user and to that
// session's own expiry. It returns the domain User type, not dbq.User: the
// identity package's public surface never hands out the generated row
// type, so nothing downstream is ever one careless writeJSON away from
// serving PasswordHash back to a browser (see the doc comment on User in
// users.go).
//
// The expiry is returned so a caller can implement sliding sessions — call
// ExtendSession when it decides the session is worth renewing — without a
// second round trip to fetch it. Deciding when to do that (every request?
// past some remaining-lifetime threshold?) is an HTTP-layer policy; this
// method only makes the information available.
func (s *Service) UserForSession(ctx context.Context, token string) (User, time.Time, error) {
	sum := sha256.Sum256([]byte(token))
	row, err := s.q.GetSessionUser(ctx, sum[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, time.Time{}, ErrNoSession
		}
		return User{}, time.Time{}, fmt.Errorf("lookup session: %w", err)
	}
	user := userFrom(dbq.User{
		ID:           row.ID,
		Email:        row.Email,
		DisplayName:  row.DisplayName,
		PasswordHash: row.PasswordHash,
		IsAdmin:      row.IsAdmin,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	})
	return user, row.SessionExpiresAt.Time, nil
}

// ExtendSession pushes a session's expiry forward to roughly now() + ttl,
// capped at maxSessionLifetime from the session's own creation. Task 10's
// authentication middleware calls it from resolveSessionCaller, on a
// session more than halfway through its lifetime, to slide the expiry
// forward without logging the caller out mid-use; that halfway threshold
// (not "every request") is what keeps the *decision to attempt* a
// renewal off the per-request path, the same way touchThrottle keeps
// ResolveAPIToken's last_used_at write off it (tokens.go). It exists
// here, in the identity package rather than at the HTTP layer, so that
// policy needs no schema or sqlc change of its own to support it.
//
// The target expiry is computed by the query itself, from Postgres' own
// now() and the ttl passed in — not by this method computing
// time.Now().Add(ttl) and handing over an already-resolved timestamp.
// That distinction is load-bearing, not stylistic: an earlier version
// did the latter, and it did not make concurrent renewals collapse into
// one write the way its comment claimed, because two concurrent requests
// each compute their own, slightly later, target and each satisfies a
// WHERE clause that only asked "is my target later than the current
// value". See identity.sql's doc comment on ExtendSession for the actual
// fix (a one-minute slack in the predicate) and for why this stays an
// UPDATE, never an upsert, so a logout racing this call cannot resurrect
// a session it just deleted.
//
// Returns the number of rows the UPDATE actually touched — 0 or 1, since
// token_hash is the table's primary key. resolveSessionCaller does not
// use this value; it exists so a test can count actual writes directly
// (see TestConcurrentRenewalsProduceExactlyOneWrite) instead of
// inferring them from the final expires_at, which looks identical
// whether one write happened or several.
func (s *Service) ExtendSession(ctx context.Context, token string, ttl time.Duration) (int64, error) {
	sum := sha256.Sum256([]byte(token))
	n, err := s.q.ExtendSession(ctx, dbq.ExtendSessionParams{
		TokenHash: sum[:],
		Ttl:       pgtype.Interval{Microseconds: ttl.Microseconds(), Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("extend session: %w", err)
	}
	return n, nil
}

// RevokeSession deletes one session, identified by its token. Deleting an
// unknown or already-expired token is not an error: the caller's goal (no
// live session for this token) is already satisfied.
func (s *Service) RevokeSession(ctx context.Context, token string) error {
	sum := sha256.Sum256([]byte(token))
	if err := s.q.DeleteSession(ctx, sum[:]); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// RevokeAllSessions drops every session of a user. ChangePassword below is
// the primary caller; it is also exported directly for anything else that
// needs to force every device out (an admin-initiated suspension, say).
func (s *Service) RevokeAllSessions(ctx context.Context, userID uuid.UUID) error {
	if err := s.q.DeleteSessionsForUser(ctx, userID); err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	return nil
}

// ChangePassword validates and stores a new password, and revokes every
// session belonging to the account in the same transaction. Rotating the
// hash and revoking sessions as two independent calls leaves a window (a
// crash, a failed second call) in which the password has changed but a
// session minted under the old one — including whatever stole it, if that
// is why the password is being changed at all — is still live. Both writes
// commit or roll back together instead.
//
// This revokes the caller's own session too: there is no way to tell "this
// session" apart from any other at this layer, and treating one session as
// exempt would mean a stolen session surviving a password change simply by
// being the one that happened to request it. Whatever HTTP handler calls
// this must re-issue a fresh session (IssueSession) for the request that
// triggered it.
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, newPassword string) error {
	// Same bounds as CreateUser's password check, and for the same
	// reasons: the byte bound is checked first because it protects
	// against an oversized argon2 input, and both must hold before the
	// password is hashed at all.
	if len(newPassword) > maxPasswordBytes {
		return fmt.Errorf("%w: must be at most %d bytes", ErrPasswordInvalid, maxPasswordBytes)
	}
	if utf8.RuneCountInString(newPassword) < minPasswordRunes {
		return fmt.Errorf("%w: must be at least %d characters", ErrPasswordInvalid, minPasswordRunes)
	}

	hash, err := HashPassword(newPassword, s.cfg.Argon2)
	if err != nil {
		return err
	}

	return s.withTx(ctx, func(q *dbq.Queries) error {
		if err := q.UpdateUserPasswordHash(ctx, dbq.UpdateUserPasswordHashParams{
			ID:           userID,
			PasswordHash: hash,
		}); err != nil {
			return fmt.Errorf("update password hash: %w", err)
		}
		if err := q.DeleteSessionsForUser(ctx, userID); err != nil {
			return fmt.Errorf("delete sessions: %w", err)
		}
		return nil
	})
}

// PruneExpiredSessions deletes every session past its expires_at and
// reports how many rows it removed. Called by cmd/maestro's
// startPruneLoop, once at start-up and then once every pruneInterval for
// the life of the process (see that function's own doc comment for the
// interval and failure-handling decisions) — this method itself stays
// pure database bookkeeping with no process-lifecycle opinion of its own.
// An unpruned row is disk bloat, not a live security exposure:
// GetSessionUser already treats it as dead regardless of whether this has
// run recently, which is what makes calling it on a coarse interval
// (rather than eagerly, inline with every session read) a correctness-
// neutral choice.
func (s *Service) PruneExpiredSessions(ctx context.Context) (int64, error) {
	n, err := s.q.DeleteExpiredSessions(ctx)
	if err != nil {
		return 0, fmt.Errorf("prune expired sessions: %w", err)
	}
	return n, nil
}

// randomToken returns a base64url-encoded string carrying
// randomTokenBytes of cryptographic randomness. It is shared by every
// bearer token this package mints — session tokens (this file) and invite
// tokens (invites.go) alike.
func randomToken() (string, error) {
	raw := make([]byte, randomTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
