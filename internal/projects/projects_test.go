package projects_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/roles"
	"github.com/neverbot/maestro/internal/testutil"
)

func testConfig() config.Config {
	return config.Config{Argon2: config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}}
}

// newUser creates a user whose display name is derived from the local
// part of its (unique, per-test) email, rather than a constant like
// "Someone" repeated across every test user. Ordering tests
// (TestListForUserOrderingIsStableOnTies, TestListMembersOrderingIsStable
// OnTies) rely on distinct display names existing by default so that a
// name-collision test is the one deliberately forcing a tie, not an
// accident every other test also happens to share.
func newUser(t *testing.T, ids *identity.Service, email string) identity.User {
	t.Helper()
	return newUserNamed(t, ids, email, strings.TrimSuffix(email, "@studio.com"))
}

func newUserNamed(t *testing.T, ids *identity.Service, email, displayName string) identity.User {
	t.Helper()
	user, err := ids.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: email, DisplayName: displayName, Password: "password12345",
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

func TestCreateUnknownCreatorReturnsErrUserNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := projects.New(pool)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "azeroth", "Azeroth", uuid.New()); !errors.Is(err, projects.ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
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

// TestListForUserOrderingIsStableOnTies exercises the `, id` tiebreak
// ListProjectsForUser's query added: with two projects sharing the same
// name (the only thing every other test's distinct-slug convention was
// hiding), the order must still be deterministic across repeated calls,
// not whatever order Postgres happens to return ties in.
func TestListForUserOrderingIsStableOnTies(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "orderer@studio.com")
	if _, err := svc.Create(ctx, "untitled-1", "Untitled", user.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Create(ctx, "untitled-2", "Untitled", user.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}

	first, err := svc.ListForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("len = %d, want 2", len(first))
	}
	if first[0].ID.String() >= first[1].ID.String() {
		t.Fatalf("tied names not broken by id ascending: %s then %s", first[0].ID, first[1].ID)
	}

	for i := 0; i < 3; i++ {
		again, err := svc.ListForUser(ctx, user.ID)
		if err != nil {
			t.Fatalf("ListForUser: %v", err)
		}
		if again[0].ID != first[0].ID || again[1].ID != first[1].ID {
			t.Fatalf("order changed between calls: got [%s %s], want [%s %s]",
				again[0].ID, again[1].ID, first[0].ID, first[1].ID)
		}
	}
}

func TestRoleOfNonMember(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner@studio.com")
	stranger := newUser(t, ids, "stranger@studio.com")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

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

	found, err := svc.BySlugForUser(ctx, "AZEROTH", user.ID)
	if err != nil {
		t.Fatalf("BySlugForUser: %v", err)
	}
	if found.ID != project.ID {
		t.Fatalf("BySlugForUser returned a different project")
	}
}

func TestSlugShapeIsValidated(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "designer4@studio.com")

	cases := []string{
		"",                                     // empty
		"-azeroth",                             // leading hyphen
		"azeroth-",                             // trailing hyphen
		"az--eroth",                            // consecutive hyphens
		"az eroth",                             // whitespace
		"az/eroth",                             // slash: would break the URL segment
		"az_eroth",                             // underscore: not part of the allowed alphabet
		strings.Repeat("a", 100),               // absurdly long
		"new",                                  // reserved: would shadow a future create-game page
		"api",                                  // reserved: would collide with /api
		"healthz",                              // reserved: would collide with /healthz
		"login",                                // reserved: would collide with /login
		"123e4567-e89b-42d3-a456-426614174000", // UUID-shaped
	}
	for _, slug := range cases {
		if _, err := svc.Create(ctx, slug, "Azeroth", user.ID); !errors.Is(err, projects.ErrSlugInvalid) {
			t.Fatalf("slug %q: err = %v, want ErrSlugInvalid", slug, err)
		}
	}
}

