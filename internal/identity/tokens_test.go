package identity_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
)

func TestAPITokenResolvesToItsProject(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)

	token, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: project.ID,
		UserID:    user.ID,
		Label:     "seed agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if !strings.HasPrefix(token, identity.TokenPrefix) {
		t.Fatalf("token = %q, want the %s prefix", token, identity.TokenPrefix)
	}
	if tok.Label != "seed agent" {
		t.Fatalf("label = %q", tok.Label)
	}
	if tok.ProjectID != project.ID || tok.UserID != user.ID {
		t.Fatal("the returned token is bound to the wrong project or user")
	}

	resolved, err := ids.ResolveAPIToken(ctx, token)
	if err != nil {
		t.Fatalf("ResolveAPIToken: %v", err)
	}
	if resolved.ProjectID != project.ID || resolved.UserID != user.ID {
		t.Fatal("the token resolved to the wrong project or user")
	}

	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: project.ID, TokenID: tok.ID}); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, err := ids.ResolveAPIToken(ctx, token); !errors.Is(err, identity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid after revocation", err)
	}
}

func TestUnknownAPIToken(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	if _, err := ids.ResolveAPIToken(context.Background(), "mst_nonsense"); !errors.Is(err, identity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestCreateAPITokenGeneratesUniqueTokens(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)

	first, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "one"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	second, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "two"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if first == second {
		t.Fatal("two tokens minted the same value")
	}
}

func TestCreateAPITokenRejectsInvalidLabel(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)

	if _, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: ""}); !errors.Is(err, identity.ErrTokenRequestInvalid) {
		t.Fatalf("err = %v, want ErrTokenRequestInvalid for an empty label", err)
	}

	tooLong := strings.Repeat("a", 201)
	if _, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: tooLong}); !errors.Is(err, identity.ErrTokenRequestInvalid) {
		t.Fatalf("err = %v, want ErrTokenRequestInvalid for an over-long label", err)
	}
}

func TestRevokeAPITokenIsScopedToItsOwnProject(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	mine, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	theirs, _ := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID)

	token, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: mine.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// Revoking the token through the wrong project must not touch it.
	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: theirs.ID, TokenID: tok.ID}); err != nil {
		t.Fatalf("RevokeAPIToken (wrong project): %v", err)
	}
	if _, err := ids.ResolveAPIToken(ctx, token); err != nil {
		t.Fatalf("token was revoked through a project that does not own it: %v", err)
	}

	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: mine.ID, TokenID: tok.ID}); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, err := ids.ResolveAPIToken(ctx, token); !errors.Is(err, identity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid after revocation", err)
	}
}

func TestListAPITokensIsScopedToOneProjectAndOmitsTheHash(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	mine, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	theirs, _ := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID)

	_, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: mine.ID, UserID: user.ID, Label: "seed agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if _, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: theirs.ID, UserID: user.ID, Label: "other project's agent"}); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	list, err := ids.ListAPITokens(ctx, mine.ID)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if list[0].ID != tok.ID || list[0].Label != "seed agent" {
		t.Fatalf("list[0] = %+v, want the seed agent token", list[0])
	}
}

