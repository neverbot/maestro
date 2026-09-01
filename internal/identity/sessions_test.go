package identity_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"sync/atomic"
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

// TestChangePasswordRevokesEverySessionOfTheAccount pins ChangePassword's
// own multi-session claim directly: a designer with several open tabs
// (several live sessions) who rotates their password loses every one of
// them, not just the one TestChangePasswordRotatesHashAndRevokesSessions
// already covers.
func TestChangePasswordRevokesEverySessionOfTheAccount(t *testing.T) {
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

	if err := svc.ChangePassword(ctx, user.ID, "newpassword12345"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	if _, _, err := svc.UserForSession(ctx, tokenA); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("tokenA err = %v, want ErrNoSession", err)
	}
	if _, _, err := svc.UserForSession(ctx, tokenB); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("tokenB err = %v, want ErrNoSession", err)
	}
}

// TestChangePasswordLeavesOtherUsersSessionsAlone pins the other half:
// ChangePassword's DeleteSessionsForUser is scoped to one user, not a
// blanket wipe.
func TestChangePasswordLeavesOtherUsersSessionsAlone(t *testing.T) {
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

	if err := svc.ChangePassword(ctx, userA.ID, "newpassword12345"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	if _, _, err := svc.UserForSession(ctx, tokenA); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("userA's session err = %v, want ErrNoSession", err)
	}
	if _, _, err := svc.UserForSession(ctx, tokenB); err != nil {
		t.Fatalf("userB's session must be untouched, got %v", err)
	}
}

// TestExtendSessionPushesExpiryForward pins the value ExtendSession
// writes: roughly now() + ttl. The session is backdated first so the
// query's own one-minute slack (identity.sql's doc comment on
// ExtendSession explains why it exists) does not treat it as already
// fresh enough to skip — a session within a minute of now()+ttl already
// is exactly the case this method is supposed to leave alone.
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

	token, _, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	tokenHash := sha256.Sum256([]byte(token))
	if _, err := pool.Exec(ctx, `UPDATE sessions SET expires_at = $1 WHERE token_hash = $2`,
		time.Now().Add(-time.Hour), tokenHash[:]); err != nil {
		t.Fatalf("backdate expires_at: %v", err)
	}

	ttl := 24 * time.Hour
	before := time.Now()
	n, err := svc.ExtendSession(ctx, token, ttl)
	after := time.Now()
	if err != nil {
		t.Fatalf("ExtendSession: %v", err)
	}
	if n != 1 {
		t.Fatalf("ExtendSession rows affected = %d, want 1", n)
	}

	_, gotExpiry, err := svc.UserForSession(ctx, token)
	if err != nil {
		t.Fatalf("UserForSession: %v", err)
	}
	// The target is computed by Postgres' own now(), not the Go process',
	// so the bracket needs a little slack for clock skew between the two
	// — a real skew of a few milliseconds is expected; a bug that renews
	// to the wrong offset (half the ttl, say) misses by hours and still
	// fails this.
	const clockSkewSlack = 2 * time.Second
	wantMin, wantMax := before.Add(ttl-clockSkewSlack), after.Add(ttl+clockSkewSlack)
	if gotExpiry.Before(wantMin) || gotExpiry.After(wantMax) {
		t.Fatalf("session expiry = %v, want between %v and %v (now + ttl, bracketed around the call)", gotExpiry, wantMin, wantMax)
	}
}

