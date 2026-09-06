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
	"github.com/neverbot/maestro/internal/projects"
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

// TestRedeemUnboundInviteAppliesDomainAllowlist pins Task 11's third
// review pass: an *unbound* invite (no email attached at creation — see
// CreateInvite's own doc comment) only ever said "whoever holds this link
// gets in". It names no domain, so ALLOWED_EMAIL_DOMAINS is still the only
// statement anyone has made about who may hold an account here, and
// RedeemInvite must still enforce it. An earlier version of this fix
// (Task 11's second review pass) skipped the allowlist for every
// redemption regardless of the invite's shape, which let any unbound
// invite's holder register with any address at all on an instance that
// had explicitly configured which domains may hold accounts — this test
// replaces the one that pinned that overly broad behavior.
func TestRedeemUnboundInviteAppliesDomainAllowlist(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.AllowedEmailDomains = []string{"studio.com"}
	svc := identity.New(pool, cfg)

	token, _, err := svc.CreateInvite(context.Background(), identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	_, err = svc.RedeemInvite(context.Background(), token, identity.CreateUserRequest{
		Email: "contractor@elsewhere.com", DisplayName: "Contractor", Password: "password12345",
	})
	if !errors.Is(err, identity.ErrEmailNotAllowed) {
		t.Fatalf("err = %v, want ErrEmailNotAllowed", err)
	}
}

// TestRedeemBoundInviteAllowsOffDomainEmail is the other half: a *bound*
// invite means the admin typed this exact address when they created it —
// they named the person, and the domain policy has already been applied
// to their intent (CreateInvite's own EmailAllowed check, Task 5
// Correction 8, still enforced at creation time). Redemption must not
// re-apply the allowlist to an address the admin already committed to by
// name.
//
// CreateInvite itself still refuses to *mint* a bound invite for a
// disallowed domain (see TestCreateInviteRejectsDisallowedDomain), so this
// test cannot demonstrate the property using one Service end to end — that
// would only prove CreateInvite's own gate works, not RedeemInvite's. It
// mints the invite through a permissively configured Service and redeems
// it through a second Service, sharing the same database, configured with
// a strict allowlist that would refuse the address on the open
// self-service path — the same shape as an operator tightening
// ALLOWED_EMAIL_DOMAINS after an invite was already minted and handed out,
// which must not retroactively break it.
func TestRedeemBoundInviteAllowsOffDomainEmail(t *testing.T) {
	pool := testutil.NewPool(t)
	creator := identity.New(pool, testConfig())

	token, _, err := creator.CreateInvite(context.Background(), identity.InviteRequest{Email: "contractor@elsewhere.com"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	strictCfg := testConfig()
	strictCfg.AllowedEmailDomains = []string{"studio.com"}
	redeemer := identity.New(pool, strictCfg)

	user, err := redeemer.RedeemInvite(context.Background(), token, identity.CreateUserRequest{
		Email: "contractor@elsewhere.com", DisplayName: "Contractor", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("RedeemInvite: %v (a bound invite must not re-apply ALLOWED_EMAIL_DOMAINS at redemption)", err)
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

	// Bracketed against the *database's* clock, not this process'.
	// CreateInvite no longer computes a timestamp at all — it sends the
	// TTL as an interval and Postgres writes now() + it — so bracketing
	// with time.Now() would be asserting that two machines' clocks agree
	// to within the round trip, which is the very assumption whose
	// failure this change removed (see CreateInvite in identity.sql).
	// Measured against the local test database, that bracket was already
	// off by about 1.6ms.
	before := dbNow(t, pool).Add(cfg.InviteTTL)
	_, summary, err := svc.CreateInvite(context.Background(), identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	after := dbNow(t, pool).Add(cfg.InviteTTL)

	if summary.ExpiresAt.Before(before) || summary.ExpiresAt.After(after) {
		t.Fatalf("ExpiresAt = %v, want between %v and %v", summary.ExpiresAt, before, after)
	}
}

func TestCreateInviteHonoursExpiresIn(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	const ttl = 2 * time.Hour
	// The database's clock, for the reason
	// TestCreateInviteUsesConfiguredDefaultTTL states.
	before := dbNow(t, pool).Add(ttl)
	_, summary, err := svc.CreateInvite(ctx, identity.InviteRequest{ExpiresIn: ttl})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	after := dbNow(t, pool).Add(ttl)
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
			successes <- user.User
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

// TestListOutstandingInvitesExcludesProjectBound pins Task 18's split
// between the instance-wide admin surface and a game's own invite roster:
// ListOutstandingInvites (POST/GET/DELETE /api/invites, gated on
// Caller.IsAdmin) must never surface a project-bound invite, since an
// instance admin has no standing in a game they are not a member of
// anywhere else in this codebase.
func TestListOutstandingInvitesExcludesProjectBound(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	projectID := createTestProject(ctx, t, pool, "azeroth")
	_, bound, err := svc.CreateInvite(ctx, identity.InviteRequest{ProjectID: &projectID, Role: "editor"})
	if err != nil {
		t.Fatalf("CreateInvite (bound): %v", err)
	}
	_, unbound, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "unbound@studio.com"})
	if err != nil {
		t.Fatalf("CreateInvite (unbound): %v", err)
	}

	outstanding, err := svc.ListOutstandingInvites(ctx)
	if err != nil {
		t.Fatalf("ListOutstandingInvites: %v", err)
	}
	for _, inv := range outstanding {
		if inv.ID == bound.ID {
			t.Fatal("ListOutstandingInvites returned a project-bound invite")
		}
	}
	found := false
	for _, inv := range outstanding {
		if inv.ID == unbound.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("ListOutstandingInvites did not include the account-only invite")
	}
}

// TestRevokeInviteIgnoresProjectBound is RevokeInvite's half of the same
// pin: DELETE /api/invites/{id} must never be able to revoke a
// project-bound invite, only the instance-wide account-only kind — a
// project-bound invite is only ever revocable through its own game's
// RevokeProjectInvite (TestRevokeProjectInviteScopedToItsGame, below).
func TestRevokeInviteIgnoresProjectBound(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	projectID := createTestProject(ctx, t, pool, "azeroth")
	token, bound, err := svc.CreateInvite(ctx, identity.InviteRequest{ProjectID: &projectID, Role: "editor"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if err := svc.RevokeInvite(ctx, bound.ID); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}

	// Still live: RevokeInvite's project_id IS NULL clause must not have
	// touched this project-bound row.
	if _, err := svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "still.live@studio.com", DisplayName: "Still Live", Password: "password12345",
	}); err != nil {
		t.Fatalf("RedeemInvite after no-op RevokeInvite: %v", err)
	}
}

// TestListAndRevokeOutstandingProjectInvites covers the project-scoped
// counterparts Task 18 added for GET and DELETE
// /api/games/{game}/invites: findable by game, revocable by game, and
// scoped so one game's revoke can never touch another game's invite.
func TestListAndRevokeOutstandingProjectInvites(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	azeroth := createTestProject(ctx, t, pool, "azeroth")
	outland := createTestProject(ctx, t, pool, "outland")

	azerothToken, azerothInvite, err := svc.CreateInvite(ctx, identity.InviteRequest{ProjectID: &azeroth, Role: "editor"})
	if err != nil {
		t.Fatalf("CreateInvite (azeroth): %v", err)
	}
	_, outlandInvite, err := svc.CreateInvite(ctx, identity.InviteRequest{ProjectID: &outland, Role: "viewer"})
	if err != nil {
		t.Fatalf("CreateInvite (outland): %v", err)
	}

	azerothList, err := svc.ListOutstandingInvitesForProject(ctx, azeroth)
	if err != nil {
		t.Fatalf("ListOutstandingInvitesForProject: %v", err)
	}
	if len(azerothList) != 1 || azerothList[0].ID != azerothInvite.ID {
		t.Fatalf("ListOutstandingInvitesForProject(azeroth) = %+v, want only %s", azerothList, azerothInvite.ID)
	}

	// A revoke scoped to the wrong game is a silent no-op, the same
	// convention RevokeAPIToken's own doc comment establishes for tokens.
	if err := svc.RevokeProjectInvite(ctx, outland, azerothInvite.ID); err != nil {
		t.Fatalf("RevokeProjectInvite (wrong game): %v", err)
	}
	if _, err := svc.RedeemInvite(ctx, azerothToken, identity.CreateUserRequest{
		Email: "azeroth.member@studio.com", DisplayName: "Azeroth Member", Password: "password12345",
	}); err != nil {
		t.Fatalf("RedeemInvite must still succeed after a cross-game revoke attempt: %v", err)
	}

	if err := svc.RevokeProjectInvite(ctx, outland, outlandInvite.ID); err != nil {
		t.Fatalf("RevokeProjectInvite: %v", err)
	}
	outlandList, err := svc.ListOutstandingInvitesForProject(ctx, outland)
	if err != nil {
		t.Fatalf("ListOutstandingInvitesForProject: %v", err)
	}
	// Revoking does not remove the row (see RevokeInvite's own doc
	// comment: it expires it in place), but a revoked invite still counts
	// as "outstanding" (not yet redeemed) by ListOutstandingProjectInvites'
	// own definition of that word — mirroring
	// TestListOutstandingInvitesAndRevoke's identical assumption for the
	// account-only listing above.
	found := false
	for _, inv := range outlandList {
		if inv.ID == outlandInvite.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("revoked invite unexpectedly disappeared from ListOutstandingInvitesForProject")
	}
}

// TestListOutstandingInvitesForProjectExcludesAccountOnly is the other
// half of the split TestListOutstandingInvitesExcludesProjectBound pins:
// both doc comments (ListOutstandingInvites and
// ListOutstandingProjectInvites) claim the two surfaces never overlap,
// but until this test existed only one direction was actually checked.
// An account-only invite (no project) must never appear in a game's own
// listing.
func TestListOutstandingInvitesForProjectExcludesAccountOnly(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	projectID := createTestProject(ctx, t, pool, "azeroth")
	_, bound, err := svc.CreateInvite(ctx, identity.InviteRequest{ProjectID: &projectID, Role: "editor"})
	if err != nil {
		t.Fatalf("CreateInvite (bound): %v", err)
	}
	if _, _, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "unbound@studio.com"}); err != nil {
		t.Fatalf("CreateInvite (unbound): %v", err)
	}

	outstanding, err := svc.ListOutstandingInvitesForProject(ctx, projectID)
	if err != nil {
		t.Fatalf("ListOutstandingInvitesForProject: %v", err)
	}
	if len(outstanding) != 1 || outstanding[0].ID != bound.ID {
		t.Fatalf("ListOutstandingInvitesForProject(azeroth) = %+v, want only the bound invite %s", outstanding, bound.ID)
	}
}

// TestRevokeProjectInviteIgnoresAccountOnly is
// TestRevokeInviteIgnoresProjectBound's other half: a game's own
// RevokeProjectInvite must never be able to revoke an account-only
// invite, even one an owner happens to guess or otherwise learn the id
// of — only the instance-wide RevokeInvite (DELETE /api/invites/{id},
// gated on Caller.IsAdmin) may touch it.
func TestRevokeProjectInviteIgnoresAccountOnly(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	projectID := createTestProject(ctx, t, pool, "azeroth")
	token, unbound, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "unbound@studio.com"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if err := svc.RevokeProjectInvite(ctx, projectID, unbound.ID); err != nil {
		t.Fatalf("RevokeProjectInvite: %v", err)
	}

	// Still live: RevokeProjectInvite's project_id = $2 clause must not
	// have touched an account-only row.
	if _, err := svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "unbound@studio.com", DisplayName: "Unbound", Password: "password12345",
	}); err != nil {
		t.Fatalf("RedeemInvite after no-op RevokeProjectInvite: %v", err)
	}
}

