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
	return newUserNamed(t, ids, email, strings.TrimSuffix(email, "@example.test"))
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "designer@example.test")

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

func TestCreateUnknownCreatorReturnsErrMemberNotFound(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	svc := projects.New(pool)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "azeroth", "Azeroth", uuid.New()); !errors.Is(err, projects.ErrMemberNotFound) {
		t.Fatalf("err = %v, want ErrMemberNotFound", err)
	}
}

func TestListForUserOnlyReturnsMemberships(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	insider := newUser(t, ids, "in@example.test")
	outsider := newUser(t, ids, "out@example.test")

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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "orderer@example.test")
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner@example.test")
	stranger := newUser(t, ids, "stranger@example.test")
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	stranger := newUser(t, ids, "stranger2@example.test")

	if _, err := svc.RoleOf(ctx, stranger.ID, uuid.New()); !errors.Is(err, projects.ErrNotAMember) {
		t.Fatalf("err = %v, want ErrNotAMember", err)
	}
}

func TestDuplicateSlugRejected(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "designer2@example.test")
	if _, err := svc.Create(ctx, "azeroth", "Azeroth", user.ID); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(ctx, "AZEROTH", "Azeroth again", user.ID); !errors.Is(err, projects.ErrSlugTaken) {
		t.Fatalf("err = %v, want ErrSlugTaken", err)
	}
}

func TestSlugIsNormalisedToLowerCase(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "designer3@example.test")
	project, err := svc.Create(ctx, "Azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if project.Slug != "azeroth" {
		t.Fatalf("slug = %q, want lower-cased azeroth", project.Slug)
	}
}

func TestSlugShapeIsValidated(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "designer4@example.test")

	cases := []string{
		// The empty string is not here any more: it is the one value
		// Create reads as "derive an address from the name", and
		// TestAnEmptySlugIsDerivedFromTheName holds that half.
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "name-check@example.test")

	cases := []struct {
		slug string
		name string
	}{
		{"name-empty", ""},
		{"name-too-long", strings.Repeat("a", 201)},
		{"name-control-char", "line one\nline two"},
		{"name-bidi-override", "evil\u202ereversed"},
	}
	for _, c := range cases {
		if _, err := svc.Create(ctx, c.slug, c.name, user.ID); !errors.Is(err, projects.ErrNameInvalid) {
			t.Fatalf("name %q: err = %v, want ErrNameInvalid", c.name, err)
		}
	}
}

