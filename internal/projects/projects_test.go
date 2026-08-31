package projects_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
)

func testConfig() config.Config {
	return config.Config{Argon2: config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}}
}

func newUser(t *testing.T, ids *identity.Service, email string) identity.User {
	t.Helper()
	user, err := ids.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: email, DisplayName: "Someone", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", email, err)
	}
	return user
}

func TestCreateProjectMakesCreatorOwner(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "designer@studio.com")

	project, err := svc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if project.Slug != "azeroth" || project.Name != "Azeroth" {
		t.Fatalf("project = %+v, want slug=azeroth name=Azeroth", project)
	}

	role, err := svc.RoleOf(ctx, user.ID, project.ID)
	if err != nil {
		t.Fatalf("RoleOf: %v", err)
	}
	if role != "owner" {
		t.Fatalf("role = %q, want owner", role)
	}
}

func TestListForUserOnlyReturnsMemberships(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	insider := newUser(t, ids, "in@studio.com")
	outsider := newUser(t, ids, "out@studio.com")

	if _, err := svc.Create(ctx, "azeroth", "Azeroth", insider.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}

	mine, err := svc.ListForUser(ctx, insider.ID)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(mine) != 1 {
		t.Fatalf("insider sees %d projects, want 1", len(mine))
	}

	theirs, err := svc.ListForUser(ctx, outsider.ID)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(theirs) != 0 {
		t.Fatalf("outsider sees %d projects, want 0", len(theirs))
	}
}

func TestRoleOfNonMember(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner@studio.com")
	stranger := newUser(t, ids, "stranger@studio.com")
	project, _ := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	if _, err := svc.RoleOf(ctx, stranger.ID, project.ID); !errors.Is(err, projects.ErrNotAMember) {
		t.Fatalf("err = %v, want ErrNotAMember", err)
	}
}

// TestRoleOfUnknownProjectAlsoErrNotAMember asserts that RoleOf gives a
// stranger the exact same error for "this game doesn't exist" as for "this
// game exists but you're not in it". Distinguishing the two would let a
// non-member enumerate which project IDs are real, which is exactly the
// kind of information the game-isolation invariant says a caller with no
// standing in a game must never get.
func TestRoleOfUnknownProjectAlsoErrNotAMember(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	stranger := newUser(t, ids, "stranger2@studio.com")

	if _, err := svc.RoleOf(ctx, stranger.ID, uuid.New()); !errors.Is(err, projects.ErrNotAMember) {
		t.Fatalf("err = %v, want ErrNotAMember", err)
	}
}

func TestDuplicateSlugRejected(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "designer2@studio.com")
	if _, err := svc.Create(ctx, "azeroth", "Azeroth", user.ID); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(ctx, "AZEROTH", "Azeroth again", user.ID); !errors.Is(err, projects.ErrSlugTaken) {
		t.Fatalf("err = %v, want ErrSlugTaken", err)
	}
}

func TestSlugIsNormalisedToLowerCase(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "designer3@studio.com")
	project, err := svc.Create(ctx, "Azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if project.Slug != "azeroth" {
		t.Fatalf("slug = %q, want lower-cased azeroth", project.Slug)
	}

	found, err := svc.BySlug(ctx, "AZEROTH")
	if err != nil {
		t.Fatalf("BySlug: %v", err)
	}
	if found.ID != project.ID {
		t.Fatalf("BySlug returned a different project")
	}
}

func TestSlugShapeIsValidated(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "designer4@studio.com")

	cases := []string{
		"",                        // empty
		"-azeroth",                // leading hyphen
		"azeroth-",                // trailing hyphen
		"az--eroth",               // consecutive hyphens
		"az eroth",                // whitespace
		"az/eroth",                // slash: would break the URL segment
		"az_eroth",                // underscore: not part of the allowed alphabet
		string(make([]byte, 200)), // absurdly long
	}
	for _, slug := range cases {
		if _, err := svc.Create(ctx, slug, "Azeroth", user.ID); !errors.Is(err, projects.ErrSlugInvalid) {
			t.Fatalf("slug %q: err = %v, want ErrSlugInvalid", slug, err)
		}
	}
}

func TestBySlugUnknownReturnsErrProjectNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := projects.New(pool)
	ctx := context.Background()

	if _, err := svc.BySlug(ctx, "no-such-project"); !errors.Is(err, projects.ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}

func TestListMembersReturnsRolesForEveryMember(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner3@studio.com")
	editor := newUser(t, ids, "editor@studio.com")
	project, _ := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	if err := svc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	members, err := svc.ListMembers(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("len(members) = %d, want 2", len(members))
	}

	roles := map[string]string{}
	for _, m := range members {
		roles[m.Email] = m.Role
	}
	if roles["owner3@studio.com"] != "owner" {
		t.Fatalf("owner role = %q, want owner", roles["owner3@studio.com"])
	}
	if roles["editor@studio.com"] != "editor" {
		t.Fatalf("editor role = %q, want editor", roles["editor@studio.com"])
	}
}

func TestSetRoleCanPromoteAnotherMemberToOwner(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner4@studio.com")
	viewer := newUser(t, ids, "viewer@studio.com")
	project, _ := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	if err := svc.SetRole(ctx, viewer.ID, project.ID, "owner"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	role, err := svc.RoleOf(ctx, viewer.ID, project.ID)
	if err != nil {
		t.Fatalf("RoleOf: %v", err)
	}
	if role != "owner" {
		t.Fatalf("role = %q, want owner", role)
	}

	// With two owners now, demoting the original one must succeed.
	if err := svc.SetRole(ctx, owner.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole demote: %v", err)
	}
}

func TestSetRoleRejectsInvalidRole(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner5@studio.com")
	other := newUser(t, ids, "other@studio.com")
	project, _ := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	if err := svc.SetRole(ctx, other.ID, project.ID, "superadmin"); !errors.Is(err, projects.ErrRoleInvalid) {
		t.Fatalf("err = %v, want ErrRoleInvalid", err)
	}
}

func TestSetRoleCannotDemoteSoleOwner(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner6@studio.com")
	project, _ := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	if err := svc.SetRole(ctx, owner.ID, project.ID, "editor"); !errors.Is(err, projects.ErrLastOwner) {
		t.Fatalf("err = %v, want ErrLastOwner", err)
	}

	// The role must be unchanged.
	role, err := svc.RoleOf(ctx, owner.ID, project.ID)
	if err != nil {
		t.Fatalf("RoleOf: %v", err)
	}
	if role != "owner" {
		t.Fatalf("role = %q, want owner (unchanged)", role)
	}
}

func TestRemoveMemberCannotRemoveSoleOwner(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner7@studio.com")
	project, _ := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	if err := svc.RemoveMember(ctx, owner.ID, project.ID); !errors.Is(err, projects.ErrLastOwner) {
		t.Fatalf("err = %v, want ErrLastOwner", err)
	}

	role, err := svc.RoleOf(ctx, owner.ID, project.ID)
	if err != nil {
		t.Fatalf("RoleOf: %v", err)
	}
	if role != "owner" {
		t.Fatalf("owner membership should still exist")
	}
}

func TestRemoveMemberSucceedsWithSecondOwner(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner8@studio.com")
	second := newUser(t, ids, "second@studio.com")
	project, _ := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	if err := svc.SetRole(ctx, second.ID, project.ID, "owner"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if err := svc.RemoveMember(ctx, owner.ID, project.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if _, err := svc.RoleOf(ctx, owner.ID, project.ID); !errors.Is(err, projects.ErrNotAMember) {
		t.Fatalf("err = %v, want ErrNotAMember", err)
	}
}

func TestRemoveMemberOfNonMemberIsNoop(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner9@studio.com")
	stranger := newUser(t, ids, "stranger3@studio.com")
	project, _ := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	if err := svc.RemoveMember(ctx, stranger.ID, project.ID); err != nil {
		t.Fatalf("RemoveMember of non-member: %v", err)
	}
}

func TestByIDRoundTrips(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner10@studio.com")
	project, _ := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)

	found, err := svc.ByID(ctx, project.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if found.Slug != "azeroth" {
		t.Fatalf("found.Slug = %q, want azeroth", found.Slug)
	}

	if _, err := svc.ByID(ctx, uuid.New()); !errors.Is(err, projects.ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}
