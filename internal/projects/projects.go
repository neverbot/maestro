// Package projects owns games and who may see them.
package projects

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/roles"
)

// Bounds on user-supplied fields, mirroring the rune-count-not-byte-length
// approach identity.prepareUser uses for the same reason: a byte check on a
// UTF-8 name rejects a short-but-legitimate multi-byte name while accepting
// a longer ASCII one of equal byte length. slugPattern is stricter than a
// rune count: a slug is not free text, it is a URL path segment
// (`/g/<slug>/...`), so it is bounded to what a path segment can safely
// carry without escaping — lower-case ASCII letters, digits and single
// internal hyphens, no leading, trailing or doubled hyphen.
const (
	minSlugRunes = 1
	maxSlugRunes = 64
	minNameRunes = 1
	maxNameRunes = 200
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// reservedSlugs are the first path segments every top-level route in this
// plan already claims (`/api/...`, `/g/...`, `/login`, `/healthz`,
// `/version`, `/static/...`, `/mcp`), plus "new": nothing routes there
// today, but the day a create-game page exists at a human-facing URL
// (rather than only the JSON POST /api/games this task's sibling task
// exposes), a game slugged "new" would silently shadow it. A slug
// colliding with any of these does not break anything today — /g/ has no
// sibling routes yet — but it becomes an ambiguity some future handler
// inherits for free the moment it does, and it is cheaper to refuse it
// now than to migrate an existing game's slug out from under a live link
// later.
var reservedSlugs = map[string]bool{
	"new": true, "api": true, "static": true, "login": true,
	"healthz": true, "g": true, "mcp": true, "version": true,
}

// bidiOverrides are the Unicode bidirectional control characters capable
// of making rendered text read in an order different from its byte order
// (the family behind "Trojan Source"-style spoofing). unicode.IsControl
// does not flag these — they are format characters (category Cf), not
// controls — so validateName checks for them separately.
var bidiOverrides = map[rune]bool{
	'‪': true, '‫': true, '‬': true, '‭': true, '‮': true, // LRE RLE PDF LRO RLO
	'⁦': true, '⁧': true, '⁨': true, '⁩': true, // LRI RLI FSI PDI
}

// Errors returned by the projects service.
var (
	// ErrNotAMember covers both "this user has no membership in this
	// project" and "this project does not exist". The two are
	// deliberately indistinguishable: RoleOf is the authorization check
	// every other layer calls before trusting a caller with a project's
	// data, and telling a non-member which project IDs are real would be
	// exactly the kind of cross-game leak the isolation invariant exists
	// to prevent. A caller that needs to tell "no such game" apart from
	// "not your game" for a plain lookup — a 404 page, say — uses ByID or
	// BySlugForUser instead, whose whole job is looking a project up
	// (BySlugForUser included, despite its name: see its own doc comment
	// for why it still collapses "no such slug" and "not yours" into one
	// answer, for a different reason than RoleOf does).
	ErrNotAMember = errors.New("user is not a member of this project")

	// ErrProjectNotFound is returned by ByID and BySlugForUser for a
	// project that does not exist (or, for BySlugForUser, is not the
	// caller's to see — see that method's doc comment).
	ErrProjectNotFound = errors.New("project not found")

	// ErrUserNotFound is returned by SetRole when the target user does
	// not exist — a foreign-key violation on memberships_user_id_fkey,
	// mapped the same way ErrSlugTaken maps projects_slug_key: by
	// constraint name, so Task 12's handler can report a 404 instead of
	// leaking a raw SQLSTATE.
	ErrUserNotFound = errors.New("user not found")

	ErrSlugTaken   = errors.New("slug already in use")
	ErrSlugInvalid = errors.New("slug does not meet requirements")
	ErrNameInvalid = errors.New("name does not meet requirements")
	ErrRoleInvalid = errors.New("role is not recognised")

	// ErrLastOwner is returned by SetRole and RemoveMember when the
	// change would leave a project with no owner at all. Without this
	// guard, a project's sole owner could demote themselves, remove
	// themselves, or be removed by someone else, leaving every row that
	// hangs off the project (every entity, token, and membership) with
	// nobody left who can manage access to it — a state the domain layer
	// has no way to recover from short of an operator touching the
	// database directly. This is the Go-side half of the guard; migration
	// 0002 adds the database-side half (a constraint trigger on
	// memberships) for the path this package cannot see at all: a user
	// row deleted directly, whose memberships.user_id ON DELETE CASCADE
	// removes their membership underneath this package entirely.
	ErrLastOwner = errors.New("project must keep at least one owner")
)

// ownerRole is string(roles.Owner). Every comparison and every
// UpsertMembership call in this file works with plain strings — role
// values arrive from the database or, eventually, an HTTP request body,
// as plain strings already — so this stays a string constant rather than
// threading the roles.Role type through every signature in this package;
// see the roles package's own doc comment for why the shared vocabulary
// still lives in one place despite that.
const ownerRole = string(roles.Owner)

// Project is the projects domain's public view of a game. It deliberately
// mirrors identity.User in shape and in never being a raw dbq struct: a
// dbq.Project's timestamps are pgtype.Timestamptz, an sqlc/pgx-specific
// wrapper type that nothing outside the db layer should have to import or
// unwrap.
type Project struct {
	ID   uuid.UUID
	Slug string
	Name string
}

func projectFrom(p dbq.Project) Project {
	return Project{ID: p.ID, Slug: p.Slug, Name: p.Name}
}

// Member is one row of a project's membership list: a user paired with
// their role in this project. It deliberately does not carry Email:
// ListMembers is authorization-free by design (see its own doc comment),
// so every caller holding a Member holds whatever fields this type
// exposes, including a viewer with no business reading a teammate's
// address. Display name, id and role are what a member list renders; an
// owner-only contact-details view, if the product ever wants one, is a
// separate, deliberately-added query rather than this type growing a
// field most callers should never see.
type Member struct {
	UserID      uuid.UUID
	DisplayName string
	Role        string
}

// Service is the projects domain.
type Service struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
}

