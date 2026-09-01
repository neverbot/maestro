package identity_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/testutil"
)

func TestSetAdminPromotesANonAdmin(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "promote-me@studio.com", DisplayName: "Promote Me", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.IsAdmin {
		t.Fatal("a user created through CreateUser must start as non-admin")
	}

	if err := svc.SetAdmin(ctx, user.ID, true); err != nil {
		t.Fatalf("SetAdmin(true): %v", err)
	}

	got, err := svc.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if !got.IsAdmin {
		t.Fatal("user should be admin after SetAdmin(true)")
	}
}

func TestSetAdminDemotesAnAdminWhenAnotherRemains(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	admin1, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "admin1@studio.com", DisplayName: "Admin One", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser admin1: %v", err)
	}
	if err := svc.SetAdmin(ctx, admin1.ID, true); err != nil {
		t.Fatalf("SetAdmin admin1: %v", err)
	}
	admin2, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "admin2@studio.com", DisplayName: "Admin Two", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser admin2: %v", err)
	}
	if err := svc.SetAdmin(ctx, admin2.ID, true); err != nil {
		t.Fatalf("SetAdmin admin2: %v", err)
	}

	// Two admins exist now, so demoting one must succeed.
	if err := svc.SetAdmin(ctx, admin1.ID, false); err != nil {
		t.Fatalf("SetAdmin(false) with another admin remaining: %v", err)
	}

	got, err := svc.UserByID(ctx, admin1.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if got.IsAdmin {
		t.Fatal("admin1 should no longer be admin")
	}
}

// TestSetAdminRefusesToDemoteTheLastAdmin pins the invariant ErrLastAdmin
// exists to protect: an instance with zero admins can never mint another
// account-only invite, list one, or promote anyone back — every one of
// those surfaces is gated on Caller.IsAdmin. This includes the case
// where the last admin is demoting themselves, not only a hypothetical
// second admin doing it to them.
func TestSetAdminRefusesToDemoteTheLastAdmin(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "only-admin@studio.com", DisplayName: "Only Admin", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := svc.SetAdmin(ctx, user.ID, true); err != nil {
		t.Fatalf("SetAdmin(true): %v", err)
	}

	if err := svc.SetAdmin(ctx, user.ID, false); !errors.Is(err, identity.ErrLastAdmin) {
		t.Fatalf("err = %v, want ErrLastAdmin", err)
	}

	got, err := svc.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if !got.IsAdmin {
		t.Fatal("a rejected demotion must leave the admin flag untouched")
	}
}

// TestSetAdminDemotingANonAdminIsANoOp mirrors SetRole's own "the guard
// only fires on an actual demotion" short-circuit: setting a non-admin's
// flag to false again must never consult, or be blocked by, the
// last-admin count.
func TestSetAdminDemotingANonAdminIsANoOp(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "never-admin@studio.com", DisplayName: "Never Admin", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := svc.SetAdmin(ctx, user.ID, false); err != nil {
		t.Fatalf("SetAdmin(false) on a non-admin must succeed as a no-op: %v", err)
	}
}

func TestSetAdminUnknownUserReturnsErrUserNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	unknown := uuid.New()
	if err := svc.SetAdmin(ctx, unknown, true); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

