package identity_test

import (
	"context"
	"errors"
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
func TestConcurrentDemotionsOfTheLastTwoAdminsLeaveExactlyOne(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	admin1, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "race1@studio.com", DisplayName: "Race One", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser admin1: %v", err)
	}
	if err := svc.SetAdmin(ctx, admin1.ID, true); err != nil {
		t.Fatalf("SetAdmin admin1: %v", err)
	}
	admin2, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email: "race2@studio.com", DisplayName: "Race Two", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser admin2: %v", err)
	}
	if err := svc.SetAdmin(ctx, admin2.ID, true); err != nil {
		t.Fatalf("SetAdmin admin2: %v", err)
	}

	var wg sync.WaitGroup
	var successes atomic.Int64
	for _, id := range []uuid.UUID{admin1.ID, admin2.ID} {
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			if err := svc.SetAdmin(ctx, id, false); err == nil {
				successes.Add(1)
			}
		}(id)
	}
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Fatalf("successful concurrent demotions = %d, want exactly 1", got)
	}

	rows := 0
	for _, id := range []uuid.UUID{admin1.ID, admin2.ID} {
		u, err := svc.UserByID(ctx, id)
		if err != nil {
			t.Fatalf("UserByID: %v", err)
		}
		if u.IsAdmin {
			rows++
		}
	}
	if rows != 1 {
		t.Fatalf("remaining admins among the two = %d, want exactly 1", rows)
	}
}
