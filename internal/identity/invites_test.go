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

	token, summary, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "new@studio.com", CreatedBy: &creator.ID})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if summary.ID == uuid.Nil {
		t.Fatal("CreateInvite returned a zero id")
	}
	if summary.CreatedBy == nil || *summary.CreatedBy != creator.ID {
		t.Fatalf("CreatedBy = %v, want %v", summary.CreatedBy, creator.ID)
	}
	if summary.Email == nil || *summary.Email != "new@studio.com" {
		t.Fatalf("Email = %v, want new@studio.com", summary.Email)
	}

	user, err := svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "new@studio.com", DisplayName: "Newcomer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("RedeemInvite: %v", err)
	}
	if user.Email != "new@studio.com" {
		t.Fatalf("Email = %q", user.Email)
	}
}

// TestUnboundInviteIsSingleUse is the property TestInviteRedemptionCreatesUser
// used to assert with a second redemption at a *different* email against an
// *email-bound* invite — which exits on the mismatch branch before anything
// single-use is even consulted, so it actually duplicated
// TestInviteBoundToEmailRejectsAnother and would still pass if
// MarkInviteRedeemed's redeemed_at IS NULL guard were deleted entirely. This
// test uses an unbound invite (no email restriction to hide behind) and two
// distinct emails (so the users table's own email-uniqueness constraint
// cannot be doing this test's job either) — the only variant that isolates
// single-use as its own property.
func TestUnboundInviteIsSingleUse(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if _, err := svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "first@studio.com", DisplayName: "First", Password: "password12345",
	}); err != nil {
		t.Fatalf("first RedeemInvite: %v", err)
	}

	_, err = svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "second@studio.com", DisplayName: "Second", Password: "password12345",
	})
	if !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
}

func TestInviteBoundToEmailRejectsAnother(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "expected@studio.com"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	_, err = svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "someone.else@studio.com", DisplayName: "Sneaky", Password: "password12345",
	})
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

	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "  New@Studio.com  "})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	user, err := svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "NEW@studio.com", DisplayName: "Newcomer", Password: "password12345",
	})
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
	_, err := svc.RedeemInvite(context.Background(), "made-up", identity.CreateUserRequest{
		Email: "x@studio.com", DisplayName: "X", Password: "password12345",
	})
	if !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
}

// TestRedeemInviteWithExistingEmailIsRejectedAsInvalid guards against an
// unbound invite being usable as an oracle for whether an arbitrary address
// already has an account. Without mapping ErrEmailTaken to ErrInviteInvalid,
// a caller holding one unbound invite could redeem it repeatedly against
// many addresses (a failed attempt never consumes the invite) and read
// account existence straight off the distinguishable error.
func TestRedeemInviteWithExistingEmailIsRejectedAsInvalid(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "existing@studio.com", DisplayName: "Existing", Password: "password12345",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	_, err = svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "existing@studio.com", DisplayName: "Impersonator", Password: "password12345",
	})
	if !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid (never ErrEmailTaken)", err)
	}
	if errors.Is(err, identity.ErrEmailTaken) {
		t.Fatalf("err = %v must not also satisfy ErrEmailTaken: that is the oracle this test guards against", err)
	}

	// The failed attempt must not have consumed the invite: a legitimate
	// holder who mistyped an address gets to try again.
	user, err := svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "genuinely-new@studio.com", DisplayName: "Genuine", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("RedeemInvite after a failed attempt: %v", err)
	}
	if user.Email != "genuinely-new@studio.com" {
		t.Fatalf("Email = %q", user.Email)
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

	projectID := createTestProject(ctx, t, pool, "castle-quest")

	token, summary, err := svc.CreateInvite(ctx, identity.InviteRequest{
		Email:     "designer@studio.com",
		ProjectID: &projectID,
		Role:      "editor",
	})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if summary.Role == nil || *summary.Role != "editor" {
		t.Fatalf("summary.Role = %v, want editor", summary.Role)
	}
	if summary.ExpiresAt.Before(time.Now()) {
		t.Fatalf("summary.ExpiresAt = %v, want a time in the future", summary.ExpiresAt)
	}

	user, err := svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345",
	})
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

	_, _, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "x@studio.com", Role: "editor"})
	if !errors.Is(err, identity.ErrInviteRequestInvalid) {
		t.Fatalf("err = %v, want ErrInviteRequestInvalid", err)
	}
}

func TestCreateInviteRejectsProjectWithoutRole(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	projectID := createTestProject(ctx, t, pool, "no-role")
	_, _, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "x@studio.com", ProjectID: &projectID})
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

	projectID := createTestProject(ctx, t, pool, "bad-role")
	_, _, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "x@studio.com", ProjectID: &projectID, Role: "superadmin"})
	if !errors.Is(err, identity.ErrInviteRequestInvalid) {
		t.Fatalf("err = %v, want ErrInviteRequestInvalid", err)
	}
}

