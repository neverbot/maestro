-- name: UpsertEntityType :one
-- Every statement in this file filters on the resolved project id,
-- including the ones addressing a row by its primary key: isolation
-- between games is enforced in SQL, not in Go, so a query trusting an id
-- alone would hand a caller another game's row the moment an id leaked.
--
-- That is the whole mechanism only for the statements addressing
-- entity_types itself (GetEntityTypeByID, DeleteEntityType): drop the
-- filter there and a leaked id reads, or deletes, another game's type,
-- which TestTypesAreScopedToTheirProject pins. It is *not* the mechanism
-- for the three statements reaching entities through an entity_type_id
-- (ListEntityFieldsOfType, MarkEntitiesOfTypeInvalid,
-- DeleteEntitiesOfType). 0004_metamodel.sql gives entities a composite
-- FOREIGN KEY (entity_type_id, project_id) REFERENCES
-- entity_types (id, project_id), so an entity's project is already
-- determined by its type's: no row can match a type id under one project
-- id and not another, and dropping the filter from those three changes no
-- result and can be caught by no test. They keep it as defence in depth,
-- against a future schema that relaxes that composite key, and so that a
-- reader adding the next entities query copies the safe shape rather than
-- having to work out which statements happen to be covered by a
-- constraint. Written down here because a comment claiming these filters
-- are what isolates games would be relied on as if it were true.
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

-- name: UpsertEntity :one
-- The same shape as UpsertEntityType, for the same reasons, and that
-- statement's comment carries the full argument. In short:
--
--   * The DO UPDATE is guarded by the caller's expected version, so the
--     whole compare-and-set is one statement and two writers cannot both
--     read version 1 and both succeed. A creating caller passes
--     noVersion, which no stored version can equal.
--   * The key column is deliberately not in the SET list. The first
--     spelling stored stands, and the returned row therefore still
--     carries it -- which is what lets Go refuse a respelling *after* the
--     write, closing the creation-race hole.
--   * No write sets updated_at: 0004_metamodel.sql puts a set_updated_at
--     trigger on all four tables, and a clause here would be a second
--     mechanism behind one column.
--   * The audit columns are carried, so the composite
--     FOREIGN KEY (updated_by_token_id, project_id) catches a token
--     scoped to another game.
--
-- search is written by this statement and by nothing else, on both arms
-- of the upsert. It cannot be a generated column: it is derived from
-- user-declared jsonb whose *text* fields are the only ones worth
-- indexing, and which of a row's fields those are is known only to the
-- Go validator that has just read the type's schema. So every write path
-- that changes name or fields must come through here, or the row stays
-- indexed under its previous words and a search stops finding it with
-- nothing to signal why.
--
-- invalid is reset to false because the caller has just validated these
-- values against the type's current schema; a row that is being written
-- is a row that has been judged.
INSERT INTO entities (project_id, entity_type_id, key, name, fields, search,
                      updated_by_user_id, updated_by_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('entity_type_id')::uuid,
        sqlc.arg('key')::text, sqlc.arg('name')::text, sqlc.arg('fields')::jsonb,
        to_tsvector('simple', sqlc.arg('name')::text || ' ' || sqlc.arg('search_text')::text),
        sqlc.narg('updated_by_user_id')::uuid, sqlc.narg('updated_by_token_id')::uuid)
ON CONFLICT (project_id, entity_type_id, lower(key)) DO UPDATE
SET name                = excluded.name,
    fields              = excluded.fields,
    search              = excluded.search,
    invalid             = false,
    version             = entities.version + 1,
    updated_by_user_id  = excluded.updated_by_user_id,
    updated_by_token_id = excluded.updated_by_token_id
WHERE entities.version = sqlc.arg('expected_version')::integer
RETURNING *;

-- name: GetEntityByKey :one
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
  AND lower(key) = lower(sqlc.arg('key')::text);

-- name: GetEntityByKeyForUpdate :one
-- FOR UPDATE, for the reason GetEntityTypeByKeyForUpdate records: without
-- the lock the read runs against the transaction's snapshot, so a caller
-- racing an in-flight edit is told to merge onto a version that is
-- already stale by the time it retries, and retries into the same refusal
-- forever. What the lock buys is not the refusal -- the guarded DO UPDATE
-- refuses on its own -- but the *number* the caller is told to merge onto.
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
  AND lower(key) = lower(sqlc.arg('key')::text)
FOR UPDATE;

-- name: GetEntityByID :one
SELECT * FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: DeleteEntity :execrows
DELETE FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;