// TestCountInvitesForProjectIncludesRedeemed pins CountInvitesForProject's
// own doc comment: it counts every invites row scoped to a project,
// redeemed included, since a game's cascade takes its already-redeemed
// (audit-trail) invite rows with it too, not only its outstanding ones —
// added in Task 18's Round 2 corrections so handleDeleteGame
// (api_projects.go) can log how many invites a game deletion is about to
// destroy.
func TestCountInvitesForProjectIncludesRedeemed(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	azeroth := createTestProject(ctx, t, pool, "azeroth")
	outland := createTestProject(ctx, t, pool, "outland")

	if _, _, err := svc.CreateInvite(ctx, identity.InviteRequest{ProjectID: &azeroth, Role: "viewer"}); err != nil {
		t.Fatalf("CreateInvite (outstanding): %v", err)
	}
	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{ProjectID: &azeroth, Role: "editor"})
	if err != nil {
		t.Fatalf("CreateInvite (to redeem): %v", err)
	}
	if _, err := svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "redeemed@studio.com", DisplayName: "Redeemed", Password: "password12345",
	}); err != nil {
		t.Fatalf("RedeemInvite: %v", err)
	}
	if _, _, err := svc.CreateInvite(ctx, identity.InviteRequest{ProjectID: &outland, Role: "viewer"}); err != nil {
		t.Fatalf("CreateInvite (other project): %v", err)
	}

	n, err := svc.CountInvitesForProject(ctx, azeroth)
	if err != nil {
		t.Fatalf("CountInvitesForProject: %v", err)
	}
	if n != 2 {
		t.Fatalf("CountInvitesForProject = %d, want 2 (outstanding + redeemed, not the other project's)", n)
	}
}