// TestCreateInviteRejectsOverlongEmail and TestCreateInviteRejectsDisallowedDomain
// guard against CreateInvite reopening what Task 5's Correction 8 closed for
// CreateUser (an unbounded text column fed straight from a request), and
// against minting a link the recipient has no power to make usable: an
// invite for a domain this instance would refuse at registration time is a
// dead credential the moment it is created.
func TestCreateInviteRejectsOverlongEmail(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	huge := make([]byte, 2000)
	for i := range huge {
		huge[i] = 'a'
	}
	_, _, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: string(huge) + "@studio.com"})
	if !errors.Is(err, identity.ErrInviteRequestInvalid) {
		t.Fatalf("err = %v, want ErrInviteRequestInvalid", err)
	}
}

func TestCreateInviteRejectsDisallowedDomain(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.AllowedEmailDomains = []string{"studio.com"}
	svc := identity.New(pool, cfg)

	_, _, err := svc.CreateInvite(context.Background(), identity.InviteRequest{Email: "outsider@elsewhere.com"})
	if !errors.Is(err, identity.ErrInviteRequestInvalid) {
		t.Fatalf("err = %v, want ErrInviteRequestInvalid", err)
	}
}

// TestRedeemUnboundInviteAllowsOffDomainEmail pins Task 11's second review
// pass, item 5: on an instance with a configured allowlist, an *unbound*
// invite (no email attached at creation — see CreateInvite's own doc
// comment) must still be redeemable with an address outside that
// allowlist. Holding the token is the authorization; ALLOWED_EMAIL_DOMAINS
// governs open self-registration (CreateUser), not who an admin chooses to
// hand a link to. Before this fix, RedeemInvite ran the same EmailAllowed
// check CreateUser does and rejected exactly this redemption with
// ErrEmailNotAllowed, silently defeating "an invite wins over the
// instance's registration mode" for the one case that phrase exists to
// describe — an admin inviting an outside contractor.
func TestRedeemUnboundInviteAllowsOffDomainEmail(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.AllowedEmailDomains = []string{"studio.com"}
	svc := identity.New(pool, cfg)

	token, _, err := svc.CreateInvite(context.Background(), identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	user, err := svc.RedeemInvite(context.Background(), token, identity.CreateUserRequest{
		Email: "contractor@elsewhere.com", DisplayName: "Contractor", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("RedeemInvite: %v (an unbound invite must not apply ALLOWED_EMAIL_DOMAINS)", err)
	}
	if user.Email != "contractor@elsewhere.com" {
		t.Fatalf("Email = %q, want contractor@elsewhere.com", user.Email)
	}
}

// TestCreateInviteUsesConfiguredDefaultTTL and TestCreateInviteHonoursExpiresIn
// cover InviteTTL becoming configurable (config.InviteTTL / INVITE_TTL)
// instead of the hardcoded 14-day constant it used to be, and the optional
// per-invite override bounded above by config.MaxInviteTTL.
func TestCreateInviteUsesConfiguredDefaultTTL(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig() // InviteTTL: 24 * time.Hour, see testConfig's doc comment.
	svc := identity.New(pool, cfg)

	before := time.Now().Add(cfg.InviteTTL)
	_, summary, err := svc.CreateInvite(context.Background(), identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	after := time.Now().Add(cfg.InviteTTL)

	if summary.ExpiresAt.Before(before) || summary.ExpiresAt.After(after) {
		t.Fatalf("ExpiresAt = %v, want between %v and %v", summary.ExpiresAt, before, after)
	}
}

func TestCreateInviteHonoursExpiresIn(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	const ttl = 2 * time.Hour
	before := time.Now().Add(ttl)
	_, summary, err := svc.CreateInvite(ctx, identity.InviteRequest{ExpiresIn: ttl})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	after := time.Now().Add(ttl)
	if summary.ExpiresAt.Before(before) || summary.ExpiresAt.After(after) {
		t.Fatalf("ExpiresAt = %v, want between %v and %v", summary.ExpiresAt, before, after)
	}
}

func TestCreateInviteRejectsExpiresInAboveMax(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())

	_, _, err := svc.CreateInvite(context.Background(), identity.InviteRequest{ExpiresIn: 365 * 24 * time.Hour})
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

	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{})
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
	// start is closed once, after every goroutine has been spawned and is
	// blocked waiting on it, so all eight redemptions actually race each
	// other instead of mostly running one after another depending on how
	// fast the scheduler gets around to each goroutine — without this the
	// test can pass even on a build that reintroduces the race, simply
	// because it rarely gets two redemptions overlapping in practice.
	start := make(chan struct{})
	var ready sync.WaitGroup
	successes := make(chan identity.User, attempts)
	failures := make(chan error, attempts)

	ready.Add(attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ready.Done()
			<-start
			email := fmt.Sprintf("racer%d@studio.com", i)
			user, err := svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
				Email: email, DisplayName: "Racer", Password: "password12345",
			})
			if err != nil {
				failures <- err
				return
			}
			successes <- user
		}(i)
	}
	ready.Wait()
	close(start)
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

