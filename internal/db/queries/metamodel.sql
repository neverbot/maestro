-- name: UpsertEntityType :one
-- Every statement in this file filters on the resolved project id,
-- including the ones addressing a row by its primary key: isolation
-- between games is enforced in SQL, not in Go, so a query trusting an id
-- alone would hand a caller another game's row the moment an id leaked.
--
-- No write here sets updated_at. 0004_metamodel.sql puts a set_updated_at
-- trigger on all four tables, so the column has one mechanism behind it
-- rather than a trigger plus a clause every future query must remember.
--
-- The DO UPDATE is guarded by the caller's expected version, so the whole
-- compare-and-set happens in one statement and two concurrent writers
-- cannot both read version 1 and both succeed. A creating caller has no
-- version to expect and passes noVersion, which no stored version can
-- equal, so the guard is a no-op on the insert path and a guaranteed
-- mismatch when a row it did not know about turns out to exist. A guard
-- that fails returns no row rather than an error; UpsertEntityType turns
-- that into the typed conflict.
--
-- Idempotent by (project, key), matched case-insensitively through the
-- entity_types_key_key index. The key column itself is deliberately not in
-- the SET list: the first spelling stored stays, so a re-seed cannot
-- rewrite the handle other rows and documents refer to. That omission is
-- also what lets UpsertEntityType in Go refuse a respelling *after* this
-- statement runs: the row returned still carries the stored key, so
-- comparing it to the submitted one catches a writer whose own locked read
-- could not have seen this row at all — a creation racing a creator, where
-- there was nothing yet to lock. So this clause is what makes an identical
-- spelling a no-op; it is not what resolves a conflict.
INSERT INTO entity_types (project_id, key, label, label_plural, description, color, icon,
                          field_schema, updated_by_user_id, updated_by_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('key')::text, sqlc.arg('label')::text,
        sqlc.arg('label_plural')::text, sqlc.arg('description')::text,
        sqlc.arg('color')::text, sqlc.arg('icon')::text, sqlc.arg('field_schema')::jsonb,
        sqlc.narg('updated_by_user_id')::uuid, sqlc.narg('updated_by_token_id')::uuid)
ON CONFLICT (project_id, lower(key)) DO UPDATE
SET label               = excluded.label,
    label_plural        = excluded.label_plural,
    description         = excluded.description,
    color               = excluded.color,
    icon                = excluded.icon,
    field_schema        = excluded.field_schema,
    version             = entity_types.version + 1,
    updated_by_user_id  = excluded.updated_by_user_id,
    updated_by_token_id = excluded.updated_by_token_id
WHERE entity_types.version = sqlc.arg('expected_version')::integer
RETURNING *;

-- name: GetEntityTypeByKey :one
SELECT * FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text);

-- name: GetEntityTypeByID :one
SELECT * FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: ListEntityTypes :many
-- Ordered by label then id: labels are not unique, and a label-only order
-- reshuffles ties between calls in whatever order Postgres returns them.
SELECT * FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid
ORDER BY label, id;

-- name: CountEntitiesOfType :one
SELECT count(*) FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid;

-- name: DeleteEntityType :execrows
DELETE FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: DeleteEntitiesOfType :exec
DELETE FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid;

-- name: ListEntityFieldsOfType :many
-- The re-validation sweep reads nothing but the stored values and the id
-- to flag, so it does not pay to load whole rows — a type can hold
-- hundreds of entities and every schema edit sweeps all of them.
SELECT id, fields FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
ORDER BY id;

-- name: MarkEntitiesOfTypeInvalid :exec
-- The invalid <> flag guard keeps the sweep from rewriting rows whose
-- verdict has not changed. Without it, every schema edit would touch each
-- of the type's entities and the set_updated_at trigger would move their
-- updated_at, so a validation pass would read as an edit of content
-- nobody edited.
UPDATE entities SET invalid = sqlc.arg('invalid')::boolean
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
  AND id = ANY(sqlc.arg('ids')::uuid[])
  AND invalid <> sqlc.arg('invalid')::boolean;

-- name: GetEntityTypeByKeyForUpdate :one
-- The upsert's own read, taken inside its transaction with the row lock
-- held, so the spelling and version a caller is told about are the ones
-- their write will actually meet. Without the lock, two writers racing on
-- one key both read the same version and the second's report of "current
-- version is N" is stale by the time it is returned.
SELECT * FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text)
FOR UPDATE;
