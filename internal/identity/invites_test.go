package identity_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/testutil"
)

func TestInviteRedemptionCreatesUser(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	// CreatedBy only records who minted the invite; nothing in this flow
	// checks IsAdmin, and CreateUser can no longer produce an admin account
	// anyway — that authorization check belongs to the HTTP handler in
	// Task 11/12, not to this service.
	creator, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "boss@studio.com", DisplayName: "Boss", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	token, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "new@studio.com", CreatedBy: &creator.ID})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	user, err := svc.RedeemInvite(ctx, token, "new@studio.com", "Newcomer", "password12345")
	if err != nil {
		t.Fatalf("RedeemInvite: %v", err)
	}
	if user.Email != "new@studio.com" {
		t.Fatalf("Email = %q", user.Email)
	}

	// A token is single use.
	_, err = svc.RedeemInvite(ctx, token, "other@studio.com", "Other", "password12345")
	if !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
}

func TestInviteBoundToEmailRejectsAnother(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	token, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "expected@studio.com"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	_, err = svc.RedeemInvite(ctx, token, "someone.else@studio.com", "Sneaky", "password12345")
	if !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
}

// TestInviteEmailComparisonNormalisesLikeUsers guards against the bound
// email being compared with different normalisation than the users table
// uses (users are keyed on lower(email)). Without normalising both sides
// the same way, an invite created for "New@Studio.com" would reject a
// redemption for "new@studio.com" even though that is the same account
// CreateUser would produce.
func TestInviteEmailComparisonNormalisesLikeUsers(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	token, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "  New@Studio.com  "})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	user, err := svc.RedeemInvite(ctx, token, "NEW@studio.com", "Newcomer", "password12345")
	if err != nil {
		t.Fatalf("RedeemInvite: %v", err)
	}
	if user.Email != "new@studio.com" {
		t.Fatalf("Email = %q, want new@studio.com", user.Email)
	}
}

func TestRedeemUnknownInvite(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	_, err := svc.RedeemInvite(context.Background(), "made-up", "x@studio.com", "X", "password12345")
	if !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
}

// TestInviteWithProjectGrantsMembership covers the invite's optional second
// grant: when a project and role are attached, redemption must both create
// the account and add it to the project at that role, not just the
// account.
func TestInviteWithProjectGrantsMembership(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	projectID := createTestProject(t, ctx, pool, "castle-quest")

	token, err := svc.CreateInvite(ctx, identity.InviteRequest{
		Email:     "designer@studio.com",
		ProjectID: &projectID,
		Role:      "editor",
	})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	user, err := svc.RedeemInvite(ctx, token, "designer@studio.com", "Designer", "password12345")
	if err != nil {
		t.Fatalf("RedeemInvite: %v", err)
	}

	var role string
	err = pool.QueryRow(ctx, `SELECT role FROM memberships WHERE user_id = $1 AND project_id = $2`, user.ID, projectID).Scan(&role)
	if err != nil {
		t.Fatalf("query membership: %v", err)
	}
	if role != "editor" {
		t.Fatalf("role = %q, want editor", role)
	}
}

// TestCreateInviteRejectsRoleWithoutProject and its sibling below guard
// against the database's own
// `CHECK ((project_id IS NULL) = (role IS NULL))` surfacing as a raw
// constraint-violation error instead of a clear, typed one: an admin
// building an invite through this service should learn what is wrong from
// Go, not from Postgres.
func TestCreateInviteRejectsRoleWithoutProject(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	_, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "x@studio.com", Role: "editor"})
	if !errors.Is(err, identity.ErrInviteRequestInvalid) {
		t.Fatalf("err = %v, want ErrInviteRequestInvalid", err)
	}
}

func TestCreateInviteRejectsProjectWithoutRole(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	projectID := createTestProject(t, ctx, pool, "no-role")
	_, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "x@studio.com", ProjectID: &projectID})
	if !errors.Is(err, identity.ErrInviteRequestInvalid) {
		t.Fatalf("err = %v, want ErrInviteRequestInvalid", err)
	}
}

