package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/db/dbq"
)

// Bounds on a user-supplied password. minPasswordRunes matches the
// project's stated minimum; maxPasswordBytes exists purely to keep argon2's
// input size bounded, since HashPassword's cost is proportional to it and
// nothing upstream of this service caps password length.
const (
	minPasswordRunes = 12
	maxPasswordBytes = 1024
)

// Errors returned by the identity service.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrEmailTaken         = errors.New("email already registered")
	ErrEmailNotAllowed    = errors.New("email domain not allowed on this instance")
	ErrPasswordInvalid    = errors.New("password does not meet requirements")
)

// Service is the identity domain: users, sessions, invites, tokens.
type Service struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
	cfg  config.Config
}

// New builds the identity service over a pool.
func New(pool *pgxpool.Pool, cfg config.Config) *Service {
	return &Service{pool: pool, q: dbq.New(pool), cfg: cfg}
}

// CreateUser registers an account. The email is normalised to lower case and
// checked against the instance's allowed domains.
func (s *Service) CreateUser(ctx context.Context, email, displayName, password string, isAdmin bool) (dbq.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !s.cfg.EmailAllowed(email) {
		return dbq.User{}, ErrEmailNotAllowed
	}
	// Bound the byte length first: RuneCountInString on an attacker-supplied
	// multi-megabyte string is itself a cheap way to force wasted CPU on
	// this codepath, so reject an oversized password before counting runes.
	if len(password) > maxPasswordBytes {
		return dbq.User{}, fmt.Errorf("%w: password must be at most %d bytes", ErrPasswordInvalid, maxPasswordBytes)
	}
	// Count runes, not bytes: a byte-length check on a UTF-8 string rejects
	// short multi-byte passphrases (e.g. a few CJK characters) while
	// accepting a 12-byte ASCII password of the same or lower entropy.
	if utf8.RuneCountInString(password) < minPasswordRunes {
		return dbq.User{}, fmt.Errorf("%w: password must be at least %d characters", ErrPasswordInvalid, minPasswordRunes)
	}

	hash, err := HashPassword(password, s.cfg.Argon2)
	if err != nil {
		return dbq.User{}, err
	}

	user, err := s.q.CreateUser(ctx, dbq.CreateUserParams{
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: hash,
		IsAdmin:      isAdmin,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		// 23505 is unique_violation. This table has exactly one unique
		// constraint today (users_email_key on lower(email)), so any 23505
		// here is the email collision; if a future migration adds another
		// unique index to this table, this mapping must be narrowed to
		// check pgErr.ConstraintName == "users_email_key" so a different
		// collision doesn't get misreported as ErrEmailTaken.
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return dbq.User{}, ErrEmailTaken
		}
		return dbq.User{}, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}

// Authenticate checks an email and password pair. Every failure returns
// ErrInvalidCredentials so the caller cannot tell an unknown address from a
// wrong password.
func (s *Service) Authenticate(ctx context.Context, email, password string) (dbq.User, error) {
	user, err := s.q.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Perform a dummy derivation so this path costs the same as a
			// real user's, closing the timing side-channel that would
			// otherwise let a caller enumerate registered addresses.
			VerifyDummy(password, s.cfg.Argon2)
			return dbq.User{}, ErrInvalidCredentials
		}
		return dbq.User{}, fmt.Errorf("lookup user: %w", err)
	}

	ok, err := VerifyPassword(password, user.PasswordHash)
	if err != nil || !ok {
		return dbq.User{}, ErrInvalidCredentials
	}
	return user, nil
}

// BootstrapFirstAdmin creates the configured admin account when the instance
// has no users yet. It is a no-op afterwards, and a no-op when unconfigured.
//
// The count-then-insert below is not atomic, so two replicas booting
// simultaneously against an empty database can both pass the count check
// and both attempt to insert. That race is resolved by the database, not by
// this function: the users_email_key unique index makes the loser's insert
// fail with a 23505, which CreateUser maps to ErrEmailTaken. That case is
// treated as "someone already bootstrapped the admin" rather than an error,
// so both replicas finish successfully and exactly one admin row exists.
func (s *Service) BootstrapFirstAdmin(ctx context.Context) error {
	if s.cfg.FirstAdminEmail == "" || s.cfg.FirstAdminPassword == "" {
		return nil
	}
	count, err := s.q.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if count > 0 {
		return nil
	}
	_, err = s.CreateUser(ctx, s.cfg.FirstAdminEmail, "Administrator", s.cfg.FirstAdminPassword, true)
	if errors.Is(err, ErrEmailNotAllowed) {
		return fmt.Errorf("FIRST_ADMIN_EMAIL is outside ALLOWED_EMAIL_DOMAINS")
	}
	if errors.Is(err, ErrEmailTaken) {
		// Another replica won the race; the admin now exists either way.
		return nil
	}
	return err
}
