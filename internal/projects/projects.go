// Package projects owns games and who may see them.
package projects

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
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

// Errors returned by the projects service.
var (
	// ErrNotAMember covers both "this user has no membership in this
	// project" and "this project does not exist". The two are
	// deliberately indistinguishable: RoleOf is the authorization check
	// every other layer calls before trusting a caller with a project's
	// data, and telling a non-member which project IDs are real would be
	// exactly the kind of cross-game leak the isolation invariant exists
	// to prevent. A caller that needs to tell "no such game" apart from
	// "not your game" — a 404 page, say — uses BySlug or ByID instead,
	// whose whole job is looking a project up, not deciding who may see
	// its contents.
	ErrNotAMember = errors.New("user is not a member of this project")

	// ErrProjectNotFound is returned by BySlug and ByID for a project
	// that does not exist. Unlike RoleOf, both of those are lookups, not
	// authorization checks, so there is nothing to protect by collapsing
	// this into a different error.
	ErrProjectNotFound = errors.New("project not found")

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
	// database directly.
	ErrLastOwner = errors.New("project must keep at least one owner")
)

// membershipRoles are the roles SetRole accepts. This must stay identical
// to the memberships table's own CHECK constraint (migration 0001,
// `role IN ('owner', 'editor', 'viewer')`) and to identity.inviteRoles,
// which enforces the same set for invites — see that var's doc comment for
// why a raw constraint violation surfacing from deep inside a transaction
// is worse than failing here in Go first.
var membershipRoles = map[string]bool{
	"owner":  true,
	"editor": true,
	"viewer": true,
}

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
// their role in this project. It exists for the same reason Project and
// identity.User do — ListMembers' underlying query joins users and
// memberships, and its generated row type is not something this package
// hands to a caller.
type Member struct {
	UserID      uuid.UUID
	Email       string
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
// length bounds. The normalised value is what gets stored: a project
// created as "Azeroth" is addressable at /g/azeroth, matching
// GetProjectBySlug's own case-insensitive lookup, so the slug a caller
// sees in Project.Slug is always exactly what appears in a URL.
func validateSlug(slug string) (string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if n := utf8.RuneCountInString(slug); n < minSlugRunes || n > maxSlugRunes {
		return "", fmt.Errorf("%w: must be between %d and %d characters", ErrSlugInvalid, minSlugRunes, maxSlugRunes)
	}
	if !slugPattern.MatchString(slug) {
		return "", fmt.Errorf("%w: must contain only lower-case letters, digits and single internal hyphens", ErrSlugInvalid)
	}
	return slug, nil
}

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < minNameRunes || n > maxNameRunes {
		return "", fmt.Errorf("%w: must be between %d and %d characters", ErrNameInvalid, minNameRunes, maxNameRunes)
	}
	return name, nil
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
			Role:      "owner",
		}); err != nil {
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
// name. This is the query behind the single-game navigation shortcut
// described in the spec: a caller that gets back exactly one project sends
// the user straight to it instead of showing a picker.
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

// BySlug resolves a project from its URL slug. The slug is matched
// case-insensitively, matching GetProjectBySlug's own `lower(slug) =
// lower(...)` comparison and Create's own normalisation.
func (s *Service) BySlug(ctx context.Context, slug string) (Project, error) {
	project, err := s.q.GetProjectBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Project{}, ErrProjectNotFound
		}
		return Project{}, fmt.Errorf("lookup project: %w", err)
	}
	return projectFrom(project), nil
}

// ByID resolves a project from its id.
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
// display name. It does not distinguish "empty project" from "unknown
// project" — an empty result for either — because listing members is only
// ever reached after a caller has already established standing to see the
// project (typically by RoleOf succeeding first); this method itself does
// not authorize anything.
func (s *Service) ListMembers(ctx context.Context, projectID uuid.UUID) ([]Member, error) {
	rows, err := s.q.ListMembers(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	out := make([]Member, len(rows))
	for i, row := range rows {
		out[i] = Member{UserID: row.ID, Email: row.Email, DisplayName: row.DisplayName, Role: row.Role}
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
// The last-owner guard only ever fires when the target is already an
// owner being moved to a different role: promoting someone, or changing a
// non-owner's role, can never reduce the owner count. When it does apply,
// CountOwnersForUpdate is called inside this method's transaction, which
// takes a row lock on every owner membership of the project — see that
// query's own doc comment for why this is what makes two concurrent
// demotions of a project's last two owners resolve safely (one succeeds,
// the other sees the now-updated count and fails) instead of racing to
// leave the project with none.
func (s *Service) SetRole(ctx context.Context, userID, projectID uuid.UUID, role string) error {
	if !membershipRoles[role] {
		return fmt.Errorf("%w: %q", ErrRoleInvalid, role)
	}
	return s.withTx(ctx, func(q *dbq.Queries) error {
		current, err := q.GetMembershipRole(ctx, dbq.GetMembershipRoleParams{UserID: userID, ProjectID: projectID})
		isMember := true
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				isMember = false
			} else {
				return fmt.Errorf("lookup membership: %w", err)
			}
		}
		if isMember && current == "owner" && role != "owner" {
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
			return fmt.Errorf("set role: %w", err)
		}
		return nil
	})
}

// RemoveMember drops a user's membership in a project. Removing a user who
// is not currently a member is not an error: the caller's goal (no
// membership for this user in this project) is already satisfied, the
// same convention identity.RevokeSession and identity.RevokeInvite already
// establish. Removing the project's sole owner is refused with
// ErrLastOwner — see SetRole's doc comment for the locking that makes this
// safe under concurrent removal attempts.
func (s *Service) RemoveMember(ctx context.Context, userID, projectID uuid.UUID) error {
	return s.withTx(ctx, func(q *dbq.Queries) error {
		current, err := q.GetMembershipRole(ctx, dbq.GetMembershipRoleParams{UserID: userID, ProjectID: projectID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("lookup membership: %w", err)
		}
		if current == "owner" {
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
		return nil
	})
}
