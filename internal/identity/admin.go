package identity

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

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
// codebase does provide is a restart, and it recovers both halves of
// being locked out, not just the flag:
//
//   - BootstrapFirstAdmin (users.go) re-promotes the account named by
//     FIRST_ADMIN_EMAIL when it already exists without the flag, not
//     only when the instance is empty. This half needs no configuration
//     beyond FIRST_ADMIN_EMAIL/FIRST_ADMIN_PASSWORD and happens on any
//     restart.
//   - The same boot also resets that account's password to
//     FIRST_ADMIN_PASSWORD — the recovery for the other way an admin
//     becomes unreachable, a forgotten or rotated-away password, which
//     restoring a flag does nothing for. Task 22 found the flag-only
//     version was not a working recovery at all for that case. This
//     half is gated behind its own one-shot opt-in,
//     FIRST_ADMIN_PASSWORD_RESET, so the standing configuration carries
//     the credential without the permission to apply it; see
//     repromoteConfiguredAdmin's own doc comment (users.go) for why.
//
// Both still require an operator with access to the process
// environment, which is already equivalent to database access, so this
// guard is not defeated by either; together they only turn a
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

// --- The instance's own accounts --------------------------------------

// UserPage is one page of the accounts on this instance, plus the cursor
// that asks for the next.
//
// It is `Users` and a cursor rather than a slice and a bool, because
// every listing in this product pages the same way and a screen that had
// to learn a second shape for one of them would be a screen with two
// pagers in it.
type UserPage struct {
	Users      []User
	NextCursor string
}

// ListUsers answers the question the administration screen could not ask
// at all: **who is on this instance**.
//
// The screen used to say so in its own prose — "Maestro cannot say who
// they are: the server has no endpoint that answers it" — and offered a
// field to type an address into instead. Administering an instance by
// typing a string from memory is how somebody grants the flag to an
// address that belongs to nobody, and never finds out.
//
// The cursor is opaque to the caller and is the last row's ordering pair
// (created_at, id). Both halves are needed: two accounts made in the
// same millisecond would otherwise page over each other, one repeated
// and one lost.
func (s *Service) ListUsers(ctx context.Context, after string, pageSize int32) (UserPage, error) {
	if pageSize <= 0 || pageSize > maxUserPageSize {
		pageSize = defaultUserPageSize
	}
	params := dbq.ListUsersParams{PageSize: pageSize + 1}
	if after != "" {
		createdAt, id, err := decodeUserCursor(after)
		if err != nil {
			return UserPage{}, err
		}
		params.AfterCreatedAt = pgtype.Timestamptz{Time: createdAt, Valid: true}
		params.AfterID = &id
	}
	rows, err := s.q.ListUsers(ctx, params)
	if err != nil {
		return UserPage{}, fmt.Errorf("list users: %w", err)
	}
	// One more than asked for is how a listing knows there is another
	// page without a second count query; the extra row is dropped rather
	// than shown.
	page := UserPage{}
	if len(rows) > int(pageSize) {
		last := rows[pageSize-1]
		page.NextCursor = encodeUserCursor(last.CreatedAt.Time, last.ID)
		rows = rows[:pageSize]
	}
	page.Users = make([]User, len(rows))
	for i, row := range rows {
		page.Users[i] = userFrom(row)
	}
	return page, nil
}

// UpdateIdentityRequest is what an administrator may change about
// somebody else's account: the address they sign in with, and the name
// they are shown by. Not their password, which only they can set, and
// not their standing, which is SetAdmin's.
type UpdateIdentityRequest struct {
	UserID      uuid.UUID
	Email       string
	DisplayName string
}

// UpdateIdentity changes an account's address and display name.
//
// **The address is how somebody signs in**, so it is validated exactly
// as registration validates one — trimmed, lower-cased, bounded in runes
// — and the unique index refuses a second account on it. The domain
// allow-list is deliberately *not* consulted: it governs who may create
// an account on this instance, and an administrator moving an existing
// colleague to a new address is not that. `prepareUserForInvite` makes
// the same distinction for the same reason.
//
// **Sessions are left alone, and that is a decision.** Changing an
// address does not change who the person is, and signing somebody out of
// three machines because an administrator fixed a typo in their name
// would be a surprise with no security behind it: a stolen session is
// revoked by the session routes, which is where that belongs.
func (s *Service) UpdateIdentity(ctx context.Context, req UpdateIdentityRequest) (User, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	displayName := strings.TrimSpace(req.DisplayName)
	if n := utf8.RuneCountInString(email); n < minEmailRunes || n > maxEmailRunes {
		return User{}, fmt.Errorf("%w: must be between %d and %d characters", ErrEmailInvalid, minEmailRunes, maxEmailRunes)
	}
	if n := utf8.RuneCountInString(displayName); n < minDisplayNameRunes || n > maxDisplayNameRunes {
		return User{}, fmt.Errorf("%w: must be between %d and %d characters", ErrDisplayNameInvalid, minDisplayNameRunes, maxDisplayNameRunes)
	}

	row, err := s.q.UpdateUserIdentity(ctx, dbq.UpdateUserIdentityParams{
		ID:          req.UserID,
		Email:       email,
		DisplayName: displayName,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		// The same named-constraint check insertUser makes, for the same
		// reason: a 23505 from some other index must not be reported as
		// "that address is taken".
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_email_key" {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("update identity: %w", err)
	}
	return userFrom(row), nil
}

// The page sizes this listing admits. The default is what the
// administration screen asks for and the cap is what stops a caller
// asking for the whole table in one answer.
const (
	defaultUserPageSize = 50
	maxUserPageSize     = 200
)

// A cursor is the ordering pair, encoded so a caller cannot read
// meaning into it and cannot construct one by hand: it is this listing's
// own bookmark and nothing else.
func encodeUserCursor(createdAt time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt.UTC().Format(time.RFC3339Nano) + " " + id.String()))
}

func decodeUserCursor(cursor string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrCursorInvalid
	}
	stamp, rest, found := strings.Cut(string(raw), " ")
	if !found {
		return time.Time{}, uuid.Nil, ErrCursorInvalid
	}
	createdAt, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrCursorInvalid
	}
	id, err := uuid.Parse(rest)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrCursorInvalid
	}
	return createdAt, id, nil
}

// ErrCursorInvalid is a cursor this listing did not issue. It is a
// refusal and not an empty first page: answering a made-up cursor with
// the start of the list would page a caller in a circle for ever.
var ErrCursorInvalid = errors.New("that cursor is not one this listing issued")
