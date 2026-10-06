-- The log beside a game's content. Every statement carries the resolved
-- project id, and which of them are load-bearing is stated rather than
-- implied:
--
--   * **DeleteComment's is.** A comment id is a value a previous answer
--     handed back and names no parent, so nothing but this filter keeps
--     a removal inside one game. Mutating it reddens TestCommentsArea's
--     "one game's log is not another's" case.
--   * **The listings' are defence in depth.** A listing is reached by a
--     target the caller addressed by key, and resolving that address is
--     already scoped to the game; the composite foreign keys mean a
--     comment cannot name a row of another game in the first place. The
--     filters stay because a read of this table should not depend on
--     somebody remembering that two other mechanisms already did the
--     work, and no test can tell them apart -- which is this comment's
--     reason for existing.
--
-- The four target columns are a CHECK-constrained choice of one
-- (0017_comments.sql), so a listing filters on the one that is set and
-- the three nulls never match anything.

-- name: InsertComment :one
INSERT INTO comments (project_id, entity_id, relation_id, entity_type_id, relation_type_id,
                      body, created_by_user_id, created_by_token_id)
VALUES (sqlc.arg('project_id')::uuid,
        sqlc.narg('entity_id')::uuid,
        sqlc.narg('relation_id')::uuid,
        sqlc.narg('entity_type_id')::uuid,
        sqlc.narg('relation_type_id')::uuid,
        sqlc.arg('body')::text,
        sqlc.narg('created_by_user_id')::uuid,
        sqlc.narg('created_by_token_id')::uuid)
RETURNING *;

-- name: ListCommentsOnEntity :many
-- Newest first, which is how a log is read, and one page at a time.
SELECT * FROM comments
WHERE project_id = sqlc.arg('project_id')::uuid AND entity_id = sqlc.arg('entity_id')::uuid
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('lim')::integer;

-- name: ListCommentsOnRelation :many
SELECT * FROM comments
WHERE project_id = sqlc.arg('project_id')::uuid AND relation_id = sqlc.arg('relation_id')::uuid
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('lim')::integer;

-- name: ListCommentsOnEntityType :many
SELECT * FROM comments
WHERE project_id = sqlc.arg('project_id')::uuid AND entity_type_id = sqlc.arg('entity_type_id')::uuid
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('lim')::integer;

-- name: ListCommentsOnRelationType :many
SELECT * FROM comments
WHERE project_id = sqlc.arg('project_id')::uuid AND relation_type_id = sqlc.arg('relation_type_id')::uuid
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('lim')::integer;

-- name: ListCommentsInProject :many
-- The game's whole log: what has been thought about this game lately,
-- whatever it was about.
SELECT * FROM comments
WHERE project_id = sqlc.arg('project_id')::uuid
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('lim')::integer;

-- name: ListCommentCountsForEntities :many
-- How many comments each of these entities carries, for a listing that
-- wants to say which rows have been thought about without reading the
-- log of every one of them.
SELECT entity_id, count(*)::bigint AS comments
FROM comments
WHERE project_id = sqlc.arg('project_id')::uuid AND entity_id = ANY (sqlc.arg('entity_ids')::uuid[])
GROUP BY entity_id;

-- name: DeleteComment :one
-- Append-only means there is no update; a note written against the wrong
-- thing is still worse than a gap, so it can go.
DELETE FROM comments
WHERE id = sqlc.arg('id')::uuid AND project_id = sqlc.arg('project_id')::uuid
RETURNING id;