func TestCreateRejectsInvalidName(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "name-check@studio.com")

	cases := []struct {
		slug string
		name string
	}{
		{"name-empty", ""},
		{"name-too-long", strings.Repeat("a", 201)},
		{"name-control-char", "line one\nline two"},
		{"name-bidi-override", "evil‮reversed"},
	}
	for _, c := range cases {
		if _, err := svc.Create(ctx, c.slug, c.name, user.ID); !errors.Is(err, projects.ErrNameInvalid) {
			t.Fatalf("name %q: err = %v, want ErrNameInvalid", c.name, err)
		}
	}
}

func TestBySlugForUserUnknownReturnsErrProjectNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "slug-lookup@studio.com")

	if _, err := svc.BySlugForUser(ctx, "no-such-project", user.ID); !errors.Is(err, projects.ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}

// TestBySlugForUserHidesExistenceFromNonMembers asserts that a real,
// existing game's slug looks exactly like an unknown one to a caller who
// is not a member of it: both return ErrProjectNotFound, never
// ErrNotAMember or any other signal that would let an authenticated user
// probe which human-chosen slugs are already taken by games they cannot
// see.
func TestBySlugForUserHidesExistenceFromNonMembers(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "slug-owner@studio.com")
	stranger := newUser(t, ids, "slug-stranger@studio.com")
	if _, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := svc.BySlugForUser(ctx, "azeroth", stranger.ID)
	if !errors.Is(err, projects.ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}

func TestBySlugForUserSucceedsForMember(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "slug-member-owner@studio.com")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	found, err := svc.BySlugForUser(ctx, "azeroth", owner.ID)
	if err != nil {
		t.Fatalf("BySlugForUser: %v", err)
	}
	if found.ID != project.ID {
		t.Fatalf("found a different project")
	}
}

func TestListMembersReturnsRolesForEveryMember(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner3@studio.com")
	editor := newUser(t, ids, "editor@studio.com")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

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

	roleByUser := map[uuid.UUID]string{}
	for _, m := range members {
		roleByUser[m.UserID] = m.Role
	}
	if roleByUser[owner.ID] != "owner" {
		t.Fatalf("owner role = %q, want owner", roleByUser[owner.ID])
	}
	if roleByUser[editor.ID] != "editor" {
		t.Fatalf("editor role = %q, want editor", roleByUser[editor.ID])
	}
}

