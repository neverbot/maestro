package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

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

// SessionTTL is how long a login lasts.
const SessionTTL = 30 * 24 * time.Hour

// sessionTokenBytes is the amount of raw entropy in a session token, before
// base64 encoding. 32 bytes (256 bits) is far beyond what is guessable;
// encoding expands this to a longer string, so a length check on the
// encoded token is not itself a check on this constant — see the tests.
const sessionTokenBytes = 32

// IssueSession mints a session token and stores only its hash. The token
// itself is returned once, for the caller to set as an httpOnly cookie
// value; nothing else in this package ever holds it again.
func (s *Service) IssueSession(ctx context.Context, userID uuid.UUID) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(token))
	err = s.q.CreateSession(ctx, dbq.CreateSessionParams{
		TokenHash: sum[:],
		UserID:    userID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(SessionTTL), Valid: true},
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

// UserForSession resolves a session token to its user. It returns the
// domain User type, not dbq.User: the identity package's public surface
// never hands out the generated row type, so nothing downstream is ever
// one careless writeJSON away from serving PasswordHash back to a browser
// (see the doc comment on User in users.go).
func (s *Service) UserForSession(ctx context.Context, token string) (User, error) {
	sum := sha256.Sum256([]byte(token))
	dbUser, err := s.q.GetSessionUser(ctx, sum[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNoSession
		}
		return User{}, fmt.Errorf("lookup session: %w", err)
	}
	return userFrom(dbUser), nil
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

// RevokeAllSessions drops every session of a user, used on password change
// so that a stolen or shared login is invalidated everywhere at once.
func (s *Service) RevokeAllSessions(ctx context.Context, userID uuid.UUID) error {
	if err := s.q.DeleteSessionsForUser(ctx, userID); err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	return nil
}

// PruneExpiredSessions deletes every session past its expires_at. Nothing
// in this task calls it: it exists for a future periodic job (a ticker in
// main, or an operator-run maintenance command) to keep the sessions table
// from growing forever with rows that GetSessionUser already treats as
// dead. Wiring that scheduler is out of this task's scope — the HTTP and
// process-lifecycle layers arrive in later tasks.
func (s *Service) PruneExpiredSessions(ctx context.Context) error {
	if err := s.q.DeleteExpiredSessions(ctx); err != nil {
		return fmt.Errorf("prune expired sessions: %w", err)
	}
	return nil
}

// randomToken returns a base64url-encoded string carrying
// sessionTokenBytes of cryptographic randomness.
func randomToken() (string, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