// New builds the projects service over a pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, q: dbq.New(pool)}
}

// withTx runs fn inside a transaction, committing on success and rolling
// back on any non-nil return. This is the same pattern identity.Service
// establishes (Task 5) for the same reason: several methods below are
// multi-write or read-then-write operations where a failure partway
// through must not leave the database half-changed. It cannot literally be
// the same method as identity.Service.withTx — they are different types in
// different packages — but it is deliberately the same four lines rather
// than a second hand-rolled variant.
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

// validateSlug normalises slug (trim, lower-case — the same normalisation
// identity applies to email) and checks its shape against slugPattern and
// length bounds, that it is not a reserved word, and that it is not
// something uuid.Parse would accept — a UUID-shaped slug is exactly the
// ambiguity a handler that accepts either a slug or an id inherits for
// free. The normalised value is what gets stored: a project created as
// "Azeroth" is addressable at /g/azeroth, matching GetProjectBySlug's own
// case-insensitive lookup, so the slug a caller sees in Project.Slug is
// always exactly what appears in a URL.
func validateSlug(slug string) (string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if n := utf8.RuneCountInString(slug); n < minSlugRunes || n > maxSlugRunes {
		return "", fmt.Errorf("%w: must be between %d and %d characters", ErrSlugInvalid, minSlugRunes, maxSlugRunes)
	}
	if !slugPattern.MatchString(slug) {
		return "", fmt.Errorf("%w: must contain only lower-case letters, digits and single internal hyphens", ErrSlugInvalid)
	}
	if reservedSlugs[slug] {
		return "", fmt.Errorf("%w: %q is reserved", ErrSlugInvalid, slug)
	}
	if _, err := uuid.Parse(slug); err == nil {
		return "", fmt.Errorf("%w: must not be a UUID", ErrSlugInvalid)
	}
	return slug, nil
}

// validateName trims name and checks its length bound and that it carries
// no control characters or Unicode bidirectional-override characters.
// Names are stored verbatim and later rendered into page titles, the game
// picker and SSE payloads, so a newline or a bidi override — invisible in
// a form field, capable of making the rendered text read in an order
// different from its byte order — would store cleanly today and only
// become someone else's problem at render time.
func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < minNameRunes || n > maxNameRunes {
		return "", fmt.Errorf("%w: must be between %d and %d characters", ErrNameInvalid, minNameRunes, maxNameRunes)
	}
	for _, r := range name {
		if unicode.IsControl(r) || bidiOverrides[r] {
			return "", fmt.Errorf("%w: must not contain control or bidirectional-override characters", ErrNameInvalid)
		}
	}
	return name, nil
}

// mapMembershipInsertError translates a foreign-key violation from an
// insert or update against memberships into the domain error naming which
// side was missing. Both UpsertMembership call sites in this file (Create
// granting the creator ownership, SetRole granting or changing any
// member's role) can hit either constraint, so the mapping lives here
// once rather than being duplicated at each call site — the same
// reasoning as the 23505/projects_slug_key mapping in Create, applied to
// 23503 (foreign_key_violation) instead of 23505 (unique_violation). It
// returns nil when err is not a foreign-key violation on either
// constraint, so callers fall through to their own generic error wrap.
func mapMembershipInsertError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return nil
	}
	switch pgErr.ConstraintName {
	case "memberships_project_id_fkey":
		return ErrProjectNotFound
	case "memberships_user_id_fkey":
		return ErrUserNotFound
	default:
		return nil
	}
}