// TestRedeemInviteForExistingUserGrantsMembershipWithoutCreatingAnAccount
// is this task's own acceptance test (Round 2): a designer who already
// has an account can be granted a second game's membership through a
// project-bound invite, without RedeemInviteForExistingUser creating a
// second account or touching the existing one's password.
func TestRedeemInviteForExistingUserGrantsMembershipWithoutCreatingAnAccount(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	existing, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "existing@studio.com", DisplayName: "Existing", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	projectID := createTestProject(ctx, t, pool, "second-game")
	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{ProjectID: &projectID, Role: "editor"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	result, err := svc.RedeemInviteForExistingUser(ctx, token, existing.ID)
	if err != nil {
		t.Fatalf("RedeemInviteForExistingUser: %v", err)
	}
	if result.ID != existing.ID {
		t.Fatalf("result.ID = %v, want the existing user's id %v", result.ID, existing.ID)
	}
	if result.ProjectID == nil || *result.ProjectID != projectID {
		t.Fatalf("result.ProjectID = %v, want %v", result.ProjectID, projectID)
	}

	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM memberships WHERE user_id = $1 AND project_id = $2`, existing.ID, projectID).Scan(&role); err != nil {
		t.Fatalf("query membership: %v", err)
	}
	if role != "editor" {
		t.Fatalf("role = %q, want editor", role)
	}

	// No second account was created.
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email = $1`, "existing@studio.com").Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Fatalf("users with this email = %d, want 1 (no new account created)", count)
	}
	// The password is untouched.
	if _, err := svc.Authenticate(ctx, "existing@studio.com", "password12345"); err != nil {
		t.Fatalf("original password should still authenticate: %v", err)
	}
}