// TestConcurrentDemotionsOfTheLastTwoAdminsLeaveExactlyOne is
// CountOwnersForUpdate's own concurrency test
// (TestConcurrentRemovalLeavesExactlyOneOwner, projects_test.go),
// applied to the instance-wide admin invariant: two goroutines racing to
// demote the instance's last two admins must never both succeed.
//
// Looped 25 times, each round against a fresh pair of admins: a Round 2
// review pointed out that a single round of this exact test can pass
// without the two goroutines' SetAdmin calls ever actually overlapping
// inside CountAdminsForUpdate's own FOR UPDATE window — go test's
// scheduler offers no guarantee the two goroutines are even both
// running before one finishes — so one green run was never strong
// evidence the lock was doing anything. Looping gives the race many
// independent chances to occur; the assertion inside each round is
// exactly as strict as the single-round version was.
func TestConcurrentDemotionsOfTheLastTwoAdminsLeaveExactlyOne(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	// CountAdminsForUpdate locks (and counts) every admin row in the
	// whole table, not a set scoped to this test — so the "exactly two
	// admins" precondition each round needs has to be true instance-wide,
	// not just among the two ids this round races. A first version of
	// this loop created two brand-new admins every round on top of
	// whichever single admin survived the previous round's race,
	// reaching 3 live admins by round 1 — at which point neither
	// concurrent demotion could ever observe count<=1, so both
	// succeeded, and the test failed for a reason that had nothing to do
	// with the lock. Fixed by carrying the previous round's survivor
	// forward as this round's first racer and only ever minting one new
	// admin per round: exactly two admins exist, instance-wide, at the
	// start of every round's race.
	firstAdmin, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "race-seed@studio.com", DisplayName: "Race Seed", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser seed admin: %v", err)
	}
	if err := svc.SetAdmin(ctx, firstAdmin.ID, true); err != nil {
		t.Fatalf("SetAdmin seed admin: %v", err)
	}
	survivor := firstAdmin.ID

	const rounds = 25
	for round := 0; round < rounds; round++ {
		challenger, err := svc.CreateUser(ctx, identity.CreateUserRequest{
			Email:       fmt.Sprintf("race-challenger-%d@studio.com", round),
			DisplayName: "Race Challenger", Password: "password12345",
		})
		if err != nil {
			t.Fatalf("round %d: CreateUser challenger: %v", round, err)
		}
		if err := svc.SetAdmin(ctx, challenger.ID, true); err != nil {
			t.Fatalf("round %d: SetAdmin challenger: %v", round, err)
		}

		var wg sync.WaitGroup
		var successes atomic.Int64
		var mu sync.Mutex
		var nextSurvivor uuid.UUID
		for _, id := range []uuid.UUID{survivor, challenger.ID} {
			wg.Add(1)
			go func(id uuid.UUID) {
				defer wg.Done()
				if err := svc.SetAdmin(ctx, id, false); err != nil {
					// Refused (ErrLastAdmin, in the passing case): id is
					// still an admin, so it carries into next round.
					mu.Lock()
					nextSurvivor = id
					mu.Unlock()
					return
				}
				successes.Add(1)
			}(id)
		}
		wg.Wait()

		if got := successes.Load(); got != 1 {
			t.Fatalf("round %d: successful concurrent demotions = %d, want exactly 1", round, got)
		}

		rows := 0
		for _, id := range []uuid.UUID{survivor, challenger.ID} {
			u, err := svc.UserByID(ctx, id)
			if err != nil {
				t.Fatalf("round %d: UserByID: %v", round, err)
			}
			if u.IsAdmin {
				rows++
			}
		}
		if rows != 1 {
			t.Fatalf("round %d: remaining admins among the two = %d, want exactly 1", round, rows)
		}
		survivor = nextSurvivor
	}
}

func TestSetAdminByEmailPromotesAndDemotes(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "byemail@studio.com", DisplayName: "By Email", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Mixed case and surrounding whitespace, to pin the same
	// normalization every other email-keyed lookup in this package
	// applies.
	if err := svc.SetAdminByEmail(ctx, "  ByEmail@Studio.com  ", true); err != nil {
		t.Fatalf("SetAdminByEmail(true): %v", err)
	}
	got, err := svc.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if !got.IsAdmin {
		t.Fatal("user should be admin after SetAdminByEmail(true)")
	}

	second, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "second-admin@studio.com", DisplayName: "Second", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser second: %v", err)
	}
	if err := svc.SetAdmin(ctx, second.ID, true); err != nil {
		t.Fatalf("SetAdmin second: %v", err)
	}

	if err := svc.SetAdminByEmail(ctx, "byemail@studio.com", false); err != nil {
		t.Fatalf("SetAdminByEmail(false): %v", err)
	}
	got, err = svc.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if got.IsAdmin {
		t.Fatal("user should no longer be admin after SetAdminByEmail(false)")
	}
}