// Create makes a project and its creator its owner, atomically: a failure
// granting the membership must not leave an ownerless project behind for
// ErrLastOwner to later refuse to ever fix.
func (s *Service) Create(ctx context.Context, slug, name string, creator uuid.UUID) (Project, error) {
	slug, err := validateSlug(slug)
	if err != nil {
		return Project{}, err
	}
	name, err = validateName(name)
	if err != nil {
		return Project{}, err
	}

	var project dbq.Project
	err = s.withTx(ctx, func(q *dbq.Queries) error {
		var cerr error
		project, cerr = q.CreateProject(ctx, dbq.CreateProjectParams{Slug: slug, Name: name})
		if cerr != nil {
			var pgErr *pgconn.PgError
			// Checked by constraint name, not just code, for the same
			// reason as identity.insertUser's ErrEmailTaken mapping
			// (Task 5): this table can grow more unique indexes, and a
			// 23505 from one of those must not be misreported as "slug
			// taken".
			if errors.As(cerr, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "projects_slug_key" {
				return ErrSlugTaken
			}
			return fmt.Errorf("create project: %w", cerr)
		}
		if err := q.UpsertMembership(ctx, dbq.UpsertMembershipParams{
			UserID:    creator,
			ProjectID: project.ID,
			Role:      ownerRole,
		}); err != nil {
			// The only foreign key that can fail here is
			// memberships_user_id_fkey: project.ID was just returned by
			// CreateProject in this same transaction, so
			// memberships_project_id_fkey cannot be the cause. Mapped
			// anyway through the shared helper rather than assuming that
			// — a future change to what Create validates before this
			// point should not have to remember to revisit this comment.
			if merr := mapMembershipInsertError(err); merr != nil {
				return merr
			}
			return fmt.Errorf("grant ownership: %w", err)
		}
		return nil
	})
	if err != nil {
		return Project{}, err
	}
	return projectFrom(project), nil
}

// ListForUser returns every project the user is a member of, ordered by
// name then id (see the query's own doc comment for why the tiebreak).
// This is the query behind the single-game navigation shortcut described
// in the spec: a caller that gets back exactly one project sends the user
// straight to it instead of showing a picker.
func (s *Service) ListForUser(ctx context.Context, userID uuid.UUID) ([]Project, error) {
	rows, err := s.q.ListProjectsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]Project, len(rows))
	for i, row := range rows {
		out[i] = projectFrom(row)
	}
	return out, nil
}

// RoleOf returns the user's role in a project, or ErrNotAMember. See
// ErrNotAMember's doc comment for why a non-existent project and a real
// project the user isn't in are not distinguished here.
func (s *Service) RoleOf(ctx context.Context, userID, projectID uuid.UUID) (string, error) {
	role, err := s.q.GetMembershipRole(ctx, dbq.GetMembershipRoleParams{UserID: userID, ProjectID: projectID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotAMember
		}
		return "", fmt.Errorf("lookup membership: %w", err)
	}
	return role, nil
}

// bySlug resolves a project from its normalised URL slug, with no
// authorization check at all. It is unexported: a raw slug lookup is an
// enumeration oracle, because unlike a project id a slug is a human-chosen
// game name ("azeroth") — letting any authenticated caller ask "does this
// slug exist" leaks exactly the cross-game information RoleOf goes out of
// its way not to (see ErrNotAMember's doc comment). BySlugForUser below is
// the exported, authorization-checked equivalent; nothing outside this
// package needs the raw form, and nothing in this plan calls it directly.
func (s *Service) bySlug(ctx context.Context, slug string) (Project, error) {
	slug, err := validateSlug(slug)
	if err != nil {
		return Project{}, err
	}
	project, err := s.q.GetProjectBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Project{}, ErrProjectNotFound
		}
		return Project{}, fmt.Errorf("lookup project: %w", err)
	}
	return projectFrom(project), nil
}

// BySlugForUser resolves a project from its URL slug and checks in the
// same call that userID is a member of it, returning ErrProjectNotFound
// for both "no such slug" and "a real game you're just not in" — the same
// non-distinction RoleOf already makes between a non-existent project and
// one the caller has no standing in, applied here to slug lookups instead
// of id lookups. This is the method a /g/{slug} route should call; see
// bySlug's own doc comment for why that method stays unexported.
func (s *Service) BySlugForUser(ctx context.Context, slug string, userID uuid.UUID) (Project, error) {
	project, err := s.bySlug(ctx, slug)
	if err != nil {
		return Project{}, err
	}
	if _, err := s.RoleOf(ctx, userID, project.ID); err != nil {
		return Project{}, ErrProjectNotFound
	}
	return project, nil
}