// TestConcurrentRenewalsProduceExactlyOneWrite pins the property
// ExtendSession's doc comment (and identity.sql's) claims and an earlier
// version of the guard did not actually deliver: several concurrent
// renewal attempts against the same session collapse into a single
// write, not one write per attempt. A quality review proved the earlier
// version wrong by holding the row lock open with pg_sleep and by firing
// fifteen concurrent requests at the real binary (three to six writes
// each run, not one) — and found that a naive "read the final
// expires_at" test would have stayed green throughout, because the
// final value looks identical whether one write happened or six.
//
// This calls ExtendSession directly, concurrently, rather than driving
// it through resolveSessionCaller over HTTP: going through the
// middleware adds a second source of nondeterminism this test does not
// want — UserForSession's own read (no row lock) can itself observe an
// already-renewed expires_at and skip calling ExtendSession at all once
// any one request's write has landed, which is a legitimate reason for
// fewer than N attempts and not what this test is trying to pin. Calling
// ExtendSession unconditionally, N times, concurrently, against one
// already-stale session isolates the SQL guard's own concurrency
// property from that Go-side decision.
//
// RowsAffected (this method's own return value, via :execrows — see its
// doc comment) is summed across every goroutine rather than reading
// expires_at afterward, so the assertion is a direct count of writes
// Postgres reports, not an inference from a value that cannot tell one
// write apart from several.
func TestConcurrentRenewalsProduceExactlyOneWrite(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "concurrent@studio.com",
		DisplayName: "Concurrent",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	token, _, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	tokenHash := sha256.Sum256([]byte(token))
	if _, err := pool.Exec(ctx, `UPDATE sessions SET expires_at = $1 WHERE token_hash = $2`,
		time.Now().Add(-time.Hour), tokenHash[:]); err != nil {
		t.Fatalf("backdate expires_at: %v", err)
	}

	const attempts = 15
	var wg sync.WaitGroup
	var totalRowsAffected atomic.Int64
	errs := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := svc.ExtendSession(ctx, token, 24*time.Hour)
			if err != nil {
				errs <- err
				return
			}
			totalRowsAffected.Add(n)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("ExtendSession: %v", err)
	}

	if got := totalRowsAffected.Load(); got != 1 {
		t.Fatalf("total rows affected across %d concurrent ExtendSession calls = %d, want exactly 1", attempts, got)
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

func TestChangeOwnPasswordSucceedsAndRevokesSessions(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "self-rotate@studio.com",
		DisplayName: "Self Rotate",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	token, _, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	if err := svc.ChangeOwnPassword(ctx, user.ID, "password12345", "newpassword12345"); err != nil {
		t.Fatalf("ChangeOwnPassword: %v", err)
	}

	if _, err := svc.Authenticate(ctx, "self-rotate@studio.com", "newpassword12345"); err != nil {
		t.Fatalf("new password should authenticate: %v", err)
	}
	if _, _, err := svc.UserForSession(ctx, token); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("session issued before ChangeOwnPassword should be revoked, err = %v", err)
	}
}

// TestChangeOwnPasswordRejectsWrongCurrentPassword pins this method's
// entire reason for existing: a caller who does not know the account's
// actual password — a live session with a stolen cookie, say — must not
// be able to rotate it, even though the session itself already
// authenticates the request.
func TestChangeOwnPasswordRejectsWrongCurrentPassword(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "stolen-session@studio.com",
		DisplayName: "Stolen Session",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	token, _, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	if err := svc.ChangeOwnPassword(ctx, user.ID, "wrongpassword", "newpassword12345"); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}

	// The password must be unchanged and the session must still be live —
	// a rejected attempt must have no side effect at all.
	if _, err := svc.Authenticate(ctx, "stolen-session@studio.com", "password12345"); err != nil {
		t.Fatalf("original password should still authenticate: %v", err)
	}
	if _, _, err := svc.UserForSession(ctx, token); err != nil {
		t.Fatalf("session must survive a rejected password-change attempt, got %v", err)
	}
}

func TestChangeOwnPasswordRejectsWeakNewPassword(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "weak-new@studio.com",
		DisplayName: "Weak New",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := svc.ChangeOwnPassword(ctx, user.ID, "password12345", "short"); !errors.Is(err, identity.ErrPasswordInvalid) {
		t.Fatalf("err = %v, want ErrPasswordInvalid", err)
	}
}

// TestChangeOwnPasswordRejectsSamePassword pins the "rotating to the
// same password" gap a Round 2 review found: without this check,
// resubmitting the current password as the new one succeeded and
// revoked every other session for a change that took no effect at all.
func TestChangeOwnPasswordRejectsSamePassword(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "same-password@studio.com",
		DisplayName: "Same Password",
		Password:    "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	token, _, err := svc.IssueSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	if err := svc.ChangeOwnPassword(ctx, user.ID, "password12345", "password12345"); !errors.Is(err, identity.ErrPasswordUnchanged) {
		t.Fatalf("err = %v, want ErrPasswordUnchanged", err)
	}

	// A rejected no-op must have no side effect: the session survives.
	if _, _, err := svc.UserForSession(ctx, token); err != nil {
		t.Fatalf("session must survive a rejected no-op change, got %v", err)
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