func TestSetAdminByEmailUnknownEmailReturnsErrUserNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	if err := svc.SetAdminByEmail(ctx, "nobody@studio.com", true); !errors.Is(err, identity.ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

func TestSetAdminByEmailStillRefusesToDemoteTheLastAdmin(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "only-by-email@studio.com", DisplayName: "Only", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := svc.SetAdmin(ctx, user.ID, true); err != nil {
		t.Fatalf("SetAdmin(true): %v", err)
	}

	if err := svc.SetAdminByEmail(ctx, "only-by-email@studio.com", false); !errors.Is(err, identity.ErrLastAdmin) {
		t.Fatalf("err = %v, want ErrLastAdmin", err)
	}
}

// TestBootstrapFirstAdminRepromotesConfiguredAdminWhenDemoted pins this
// task's own recovery path: a non-empty instance whose configured
// FIRST_ADMIN_EMAIL account has since lost the flag (demoted by another
// admin, or however it happened) gets it back on the next boot, without
// BootstrapFirstAdmin creating a second account or touching the
// account's password.
func TestBootstrapFirstAdminRepromotesConfiguredAdminWhenDemoted(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "admin@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("first BootstrapFirstAdmin: %v", err)
	}
	admin, err := svc.Authenticate(ctx, "admin@studio.com", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !admin.IsAdmin {
		t.Fatal("bootstrap admin should be an admin immediately after bootstrapping")
	}

	// A second admin exists so demoting the first is not itself refused
	// by ErrLastAdmin — this test is about the boot-time recovery path,
	// not the demotion guard.
	other, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "other@studio.com", DisplayName: "Other", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser other: %v", err)
	}
	if err := svc.SetAdmin(ctx, other.ID, true); err != nil {
		t.Fatalf("SetAdmin other: %v", err)
	}
	if err := svc.SetAdmin(ctx, admin.ID, false); err != nil {
		t.Fatalf("demote configured admin: %v", err)
	}
	demoted, err := svc.UserByID(ctx, admin.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if demoted.IsAdmin {
		t.Fatal("precondition failed: configured admin should be demoted before the recovery boot")
	}

	// Simulate a restart: BootstrapFirstAdmin runs again against a
	// non-empty instance whose configured admin has lost the flag.
	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("second BootstrapFirstAdmin: %v", err)
	}

	recovered, err := svc.UserByID(ctx, admin.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if !recovered.IsAdmin {
		t.Fatal("BootstrapFirstAdmin should re-promote the configured admin email on a later boot")
	}
	// The password must be untouched — re-promotion only ever sets the
	// flag.
	if _, err := svc.Authenticate(ctx, "admin@studio.com", "password12345"); err != nil {
		t.Fatalf("configured admin's original password should still authenticate: %v", err)
	}
}

// TestBootstrapFirstAdminResetsPasswordWhenConfiguredAdminPasswordDoesNotMatch
// pins Task 22's fix to the admin-recovery path: Task 21's own
// repromoteConfiguredAdmin restored the flag but explicitly left the
// password untouched, which meant a rotated (or forgotten) admin password
// could never actually be recovered by restarting with
// FIRST_ADMIN_EMAIL/FIRST_ADMIN_PASSWORD — the flag was already true, so
// the old function returned immediately without even looking at the
// password. This pins the new behaviour: when the configured admin's
// stored hash does not verify against FIRST_ADMIN_PASSWORD, this boot
// resets it, and every prior session for that account is gone.
func TestBootstrapFirstAdminResetsPasswordWhenConfiguredAdminPasswordDoesNotMatch(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "admin@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("first BootstrapFirstAdmin: %v", err)
	}
	admin, err := svc.Authenticate(ctx, "admin@studio.com", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	// The admin rotates their own password away from the configured
	// value — the exact scenario the plan's critical review verified
	// live left an instance permanently unrecoverable.
	if err := svc.ChangeOwnPassword(ctx, admin.ID, "password12345", "a-rotated-password"); err != nil {
		t.Fatalf("ChangeOwnPassword: %v", err)
	}
	token, _, err := svc.IssueSession(ctx, admin.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	// Simulate a restart with the same, now-stale, configuration, plus
	// the FIRST_ADMIN_PASSWORD_RESET opt-in an operator sets for exactly
	// this one boot.
	resetCfg := cfg
	resetCfg.FirstAdminPasswordReset = true
	if err := identity.New(pool, resetCfg).BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("second BootstrapFirstAdmin: %v", err)
	}

	if _, err := svc.Authenticate(ctx, "admin@studio.com", "password12345"); err != nil {
		t.Fatalf("configured password should authenticate again after recovery: %v", err)
	}
	if _, err := svc.Authenticate(ctx, "admin@studio.com", "a-rotated-password"); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("rotated password should no longer authenticate, err = %v", err)
	}
	if _, _, err := svc.UserForSession(ctx, token); !errors.Is(err, identity.ErrNoSession) {
		t.Fatalf("session minted before the reset should be revoked, err = %v", err)
	}
}