// TestRedeemExpiredInviteReturnsErrInviteExpired mirrors sessions_test.go's
// TestExpiredSessionIsRejectedAndPruned: an invite past its expires_at must
// resolve to the specific ErrInviteExpired (not the generic ErrInviteInvalid)
// — see ErrInviteExpired's doc comment for why that split is safe — and
// PruneExpiredInvites, the counterpart to invites_expires_idx, must actually
// remove it.
func TestRedeemExpiredInviteReturnsErrInviteExpired(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	tokenHash := sha256.Sum256([]byte("expired-invite-token"))
	if _, err := pool.Exec(ctx,
		`INSERT INTO invites (token_hash, expires_at) VALUES ($1, $2)`,
		tokenHash[:], time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("insert expired invite: %v", err)
	}

	_, err := svc.RedeemInvite(ctx, "expired-invite-token", identity.CreateUserRequest{
		Email: "x@studio.com", DisplayName: "X", Password: "password12345",
	})
	if !errors.Is(err, identity.ErrInviteExpired) {
		t.Fatalf("err = %v, want ErrInviteExpired", err)
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

// TestRedeemAlreadyRedeemedInviteReturnsGenericError guards the other half
// of resolveInviteMiss's split: an already-redeemed invite must collapse
// into the generic ErrInviteInvalid, not ErrInviteExpired or anything else
// that would tell one holder of a shared link whether another holder
// already used it.
func TestRedeemAlreadyRedeemedInviteReturnsGenericError(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, err := svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "first@studio.com", DisplayName: "First", Password: "password12345",
	}); err != nil {
		t.Fatalf("first RedeemInvite: %v", err)
	}

	_, err = svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "second@studio.com", DisplayName: "Second", Password: "password12345",
	})
	if !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
	if errors.Is(err, identity.ErrInviteExpired) {
		t.Fatalf("err = %v must not also satisfy ErrInviteExpired", err)
	}
}

// TestListOutstandingInvitesAndRevoke covers the admin recovery path for a
// mis-sent invite: it must be findable (ListOutstandingInvites) and
// revocable (RevokeInvite) without direct database access, and once
// revoked it must behave exactly like an expired one to RedeemInvite.
func TestListOutstandingInvitesAndRevoke(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	token, summary, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "mistake@studio.com"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	outstanding, err := svc.ListOutstandingInvites(ctx)
	if err != nil {
		t.Fatalf("ListOutstandingInvites: %v", err)
	}
	found := false
	for _, inv := range outstanding {
		if inv.ID == summary.ID {
			found = true
			if inv.Email == nil || *inv.Email != "mistake@studio.com" {
				t.Fatalf("listed invite Email = %v, want mistake@studio.com", inv.Email)
			}
		}
	}
	if !found {
		t.Fatalf("ListOutstandingInvites did not include invite %s", summary.ID)
	}

	if err := svc.RevokeInvite(ctx, summary.ID); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}

	_, err = svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "mistake@studio.com", DisplayName: "Mistake", Password: "password12345",
	})
	if !errors.Is(err, identity.ErrInviteExpired) {
		t.Fatalf("err = %v, want ErrInviteExpired after revocation", err)
	}
}

// TestRevokeUnknownInviteIsANoOp mirrors sessions_test.go's
// TestRevokeUnknownSessionIsANoOp: revoking an id nobody minted must not
// error, the same convention RevokeSession already established.
func TestRevokeUnknownInviteIsANoOp(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())

	if err := svc.RevokeInvite(context.Background(), uuid.New()); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
}

// createTestProject inserts a bare project row directly, bypassing the
// projects package (added in Task 8, which this task precedes), since all
// this test needs is a project_id to attach an invite to. ctx is taken
// before t, matching every Service method in this package (ctx always
// leads).
func createTestProject(ctx context.Context, t *testing.T, pool *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	row := pool.QueryRow(ctx, `INSERT INTO projects (slug, name) VALUES ($1, $2) RETURNING id`, slug, slug)
	if err := row.Scan(&id); err != nil {
		t.Fatalf("insert test project: %v", err)
	}
	return id
}
