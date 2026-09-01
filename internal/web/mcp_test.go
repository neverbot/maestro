package web_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/web"
)

func TestMCPWhoamiReportsTheTokenProject(t *testing.T) {
	_, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	caller, err := web.CallerForToken(ctx, ids, token)
	if err != nil {
		t.Fatalf("CallerForToken: %v", err)
	}

	out, err := web.MCPWhoami(ctx, web.MCPDeps{Identity: ids, Projects: projSvc}, caller)
	if err != nil {
		t.Fatalf("MCPWhoami: %v", err)
	}
	if out.UserID != user.ID {
		t.Fatal("whoami reported the wrong user")
	}
	if out.ProjectID == nil || *out.ProjectID != project.ID {
		t.Fatal("whoami did not report the token's project")
	}
	if out.ProjectSlug != "azeroth" {
		t.Fatalf("ProjectSlug = %q, want azeroth", out.ProjectSlug)
	}
}

func TestMCPGamesGetRefusesAnotherProject(t *testing.T) {
	_, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer2@studio.com", DisplayName: "Designer", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mine, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create mine: %v", err)
	}
	theirs, err := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID)
	if err != nil {
		t.Fatalf("Create theirs: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: mine.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	caller, err := web.CallerForToken(ctx, ids, token)
	if err != nil {
		t.Fatalf("CallerForToken: %v", err)
	}

	if _, err := web.MCPGamesGet(ctx, web.MCPDeps{Identity: ids, Projects: projSvc}, caller, theirs.ID); err == nil {
		t.Fatal("a token must not read another game, even one its user owns")
	}
}

func TestMCPGamesGetRefusesAnotherProjectForAdmins(t *testing.T) {
	// This test needs a genuine admin. CreateUser can never mint one (Task
	// 5, Correction 11), so it goes through BootstrapFirstAdmin exactly as
	// production does, which means it needs its own config — testConfig()
	// used by newTestServer deliberately leaves FirstAdminEmail/Password
	// unset so unrelated tests don't get a surprise admin row.
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "boss@studio.com"
	cfg.FirstAdminPassword = "password12345"
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	ctx := context.Background()

	if err := ids.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("BootstrapFirstAdmin: %v", err)
	}
	admin, err := ids.Authenticate(ctx, cfg.FirstAdminEmail, cfg.FirstAdminPassword)
	if err != nil {
		t.Fatalf("Authenticate admin: %v", err)
	}
	mine, err := projSvc.Create(ctx, "azeroth", "Azeroth", admin.ID)
	if err != nil {
		t.Fatalf("Create mine: %v", err)
	}
	theirs, err := projSvc.Create(ctx, "le-mans", "Le Mans", admin.ID)
	if err != nil {
		t.Fatalf("Create theirs: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: mine.ID, UserID: admin.ID, Label: "admin agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	caller, err := web.CallerForToken(ctx, ids, token)
	if err != nil {
		t.Fatalf("CallerForToken: %v", err)
	}
	if _, err := web.MCPGamesGet(ctx, web.MCPDeps{Identity: ids, Projects: projSvc}, caller, theirs.ID); err == nil {
		t.Fatal("admin tokens are not exempt from project scope")
	}
}

func TestMCPGamesListReturnsExactlyTheTokensOneGame(t *testing.T) {
	_, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer3@studio.com", DisplayName: "Designer", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mine, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create mine: %v", err)
	}
	if _, err := projSvc.Create(ctx, "le-mans", "Le Mans", user.ID); err != nil {
		t.Fatalf("Create le-mans: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: mine.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	caller, err := web.CallerForToken(ctx, ids, token)
	if err != nil {
		t.Fatalf("CallerForToken: %v", err)
	}

	out, err := web.MCPGamesList(ctx, web.MCPDeps{Identity: ids, Projects: projSvc}, caller)
	if err != nil {
		t.Fatalf("MCPGamesList: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].ID != mine.ID {
		t.Fatalf("items = %+v, want exactly [%s]", out.Items, mine.ID)
	}
	if out.Truncated {
		t.Fatal("Truncated = true for a single-item result")
	}
	if out.NextCursor != nil {
		t.Fatalf("NextCursor = %v, want nil for a single-item result", out.NextCursor)
	}
}

func TestMCPGamesGetReturnsTheCallersOwnGame(t *testing.T) {
	_, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer4@studio.com", DisplayName: "Designer", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mine, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create mine: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: mine.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	caller, err := web.CallerForToken(ctx, ids, token)
	if err != nil {
		t.Fatalf("CallerForToken: %v", err)
	}
	out, err := web.MCPGamesGet(ctx, web.MCPDeps{Identity: ids, Projects: projSvc}, caller, mine.ID)
	if err != nil {
		t.Fatalf("MCPGamesGet: %v", err)
	}
	if out.Slug != "azeroth" {
		t.Fatalf("Slug = %q, want azeroth", out.Slug)
	}
}

// TestMCPGamesGetReportsNotFoundForAMissingProject exercises the actual
// not-found branch: a project id that requireScope lets through — it is
// the caller's own scope, by construction — but that names no real row.
// An earlier version of this test looked up a project that existed and
// asserted its slug, useful coverage but not of the not-found branch its
// own name claimed to test; that coverage is kept above as
// TestMCPGamesGetReturnsTheCallersOwnGame.
//
// Reaching this branch through MCPGamesGet needs a caller whose own
// binding *is* the missing id: a real token cannot be minted for one
// (api_tokens' foreign key on project_id refuses it, and Projects has no
// delete method yet — Task 17), so this constructs the Caller directly
// rather than through CallerForToken. Caller's own doc comment
// (auth.go) asks callers to go through newTokenCaller/newSessionCaller
// instead of a struct literal; both are unexported, and this is the one
// place in this package's tests that needs a caller whose scope names a
// project id no database row will ever back, which no constructor this
// package exports can produce.
func TestMCPGamesGetReportsNotFoundForAMissingProject(t *testing.T) {
	_, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer5@studio.com", DisplayName: "Designer", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	missing := uuid.New()
	tokenID := uuid.New()
	caller := web.Caller{UserID: user.ID, TokenID: &tokenID, ProjectID: &missing}

	if _, err := web.MCPGamesGet(ctx, web.MCPDeps{Identity: ids, Projects: projSvc}, caller, missing); !errors.Is(err, projects.ErrProjectNotFound) {
		t.Fatalf("MCPGamesGet(missing) = %v, want ErrProjectNotFound", err)
	}
}

func TestCallerForTokenRejectsARevokedToken(t *testing.T) {
	_, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer6@studio.com", DisplayName: "Designer", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}
	token, summary, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: project.ID, TokenID: summary.ID}); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}

	if _, err := web.CallerForToken(ctx, ids, token); err == nil {
		t.Fatal("a revoked token must not resolve to a caller")
	}
}