// TestListMembersOrderingIsStableOnTies is ListForUser's ordering test
// (above) mirrored for ListMembers: two members forced to share a display
// name must still come back in a deterministic order across repeated
// calls, via the `, id` tiebreak ListMembers' query added.
func TestListMembersOrderingIsStableOnTies(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "tie-owner@studio.com")
	twinA := newUserNamed(t, ids, "tie-a@studio.com", "Twin")
	twinB := newUserNamed(t, ids, "tie-b@studio.com", "Twin")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.SetRole(ctx, twinA.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if err := svc.SetRole(ctx, twinB.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	first, err := svc.ListMembers(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	var twins []uuid.UUID
	for _, m := range first {
		if m.DisplayName == "Twin" {
			twins = append(twins, m.UserID)
		}
	}
	if len(twins) != 2 {
		t.Fatalf("len(twins) = %d, want 2", len(twins))
	}
	if twins[0].String() >= twins[1].String() {
		t.Fatalf("tied names not broken by id ascending: %s then %s", twins[0], twins[1])
	}

	for i := 0; i < 3; i++ {
		again, err := svc.ListMembers(ctx, project.ID)
		if err != nil {
			t.Fatalf("ListMembers: %v", err)
		}
		var againTwins []uuid.UUID
		for _, m := range again {
			if m.DisplayName == "Twin" {
				againTwins = append(againTwins, m.UserID)
			}
		}
		if len(againTwins) != 2 || againTwins[0] != twins[0] || againTwins[1] != twins[1] {
			t.Fatalf("order changed between calls: got %v, want %v", againTwins, twins)
		}
	}
}

func TestSetRoleCanPromoteAnotherMemberToOwner(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner4@studio.com")
	viewer := newUser(t, ids, "viewer@studio.com")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

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

// TestSetRoleHasNoAuthorizationCheck pins, as a documented decision
// rather than an accident, that SetRole trusts its caller entirely: it
// grants membership to a user with no prior relationship to the project
// at all -- no invite, no existing role, nothing -- because deciding who
// may call SetRole is Task 12's job, not this package's. If a future
// change adds an ownership check inside SetRole itself, this test starts
// failing, forcing whoever did that to notice and update this comment
// rather than the contract silently shifting underneath Task 12's own
// authorization logic.
func TestSetRoleHasNoAuthorizationCheck(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "noauth-owner@studio.com")
	stranger := newUser(t, ids, "noauth-stranger@studio.com")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.SetRole(ctx, stranger.ID, project.ID, "owner"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	role, err := svc.RoleOf(ctx, stranger.ID, project.ID)
	if err != nil {
		t.Fatalf("RoleOf: %v", err)
	}
	if role != "owner" {
		t.Fatalf("role = %q, want owner", role)
	}
}

func TestSetRoleRejectsInvalidRole(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner5@studio.com")
	other := newUser(t, ids, "other@studio.com")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

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
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

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

func TestSetRoleUnknownProjectReturnsErrProjectNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "fk-project@studio.com")
	if err := svc.SetRole(ctx, user.ID, uuid.New(), "editor"); !errors.Is(err, projects.ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}

func TestSetRoleUnknownUserReturnsErrUserNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "fk-user-owner@studio.com")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.SetRole(ctx, uuid.New(), project.ID, "editor"); !errors.Is(err, projects.ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

func TestRemoveMemberCannotRemoveSoleOwner(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner7@studio.com")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

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
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

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
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.RemoveMember(ctx, stranger.ID, project.ID); err != nil {
		t.Fatalf("RemoveMember of non-member: %v", err)
	}
}

// TestRemoveMemberRevokesTheirTokensInThatProject guards the hole a
// review found in Task 9's original draft: Task 10's auth middleware
// reads a resolved token's ProjectID and treats it as the caller's
// scope, so a removed member whose token kept working would still have
// full access to a game they were just expelled from. RemoveMember must
// close that in the same call, not leave it as something an operator
// remembers to do separately.
func TestRemoveMemberRevokesTheirTokensInThatProject(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner10@studio.com")
	member := newUser(t, ids, "member1@studio.com")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.SetRole(ctx, member.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: project.ID, UserID: member.ID, Label: "member's agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	if err := svc.RemoveMember(ctx, member.ID, project.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	if _, err := ids.ResolveAPIToken(ctx, token); !errors.Is(err, identity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid: a removed member's token in that game must stop working", err)
	}
}

// TestRemoveMemberLeavesTheirTokensInOtherProjectsAlone pins the exact
// blast radius RemoveMember's own doc comment commits to: removing a
// member from one game must not touch tokens the same person holds for
// a different game they are still a member of.
func TestRemoveMemberLeavesTheirTokensInOtherProjectsAlone(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner11@studio.com")
	member := newUser(t, ids, "member2@studio.com")
	azeroth, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create (azeroth): %v", err)
	}
	leMans, err := svc.Create(ctx, "le-mans", "Le Mans", owner.ID)
	if err != nil {
		t.Fatalf("Create (le mans): %v", err)
	}
	if err := svc.SetRole(ctx, member.ID, azeroth.ID, "editor"); err != nil {
		t.Fatalf("SetRole (azeroth): %v", err)
	}
	if err := svc.SetRole(ctx, member.ID, leMans.ID, "editor"); err != nil {
		t.Fatalf("SetRole (le mans): %v", err)
	}
	otherProjectToken, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: leMans.ID, UserID: member.ID, Label: "member's other agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	if err := svc.RemoveMember(ctx, member.ID, azeroth.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	if _, err := ids.ResolveAPIToken(ctx, otherProjectToken); err != nil {
		t.Fatalf("token in an unrelated project was revoked by a different game's RemoveMember: %v", err)
	}
}

