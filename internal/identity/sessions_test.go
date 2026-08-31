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

	token, err := svc.IssueSession(ctx, user.ID)
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

	got, err := svc.UserForSession(ctx, token)
	if err != nil {
		t.Fatalf("UserForSession: %v", err)
	}
	if got.ID != user.ID {
		t.Fatal("UserForSession returned a different user")
	}

	if err := svc.RevokeSession(ctx, token); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, err := svc.UserForSession(ctx, token); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
}

func TestUnknownSessionToken(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	if _, err := svc.UserForSession(context.Background(), "not-a-real-token"); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
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

	tokenA, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	tokenB, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	if err := svc.RevokeAllSessions(ctx, user.ID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	if _, err := svc.UserForSession(ctx, tokenA); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("tokenA err = %v, want ErrNoSession", err)
	}
	if _, err := svc.UserForSession(ctx, tokenB); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("tokenB err = %v, want ErrNoSession", err)
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

	// IssueSession always mints a session SessionTTL in the future, so an
	// already-expired session has to be inserted directly to exercise the
	// expires_at > now() branch of GetSessionUser and PruneExpiredSessions.
	tokenHash := sha256.Sum256([]byte("expired-token"))
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash[:], user.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("insert expired session: %v", err)
	}

	if _, err := svc.UserForSession(ctx, "expired-token"); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession for an expired session", err)
	}

	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, user.ID).Scan(&before); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if before != 1 {
		t.Fatalf("expected the expired session row to still exist before pruning, got %d rows", before)
	}

	if err := svc.PruneExpiredSessions(ctx); err != nil {
		t.Fatalf("PruneExpiredSessions: %v", err)
	}

	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, user.ID).Scan(&after); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if after != 0 {
		t.Fatalf("expected PruneExpiredSessions to delete the expired row, got %d rows remaining", after)
	}
}