// TestRedeemInviteForExistingUserPromotesAnExistingMember mirrors the
// "an invite is a deferred SetRole" semantics Task 18 established: an
// already-a-member existing user redeeming a higher-role invite upserts
// to the new role rather than being refused.
func TestRedeemInviteForExistingUserPromotesAnExistingMember(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	existing, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "already-member@studio.com", DisplayName: "Already Member", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	projectID := createTestProject(ctx, t, pool, "promote-game")
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, project_id, role) VALUES ($1, $2, 'viewer')`, existing.ID, projectID); err != nil {
		t.Fatalf("seed viewer membership: %v", err)
	}

	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{ProjectID: &projectID, Role: "owner"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, err := svc.RedeemInviteForExistingUser(ctx, token, existing.ID); err != nil {
		t.Fatalf("RedeemInviteForExistingUser: %v", err)
	}

	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM memberships WHERE user_id = $1 AND project_id = $2`, existing.ID, projectID).Scan(&role); err != nil {
		t.Fatalf("query membership: %v", err)
	}
	if role != "owner" {
		t.Fatalf("role = %q, want owner", role)
	}
}

func TestRedeemInviteForExistingUserRejectsAccountOnlyInvite(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	existing, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "no-project@studio.com", DisplayName: "No Project", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if _, err := svc.RedeemInviteForExistingUser(ctx, token, existing.ID); !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
}