// TestResolveAPITokenThrottlesLastUsedAtWrites pins the *effect* of the
// throttle — the column doesn't move on an immediate re-resolve, and does
// move once the window has passed — which is what TouchAPIToken's SQL
// WHERE clause (identity.sql) guarantees on its own. It does NOT, by
// itself, prove the Go-side pre-check in ResolveAPIToken exists: removing
// that pre-check and always issuing TouchAPIToken leaves this test green,
// because the SQL predicate still blocks the write it doesn't need to
// make. See TestResolveAPITokenIssuesOneStatementInsideTheThrottleWindow
// AndTwoOutsideIt below for the test that pins the Go-side check
// specifically, by counting round trips rather than reading a value. This
// reads the column directly with the pool rather than trusting the value
// ResolveAPIToken itself returns (which reflects the row as read before
// the touch, not after it) so it observes exactly what a refactor that
// dropped the throttle condition would change: whether the column moves
// on every call or only when it should.
func TestResolveAPITokenThrottlesLastUsedAtWrites(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	token, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	lastUsedAt := func() time.Time {
		t.Helper()
		var ts time.Time
		if err := pool.QueryRow(ctx, `SELECT last_used_at FROM api_tokens WHERE id = $1`, tok.ID).Scan(&ts); err != nil {
			t.Fatalf("read last_used_at: %v", err)
		}
		return ts
	}

	if _, err := ids.ResolveAPIToken(ctx, token); err != nil {
		t.Fatalf("ResolveAPIToken: %v", err)
	}
	first := lastUsedAt()
	if first.IsZero() {
		t.Fatal("last_used_at was not set on the first resolve")
	}

	if _, err := ids.ResolveAPIToken(ctx, token); err != nil {
		t.Fatalf("ResolveAPIToken: %v", err)
	}
	if second := lastUsedAt(); !second.Equal(first) {
		t.Fatalf("last_used_at moved on an immediate re-resolve: %v -> %v, want unchanged (throttled)", first, second)
	}

	// Backdate past the five-minute throttle window directly, the same
	// way TestExpiredSessionIsRejectedAndPruned (sessions_test.go)
	// backdates a session's expires_at: this exercises TouchAPIToken's
	// write branch deterministically, without a test that waits on a
	// real clock.
	if _, err := pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = $1 WHERE id = $2`, time.Now().Add(-6*time.Minute), tok.ID); err != nil {
		t.Fatalf("backdate last_used_at: %v", err)
	}

	if _, err := ids.ResolveAPIToken(ctx, token); err != nil {
		t.Fatalf("ResolveAPIToken: %v", err)
	}
	if third := lastUsedAt(); !third.After(first) {
		t.Fatalf("last_used_at did not advance once the throttle window had passed: %v -> %v", first, third)
	}
}

// statementCounter is a pgx.QueryTracer that counts statements (Query,
// QueryRow, and Exec calls) issued through the connection it is attached
// to. It exists only for
// TestResolveAPITokenIssuesOneStatementInsideTheThrottleWindowAndTwoOutsideIt,
// below, to count round trips directly rather than infer them from a
// value that a value-only test cannot tell apart from "the SQL predicate
// alone did the job".
type statementCounter struct {
	n atomic.Int64
}

func (c *statementCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}

func (c *statementCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *statementCounter) reset()       { c.n.Store(0) }
func (c *statementCounter) count() int64 { return c.n.Load() }

// TestResolveAPITokenIssuesOneStatementInsideTheThrottleWindowAndTwoOutsideIt
// is the test TestResolveAPITokenThrottlesLastUsedAtWrites cannot be: that
// test observes last_used_at's value, which TouchAPIToken's own SQL WHERE
// clause already guarantees on its own — deleting the Go-side pre-check in
// ResolveAPIToken (the one Round 2 Correction 10 added, in tokens.go)
// leaves that test green, because the query still gets issued and still
// updates nothing. The only observable difference the Go-side check makes
// is how many statements go out over the wire: one (GetLiveAPIToken only)
// when the pre-check decides TouchAPIToken isn't worth calling, two
// (GetLiveAPIToken and TouchAPIToken, the latter still a no-op via its own
// WHERE clause) when it wasn't there to decide that, or was there and
// decided wrong. Counting round trips with a pgx.QueryTracer is the only
// way to see that distinction from outside the package.
//
// This attaches the tracer to a second pool built from testutil's own
// pool config (same database, same connection string), rather than the
// pool CreateAPIToken and the backdating UPDATE below use directly, so
// only the statements ResolveAPIToken itself issues are counted — setup
// and the deliberate backdate are excluded on purpose, not by luck.
func TestResolveAPITokenIssuesOneStatementInsideTheThrottleWindowAndTwoOutsideIt(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	token, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// A freshly minted token has no last_used_at at all (NULL), so its very
	// first resolve always touches regardless of the Go-side pre-check —
	// that branch is "missing", not "stale", and both trigger a write. This
	// warm-up resolve, through the untraced pool, gets last_used_at off
	// NULL and onto a fresh timestamp before the counting below starts, so
	// the counted "inside window" resolve is actually exercising the
	// pre-check's stale-vs-fresh comparison, not its missing-value branch.
	if _, err := ids.ResolveAPIToken(ctx, token); err != nil {
		t.Fatalf("warm-up ResolveAPIToken: %v", err)
	}

	tracer := &statementCounter{}
	tracedCfg := pool.Config()
	tracedCfg.ConnConfig.Tracer = tracer
	tracedPool, err := pgxpool.NewWithConfig(ctx, tracedCfg)
	if err != nil {
		t.Fatalf("open traced pool: %v", err)
	}
	t.Cleanup(tracedPool.Close)
	tracedIDs := identity.New(tracedPool, testConfig())

	tracer.reset()
	if _, err := tracedIDs.ResolveAPIToken(ctx, token); err != nil {
		t.Fatalf("ResolveAPIToken (inside window): %v", err)
	}
	if got := tracer.count(); got != 1 {
		t.Fatalf("issued %d statements inside the throttle window, want 1 (GetLiveAPIToken only — the Go-side pre-check should have skipped TouchAPIToken entirely)", got)
	}

	// Backdate directly through the untraced pool, so this write is not
	// itself counted.
	if _, err := pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = $1 WHERE id = $2`, time.Now().Add(-6*time.Minute), tok.ID); err != nil {
		t.Fatalf("backdate last_used_at: %v", err)
	}

	tracer.reset()
	if _, err := tracedIDs.ResolveAPIToken(ctx, token); err != nil {
		t.Fatalf("ResolveAPIToken (outside window): %v", err)
	}
	if got := tracer.count(); got != 2 {
		t.Fatalf("issued %d statements outside the throttle window, want 2 (GetLiveAPIToken, then TouchAPIToken)", got)
	}
}

