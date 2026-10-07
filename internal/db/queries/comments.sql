-- The log beside a game's content. Every statement carries the resolved
-- project id, and which of them are load-bearing is stated rather than
-- implied:
--
--   * **DeleteComment's is.** A comment id is a value a previous answer
--     handed back and names no parent, so nothing but this filter keeps
--     a removal inside one game. Mutating it reddens TestCommentsArea's
--     "one game's log is not another's" case.
-- **The author comes back as three facts, not one.** `author` is what to
-- print, `author_of` is the person a token traces back to, and which of
-- the two audit columns is set says whether a person or an agent wrote
-- it: a reader who cannot tell a machine from a colleague is reading a
-- log that is lying to them by omission, and the token label alone
-- ("rl-aeternum") reads as a name.
--
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
-- **The author comes back with the row**, from the same join every read
-- here makes: a log whose entries do not say who wrote them is half a
-- log, and the two kinds of author this product has are a person with a
-- display name and an agent with a token's label.
WITH written AS (
    INSERT INTO comments (project_id, entity_id, entity_type_id, relation_type_id,
                          body, created_by_user_id, created_by_token_id)
    VALUES (sqlc.arg('project_id')::uuid,
            sqlc.narg('entity_id')::uuid,
            sqlc.narg('entity_type_id')::uuid,
            sqlc.narg('relation_type_id')::uuid,
            sqlc.arg('body')::text,
            sqlc.narg('created_by_user_id')::uuid,
            sqlc.narg('created_by_token_id')::uuid)
    RETURNING *
)
SELECT w.*, COALESCE(t.label, u.display_name, '')::text AS author,
       COALESCE(owner.display_name, u.display_name, '')::text AS author_of
FROM written w
LEFT JOIN users u ON u.id = w.created_by_user_id
LEFT JOIN api_tokens t ON t.id = w.created_by_token_id
LEFT JOIN users owner ON owner.id = t.user_id;

-- name: ListCommentsOnEntity :many
-- Newest first, which is how a log is read, and one page at a time.
SELECT c.*, COALESCE(t.label, u.display_name, '')::text AS author,
       COALESCE(owner.display_name, u.display_name, '')::text AS author_of
FROM comments c
LEFT JOIN users u ON u.id = c.created_by_user_id
LEFT JOIN api_tokens t ON t.id = c.created_by_token_id
LEFT JOIN users owner ON owner.id = t.user_id
WHERE c.project_id = sqlc.arg('project_id')::uuid AND c.entity_id = sqlc.arg('entity_id')::uuid
ORDER BY c.created_at DESC, c.id DESC
LIMIT sqlc.arg('lim')::integer;

-- name: ListCommentsOnEntityType :many
SELECT c.*, COALESCE(t.label, u.display_name, '')::text AS author,
       COALESCE(owner.display_name, u.display_name, '')::text AS author_of
FROM comments c
LEFT JOIN users u ON u.id = c.created_by_user_id
LEFT JOIN api_tokens t ON t.id = c.created_by_token_id
LEFT JOIN users owner ON owner.id = t.user_id
WHERE c.project_id = sqlc.arg('project_id')::uuid AND c.entity_type_id = sqlc.arg('entity_type_id')::uuid
ORDER BY c.created_at DESC, c.id DESC
LIMIT sqlc.arg('lim')::integer;

-- name: ListCommentsOnRelationType :many
SELECT c.*, COALESCE(t.label, u.display_name, '')::text AS author,
       COALESCE(owner.display_name, u.display_name, '')::text AS author_of
FROM comments c
LEFT JOIN users u ON u.id = c.created_by_user_id
LEFT JOIN api_tokens t ON t.id = c.created_by_token_id
LEFT JOIN users owner ON owner.id = t.user_id
WHERE c.project_id = sqlc.arg('project_id')::uuid AND c.relation_type_id = sqlc.arg('relation_type_id')::uuid
ORDER BY c.created_at DESC, c.id DESC
LIMIT sqlc.arg('lim')::integer;

-- name: ListCommentsInProject :many
-- The game's whole log: what has been thought about this game lately,
-- whatever it was about.
SELECT c.*, COALESCE(t.label, u.display_name, '')::text AS author,
       COALESCE(owner.display_name, u.display_name, '')::text AS author_of
FROM comments c
LEFT JOIN users u ON u.id = c.created_by_user_id
LEFT JOIN api_tokens t ON t.id = c.created_by_token_id
LEFT JOIN users owner ON owner.id = t.user_id
WHERE c.project_id = sqlc.arg('project_id')::uuid
ORDER BY c.created_at DESC, c.id DESC
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
