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
type UserPage struct {
	Users      []User
	NextCursor string
}

// ListUsers answers the question the administration screen could not ask
// at all: **who is on this instance**.
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

// SetLocale records the language a person reads this product in. The
// empty string clears the choice, which is not the same as choosing
// English: an account with no choice follows the browser's own
// languages.
//
// **Which tags are admitted is not this package's to know.** The
// catalogues that ship decide, and they live in internal/web beside the
// files a browser fetches; this stores what it is given and the web
// layer refuses a tag it has no catalogue for.
func (s *Service) SetLocale(ctx context.Context, userID uuid.UUID, locale string) (User, error) {
	row, err := s.q.SetUserLocale(ctx, dbq.SetUserLocaleParams{ID: userID, Locale: strings.TrimSpace(locale)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("set locale: %w", err)
	}
	return userFrom(row), nil
}