// TestCreateInviteRejectsUnknownRole guards against an invite's role
// drifting out of sync with the set the memberships table actually accepts
// (owner, editor, viewer): a role this service happily stored but
// memberships rejected would only surface as a broken redemption, deep
// inside RedeemInvite's transaction.
func TestCreateInviteRejectsUnknownRole(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	projectID := createTestProject(t, ctx, pool, "bad-role")
	_, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "x@studio.com", ProjectID: &projectID, Role: "superadmin"})
	if !errors.Is(err, identity.ErrInviteRequestInvalid) {
		t.Fatalf("err = %v, want ErrInviteRequestInvalid", err)
	}
}

// TestRedeemInviteConcurrentDoubleRedemptionIsRejected guards the property
// that matters most about a single-use token: under concurrent use of the
// very same token, exactly one redemption may succeed. Without a row lock
// inside the transaction that marks the invite redeemed, two concurrent
// callers can both observe the invite as live and both create an account
// from it.
func TestRedeemInviteConcurrentDoubleRedemptionIsRejected(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	token, err := svc.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	// Each attempt redeems with its own distinct email. This matters: the
	// invite itself is unbound, so nothing here relies on the users table's
	// email-uniqueness constraint to reject a would-be second redemption —
	// that constraint is exactly what must NOT be doing this test's job.
	// The only thing that may stop a second, differently-addressed
	// redemption of the same token is the invite-row lock inside
	// RedeemInvite's transaction (see its doc comment).
	const attempts = 8
	var wg sync.WaitGroup
	successes := make(chan identity.User, attempts)
	failures := make(chan error, attempts)

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			email := fmt.Sprintf("racer%d@studio.com", i)
			user, err := svc.RedeemInvite(ctx, token, email, "Racer", "password12345")
			if err != nil {
				failures <- err
				return
			}
			successes <- user
		}(i)
	}
	wg.Wait()
	close(successes)
	close(failures)

	var users []identity.User
	for u := range successes {
		users = append(users, u)
	}
	if len(users) != 1 {
		t.Fatalf("got %d successful redemptions, want exactly 1", len(users))
	}
	for err := range failures {
		if !errors.Is(err, identity.ErrInviteInvalid) {
			t.Fatalf("losing redemption err = %v, want ErrInviteInvalid", err)
		}
	}

	var userCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email LIKE 'racer%@studio.com'`).Scan(&userCount); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("users created = %d, want exactly 1", userCount)
	}
}

// TestRedeemExpiredInviteIsRejectedAndPruned mirrors sessions_test.go's
// TestExpiredSessionIsRejectedAndPruned: an invite past its expires_at must
// be indistinguishable from an unknown one to RedeemInvite (see
// ErrInviteInvalid's doc comment), and PruneExpiredInvites — the
// counterpart to invites_expires_idx, which exists for exactly this query —
// must actually remove it.
func TestRedeemExpiredInviteIsRejectedAndPruned(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	tokenHash := sha256.Sum256([]byte("expired-invite-token"))
	if _, err := pool.Exec(ctx,
		`INSERT INTO invites (token_hash, expires_at) VALUES ($1, $2)`,
		tokenHash[:], time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("insert expired invite: %v", err)
	}

	_, err := svc.RedeemInvite(ctx, "expired-invite-token", "x@studio.com", "X", "password12345")
	if !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid for an expired invite", err)
	}

	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invites`).Scan(&before); err != nil {
		t.Fatalf("count invites: %v", err)
	}
	if before != 1 {
		t.Fatalf("invites before prune = %d, want 1", before)
	}

	n, err := svc.PruneExpiredInvites(ctx)
	if err != nil {
		t.Fatalf("PruneExpiredInvites: %v", err)
	}
	if n != 1 {
		t.Fatalf("PruneExpiredInvites removed %d rows, want 1", n)
	}

	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invites`).Scan(&after); err != nil {
		t.Fatalf("count invites: %v", err)
	}
	if after != 0 {
		t.Fatalf("invites after prune = %d, want 0", after)
	}
}

// createTestProject inserts a bare project row directly, bypassing the
// projects package (added in Task 8, which this task precedes), since all
// this test needs is a project_id to attach an invite to.
func createTestProject(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	row := pool.QueryRow(ctx, `INSERT INTO projects (slug, name) VALUES ($1, $2) RETURNING id`, slug, slug)
	if err := row.Scan(&id); err != nil {
		t.Fatalf("insert test project: %v", err)
	}
	return id
}