// ByID resolves a project from its id, with no membership check: unlike a
// slug, a UUID is not a human-chosen, guessable name, so a raw id lookup
// is not the same enumeration oracle bySlug is — reaching this method
// already requires holding a specific id from somewhere (a caller's own
// ListForUser result, a token's bound project id), not guessing at one.
func (s *Service) ByID(ctx context.Context, id uuid.UUID) (Project, error) {
	project, err := s.q.GetProjectByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Project{}, ErrProjectNotFound
		}
		return Project{}, fmt.Errorf("lookup project: %w", err)
	}
	return projectFrom(project), nil
}

// ListMembers returns every member of a project and their role, ordered by
// display name then id (see the query's own doc comment for why the
// tiebreak). It performs no authorization check of its own and, as of
// this package's last review, carries no email address either (see
// Member's own doc comment) — it does not distinguish "empty project"
// from "unknown project" either, an empty result for both, because
// listing members is only ever meant to be reached after a caller has
// already established standing to see the project (typically by RoleOf
// succeeding first); this method itself decides none of that.
func (s *Service) ListMembers(ctx context.Context, projectID uuid.UUID) ([]Member, error) {
	rows, err := s.q.ListMembers(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	out := make([]Member, len(rows))
	for i, row := range rows {
		out[i] = Member{UserID: row.ID, DisplayName: row.DisplayName, Role: row.Role}
	}
	return out, nil
}

// SetRole grants or changes a user's membership role in a project. It is
// the one method for both "add this user to the project" and "change this
// existing member's role", matching UpsertMembership's own insert-or-update
// semantics: there is no meaningful difference between the two from the
// database's point of view, and giving them separate methods would only
// invite a caller to duplicate the role-validation and last-owner checks
// below across both.
//
// SetRole performs no authorization check of its own — it trusts the
// caller (Task 12's HTTP handler) to have already decided this call is
// allowed. This is a deliberate decision, not an oversight: authorization
// here would need to know things this package has no business
// knowing (who is making the request, and on whose behalf), and Task 12's
// own corrections explain why that decision belongs at the HTTP layer.
//
// The last-owner guard only ever fires when the target is already an
// owner being moved to a different role: promoting someone, or changing a
// non-owner's role, can never reduce the owner count. When it does apply,
// CountOwnersForUpdate is called inside this method's transaction, taking
// a row lock on every owner membership of the project. That lock is
// deliberate defence in depth, not the sole thing preventing two
// concurrent demotions of a project's last two owners from both
// succeeding: migration 0002's constraint trigger enforces the identical
// invariant independently, re-checked at commit time regardless of this
// lock, and already serializes that race on its own — see
// CountOwnersForUpdate's own doc comment for why, and for the review that
// confirmed it directly by removing this lock and running the concurrency
// test unchanged. What the lock earns here is failing fast with a typed
// ErrLastOwner instead of the transaction aborting on a raw trigger
// exception.
//
// Returns the labels of every token this call revoked because the new
// role dropped below editor (see the demotion branch below), never nil —
// the same convention RemoveMember's return follows, for the same
// reason: a quality review found the first version of this method's
// HTTP handler answered a bare 200 after silently killing a demoted
// member's agents, leaving the owner who demoted them with no way to
// know which ones. A promotion, or a role change that never crosses the
// editor threshold, always returns an empty (non-nil) slice.
func (s *Service) SetRole(ctx context.Context, userID, projectID uuid.UUID, role string) ([]string, error) {
	if !roles.Valid(role) {
		return nil, fmt.Errorf("%w: %q", ErrRoleInvalid, role)
	}
	var revokedLabels []string
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		current, err := q.GetMembershipRole(ctx, dbq.GetMembershipRoleParams{UserID: userID, ProjectID: projectID})
		isMember := true
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				isMember = false
			} else {
				return fmt.Errorf("lookup membership: %w", err)
			}
		}
		if isMember && current == ownerRole && role != ownerRole {
			n, cerr := q.CountOwnersForUpdate(ctx, projectID)
			if cerr != nil {
				return fmt.Errorf("count owners: %w", cerr)
			}
			if n <= 1 {
				return ErrLastOwner
			}
		}
		if err := q.UpsertMembership(ctx, dbq.UpsertMembershipParams{
			UserID:    userID,
			ProjectID: projectID,
			Role:      role,
		}); err != nil {
			if merr := mapMembershipInsertError(err); merr != nil {
				return merr
			}
			return fmt.Errorf("set role: %w", err)
		}

		// A token grants an agent editor-equivalent access for as long as
		// it lives, independent of its minter's current standing (Task
		// 12's quality review settled this explicitly: a token outlives
		// the person who made it, so re-deriving its access from a live
		// membership lookup on every request would contradict that
		// design, not honour it — see ProjectScope's own doc comment in
		// internal/web/api_projects.go). The invariant that makes that
		// safe is that a token never exceeds its minter's *current*
		// standing, and the only way to keep that true without a
		// per-request lookup is to close it off the moment standing
		// changes: demoting a member below editor revokes every token
		// they minted in this project, the same way RemoveMember already
		// revokes them outright when membership itself is lost. Promoting,
		// or moving between editor and owner, never triggers this — both
		// remain roles.AtLeast Editor.
		if !roles.AtLeast(roles.Role(role), roles.Editor) {
			labels, err := q.RevokeAPITokensForMember(ctx, dbq.RevokeAPITokensForMemberParams{UserID: userID, ProjectID: projectID})
			if err != nil {
				return fmt.Errorf("revoke demoted member's tokens: %w", err)
			}
			revokedLabels = labels
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if revokedLabels == nil {
		revokedLabels = []string{}
	}
	return revokedLabels, nil
}

