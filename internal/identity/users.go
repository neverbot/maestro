package identity

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/db/dbq"
)

// Bounds on user-supplied fields. minPasswordRunes matches the project's
// stated minimum; the max* bounds keep attacker-controlled strings out of
// unbounded text columns and, for the password, out of an unbounded argon2
// input.
const (
	minPasswordRunes    = 12
	maxPasswordBytes    = 1024
	minEmailRunes       = 3   // the shortest an address can be ("a@b")
	maxEmailRunes       = 254 // RFC 5321's limit on a path
	minDisplayNameRunes = 1
	maxDisplayNameRunes = 200
)

// Errors returned by the identity service.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrEmailTaken         = errors.New("email already registered")
	ErrEmailNotAllowed    = errors.New("email domain not allowed on this instance")
	ErrPasswordInvalid    = errors.New("password does not meet requirements")
	ErrEmailInvalid       = errors.New("email does not meet requirements")
	ErrDisplayNameInvalid = errors.New("display name does not meet requirements")
	// ErrPasswordUnchanged is returned by ChangeOwnPassword when the new
	// password is byte-identical to the current one. A no-op rotation
	// still pays ChangePassword's full cost — a fresh hash, and every
	// other session on the account revoked — for a password that reads
	// exactly the same afterwards; refusing it before either happens
	// tells the caller their input did nothing, rather than silently
	// logging out every other device for a change that never took
	// effect.
	ErrPasswordUnchanged = errors.New("new password must differ from the current password")
)

// User is the identity domain's public view of an account. It deliberately
// omits dbq.User's PasswordHash: dbq.User is generated with no JSON tags and
// an exported hash field, so returning it directly from a public method
// leaves every future caller one careless writeJSON away from serving the
// argon2 hash back to the browser. Nothing does that today; the point of
// this type is that nothing can, by construction.
type User struct {
	ID          uuid.UUID
	Email       string
	DisplayName string
	IsAdmin     bool
	CreatedAt   time.Time
}

func userFrom(u dbq.User) User {
	return User{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		IsAdmin:     u.IsAdmin,
		CreatedAt:   u.CreatedAt.Time,
	}
}

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