func TestByIDRoundTrips(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner10@studio.com")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

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

// TestConcurrentRemovalLeavesExactlyOneOwner pins the end-to-end
// invariant: under concurrent removal, a project always keeps exactly one
// of its last two owners, never zero. Every other test in this file
// drives RemoveMember/SetRole from a single goroutine, so this is the
// only one that would notice the invariant breaking under a race at all.
//
// It does NOT demonstrate that CountOwnersForUpdate's FOR UPDATE lock is
// what makes this safe, and its comment used to claim exactly that — a
// claim a quality review disproved directly: stripping FOR UPDATE from
// CountOwnersForUpdate and running this test roughly eighty times
// produced zero failures. The invariant is defended in two independent
// layers — this package's row lock, and migration 0002's constraint
// trigger, which re-checks "does this project still have an owner" at
// commit time regardless of any lock — and the trigger alone already
// serializes the two-goroutine race this test drives, because only one
// of two concurrent transactions can be the second to reach its deferred
// commit-time check. Dropping either layer alone will not turn this test
// red; both would have to go missing at once for that. What the lock
// still buys, independent of correctness, is failing fast inside this
// package with a typed ErrLastOwner instead of the transaction aborting
// on a raw trigger exception — worth keeping as deliberate defence in
// depth, not because this test proves it load-bearing.
//
// Five runs, two goroutines each racing to remove one of a project's two
// owners: exactly one must succeed and the other must see ErrLastOwner,
// every time, and the project must never end up with zero owners.
func TestConcurrentRemovalLeavesExactlyOneOwner(t *testing.T) {
	for run := 0; run < 5; run++ {
		pool := testutil.NewPool(t)
		ids := identity.New(pool, testConfig())
		svc := projects.New(pool)
		ctx := context.Background()

		ownerA := newUser(t, ids, fmt.Sprintf("race-a-%d@studio.com", run))
		ownerB := newUser(t, ids, fmt.Sprintf("race-b-%d@studio.com", run))
		project, err := svc.Create(ctx, fmt.Sprintf("race-%d", run), "Race", ownerA.ID)
		if err != nil {
			t.Fatalf("run %d: Create: %v", run, err)
		}
		if err := svc.SetRole(ctx, ownerB.ID, project.ID, "owner"); err != nil {
			t.Fatalf("run %d: SetRole: %v", run, err)
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); errs[0] = svc.RemoveMember(ctx, ownerA.ID, project.ID) }()
		go func() { defer wg.Done(); errs[1] = svc.RemoveMember(ctx, ownerB.ID, project.ID) }()
		wg.Wait()

		succeeded, refused := 0, 0
		for _, e := range errs {
			switch {
			case e == nil:
				succeeded++
			case errors.Is(e, projects.ErrLastOwner):
				refused++
			default:
				t.Fatalf("run %d: unexpected error: %v", run, e)
			}
		}
		if succeeded != 1 || refused != 1 {
			t.Fatalf("run %d: succeeded=%d refused=%d, want exactly one of each", run, succeeded, refused)
		}

		remainingOwners := 0
		for _, uid := range []uuid.UUID{ownerA.ID, ownerB.ID} {
			if role, err := svc.RoleOf(ctx, uid, project.ID); err == nil && role == "owner" {
				remainingOwners++
			}
		}
		if remainingOwners != 1 {
			t.Fatalf("run %d: %d owners remain, want exactly 1", run, remainingOwners)
		}
	}
}

