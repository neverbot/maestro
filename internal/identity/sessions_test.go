package identity_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/testutil"
)

func TestSessionLifecycle(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "designer@studio.com",
		DisplayName: "Designer",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	token, _, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	// A length check on the base64-encoded token is not the same as a
	// check on its underlying entropy: base64 expands 32 raw bytes to a
	// 43-character string, so "len(token) < 32" would still pass even if
	// the token carried far fewer than 32 bytes of randomness. Decode it
	// and check the raw byte count directly.
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token is not valid base64url: %v", err)
	}
	if len(raw) < 32 {
		t.Fatalf("token carries %d raw bytes of entropy, want at least 32", len(raw))
	}

	got, _, err := svc.UserForSession(ctx, token)
	if err != nil {
		t.Fatalf("UserForSession: %v", err)
	}
	if got.ID != user.ID {
		t.Fatal("UserForSession returned a different user")
	}

	if err := svc.RevokeSession(ctx, token); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, _, err := svc.UserForSession(ctx, token); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
}

func TestUnknownSessionToken(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	if _, _, err := svc.UserForSession(context.Background(), "not-a-real-token"); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
}

func TestIssueSessionSetsExpiryFromConfig(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "ttl@studio.com",
		DisplayName: "TTL",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	before := time.Now()
	token, expiresAt, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	after := time.Now()

	wantFrom := before.Add(cfg.SessionTTL)
	wantTo := after.Add(cfg.SessionTTL)
	if expiresAt.Before(wantFrom) || expiresAt.After(wantTo) {
		t.Fatalf("expiresAt = %v, want between %v and %v (a dropped pgtype.Timestamptz{Valid: true} would report the zero time)", expiresAt, wantFrom, wantTo)
	}

	// The expiry UserForSession reports back for the stored row must agree
	// with what IssueSession returned, within Postgres's timestamptz
	// precision.
	_, gotExpiry, err := svc.UserForSession(ctx, token)
	if err != nil {
		t.Fatalf("UserForSession: %v", err)
	}
	if diff := gotExpiry.Sub(expiresAt); diff < -time.Second || diff > time.Second {
		t.Fatalf("stored expiry = %v, want close to %v", gotExpiry, expiresAt)
	}
}

func TestIssueSessionProducesDistinctTokens(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "distinct@studio.com",
		DisplayName: "Distinct",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	tokenA, _, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	tokenB, _, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	if tokenA == tokenB {
		t.Fatal("two calls to IssueSession must not produce the same token")
	}
}

func TestRevokeUnknownSessionIsANoOp(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	if err := svc.RevokeSession(context.Background(), "not-a-real-token"); err != nil {
		t.Fatalf("RevokeSession on an unknown token must be a documented no-op, got %v", err)
	}
}

func TestRevokeAllSessions(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "multi@studio.com",
		DisplayName: "Multi",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	tokenA, _, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	tokenB, _, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	if err := svc.RevokeAllSessions(ctx, user.ID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	if _, _, err := svc.UserForSession(ctx, tokenA); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("tokenA err = %v, want ErrNoSession", err)
	}
	if _, _, err := svc.UserForSession(ctx, tokenB); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("tokenB err = %v, want ErrNoSession", err)
	}
}

func TestRevokeAllSessionsLeavesOtherUsersAlone(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	userA, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "a@studio.com", DisplayName: "A", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser A: %v", err)
	}
	userB, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "b@studio.com", DisplayName: "B", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser B: %v", err)
	}

	tokenA, _, err := svc.IssueSession(ctx, userA.ID)
	if err != nil {
		t.Fatalf("IssueSession A: %v", err)
	}
	tokenB, _, err := svc.IssueSession(ctx, userB.ID)
	if err != nil {
		t.Fatalf("IssueSession B: %v", err)
	}

	if err := svc.RevokeAllSessions(ctx, userA.ID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	if _, _, err := svc.UserForSession(ctx, tokenA); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("userA's session err = %v, want ErrNoSession", err)
	}
	if _, _, err := svc.UserForSession(ctx, tokenB); err != nil {
		t.Fatalf("userB's session must be untouched, got %v", err)
	}
}

func TestExtendSessionPushesExpiryForward(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "extend@studio.com",
		DisplayName: "Extend",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	token, firstExpiry, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	newExpiry := firstExpiry.Add(24 * time.Hour)
	if err := svc.ExtendSession(ctx, token, newExpiry); err != nil {
		t.Fatalf("ExtendSession: %v", err)
	}

	_, gotExpiry, err := svc.UserForSession(ctx, token)
	if err != nil {
		t.Fatalf("UserForSession: %v", err)
	}
	if diff := gotExpiry.Sub(newExpiry); diff < -time.Second || diff > time.Second {
		t.Fatalf("session expiry = %v, want close to %v", gotExpiry, newExpiry)
	}
}

func TestChangePasswordRotatesHashAndRevokesSessions(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "rotate@studio.com",
		DisplayName: "Rotate",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	token, _, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	if err := svc.ChangePassword(ctx, user.ID, "newpassword12345"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	if _, err := svc.Authenticate(ctx, "rotate@studio.com", "password12345"); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("old password should no longer authenticate, err = %v", err)
	}
	if _, err := svc.Authenticate(ctx, "rotate@studio.com", "newpassword12345"); err != nil {
		t.Fatalf("new password should authenticate: %v", err)
	}
	if _, _, err := svc.UserForSession(ctx, token); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("session issued before ChangePassword should be revoked, err = %v", err)
	}
}

func TestChangePasswordRejectsWeakPassword(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "weak@studio.com",
		DisplayName: "Weak",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := svc.ChangePassword(ctx, user.ID, "short"); !errors.Is(err, identity.ErrPasswordInvalid) {
		t.Fatalf("err = %v, want ErrPasswordInvalid", err)
	}
}

func TestExpiredSessionIsRejectedAndPruned(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "stale@studio.com",
		DisplayName: "Stale",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// IssueSession always mints a session s.cfg.SessionTTL in the future,
	// so an already-expired session has to be inserted directly to
	// exercise the expires_at > now() branch of GetSessionUser and
	// PruneExpiredSessions.
	tokenHash := sha256.Sum256([]byte("expired-token"))
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash[:], user.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("insert expired session: %v", err)
	}

	if _, _, err := svc.UserForSession(ctx, "expired-token"); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession for an expired session", err)
	}

	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, user.ID).Scan(&before); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if before != 1 {
		t.Fatalf("expected the expired session row to still exist before pruning, got %d rows", before)
	}

	n, err := svc.PruneExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("PruneExpiredSessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("PruneExpiredSessions reported %d rows deleted, want 1", n)
	}

	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, user.ID).Scan(&after); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if after != 0 {
		t.Fatalf("expected PruneExpiredSessions to delete the expired row, got %d rows remaining", after)
	}
}

// TestDeletingUserCascadesSessions guards a revocation guarantee this
// package leans on but does not itself enforce: deleting a user's row must
// take its sessions with it. That guarantee lives in the sessions table's
// "ON DELETE CASCADE" foreign key in the migration, not in any Go code
// here, so nothing in this package's own logic would catch it if the
// migration ever lost that clause.
func TestDeletingUserCascadesSessions(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "cascade@studio.com",
		DisplayName: "Cascade",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, _, err := svc.IssueSession(ctx, user.ID); err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, user.ID).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected deleting the user to cascade-delete its sessions, got %d rows remaining", n)
	}
}