// withTx runs fn inside a transaction, committing on success and rolling
// back on any error, including a panic recovered by rollback's own defer at
// the pgx layer. Every multi-statement write in this package goes through
// this instead of hand-rolling Begin/Rollback/Commit at each call site, so
// that correctness (in particular: the rollback on every non-nil return,
// including one from fn itself) lives in exactly one place.
func (s *Service) withTx(ctx context.Context, fn func(*dbq.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(dbq.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// CreateUserRequest is the input to CreateUser. It exists so that three
// adjacent strings cannot compile-check as correct while being silently
// transposed at a call site — storing a password in display_name is a
// permanent, silent corruption that a positional signature does not guard
// against.
type CreateUserRequest struct {
	Email       string
	DisplayName string
	Password    string
}

// CreateUser registers a non-admin account. The email is normalised to
// lower case and checked against the instance's allowed domains. Every
// account created through the public API is non-admin by construction: the
// only caller allowed to mint an admin is BootstrapFirstAdmin, via the
// unexported createUser below, so a future registration handler is never
// one boolean away from privilege escalation.
func (s *Service) CreateUser(ctx context.Context, req CreateUserRequest) (User, error) {
	return s.createUser(ctx, req, false)
}

// createUser is the non-transactional entry point: everything that isn't
// inside someone else's withTx call goes through s.q via this thin wrapper.
func (s *Service) createUser(ctx context.Context, req CreateUserRequest, isAdmin bool) (User, error) {
	p, err := s.prepareUser(req)
	if err != nil {
		return User{}, err
	}
	return s.insertUser(ctx, s.q, p, isAdmin)
}

// preparedUser is a CreateUserRequest that has passed every validation
// check and had its password hashed. Producing one is CPU-bound (argon2,
// specifically) but touches no connection and holds no lock; inserting one
// (insertUser, below) is the reverse. Keeping them as two steps lets a
// caller that is about to insert inside a transaction — invites.go's
// RedeemInvite — call prepareUser before opening it: hashing inside an open
// transaction would pin a pool connection for the full argon2 derivation
// and, worse for RedeemInvite specifically, lengthen how long the
// invite row's lock (see MarkInviteRedeemed's doc comment) is held, for a
// step that has no interaction with the transaction's atomicity at all.
type preparedUser struct {
	email        string
	displayName  string
	passwordHash string
}

// prepareUser validates req and hashes its password, without touching the
// database. See preparedUser's doc comment for why this is split out from
// insertUser. It applies the instance's ALLOWED_EMAIL_DOMAINS allowlist;
// prepareUserForInvite below is the invite-redemption variant that does
// not.
func (s *Service) prepareUser(req CreateUserRequest) (preparedUser, error) {
	return s.prepareUserChecked(req, true)
}

// prepareUserForInvite is prepareUser's counterpart for a *bound* invite
// redemption: it skips the ALLOWED_EMAIL_DOMAINS check prepareUser
// otherwise applies. RedeemInvite (invites.go) selects between the two by
// invite.Email — this one only when the invite names a specific address;
// prepareUser (still applying the allowlist) for an unbound one. That
// split, not "every invite redemption", is deliberate: it turned out to
// matter where the admin's authorization actually sits.
//
// A *bound* invite (one CreateInvite attached a specific email to) means
// the admin typed that exact address when they created it — they named
// the person, and CreateInvite already ran EmailAllowed once, at creation
// time, against it (see CreateInvite's own check in invites.go, which is
// unchanged and still enforced there). The domain policy has already been
// applied to the admin's intent; skipping it again at redemption is what
// makes "an invite wins over the instance's registration mode" true for
// the case it exists to cover — an admin deliberately inviting a specific
// outside contractor.
//
// An *unbound* invite only ever said "whoever holds this link gets in".
// It names no domain, so ALLOWED_EMAIL_DOMAINS is still the only
// statement anyone has made about who may hold an account on this
// instance, and it should stand — an earlier version of this method
// skipped the check unconditionally, which let any unbound invite's
// holder register with any address at all, silently overriding a
// configured allowlist the admin never actually opted out of for that
// link. An unbound invite is also the one most likely to end up pasted
// into a shared channel or forwarded on, which is exactly when that
// second gate is worth having. A future admin who genuinely needs an
// unbound link for an off-domain contractor needs a narrower, explicit
// opt-out on InviteRequest — not a change to this default — and that is
// deliberately not built here (see the plan's Task 11 corrections).
func (s *Service) prepareUserForInvite(req CreateUserRequest) (preparedUser, error) {
	return s.prepareUserChecked(req, false)
}

// prepareUserChecked is the shared implementation behind prepareUser and
// prepareUserForInvite; checkDomain selects whether EmailAllowed is
// consulted. See the two exported-within-package wrappers' doc comments
// for which one to call and why they differ.
func (s *Service) prepareUserChecked(req CreateUserRequest, checkDomain bool) (preparedUser, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	displayName := strings.TrimSpace(req.DisplayName)
	password := req.Password

	// Rune counts, not byte lengths: a byte-length check on a UTF-8 string
	// rejects a short-but-strong multi-byte passphrase or name while
	// accepting a longer, weaker ASCII one of the same byte length.
	if n := utf8.RuneCountInString(email); n < minEmailRunes || n > maxEmailRunes {
		return preparedUser{}, fmt.Errorf("%w: must be between %d and %d characters", ErrEmailInvalid, minEmailRunes, maxEmailRunes)
	}
	if n := utf8.RuneCountInString(displayName); n < minDisplayNameRunes || n > maxDisplayNameRunes {
		return preparedUser{}, fmt.Errorf("%w: must be between %d and %d characters", ErrDisplayNameInvalid, minDisplayNameRunes, maxDisplayNameRunes)
	}
	if checkDomain && !s.cfg.EmailAllowed(email) {
		return preparedUser{}, ErrEmailNotAllowed
	}
	// Bound the byte length before counting runes: password is fed to
	// argon2 next, and argon2's cost is proportional to its input length,
	// so this bound must be checked before hashing regardless of how cheap
	// the rune count above is.
	if len(password) > maxPasswordBytes {
		return preparedUser{}, fmt.Errorf("%w: must be at most %d bytes", ErrPasswordInvalid, maxPasswordBytes)
	}
	if utf8.RuneCountInString(password) < minPasswordRunes {
		return preparedUser{}, fmt.Errorf("%w: must be at least %d characters", ErrPasswordInvalid, minPasswordRunes)
	}

	hash, err := HashPassword(password, s.cfg.Argon2)
	if err != nil {
		return preparedUser{}, err
	}
	return preparedUser{email: email, displayName: displayName, passwordHash: hash}, nil
}

// insertUser writes an already-validated, already-hashed user through q.
// Callers pass s.q outside a transaction (createUser) or a transaction-
// scoped *dbq.Queries from withTx (invites.go's RedeemInvite), so the
// insert can participate in a larger atomic write without itself knowing
// or caring which.
func (s *Service) insertUser(ctx context.Context, q *dbq.Queries, p preparedUser, isAdmin bool) (User, error) {
	dbUser, err := q.CreateUser(ctx, dbq.CreateUserParams{
		Email:        p.email,
		DisplayName:  p.displayName,
		PasswordHash: p.passwordHash,
		IsAdmin:      isAdmin,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		// 23505 is unique_violation. users_email_key is checked by name,
		// not just by code, because this table will grow more unique
		// indexes (api_tokens.token_hash, invites.token_hash already exist
		// elsewhere), and a 23505 from one of those must not be
		// misreported as "email taken".
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_email_key" {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return userFrom(dbUser), nil
}

// sentinelHash returns a syntactically valid encoded argon2id hash whose
// cost parameters match s.cfg.Argon2, but whose salt and key bytes are
// fixed and meaningless. No password is expected to verify against it; its
// only purpose is to give Authenticate's unknown-user path a real hash to
// call verify against — see verify's doc comment for why.
func (s *Service) sentinelHash() string {
	p := s.cfg.Argon2
	salt := bytes.Repeat([]byte{0xA5}, int(p.SaltLen))
	key := bytes.Repeat([]byte{0x5A}, int(p.KeyLen))
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
}

// verify compares password against hash. It exists as the single call site
// through which both branches of Authenticate must pass: the known-user
// branch passes the account's real stored hash, and the unknown-user branch
// passes sentinelHash() instead of skipping the call. Because there is only
// one call to VerifyPassword in this file, a future refactor of Authenticate
// cannot accidentally delete "the dummy branch" and reopen the timing
// side-channel this closes — there is no separate dummy branch to delete.
func (s *Service) verify(password, hash string) (bool, error) {
	return VerifyPassword(password, hash)
}

// Authenticate checks an email and password pair. Every failure returns
// ErrInvalidCredentials so the caller cannot tell an unknown address from a
// wrong password. On success, it re-hashes the password when the stored
// hash's cost parameters are stale, so raising Config.Argon2's cost takes
// effect for existing accounts as they log in.
func (s *Service) Authenticate(ctx context.Context, email, password string) (User, error) {
	dbUser, err := s.q.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return User{}, fmt.Errorf("lookup user: %w", err)
	}

	hash := s.sentinelHash()
	if found {
		hash = dbUser.PasswordHash
	}

	ok, verr := s.verify(password, hash)
	if verr != nil {
		if found {
			// A stored hash that fails to parse must still present to the
			// caller as an ordinary wrong password — telling them "your
			// account's hash is corrupt" leaks exactly as much as telling
			// them the account exists. But silently swallowing it leaves an
			// operator with no way to distinguish a truncated column, a
			// botched migration or a partial write from a user who keeps
			// mistyping their password, so log it at ERROR with the user's
			// ID and never the hash itself.
			slog.ErrorContext(ctx, "stored password hash failed to parse",
				"user_id", dbUser.ID, "error", verr)
		}
		return User{}, ErrInvalidCredentials
	}
	if !found || !ok {
		return User{}, ErrInvalidCredentials
	}

	if needs, rerr := NeedsRehash(hash, s.cfg.Argon2); rerr == nil && needs {
		newHash, herr := HashPassword(password, s.cfg.Argon2)
		if herr != nil {
			slog.ErrorContext(ctx, "rehash after login failed", "user_id", dbUser.ID, "error", herr)
		} else if uerr := s.q.UpdateUserPasswordHash(ctx, dbq.UpdateUserPasswordHashParams{
			ID:           dbUser.ID,
			PasswordHash: newHash,
		}); uerr != nil {
			// A failed rehash must never fail the login: the password was
			// already verified against the old hash, and the old hash is
			// still valid at its own (stale) cost parameters.
			slog.ErrorContext(ctx, "rehash after login failed", "user_id", dbUser.ID, "error", uerr)
		}
	}

	return userFrom(dbUser), nil
}

// bootstrapRaceHook, when non-nil, is invoked immediately after
// BootstrapFirstAdmin observes its own insert lose the create-first-admin
// race to another concurrent caller (a genuine ErrEmailTaken from
// createUser, not a pre-existing user found by the earlier count check). It
// exists purely so the package's own test suite can assert that a
// concurrency test actually forced that race, rather than silently
// degenerating into every goroutine taking the count>0 shortcut. It must
// stay nil, and unexported, in production.
var bootstrapRaceHook func()

// BootstrapFirstAdmin creates the configured admin account when the instance
// has no users yet, and is a no-op when unconfigured (FIRST_ADMIN_EMAIL or
// FIRST_ADMIN_PASSWORD unset).
//
// When the instance is not empty, this used to be an unconditional no-op —
// Task 21's own review found that left no recovery at all from the
// instance's own last-admin guard (SetAdmin's ErrLastAdmin, admin.go):
// nothing anywhere sets IsAdmin once every admin is gone, or once the one
// admin account is simply unreachable (a forgotten password, with no
// reset flow anywhere in this product by design). It now also
// re-promotes: if a user with FIRST_ADMIN_EMAIL already exists and is not
// currently an admin, this sets the flag, without touching their password
// or anything else about the account (see repromoteConfiguredAdmin
// below). This is deliberately narrower than "create the account if it's
// missing" — a FIRST_ADMIN_EMAIL that matches nobody is left alone, not
// used to conjure a brand-new admin account on every boot of an instance
// that already has users, which would be a surprising escalation vector
// for a misconfigured environment variable. The recovery this grants is
// not a new capability: an operator who can set process environment
// variables already has equivalent access to the database directly, so
// this only turns a break-glass `psql UPDATE` into a documented restart.
//
// The count-then-insert below is not atomic, so two replicas booting
// simultaneously against an empty database can both pass the count check
// and both attempt to insert. That race is resolved by the database, not by
// this function: the users_email_key unique index makes the loser's insert
// fail with a 23505, which createUser maps to ErrEmailTaken. That case is
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
		return s.repromoteConfiguredAdmin(ctx)
	}

	_, err = s.createUser(ctx, CreateUserRequest{
		Email:       s.cfg.FirstAdminEmail,
		DisplayName: "Administrator",
		Password:    s.cfg.FirstAdminPassword,
	}, true)

	switch {
	case errors.Is(err, ErrEmailTaken):
		// Another replica won the race; the admin now exists either way.
		if bootstrapRaceHook != nil {
			bootstrapRaceHook()
		}
		return nil
	case errors.Is(err, ErrEmailNotAllowed):
		return fmt.Errorf("FIRST_ADMIN_EMAIL is outside ALLOWED_EMAIL_DOMAINS: %w", err)
	case errors.Is(err, ErrEmailInvalid):
		return fmt.Errorf("FIRST_ADMIN_EMAIL is invalid: %w", err)
	case errors.Is(err, ErrPasswordInvalid):
		return fmt.Errorf("FIRST_ADMIN_PASSWORD is invalid: %w", err)
	case err != nil:
		return fmt.Errorf("create first admin: %w", err)
	}
	return nil
}

// repromoteConfiguredAdmin is BootstrapFirstAdmin's recovery path for a
// non-empty instance: if s.cfg.FirstAdminEmail names an existing user who
// is not currently an admin, it sets IsAdmin true and nothing else. A
// FIRST_ADMIN_EMAIL matching no existing account is left alone — see
// BootstrapFirstAdmin's own doc comment for why this never creates an
// account here, only promotes one that already exists. It runs on every
// boot once FIRST_ADMIN_EMAIL/FIRST_ADMIN_PASSWORD are set, regardless of
// whether anything actually needs fixing; the common case (the
// configured admin already holds the flag) costs one extra indexed
// lookup by email and nothing else.
func (s *Service) repromoteConfiguredAdmin(ctx context.Context) error {
	dbUser, err := s.q.GetUserByEmail(ctx, s.cfg.FirstAdminEmail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("lookup configured admin: %w", err)
	}
	if dbUser.IsAdmin {
		return nil
	}
	if err := s.SetAdmin(ctx, dbUser.ID, true); err != nil {
		return fmt.Errorf("re-promote configured admin: %w", err)
	}
	return nil
}

// UserByID loads one user. It was originally added for Task 10's
// authentication middleware, to fill the gap between a resolved bearer
// token (a user id and project binding, no IsAdmin or display name) and
// a full User — that call site is gone now (a Round 3 review had
// GetLiveAPIToken join users directly instead, so the middleware never
// needs this method: see identity.APITokenSummary.UserIsAdmin and
// web.resolveBearerCaller). It stays exported for Task 13's `whoami` MCP
// tool, which needs the same user-id-to-User lookup for a caller that
// arrived with no display name of its own, without the identity package
// ever handing out dbq.User directly (see User's own doc comment).
func (s *Service) UserByID(ctx context.Context, id uuid.UUID) (User, error) {
	dbUser, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		return User{}, fmt.Errorf("lookup user: %w", err)
	}
	return userFrom(dbUser), nil
}