// TestDeletingSoleOwnerUserIsBlockedByDatabase proves migration 0002's
// constraint trigger, not this package's Go code, is what stops the
// back door: memberships.user_id is ON DELETE CASCADE, so deleting a
// user row deletes their memberships underneath this package entirely,
// with none of SetRole's or RemoveMember's own guards ever running.
// identity.Service has no DeleteUser yet (Task 8's corrections note this
// is latent until one lands), so the delete is issued directly here to
// simulate one.
func TestDeletingSoleOwnerUserIsBlockedByDatabase(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "cascade-owner@studio.com")
	project, err := svc.Create(ctx, "cascade-azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID); err == nil {
		t.Fatal("deleting the sole owner's user row succeeded, want the last-owner trigger to block it")
	}

	role, err := svc.RoleOf(ctx, owner.ID, project.ID)
	if err != nil {
		t.Fatalf("RoleOf after blocked delete: %v", err)
	}
	if role != "owner" {
		t.Fatalf("role = %q, want owner (the blocked delete must have rolled back)", role)
	}
}

// TestDeletingNonSoleOwnerUserSucceeds is
// TestDeletingSoleOwnerUserIsBlockedByDatabase's counterpart: the trigger
// must not fire at all when a project still has another owner left after
// the delete.
func TestDeletingNonSoleOwnerUserSucceeds(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	ownerA := newUser(t, ids, "cascade-a@studio.com")
	ownerB := newUser(t, ids, "cascade-b@studio.com")
	project, err := svc.Create(ctx, "cascade-two-owners", "Two Owners", ownerA.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.SetRole(ctx, ownerB.ID, project.ID, "owner"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", ownerA.ID); err != nil {
		t.Fatalf("delete user with a second owner present: %v", err)
	}
	role, err := svc.RoleOf(ctx, ownerB.ID, project.ID)
	if err != nil || role != "owner" {
		t.Fatalf("RoleOf(ownerB) = (%q, %v), want (owner, nil)", role, err)
	}
}

// TestDeletingProjectCascadesDespiteTrigger is the guard the trigger's own
// "project itself is being deleted" escape hatch exists for: deleting a
// project cascades to its sole owner's membership row too, and that must
// succeed rather than the trigger mistaking a legitimate project deletion
// for an attempt to strip a live project of its last owner.
func TestDeletingProjectCascadesDespiteTrigger(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "cascade-project-owner@studio.com")
	project, err := svc.Create(ctx, "cascade-doomed", "Doomed", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM projects WHERE id = $1", project.ID); err != nil {
		t.Fatalf("delete project (must not trip the last-owner trigger): %v", err)
	}
	if _, err := svc.ByID(ctx, project.ID); !errors.Is(err, projects.ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}

// TestAllRolesAcceptedByDatabase walks roles.All() and inserts each one
// directly, bypassing SetRole's own Go-side validation, to catch this
// package (or identity's) role vocabulary drifting from the memberships
// table's CHECK constraint in migration 0001 -- the failure mode
// roles.Valid exists to catch before it ever reaches SQL. A role Go
// considers valid but the database rejects (or the reverse) fails this
// test; no doc comment can do that job.
func TestAllRolesAcceptedByDatabase(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "roles-owner@studio.com")
	project, err := svc.Create(ctx, "roles-check", "Roles Check", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	q := dbq.New(pool)
	for i, r := range roles.All() {
		member := newUser(t, ids, fmt.Sprintf("roles-member-%d@studio.com", i))
		if err := q.UpsertMembership(ctx, dbq.UpsertMembershipParams{
			UserID: member.ID, ProjectID: project.ID, Role: string(r),
		}); err != nil {
			t.Fatalf("role %q rejected by the database: %v", r, err)
		}
	}

	bogus := newUser(t, ids, "roles-bogus@studio.com")
	if err := q.UpsertMembership(ctx, dbq.UpsertMembershipParams{
		UserID: bogus.ID, ProjectID: project.ID, Role: "superadmin",
	}); err == nil {
		t.Fatal("a role outside roles.All() was accepted by the database")
	}
}