// TestBootstrapFirstAdminResetLeavesPasswordAloneWhenAlreadyCorrect pins
// the common case on the other side of the fix above: an instance that
// keeps FIRST_ADMIN_EMAIL/FIRST_ADMIN_PASSWORD set permanently (as
// compose.yml does for local development) must not have every ordinary
// restart silently log its admin out of every device — the reset only
// ever fires on an actual mismatch.
func TestBootstrapFirstAdminResetLeavesPasswordAloneWhenAlreadyCorrect(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "admin@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("first BootstrapFirstAdmin: %v", err)
	}
	admin, err := svc.Authenticate(ctx, "admin@studio.com", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	token, _, err := svc.IssueSession(ctx, admin.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	// Restart with the exact same, already-correct configuration — and
	// with the opt-in set, since the verify-then-reset check lives
	// inside the opted-in path and a redundant reset must still be a
	// no-op.
	resetCfg := cfg
	resetCfg.FirstAdminPasswordReset = true
	if err := identity.New(pool, resetCfg).BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("second BootstrapFirstAdmin: %v", err)
	}

	if _, _, err := svc.UserForSession(ctx, token); err != nil {
		t.Fatalf("a session predating a no-op restart must survive it: %v", err)
	}
}

// TestBootstrapFirstAdminDoesNotCreateAnAccountOnANonEmptyInstance pins
// the other half of the same decision: a FIRST_ADMIN_EMAIL that matches
// no existing account on a non-empty instance is left alone, not used to
// conjure a brand-new admin account on every boot.
func TestBootstrapFirstAdminDoesNotCreateAnAccountOnANonEmptyInstance(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "nobody-yet@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "someone@studio.com", DisplayName: "Someone", Password: "password12345",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("BootstrapFirstAdmin: %v", err)
	}

	if _, err := svc.Authenticate(ctx, "nobody-yet@studio.com", "password12345"); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("BootstrapFirstAdmin must not create an account on a non-empty instance, err = %v", err)
	}
}

// TestBootstrapFirstAdminIsANoOpWhenConfiguredAdminAlreadyHoldsTheFlag
// pins the common case: nothing changes, and nothing errors, when the
// configured admin already has the flag.
func TestBootstrapFirstAdminIsANoOpWhenConfiguredAdminAlreadyHoldsTheFlag(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "admin@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("first BootstrapFirstAdmin: %v", err)
	}
	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("second BootstrapFirstAdmin: %v", err)
	}

	admin, err := svc.Authenticate(ctx, "admin@studio.com", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !admin.IsAdmin {
		t.Fatal("configured admin should still be an admin")
	}
}

// TestBootstrapFirstAdminLeavesARotatedPasswordAloneWithoutTheOptIn pins
// the gate Task 22's own review demanded after proving live what the
// unconditional reset cost. FIRST_ADMIN_PASSWORD alone is a bootstrap
// seed, nothing more: an admin who deliberately rotates their password
// keeps it across every ordinary restart of an instance that leaves
// FIRST_ADMIN_EMAIL/FIRST_ADMIN_PASSWORD set — as compose.yml ships and
// the readme normalises — and keeps their sessions too.
func TestBootstrapFirstAdminLeavesARotatedPasswordAloneWithoutTheOptIn(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "admin@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("first BootstrapFirstAdmin: %v", err)
	}
	admin, err := svc.Authenticate(ctx, "admin@studio.com", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if err := svc.ChangeOwnPassword(ctx, admin.ID, "password12345", "a-rotated-password"); err != nil {
		t.Fatalf("ChangeOwnPassword: %v", err)
	}
	token, _, err := svc.IssueSession(ctx, admin.ID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	// An ordinary restart: same configuration, no opt-in.
	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("second BootstrapFirstAdmin: %v", err)
	}

	if _, err := svc.Authenticate(ctx, "admin@studio.com", "a-rotated-password"); err != nil {
		t.Fatalf("a deliberately rotated password must survive an ordinary restart: %v", err)
	}
	if _, err := svc.Authenticate(ctx, "admin@studio.com", "password12345"); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("the configured password must stay inert without the opt-in, err = %v", err)
	}
	if _, _, err := svc.UserForSession(ctx, token); err != nil {
		t.Fatalf("a session predating an ordinary restart must survive it: %v", err)
	}
}