func TestListMembersReturnsRolesForEveryMember(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner3@example.test")
	editor := newUser(t, ids, "editor@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.SetRole(ctx, editor.ID, project.ID, "editor"); err != nil {
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "tie-owner@example.test")
	twinA := newUserNamed(t, ids, "tie-a@example.test", "Twin")
	twinB := newUserNamed(t, ids, "tie-b@example.test", "Twin")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.SetRole(ctx, twinA.ID, project.ID, "viewer"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if _, err := svc.SetRole(ctx, twinB.ID, project.ID, "viewer"); err != nil {
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner4@example.test")
	viewer := newUser(t, ids, "viewer@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.SetRole(ctx, viewer.ID, project.ID, "owner"); err != nil {
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
	if _, err := svc.SetRole(ctx, owner.ID, project.ID, "editor"); err != nil {
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "noauth-owner@example.test")
	stranger := newUser(t, ids, "noauth-stranger@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.SetRole(ctx, stranger.ID, project.ID, "owner"); err != nil {
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner5@example.test")
	other := newUser(t, ids, "other@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.SetRole(ctx, other.ID, project.ID, "superadmin"); !errors.Is(err, projects.ErrRoleInvalid) {
		t.Fatalf("err = %v, want ErrRoleInvalid", err)
	}
}

func TestSetRoleCannotDemoteSoleOwner(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner6@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.SetRole(ctx, owner.ID, project.ID, "editor"); !errors.Is(err, projects.ErrLastOwner) {
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "fk-project@example.test")
	if _, err := svc.SetRole(ctx, user.ID, uuid.New(), "editor"); !errors.Is(err, projects.ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}

func TestSetRoleUnknownUserReturnsErrMemberNotFound(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "fk-user-owner@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.SetRole(ctx, uuid.New(), project.ID, "editor"); !errors.Is(err, projects.ErrMemberNotFound) {
		t.Fatalf("err = %v, want ErrMemberNotFound", err)
	}
}

func TestRemoveMemberCannotRemoveSoleOwner(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner7@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.RemoveMember(ctx, owner.ID, project.ID); !errors.Is(err, projects.ErrLastOwner) {
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner8@example.test")
	second := newUser(t, ids, "second@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.SetRole(ctx, second.ID, project.ID, "owner"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if _, err := svc.RemoveMember(ctx, owner.ID, project.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if _, err := svc.RoleOf(ctx, owner.ID, project.ID); !errors.Is(err, projects.ErrNotAMember) {
		t.Fatalf("err = %v, want ErrNotAMember", err)
	}
}

func TestRemoveMemberOfNonMemberIsNoop(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner9@example.test")
	stranger := newUser(t, ids, "stranger3@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if revoked, err := svc.RemoveMember(ctx, stranger.ID, project.ID); err != nil {
		t.Fatalf("RemoveMember of non-member: %v", err)
	} else if len(revoked) != 0 {
		t.Fatalf("revoked = %v, want none for a non-member", revoked)
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner10@example.test")
	member := newUser(t, ids, "member1@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.SetRole(ctx, member.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: project.ID, UserID: member.ID, Label: "member's agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	revoked, err := svc.RemoveMember(ctx, member.ID, project.ID)
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != "member's agent" {
		t.Fatalf("revoked = %v, want [\"member's agent\"]", revoked)
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner11@example.test")
	member := newUser(t, ids, "member2@example.test")
	azeroth, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create (azeroth): %v", err)
	}
	leMans, err := svc.Create(ctx, "le-mans", "Le Mans", owner.ID)
	if err != nil {
		t.Fatalf("Create (le mans): %v", err)
	}
	if _, err := svc.SetRole(ctx, member.ID, azeroth.ID, "editor"); err != nil {
		t.Fatalf("SetRole (azeroth): %v", err)
	}
	if _, err := svc.SetRole(ctx, member.ID, leMans.ID, "editor"); err != nil {
		t.Fatalf("SetRole (le mans): %v", err)
	}
	otherProjectToken, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: leMans.ID, UserID: member.ID, Label: "member's other agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	if _, err := svc.RemoveMember(ctx, member.ID, azeroth.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	if _, err := ids.ResolveAPIToken(ctx, otherProjectToken); err != nil {
		t.Fatalf("token in an unrelated project was revoked by a different game's RemoveMember: %v", err)
	}
}

func TestByIDRoundTrips(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner10@example.test")
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
	t.Parallel()
	for run := 0; run < 5; run++ {
		pool := testutil.NewPool(t)
		ids := identity.New(pool, testConfig())
		svc := projects.New(pool)
		ctx := context.Background()

		ownerA := newUser(t, ids, fmt.Sprintf("race-a-%d@example.test", run))
		ownerB := newUser(t, ids, fmt.Sprintf("race-b-%d@example.test", run))
		project, err := svc.Create(ctx, fmt.Sprintf("race-%d", run), "Race", ownerA.ID)
		if err != nil {
			t.Fatalf("run %d: Create: %v", run, err)
		}
		if _, err := svc.SetRole(ctx, ownerB.ID, project.ID, "owner"); err != nil {
			t.Fatalf("run %d: SetRole: %v", run, err)
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); _, errs[0] = svc.RemoveMember(ctx, ownerA.ID, project.ID) }()
		go func() { defer wg.Done(); _, errs[1] = svc.RemoveMember(ctx, ownerB.ID, project.ID) }()
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

// TestConcurrentRemovalAndDemotionOfDifferentOwnersLeavesExactlyOneOwner
// is the mixed race neither TestConcurrentRemovalLeavesExactlyOneOwner
// (both goroutines call RemoveMember) nor any SetRole test alone
// exercises: one goroutine removing owner A while a second demotes owner
// B to viewer, concurrently, against the same two-owner project.
// CountOwnersForUpdate's row lock and migration 0002's constraint
// trigger both apply identically regardless of which of the two methods
// is doing the counting or the demoting, so the same invariant —
// exactly one of the two must win, the project never ends up with zero
// owners — has to hold across the mix, not just within either method on
// its own.
func TestConcurrentRemovalAndDemotionOfDifferentOwnersLeavesExactlyOneOwner(t *testing.T) {
	t.Parallel()
	for run := 0; run < 5; run++ {
		pool := testutil.NewPool(t)
		ids := identity.New(pool, testConfig())
		svc := projects.New(pool)
		ctx := context.Background()

		ownerA := newUser(t, ids, fmt.Sprintf("mixed-race-a-%d@example.test", run))
		ownerB := newUser(t, ids, fmt.Sprintf("mixed-race-b-%d@example.test", run))
		project, err := svc.Create(ctx, fmt.Sprintf("mixed-race-%d", run), "Mixed Race", ownerA.ID)
		if err != nil {
			t.Fatalf("run %d: Create: %v", run, err)
		}
		if _, err := svc.SetRole(ctx, ownerB.ID, project.ID, "owner"); err != nil {
			t.Fatalf("run %d: SetRole: %v", run, err)
		}

		var wg sync.WaitGroup
		var removeErr, demoteErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, removeErr = svc.RemoveMember(ctx, ownerA.ID, project.ID) }()
		go func() { defer wg.Done(); _, demoteErr = svc.SetRole(ctx, ownerB.ID, project.ID, "viewer") }()
		wg.Wait()

		succeeded, refused := 0, 0
		for _, e := range []error{removeErr, demoteErr} {
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
			t.Fatalf("run %d: succeeded=%d refused=%d, want exactly one of each (removeErr=%v demoteErr=%v)", run, succeeded, refused, removeErr, demoteErr)
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

// TestSetRoleDemotionBelowEditorRevokesTheDemotedMembersTokens pins the
// design a quality review settled on for Task 12: a token is
// editor-equivalent and carries no role of its own, so the only way to
// keep it from ever exceeding its minter's current standing — without a
// membership lookup on every authenticated request — is to revoke it the
// moment that standing drops below editor. Demoting to editor or
// promoting must never trigger this; only a demotion to viewer does,
// since editor and owner both remain roles.AtLeast Editor.
func TestSetRoleDemotionBelowEditorRevokesTheDemotedMembersTokens(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "demote-owner@example.test")
	member := newUser(t, ids, "demote-member@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.SetRole(ctx, member.ID, project.ID, "editor"); err != nil {
		t.Fatalf("SetRole (editor): %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: project.ID, UserID: member.ID, Label: "demoted member's agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// Demoting editor to editor (a no-op change) and re-promoting must
	// not revoke anything: both stay AtLeast Editor, and SetRole must
	// report no revoked labels for either.
	revoked, err := svc.SetRole(ctx, member.ID, project.ID, "editor")
	if err != nil {
		t.Fatalf("SetRole (editor again): %v", err)
	}
	if len(revoked) != 0 {
		t.Fatalf("revoked = %v, want none for a same-tier role change", revoked)
	}
	if _, err := ids.ResolveAPIToken(ctx, token); err != nil {
		t.Fatalf("token revoked by a same-tier role change: %v", err)
	}

	revoked, err = svc.SetRole(ctx, member.ID, project.ID, "viewer")
	if err != nil {
		t.Fatalf("SetRole (viewer): %v", err)
	}
	if len(revoked) != 1 || revoked[0] != "demoted member's agent" {
		t.Fatalf("revoked = %v, want [\"demoted member's agent\"]", revoked)
	}
	if _, err := ids.ResolveAPIToken(ctx, token); !errors.Is(err, identity.ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid: a demotion below editor must revoke the member's tokens", err)
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "cascade-owner@example.test")
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	ownerA := newUser(t, ids, "cascade-a@example.test")
	ownerB := newUser(t, ids, "cascade-b@example.test")
	project, err := svc.Create(ctx, "cascade-two-owners", "Two Owners", ownerA.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.SetRole(ctx, ownerB.ID, project.ID, "owner"); err != nil {
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "cascade-project-owner@example.test")
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
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "roles-owner@example.test")
	project, err := svc.Create(ctx, "roles-check", "Roles Check", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	q := dbq.New(pool)
	for i, r := range roles.All() {
		member := newUser(t, ids, fmt.Sprintf("roles-member-%d@example.test", i))
		if err := q.UpsertMembership(ctx, dbq.UpsertMembershipParams{
			UserID: member.ID, ProjectID: project.ID, Role: string(r),
		}); err != nil {
			t.Fatalf("role %q rejected by the database: %v", r, err)
		}
	}

	bogus := newUser(t, ids, "roles-bogus@example.test")
	if err := q.UpsertMembership(ctx, dbq.UpsertMembershipParams{
		UserID: bogus.ID, ProjectID: project.ID, Role: "superadmin",
	}); err == nil {
		t.Fatal("a role outside roles.All() was accepted by the database")
	}
}

// TestDeleteProjectCascadesMembershipsAndTokens exercises Task 17's own
// claim: a plain DELETE FROM projects is enough on its own, with no
// hand-rolled transaction, because every membership and API token row
// scoped to the project cascades away through the ON DELETE CASCADE
// foreign keys migration 0001 already declares, and migration 0002's
// last-owner trigger has an escape hatch built for exactly this
// statement (see Service.Delete's own doc comment).
//
// This is also the escape hatch's own regression guard: project here has
// exactly one member, its owner, so if that escape hatch ever stopped
// recognising this statement, the trigger would raise on the cascaded
// membership delete and svc.Delete below would fail loudly with a raw
// constraint-violation error instead of quietly returning nil.
func TestDeleteProjectCascadesMembershipsAndTokens(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: owner.ID, Label: "agent"}); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	if err := svc.Delete(ctx, project.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := svc.ByID(ctx, project.ID); !errors.Is(err, projects.ErrProjectNotFound) {
		t.Fatalf("ByID after delete: err = %v, want ErrProjectNotFound", err)
	}
	if _, err := svc.RoleOf(ctx, owner.ID, project.ID); !errors.Is(err, projects.ErrNotAMember) {
		t.Fatalf("RoleOf after delete: err = %v, want ErrNotAMember (membership must be gone)", err)
	}
}

// TestDeleteProjectTwiceIsIdempotent pins the concurrent-delete
// behaviour Service.Delete's own doc comment describes: a second delete
// of an id already gone is not an error, the same convention
// DeleteMembership and RevokeAPITokensForMember already follow. Two
// requests racing to delete the same game both see a plain success, not
// one 204 and one 500 for what is, from either caller's perspective, the
// same outcome ("the game is gone").
func TestDeleteProjectTwiceIsIdempotent(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	owner := newUser(t, ids, "owner@example.test")
	project, err := svc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.Delete(ctx, project.ID); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := svc.Delete(ctx, project.ID); err != nil {
		t.Fatalf("second Delete on an already-deleted project: %v, want nil", err)
	}
}

// TestSlugFromDerivesAnAddressFromAName pins the rule a designer never
// has to learn, including the two cases that make it worth having: an
// accent folds instead of vanishing, and a run of anything else is one
// hyphen.
func TestSlugFromDerivesAnAddressFromAName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Azeroth":                   "azeroth",
		"  The Ashfall  ":           "the-ashfall",
		"Ámbar":                     "ambar",
		"Crónicas de Valdivia":      "cronicas-de-valdivia",
		"Le Mans 1971":              "le-mans-1971",
		"Rock / Paper / Scissors":   "rock-paper-scissors",
		"¡¿Qué?!":                   "que",
		"...":                       "",
		"日本語":                       "",
		strings.Repeat("Long ", 40): strings.TrimRight(strings.Repeat("long-", 13)[:64], "-"),
	}
	for name, want := range cases {
		if got := projects.SlugFrom(name); got != want {
			t.Errorf("SlugFrom(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestAnEmptySlugIsDerivedFromTheName is the other half, through the
// service and the database: the create form has no address field any
// more, so this is the path every game made in a browser now takes.
func TestAnEmptySlugIsDerivedFromTheName(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	svc := projects.New(pool)
	ctx := context.Background()

	user := newUser(t, ids, "derived@example.test")

	first, err := svc.Create(ctx, "", "Crónicas de Valdivia", user.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if first.Slug != "cronicas-de-valdivia" {
		t.Errorf("slug = %q, want the name's own address", first.Slug)
	}

	// **A second game of the same name resolves rather than refusing.**
	// The person never typed an address, so an error about one would be
	// about a word they have not seen.
	second, err := svc.Create(ctx, "", "Crónicas de Valdivia", user.ID)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if second.Slug != "cronicas-de-valdivia-2" {
		t.Errorf("second slug = %q, want the next free number", second.Slug)
	}

	// A caller that *did* type an address still hears that it is taken:
	// they named it, so the refusal is about something they know.
	if _, err := svc.Create(ctx, "cronicas-de-valdivia", "Something else", user.ID); !errors.Is(err, projects.ErrSlugTaken) {
		t.Errorf("an explicit taken slug: err = %v, want ErrSlugTaken", err)
	}

	// And a name with no usable address in it still gets one.
	fallback, err := svc.Create(ctx, "", "日本語", user.ID)
	if err != nil {
		t.Fatalf("Create with an underivable name: %v", err)
	}
	if fallback.Slug == "" {
		t.Error("a name that yields no address left the game without one")
	}
}
