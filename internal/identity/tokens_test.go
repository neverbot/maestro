package identity_test

import (
	"context"
	"errors"
	"strings"
	"testing"

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

	clear, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: project.ID,
		UserID:    user.ID,
		Label:     "seed agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if !strings.HasPrefix(clear, identity.TokenPrefix) {
		t.Fatalf("token = %q, want the %s prefix", clear, identity.TokenPrefix)
	}
	if tok.Label != "seed agent" {
		t.Fatalf("label = %q", tok.Label)
	}
	if tok.ProjectID != project.ID || tok.UserID != user.ID {
		t.Fatal("the returned token is bound to the wrong project or user")
	}

	resolved, err := ids.ResolveAPIToken(ctx, clear)
	if err != nil {
		t.Fatalf("ResolveAPIToken: %v", err)
	}
	if resolved.ProjectID != project.ID || resolved.UserID != user.ID {
		t.Fatal("the token resolved to the wrong project or user")
	}

	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: project.ID, TokenID: tok.ID}); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, err := ids.ResolveAPIToken(ctx, clear); !errors.Is(err, identity.ErrTokenInvalid) {
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
		t.Fatal("two tokens minted the same clear value")
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

	clear, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: mine.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// Revoking the token through the wrong project must not touch it.
	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: theirs.ID, TokenID: tok.ID}); err != nil {
		t.Fatalf("RevokeAPIToken (wrong project): %v", err)
	}
	if _, err := ids.ResolveAPIToken(ctx, clear); err != nil {
		t.Fatalf("token was revoked through a project that does not own it: %v", err)
	}

	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: mine.ID, TokenID: tok.ID}); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, err := ids.ResolveAPIToken(ctx, clear); !errors.Is(err, identity.ErrTokenInvalid) {
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

func TestRevokedTokenIsIndistinguishableFromUnknown(t *testing.T) {
	pool := testutil.NewPool(t)
	ids := identity.New(pool, testConfig())
	projSvc := projects.New(pool)
	ctx := context.Background()

	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345"})
	project, _ := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	clear, tok, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{ProjectID: project.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := ids.RevokeAPIToken(ctx, identity.RevokeAPITokenRequest{ProjectID: project.ID, TokenID: tok.ID}); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}

	_, revokedErr := ids.ResolveAPIToken(ctx, clear)
	_, unknownErr := ids.ResolveAPIToken(ctx, "mst_totallyunknown")
	if !errors.Is(revokedErr, identity.ErrTokenInvalid) || !errors.Is(unknownErr, identity.ErrTokenInvalid) {
		t.Fatalf("revokedErr = %v, unknownErr = %v, want both ErrTokenInvalid", revokedErr, unknownErr)
	}
}