// TestBootstrapFirstAdminStillRepromotesWithoutTheOptIn pins the half of
// the recovery path the opt-in deliberately does not gate: restoring
// is_admin destroys nothing an operator would miss, so it stays
// unconditional. Only the password write is opt-in.
func TestBootstrapFirstAdminStillRepromotesWithoutTheOptIn(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "admin@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("first BootstrapFirstAdmin: %v", err)
	}
	admin, err := svc.Authenticate(ctx, "admin@studio.com", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	other, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "other@studio.com", DisplayName: "Other", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser other: %v", err)
	}
	if err := svc.SetAdmin(ctx, other.ID, true); err != nil {
		t.Fatalf("SetAdmin other: %v", err)
	}
	if err := svc.SetAdmin(ctx, admin.ID, false); err != nil {
		t.Fatalf("demote configured admin: %v", err)
	}

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("second BootstrapFirstAdmin: %v", err)
	}

	recovered, err := svc.UserByID(ctx, admin.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if !recovered.IsAdmin {
		t.Fatal("flag restoration must stay unconditional, opt-in or not")
	}
}

// TestBootstrapFirstAdminIgnoresAShortPasswordWithoutTheOptIn pins the
// third problem the opt-in dissolves. Once the reset went through
// ChangePassword, a FIRST_ADMIN_PASSWORD below the minimum length
// aborted start-up on instances that had booted fine for as long as that
// value had only ever been a seed for an already-created account. With
// no opt-in there is no password write, so there is nothing to validate
// and nothing to abort.
func TestBootstrapFirstAdminIgnoresAShortPasswordWithoutTheOptIn(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "admin@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("first BootstrapFirstAdmin: %v", err)
	}

	shortCfg := cfg
	shortCfg.FirstAdminPassword = "short"
	if err := identity.New(pool, shortCfg).BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("a short FIRST_ADMIN_PASSWORD must not abort start-up without the opt-in: %v", err)
	}
	if _, err := svc.Authenticate(ctx, "admin@studio.com", "password12345"); err != nil {
		t.Fatalf("the existing password must be untouched: %v", err)
	}
}

// TestBootstrapFirstAdminRejectsAShortPasswordWithTheOptIn pins the
// other side: once an operator has explicitly asked for the reset, a
// password the product would refuse from any other surface must fail
// loudly at start-up rather than half-apply.
func TestBootstrapFirstAdminRejectsAShortPasswordWithTheOptIn(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "admin@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("first BootstrapFirstAdmin: %v", err)
	}

	shortCfg := cfg
	shortCfg.FirstAdminPassword = "short"
	shortCfg.FirstAdminPasswordReset = true
	err := identity.New(pool, shortCfg).BootstrapFirstAdmin(ctx)
	if err == nil {
		t.Fatal("expected an error for a too-short FIRST_ADMIN_PASSWORD with the opt-in set")
	}
	if !strings.Contains(err.Error(), "FIRST_ADMIN_PASSWORD") {
		t.Fatalf("error = %q, want it to mention FIRST_ADMIN_PASSWORD", err)
	}
}

// TestBootstrapFirstAdminLogsTheRecoveryReset pins the audit trail. The
// reset overwrites a password and revokes every session for the account;
// before Task 22's review it did all of that with no log line at all, on
// a boot whose only output was "maestro listening". The same commit had
// just added actor/target logging to handleSetAdmin on the argument that
// admin promotion was "the most privilege-sensitive mutation in the
// product and the least attributable" — this is strictly more so.
func TestBootstrapFirstAdminLogsTheRecoveryReset(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "admin@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("first BootstrapFirstAdmin: %v", err)
	}
	admin, err := svc.Authenticate(ctx, "admin@studio.com", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if err := svc.ChangeOwnPassword(ctx, admin.ID, "password12345", "a-rotated-password"); err != nil {
		t.Fatalf("ChangeOwnPassword: %v", err)
	}
	// Demote too, so this one boot exercises both log lines.
	other, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "other@studio.com", DisplayName: "Other", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser other: %v", err)
	}
	if err := svc.SetAdmin(ctx, other.ID, true); err != nil {
		t.Fatalf("SetAdmin other: %v", err)
	}
	if err := svc.SetAdmin(ctx, admin.ID, false); err != nil {
		t.Fatalf("demote configured admin: %v", err)
	}

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	resetCfg := cfg
	resetCfg.FirstAdminPasswordReset = true
	if err := identity.New(pool, resetCfg).BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("recovery BootstrapFirstAdmin: %v", err)
	}

	logged := buf.String()
	for _, want := range []string{
		"level=WARN",
		"configured admin re-promoted",
		"configured admin password reset",
		"sessions_revoked=true",
		admin.ID.String(),
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("recovery boot logged %q, want it to contain %q", logged, want)
		}
	}
	// The password itself must never reach a log line.
	if strings.Contains(logged, "password12345") {
		t.Fatalf("recovery boot logged the configured password: %q", logged)
	}
}
