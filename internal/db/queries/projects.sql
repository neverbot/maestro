-- name: CreateProject :one
INSERT INTO projects (slug, name)
VALUES (sqlc.arg('slug')::text, sqlc.arg('name')::text)
RETURNING *;

-- name: GetProjectBySlug :one
SELECT * FROM projects WHERE lower(slug) = lower(sqlc.arg('slug')::text);

-- name: GetProjectBySlugForUser :one
-- The slug lookup every caller-facing path uses, and the reason
-- GetProjectBySlug above has no Go caller of its own.
--
-- **The join is the whole point and it is a security property, not an
-- optimisation.** A slug is a name a human chose and typed, so it is
-- guessable in a way a uuid is not; resolving one without a membership
-- check and *then* judging standing would let a stranger tell "azeroth
-- exists and you are not in it" from "there is no azeroth", one guess at
-- a time. Joining membership into the lookup collapses both into "no
-- rows", which is the answer projects.ErrNotAMember's own doc comment
-- already argues for and the shape Task 8's Correction 12 specified when
-- it removed the bare BySlug this replaces.
--
-- The role comes back with the project because the caller that needs one
-- needs the other in the same breath (internal/web's requireProject),
-- and two queries would be two chances for a demotion to land between
-- them -- the exact time-of-check-to-time-of-use window requireProject's
-- own doc comment records closing once already.
--
-- lower(slug) on both sides, matching the case-insensitive uniqueness a
-- slug already has: /g/Azeroth and /g/azeroth are one game.
SELECT p.id, p.slug, p.name, m.role
FROM projects p
JOIN memberships m ON m.project_id = p.id
WHERE lower(p.slug) = lower(sqlc.arg('slug')::text)
  AND m.user_id = sqlc.arg('user_id')::uuid;

-- name: GetProjectByID :one
SELECT * FROM projects WHERE id = sqlc.arg('id')::uuid;

-- name: ListProjectsForUser :many
-- Ordered by name then id: name alone is not unique (two games can both
-- be called "Untitled"), so a name-only order reshuffles ties between
-- calls in whatever order Postgres happens to return them. id, being a
-- primary key, is always unique, so appending it as a tiebreak makes the
-- order stable across repeated calls with identical input.
SELECT p.* FROM projects p
JOIN memberships m ON m.project_id = p.id
WHERE m.user_id = sqlc.arg('user_id')::uuid
ORDER BY p.name, p.id;

-- name: GetMembershipRole :one
SELECT role FROM memberships
WHERE user_id = sqlc.arg('user_id')::uuid AND project_id = sqlc.arg('project_id')::uuid;

-- name: ListMembers :many
-- No u.email: ListMembers is authorization-free by design (see
-- projects.ListMembers's own doc comment), so every caller holding a
-- Member holds whatever this query returns, including a viewer with no
-- business reading a teammate's email. display_name, id and role are what
-- a member list renders; an owner-only contact-details view, if the
-- product ever wants one, is a separate query added deliberately rather
-- than this one growing a field most callers should not see.
-- Ordered by display_name then id, for the same tiebreak reason as
-- ListProjectsForUser above: display names are not unique.
SELECT u.id, u.display_name, m.role
FROM memberships m
JOIN users u ON u.id = m.user_id
WHERE m.project_id = sqlc.arg('project_id')::uuid
ORDER BY u.display_name, u.id;

-- name: CountOwnersForUpdate :one
-- Locks every owner row of the project before counting, so that SetRole
-- and RemoveMember (both of which call this from inside a transaction
-- before demoting or deleting an owner) serialize against any other
-- concurrent call doing the same thing for the same project. FOR UPDATE
-- cannot be combined directly with an aggregate, hence the subquery: the
-- lock is taken in the inner SELECT, the count happens in the outer one.
-- Under READ COMMITTED, a blocked transaction that unblocks re-checks the
-- WHERE clause against the row's latest committed version, so a row
-- demoted away from 'owner' by the transaction that held the lock is
-- correctly excluded from the count the next transaction sees.
--
-- This lock is deliberate defence in depth, not the only thing standing
-- between two concurrent callers and a zero-owner project: migration
-- 0002's constraint trigger enforces the identical invariant
-- independently, re-checked at commit time regardless of whether this
-- lock is taken at all, and a quality review confirmed it alone already
-- serializes the two-goroutine race projects_test.go's
-- TestConcurrentRemovalLeavesExactlyOneOwner drives — stripping this
-- FOR UPDATE and running that test roughly eighty times produced no
-- failures. What this lock earns on its own is SetRole/RemoveMember
-- failing fast with a typed ErrLastOwner instead of the transaction
-- aborting on a raw trigger exception; see that test's own comment for
-- the fuller version of this note.
SELECT count(*) FROM (
    SELECT 1 FROM memberships
    WHERE project_id = sqlc.arg('project_id')::uuid AND role = 'owner'
    FOR UPDATE
) owners;

-- name: DeleteMembership :exec
DELETE FROM memberships
WHERE user_id = sqlc.arg('user_id')::uuid AND project_id = sqlc.arg('project_id')::uuid;

-- name: RevokeAPITokensForMember :many
-- Called from RemoveMember and, since the quality review that added the
-- "a token is editor-equivalent" rule, from SetRole too, both inside the
-- same transaction as the membership change: every live API token in
-- this project created by the member is revoked — not every token that
-- member has ever created across every project they belong to, only the
-- ones scoped to the project their standing just changed in. This is SQL
-- over api_tokens, not a reason for this package to import identity: the
-- query runs through the same dbq.Queries handle UpsertMembership above
-- already crosses in the other direction (UpsertMembership is defined in
-- identity.sql and called from this package). Idempotent and already
-- project-scoped for the same reason identity's own RevokeAPIToken is
-- (see that query's comment): a member with no tokens, or already-revoked
-- ones, is a harmless no-op, so neither caller needs to check what exists
-- before calling this.
--
-- RETURNING label, not :exec, so RemoveMember can hand its caller the
-- labels of whichever agents just lost access — a revoked token id means
-- nothing to the owner who removed the member; the label ("nightly
-- export", "seed agent") is what lets them recognise which of their
-- agents just stopped working.
UPDATE api_tokens SET revoked_at = now()
WHERE user_id = sqlc.arg('user_id')::uuid AND project_id = sqlc.arg('project_id')::uuid
  AND revoked_at IS NULL
RETURNING label;

-- name: DeleteProject :exec
-- Every membership, API token and bound invite scoped to this project
-- disappears in the same statement, through the ON DELETE CASCADE
-- foreign keys migration 0001 declares on each (Task 17's own plan
-- section works through why that is enough on its own). Migration
-- 0002's last-owner trigger has an explicit escape hatch for exactly
-- this statement, so it never trips on the project's own last owner
-- being cascaded away. Matching zero rows (the id was already deleted,
-- typically a second concurrent DELETE landing after the first already
-- committed) is not an error here, the same idempotent convention
-- DeleteMembership and RevokeAPITokensForMember already follow: the
-- Go layer above does not distinguish "deleted" from "already gone".
DELETE FROM projects WHERE id = sqlc.arg('id')::uuid;