func TestRevokedTokenIsIndistinguishableFromUnknown(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	token, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: project.ID, TokenID: tok.ID}); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}

	_, revokedErr := ids.ResolveAPIToken(ctx, token)
	_, unknownErr := ids.ResolveAPIToken(ctx, "mst_totallyunknown")
	if !errors.Is(revokedErr, identity.ErrTokenInvalid) || !errors.Is(unknownErr, identity.ErrTokenInvalid) {
		t.Fatalf("revokedErr = %v, unknownErr = %v, want both ErrTokenInvalid", revokedErr, unknownErr)
	}
}

// TestResolveAPITokenReturnsTheCorrectProjectAmongMany guards against a
// weaker version of this file's very first test: with only one project
// in the database, an assertion that a resolved token names "the"
// project passes even if ResolveAPIToken silently dropped project
// scoping altogether. This creates two games, a token in each, and
// checks each resolves to its own binding — the one this package's
// isolation invariant is actually about.
func TestResolveAPITokenReturnsTheCorrectProjectAmongMany(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	azeroth, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	leMans, _ := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID)

	azerothToken, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: azeroth.ID, UserID: user.ID, Label: "azeroth agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken (azeroth): %v", err)
	}
	leMansToken, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: leMans.ID, UserID: user.ID, Label: "le mans agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken (le mans): %v", err)
	}

	resolvedAzeroth, err := ids.ResolveAPIToken(ctx, azerothToken)
	if err != nil {
		t.Fatalf("ResolveAPIToken (azeroth): %v", err)
	}
	if resolvedAzeroth.ProjectID != azeroth.ID {
		t.Fatalf("azeroth token resolved to project %v, want %v", resolvedAzeroth.ProjectID, azeroth.ID)
	}

	resolvedLeMans, err := ids.ResolveAPIToken(ctx, leMansToken)
	if err != nil {
		t.Fatalf("ResolveAPIToken (le mans): %v", err)
	}
	if resolvedLeMans.ProjectID != leMans.ID {
		t.Fatalf("le mans token resolved to project %v, want %v", resolvedLeMans.ProjectID, leMans.ID)
	}
}

