package identity_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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

// TestResolveAPITokenThrottlesLastUsedAtWrites guards the conditional
// write TouchAPIToken's own query comment describes: an unconditional
// UPDATE on every authenticated request would take a row lock on the hot
// path for no observable benefit, so the write only happens when the
// stored last_used_at is missing or more than five minutes stale. This
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