// TestRedeemInviteForExistingUserRejectsBoundInviteForAnotherEmail pins
// the binding check: an invite naming one email must not grant
// membership to whichever account happens to be logged in, only to the
// account that email actually belongs to.
func TestRedeemInviteForExistingUserRejectsBoundInviteForAnotherEmail(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	intended := "intended@studio.com"
	bystander, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "bystander@studio.com", DisplayName: "Bystander", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser bystander: %v", err)
	}
	projectID := createTestProject(ctx, t, pool, "bound-game")
	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: intended, ProjectID: &projectID, Role: "editor"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if _, err := svc.RedeemInviteForExistingUser(ctx, token, bystander.ID); !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE user_id = $1 AND project_id = $2`, bystander.ID, projectID).Scan(&count); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if count != 0 {
		t.Fatal("a bound invite for someone else's email must not grant the logged-in bystander membership")
	}
}

func TestRedeemInviteForExistingUserAllowsBoundInviteForMatchingEmail(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	existing, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "matching@studio.com", DisplayName: "Matching", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	projectID := createTestProject(ctx, t, pool, "matching-game")
	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{Email: "MATCHING@studio.com", ProjectID: &projectID, Role: "viewer"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if _, err := svc.RedeemInviteForExistingUser(ctx, token, existing.ID); err != nil {
		t.Fatalf("RedeemInviteForExistingUser: %v", err)
	}
}

func TestRedeemInviteForExistingUserUnknownTokenReturnsErrInviteInvalid(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	existing, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "solo@studio.com", DisplayName: "Solo", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if _, err := svc.RedeemInviteForExistingUser(ctx, "not-a-real-token", existing.ID); !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
}

// TestRedeemInviteForExistingUserMarksInviteRedeemed pins that this path
// consumes the invite exactly like the anonymous one — a project owner
// watching GET .../invites sees it disappear from the outstanding list
// either way.
func TestRedeemInviteForExistingUserMarksInviteRedeemed(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	existing, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "redeemer@studio.com", DisplayName: "Redeemer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	projectID := createTestProject(ctx, t, pool, "redeem-marks-game")
	token, _, err := svc.CreateInvite(ctx, identity.InviteRequest{ProjectID: &projectID, Role: "viewer"})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if _, err := svc.RedeemInviteForExistingUser(ctx, token, existing.ID); err != nil {
		t.Fatalf("RedeemInviteForExistingUser: %v", err)
	}

	before, err := svc.ListOutstandingInvitesForProject(ctx, projectID)
	if err != nil {
		t.Fatalf("ListOutstandingInvitesForProject: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("outstanding invites after redemption = %d, want 0", len(before))
	}

	// And it cannot be redeemed a second time.
	other, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "other-redeemer@studio.com", DisplayName: "Other Redeemer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser other: %v", err)
	}
	if _, err := svc.RedeemInviteForExistingUser(ctx, token, other.ID); !errors.Is(err, identity.ErrInviteInvalid) {
		t.Fatalf("second redemption err = %v, want ErrInviteInvalid", err)
	}
}

// TestAnInviteIsJudgedByTheClockThatWroteItsExpiry is the regression
// test for the defect a flaky test led back to.
//
// `expires_at` used to be `time.Now().Add(ttl)` computed in this
// process, while `GetLiveInvite` and `MarkInviteRedeemed` both compare
// it against Postgres' own `now()`. So whether an invite was live was a
// claim about two clocks agreeing — the application's and the database
// server's, which in a Compose deployment are two containers. It was
// found as `TestRegisterWithExpiredInviteReportsExpired` failing under
// load, because that test's whole margin was nine milliseconds of
// tolerance for a skew nothing bounds.
//
// What is pinned here is the property that replaced it: an invite's
// life is decided entirely by the clock that wrote its expiry, so
// moving `expires_at` in the database's own terms is what makes an
// invite live or dead, and this process' clock is nowhere in the
// answer. Both directions, because a fix that expired everything would
// pass a one-sided version of this test.
func TestAnInviteIsJudgedByTheClockThatWroteItsExpiry(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	// The stored expiry tracks the database's clock, not this one.
	before := dbNow(t, pool)
	token, summary, err := svc.CreateInvite(ctx, identity.InviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if gap := summary.ExpiresAt.Sub(before); gap < time.Hour || gap > time.Hour+time.Minute {
		t.Fatalf("expires_at is %v past the database's clock, want about an hour: "+
			"the TTL is applied by whichever clock judges it", gap)
	}

	// A second before the database's now: refused, and refused as
	// expired rather than as unknown.
	if _, err := pool.Exec(ctx,
		`UPDATE invites SET expires_at = now() - interval '1 second' WHERE id = $1`,
		summary.ID); err != nil {
		t.Fatalf("expire the invite: %v", err)
	}
	_, err = svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "late@studio.com", DisplayName: "Late", Password: "password12345",
	})
	if !errors.Is(err, identity.ErrInviteExpired) {
		t.Fatalf("redeeming a database-expired invite = %v, want ErrInviteExpired", err)
	}

	// A second after it: accepted. The row is otherwise untouched, so
	// the only thing that changed is where its expiry sits relative to
	// the one clock that matters.
	if _, err := pool.Exec(ctx,
		`UPDATE invites SET expires_at = now() + interval '1 hour' WHERE id = $1`,
		summary.ID); err != nil {
		t.Fatalf("un-expire the invite: %v", err)
	}
	if _, err := svc.RedeemInvite(ctx, token, identity.CreateUserRequest{
		Email: "intime@studio.com", DisplayName: "In Time", Password: "password12345",
	}); err != nil {
		t.Fatalf("redeeming a database-live invite: %v", err)
	}
}

// TestARevokedInviteListsAsRevoked is the read side of the same rule.
//
// RevokeProjectInvite sets `expires_at` to Postgres' `now()`, and the
// listing's `revoked` flag used to be `!ExpiresAt.After(time.Now())`
// computed in internal/web — a comparison across two clocks whose whole
// margin was one HTTP round trip, so a database clock a few
// milliseconds ahead reported a just-revoked invite as live. The flag is
// now computed beside the column it is about.
func TestARevokedInviteListsAsRevoked(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	owner, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	var projectID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ('azeroth', 'Azeroth') RETURNING id`).
		Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	role := "viewer"
	_, live, err := svc.CreateInvite(ctx, identity.InviteRequest{
		ProjectID: &projectID, Role: role, CreatedBy: &owner.ID,
	})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	listed, err := svc.ListOutstandingInvitesForProject(ctx, projectID)
	if err != nil {
		t.Fatalf("ListOutstandingInvitesForProject: %v", err)
	}
	if len(listed) != 1 || listed[0].Revoked {
		t.Fatalf("a live invite lists as %+v, want revoked false", listed)
	}

	if err := svc.RevokeProjectInvite(ctx, projectID, live.ID); err != nil {
		t.Fatalf("RevokeProjectInvite: %v", err)
	}
	listed, err = svc.ListOutstandingInvitesForProject(ctx, projectID)
	if err != nil {
		t.Fatalf("ListOutstandingInvitesForProject: %v", err)
	}
	// Still listed — revocation keeps the audit trail — and flagged.
	if len(listed) != 1 || !listed[0].Revoked {
		t.Fatalf("a revoked invite lists as %+v, want one row with revoked true", listed)
	}
}

