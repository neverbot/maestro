package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/roles"
	"github.com/neverbot/maestro/internal/testutil"
)

// TestDeleteGameMapsProjectNotFoundTo404 pins handleDeleteGame's
// projects.ErrProjectNotFound branch — the one Task 22 added after
// finding the race handleChangeRole's own comment had predicted was
// falling into a generic 500.
//
// It is an internal test on purpose. That branch cannot be reached
// through the router: requireProject re-resolves membership on every
// request, and the membership row cascades away with the project, so a
// second DELETE through ServeHTTP is refused with 403 long before
// handleDeleteGame runs — which is exactly what
// TestDeletingGameTwiceIsIdempotent (api_projects_test.go) pins, and
// exactly why a Task 22 review could replace this branch's condition
// with `if false` and watch the whole internal/web suite stay green.
//
// The real interleaving the branch exists for is narrower than "delete
// twice": requireProject's membership lookup and handleDeleteGame's own
// ByID call are two separate round trips, and a concurrent delete that
// commits *between* them leaves this handler holding a perfectly valid
// ProjectScope for a project row that no longer exists. That is what
// this test reconstructs — the handler is called directly with the
// scope requireProject resolved a moment earlier, against a project
// deleted in the gap.
func TestDeleteGameMapsProjectNotFoundTo404(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := config.Config{
		SessionTTL: 24 * time.Hour,
		InviteTTL:  24 * time.Hour,
		Argon2:     config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
	}
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	srv := NewServer(Options{Version: "test", Config: cfg, Identity: ids, Projects: projSvc})
	ctx := context.Background()

	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// requireProject would have resolved exactly this scope, from a
	// membership row that still existed at that instant.
	scope := ProjectScope{ProjectID: project.ID, Role: string(roles.Owner)}

	// The concurrent delete lands in the gap between that lookup and
	// this handler's own ByID.
	if err := projSvc.Delete(ctx, project.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/games/"+project.ID.String()+"?confirm=azeroth", nil)
	rec := httptest.NewRecorder()
	srv.handleDeleteGame(rec, req, newSessionCaller(owner.ID, false), scope)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a game deleted between admission and this handler's own lookup; body = %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["error"] != errCodeNotFound {
		t.Fatalf("error = %v, want %q", payload["error"], errCodeNotFound)
	}
}