// RemoveMember drops a user's membership in a project, and revokes every
// API token in that same project the removed user created — in the same
// transaction as the membership deletion, so a failure revoking tokens
// rolls back the membership deletion too, and a crash between the two
// can never leave a token live for a project its creator no longer
// belongs to. This is deliberately scoped to this project only: a
// member can belong to other projects with tokens of their own, and
// removing them from one project must not touch those. Removing a user
// who is not currently a member is not an error: the caller's goal (no
// membership for this user in this project) is already satisfied, the
// same convention identity.RevokeSession and identity.RevokeInvite
// already establish; the token revocation below shares that no-op
// convention too (see RevokeAPITokensForMember's own comment), so it
// never needs to check what exists before running. Removing the
// project's sole owner is refused with ErrLastOwner — see SetRole's doc
// comment for the two independent layers (this package's row lock and
// migration 0002's trigger) that defend that invariant under concurrent
// removal and under a direct user deletion, and for what each one does
// and does not earn on its own. Like SetRole, this method performs no
// authorization check of its own; see SetRole's doc comment for why.
//
// api_tokens.user_id records who created a token, not a live
// authorization link, and this package does not import identity to make
// this call (nor does identity import this package): RevokeAPITokensForMember
// is plain SQL over api_tokens, defined in this package's own query file
// and reached through the same shared dbq.Queries handle UpsertMembership
// elsewhere in this file already uses the other way — a query defined in
// identity.sql, called from here.
//
// Returns the labels of every token this call revoked, never nil (see
// this method's own tail for why) — a quality review pointed out that
// answering with a bare success left an owner with no idea which of
// their agents just stopped working: a revoked token id means nothing on
// its own, but "nightly export" or "seed agent" does.
func (s *Service) RemoveMember(ctx context.Context, userID, projectID uuid.UUID) ([]string, error) {
	var revokedLabels []string
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		current, err := q.GetMembershipRole(ctx, dbq.GetMembershipRoleParams{UserID: userID, ProjectID: projectID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("lookup membership: %w", err)
		}
		if current == ownerRole {
			n, cerr := q.CountOwnersForUpdate(ctx, projectID)
			if cerr != nil {
				return fmt.Errorf("count owners: %w", cerr)
			}
			if n <= 1 {
				return ErrLastOwner
			}
		}
		if err := q.DeleteMembership(ctx, dbq.DeleteMembershipParams{UserID: userID, ProjectID: projectID}); err != nil {
			return fmt.Errorf("remove member: %w", err)
		}
		labels, err := q.RevokeAPITokensForMember(ctx, dbq.RevokeAPITokensForMemberParams{UserID: userID, ProjectID: projectID})
		if err != nil {
			return fmt.Errorf("revoke member's tokens: %w", err)
		}
		revokedLabels = labels
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Never nil: the caller (Task 12's handler) marshals this straight
	// into a JSON response, and a removed member with no live tokens
	// should answer with an empty array ("revoked_tokens": []), not JSON
	// null — RevokeAPITokensForMember's own :many query returns a nil
	// slice, not an empty one, when it matches zero rows.
	if revokedLabels == nil {
		revokedLabels = []string{}
	}
	return revokedLabels, nil
}