// TestInviteForAVanishedGameIsRefusedNotAFault provokes the foreign-key
// violation rather than constructing it: the game is really created,
// really deleted, and the insert really fails against Postgres, which is
// what makes this a statement about the path and not only about the
// mapping.
//
// It is the race an owner meets when a co-owner deletes the game between
// their membership check and this insert. Before mapInviteInsertError,
// the raw SQLSTATE 23503 reached internal/web unrecognised and was
// answered 500 — a caller told this server broke, when what happened is
// that the thing they named is gone.
func TestInviteForAVanishedGameIsRefusedNotAFault(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	owner, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := projSvc.Delete(ctx, project.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, _, err = svc.CreateInvite(ctx, identity.InviteRequest{
		ProjectID: &project.ID, Role: "editor", CreatedBy: &owner.ID,
	})
	if !errors.Is(err, identity.ErrInviteRequestInvalid) {
		t.Fatalf("CreateInvite for a deleted game = %v, want ErrInviteRequestInvalid", err)
	}
}

// TestInviteByAVanishedAccountIsRefusedNotAFault is the other constraint
// on the same insert, carried in the same step rather than left for the
// next reader to find: invites.created_by references users, so an admin
// whose own account was deleted mid-request hits it exactly the way the
// game half above does.
func TestInviteByAVanishedAccountIsRefusedNotAFault(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	gone := uuid.New()

	_, _, err := svc.CreateInvite(context.Background(), identity.InviteRequest{CreatedBy: &gone})
	if !errors.Is(err, identity.ErrInviteRequestInvalid) {
		t.Fatalf("CreateInvite by an unknown account = %v, want ErrInviteRequestInvalid", err)
	}
}
