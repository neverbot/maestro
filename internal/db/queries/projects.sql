-- name: CreateProject :one
INSERT INTO projects (slug, name)
VALUES (sqlc.arg('slug')::text, sqlc.arg('name')::text)
RETURNING *;

-- name: GetProjectBySlug :one
SELECT * FROM projects WHERE lower(slug) = lower(sqlc.arg('slug')::text);

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
SELECT count(*) FROM (
    SELECT 1 FROM memberships
    WHERE project_id = sqlc.arg('project_id')::uuid AND role = 'owner'
    FOR UPDATE
) owners;

-- name: DeleteMembership :exec
DELETE FROM memberships
WHERE user_id = sqlc.arg('user_id')::uuid AND project_id = sqlc.arg('project_id')::uuid;
