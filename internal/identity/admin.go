package identity

import (
	"context"
	"errors"
	"fmt"

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
// zero admins can never recover through this codebase's own surfaces:
// POST /api/invites, GET/DELETE /api/invites and SetAdmin itself
// (api_admin.go's requireAdminCaller) are all gated on Caller.IsAdmin,
// and BootstrapFirstAdmin (users.go) only ever runs once, against an
// empty users table, at first boot. Once the count reaches zero there is
// no REST call, no MCP tool and no scheduled job anywhere in this
// codebase that can ever set it non-zero again — the exact "unique and
// stranded if lost" defect this task exists to close. That is why this
// guard is unconditional rather than a default an operator can talk
// their way past: there is no supported recovery path this plan wants to
// lean on so soon after Task 18 built the invite surface specifically to
// avoid needing psql for the analogous problem.
var ErrLastAdmin = errors.New("instance must keep at least one admin")

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
