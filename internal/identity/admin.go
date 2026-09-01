package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// ErrUserNotFound is returned by SetAdmin when targetUserID names no
// existing user.
var ErrUserNotFound = errors.New("user not found")

// ErrLastAdmin is returned by SetAdmin when demoting the target would
// leave the instance with no admin at all — the same guard
// projects.ErrLastOwner applies at project scope, applied here to the
// one authority in this plan that is instance-wide rather than
// per-project (see Caller.ScopedProject's own doc comment,
// internal/web/auth.go, for the project-scoped equivalent this
// deliberately does not touch).
//
// The stakes are higher here than for a project's last owner. A project
// with zero owners can still be reasoned about — it is merely stuck
// until an operator intervenes some other way — but an instance with
// zero admins can never recover through any REST call, MCP tool or
// scheduled job while the process keeps running: POST /api/invites,
// GET/DELETE /api/invites and this surface itself are all gated on
// Caller.IsAdmin, and nothing else sets it. The one recovery path this
// codebase does provide is a restart: BootstrapFirstAdmin (users.go) now
// re-promotes the account named by FIRST_ADMIN_EMAIL when it already
// exists without the flag, not only when the instance is empty — see
// that function's own doc comment. That still requires an operator with
// access to the process environment, which is already equivalent to
// database access, so this guard is not defeated by it; it only turns a
// break-glass `psql` session into a documented restart, which is why the
// guard below stays unconditional rather than a default an operator can
// talk their way past inside a running process.
var ErrLastAdmin = errors.New("instance must keep at least one admin")

// SetAdminByEmail resolves email to a user id and applies SetAdmin. This
// is the production entry point handleSetAdmin (internal/web/api_admin.go)
// calls: an admin identifies who to promote or demote by email, the same
// selector POST /api/invites already uses to identify who an
// account-only invite is for (createInstanceInviteRequest.Email). This
// codebase's admin surface already established that vocabulary, so this
// method reuses it rather than requiring a user-listing endpoint whose
// only consumer would be this one call site — see this task's own plan
// section (Decision 2) for the tradeoff against a listing endpoint.
//
// An unknown email reports ErrUserNotFound, the same sentinel SetAdmin
// itself returns for an unknown id — there is only one "no such user"
// outcome from this surface, however the caller identified the target.
func (s *Service) SetAdminByEmail(ctx context.Context, email string, isAdmin bool) error {
	email = strings.ToLower(strings.TrimSpace(email))
	dbUser, err := s.q.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		return fmt.Errorf("lookup user by email: %w", err)
	}
	return s.SetAdmin(ctx, dbUser.ID, isAdmin)
}

// SetAdmin promotes or demotes targetUserID's instance-admin flag.
// Authorization is the HTTP layer's job (requireAdminCaller,
// internal/web/api_admin.go) — this method trusts its caller completely,
// the same way projects.SetRole trusts its own HTTP layer to have
// already decided the call is allowed (that method's own doc comment
// explains why authorization does not belong in a domain service: it
// would need to know who is asking and on whose behalf, which this
// package has no business knowing).
//
// The last-admin guard (ErrLastAdmin) only ever fires when the target is
// already an admin being demoted (isAdmin false) — promoting someone, or
// setting an already-non-admin's flag to false again, can never reduce
// the admin count, mirroring SetRole's identical short-circuit for
// ErrLastOwner. It applies unconditionally to every demotion, including
// a caller demoting themselves: an admin who is not the last one may
// step down freely (the same "leaving is always your own choice" logic
// handleRemoveMember applies to project membership), but the instance
// itself may never be left with zero.
//
// See CountAdminsForUpdate's own doc comment (identity.sql) for why the
// FOR UPDATE lock taken here is the sole thing enforcing this invariant
// under concurrent demotions, not defence in depth for a second,
// independent mechanism the way CountOwnersForUpdate's identical shape
// is for a project's last owner.
func (s *Service) SetAdmin(ctx context.Context, targetUserID uuid.UUID, isAdmin bool) error {
	return s.withTx(ctx, func(q *dbq.Queries) error {
		target, err := q.GetUserByID(ctx, targetUserID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUserNotFound
			}
			return fmt.Errorf("lookup user: %w", err)
		}

		if target.IsAdmin && !isAdmin {
			n, cerr := q.CountAdminsForUpdate(ctx)
			if cerr != nil {
				return fmt.Errorf("count admins: %w", cerr)
			}
			if n <= 1 {
				return ErrLastAdmin
			}
		}

		if err := q.UpdateUserIsAdmin(ctx, dbq.UpdateUserIsAdminParams{
			ID:      targetUserID,
			IsAdmin: isAdmin,
		}); err != nil {
			return fmt.Errorf("update is_admin: %w", err)
		}
		return nil
	})
}