func TestRevokeUnknownAPITokenIsANoOp(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)

	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: project.ID, TokenID: uuid.New()}); err != nil {
		t.Fatalf("RevokeAPIToken of an unknown id: %v", err)
	}
}

func TestRevokeAlreadyRevokedAPITokenIsANoOp(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	_, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	req := identity.RevokeAPITokenRequest{ProjectID: project.ID, TokenID: tok.ID}
	if err := ids.RevokeAPIToken(ctx, req); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}

	if err := ids.RevokeAPIToken(ctx, req); err != nil {
		t.Fatalf("RevokeAPIToken of an already-revoked token: %v", err)
	}
}

// TestResolveAPITokenRejectsTamperedTokenWithoutTouchingTheDatabase
// exercises the checksum newTokenBody appends to every minted token:
// flipping one character in the body must be caught by
// verifyTokenChecksum, before ResolveAPIToken ever hashes the value or
// queries api_tokens. This can't observe "no query happened" directly
// from outside the package, but ErrTokenInvalid for a value whose prefix
// and length look right, and that was never itself minted or revoked,
// is exactly the outcome that check exists to produce.
func TestResolveAPITokenRejectsTamperedTokenWithoutTouchingTheDatabase(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// Flip the last character of the token, inside its checksum suffix.
	tampered := token[:len(token)-1]
	if token[len(token)-1] == 'a' {
		tampered += "b"
	} else {
		tampered += "a"
	}

	if _, err := ids.ResolveAPIToken(ctx, tampered); !errors.Is(err, identity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid for a tampered token", err)
	}
}

// TestCheckAPITokenNeverTouchesLastUsedAt pins CheckAPIToken's whole
// reason for existing: unlike ResolveAPIToken, it must never move
// last_used_at, no matter how many times it is called or how far past
// touchThrottle the previous write was. internal/web/events.go's SSE
// heartbeat re-check calls this, not ResolveAPIToken, specifically so an
// open browser tab does nothing to keep a token looking recently used.
func TestCheckAPITokenNeverTouchesLastUsedAt(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	token, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	lastUsedAt := func() (time.Time, bool) {
		t.Helper()
		var ts *time.Time
		if err := pool.QueryRow(ctx, `SELECT last_used_at FROM api_tokens WHERE id = $1`, tok.ID).Scan(&ts); err != nil {
			t.Fatalf("read last_used_at: %v", err)
		}
		if ts == nil {
			return time.Time{}, false
		}
		return *ts, true
	}

	if _, ok := lastUsedAt(); ok {
		t.Fatal("last_used_at was already set before any resolve")
	}

	for i := 0; i < 3; i++ {
		if _, err := ids.CheckAPIToken(ctx, token); err != nil {
			t.Fatalf("CheckAPIToken: %v", err)
		}
	}
	if _, ok := lastUsedAt(); ok {
		t.Fatal("CheckAPIToken set last_used_at — it must be read-only")
	}

	// Even well past touchThrottle, still no write.
	if _, err := pool.Exec(ctx, `UPDATE api_tokens SET created_at = created_at - interval '1 hour' WHERE id = $1`, tok.ID); err != nil {
		t.Fatalf("backdate created_at: %v", err)
	}
	if _, err := ids.CheckAPIToken(ctx, token); err != nil {
		t.Fatalf("CheckAPIToken: %v", err)
	}
	if _, ok := lastUsedAt(); ok {
		t.Fatal("CheckAPIToken set last_used_at after backdating — it must be read-only")
	}
}
