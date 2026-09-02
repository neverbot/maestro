-- name: UpsertEntityType :one
-- Every statement in this file carries the resolved project id -- the
-- reads and deletes as a filter, including the ones addressing a row by
-- its primary key, and the upserts as the column they write. Isolation
-- between games is enforced in SQL, not in Go, so a query trusting an id
-- alone would hand a caller another game's row the moment an id leaked.
--
-- That is the whole mechanism only for the statements addressing a row
-- by its own primary key: GetEntityTypeByID and DeleteEntityType, which
-- TestTypesAreScopedToTheirProject pins, and GetEntityByID and
-- DeleteEntity, which
-- TestTheEntityQueriesThatAddressARowByIDAreScopedToTheProject pins.
-- Drop the filter from any of the four and a leaked id reads, or
-- deletes, another game's row. The entity pair needed a test of its own
-- against the queries: their only caller is RemoveEntity, which reads
-- the row, then its type, then deletes, so each of its three filters
-- masks the others and only mutating all three at once is observable
-- through the service.
--
-- It is *not* the mechanism for the statements reaching entities through
-- an entity_type_id (ListEntityFieldsOfType, MarkEntitiesOfTypeInvalid,
-- DeleteEntitiesOfType, GetEntityByKey, GetEntityByKeyForUpdate).
-- 0004_metamodel.sql gives entities a composite
-- FOREIGN KEY (entity_type_id, project_id) REFERENCES
-- entity_types (id, project_id), so an entity's project is already
-- determined by its type's: no row can match a type id under one project
-- id and not another, and dropping the filter from any of those five
-- changes no result and can be caught by no test. They keep it as
-- defence in depth, against a future schema that relaxes that composite
-- key, and so that a
-- reader adding the next entities query copies the safe shape rather than
-- having to work out which statements happen to be covered by a
-- constraint. Written down here because a comment claiming these filters
-- are what isolates games would be relied on as if it were true.
--
-- UpsertEntity is deliberately not in that list, and is not unobservable
-- either: it has no project filter at all. Its project_id is a NOT NULL
-- column value it writes and part of its ON CONFLICT
-- (project_id, entity_type_id, lower(key)) target, so removing it is a
-- syntax or constraint error rather than a silently widened query --
-- nothing about it can be dropped and still compile.
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

-- name: LockEndpointEntityTypes :many
-- Reads the entity types an endpoint rule names, and holds a share lock
-- on each until the reading transaction ends.
--
-- The read is what UpsertRelationType checks its endpoint lists against;
-- the lock is what stops RemoveEntityType from deleting one of them
-- underneath a relation type that is being created. Without it the
-- creation path had no row for PruneEntityTypeFromEndpointLists to find
-- and took no lock of its own, so a relation type created between the
-- prune's statement and the removal's commit kept a dangling id. FOR
-- SHARE and not FOR UPDATE: two writers may name the same entity type at
-- once, and only a DELETE of it has to wait.
--
-- **Lock order is load-bearing.** UpsertRelationType takes this lock
-- before the FOR UPDATE on its own relation_types row, and
-- RemoveEntityType deletes the entity type before pruning the relation
-- types; both therefore take entity_types first, and neither can hold
-- what the other is waiting for. Moving this call after the relation
-- type's row lock reintroduces a deadlock between the two.
SELECT id FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid
  AND id = ANY (sqlc.arg('ids')::uuid[])
FOR SHARE;

-- name: PruneEntityTypeFromEndpointLists :exec
-- Removes a deleted entity type's id from every relation type's endpoint
-- lists. source_type_ids and target_type_ids are plain uuid[]: Postgres
-- has no foreign key from an array element, so nothing but this
-- statement keeps them from outliving the type they name. It runs in the
-- same transaction as the type's own delete.
UPDATE relation_types
SET source_type_ids = array_remove(source_type_ids, sqlc.arg('entity_type_id')::uuid),
    target_type_ids = array_remove(target_type_ids, sqlc.arg('entity_type_id')::uuid)
WHERE project_id = sqlc.arg('project_id')::uuid
  AND sqlc.arg('entity_type_id')::uuid = ANY (source_type_ids || target_type_ids);

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
-- refuses on its own -- but the *number* the caller is told to merge
-- onto. TestTheReportedCurrentEntityVersionIsTheOneTheWriteWouldHaveMet
-- pins exactly that, because nothing else here does: both entity race
-- tests block on the unique index inside the INSERT, not on this lock.
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

-- name: UpsertRelationType :one
-- Guarded, audited and trigger-owned exactly as UpsertEntityType is; see
-- that statement's comment for the full argument. In short: the DO UPDATE
-- carries the caller's expected version so the compare-and-set is one
-- statement, no write sets updated_at because the set_updated_at trigger
-- owns it, and key is not in the SET list, so the stored spelling stands
-- and the returned row is what lets Go refuse a respelling after the
-- write.
INSERT INTO relation_types (project_id, key, label, description,
                            source_type_ids, target_type_ids, semantic_role, field_schema,
                            updated_by_user_id, updated_by_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('key')::text, sqlc.arg('label')::text,
        sqlc.arg('description')::text, sqlc.arg('source_type_ids')::uuid[],
        sqlc.arg('target_type_ids')::uuid[], sqlc.narg('semantic_role')::text,
        sqlc.arg('field_schema')::jsonb,
        sqlc.narg('updated_by_user_id')::uuid, sqlc.narg('updated_by_token_id')::uuid)
ON CONFLICT (project_id, lower(key)) DO UPDATE
SET label               = excluded.label,
    description         = excluded.description,
    source_type_ids     = excluded.source_type_ids,
    target_type_ids     = excluded.target_type_ids,
    semantic_role       = excluded.semantic_role,
    field_schema        = excluded.field_schema,
    version             = relation_types.version + 1,
    updated_by_user_id  = excluded.updated_by_user_id,
    updated_by_token_id = excluded.updated_by_token_id
WHERE relation_types.version = sqlc.arg('expected_version')::integer
RETURNING *;

-- name: GetRelationTypeByKey :one
SELECT * FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text);

-- name: GetRelationTypeByKeyForUpdate :one
-- FOR UPDATE, for the reason GetEntityTypeByKeyForUpdate records: the
-- lock is what makes the reported current version the one this caller's
-- own write would have met, so "re-read and retry with 2" is advice that
-- works.
SELECT * FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text)
FOR UPDATE;

-- name: GetRelationTypeByID :one
SELECT * FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: ListRelationTypes :many
-- Ordered by label then id: labels are not unique, and a label-only
-- order reshuffles ties between calls.
SELECT * FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid ORDER BY label, id;

-- name: CountRelationsOfType :one
SELECT count(*) FROM relations
WHERE project_id = sqlc.arg('project_id')::uuid
  AND relation_type_id = sqlc.arg('relation_type_id')::uuid;

-- name: DeleteRelationsOfType :exec
DELETE FROM relations
WHERE project_id = sqlc.arg('project_id')::uuid
  AND relation_type_id = sqlc.arg('relation_type_id')::uuid;

-- name: DeleteRelationType :execrows
DELETE FROM relation_types
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: UpsertRelation :one
-- No version guard, because relations carry no version column: an edge
-- is identified by (type, source, target) and re-writing its fields is
-- the operation, not a lost update. Two writers editing one edge's
-- fields therefore both succeed and the last one wins, which is this
-- table's documented concurrency behaviour and not an oversight -- see
-- RelationInput.
--
-- The ON CONFLICT target is relations_edge_key, so a re-seed of the same
-- edge updates it rather than laying a second copy beside it. That is
-- also why a game cannot hold two edges of one type between one ordered
-- pair; UpsertRelation's doc comment says what to do when it genuinely
-- needs to.
--
-- No updated_at either -- the set_updated_at trigger owns that column on
-- all four tables. project_id is the column this statement writes rather
-- than a filter it applies, exactly as in UpsertEntity, and the
-- composite foreign keys on the type and both endpoints are what keep an
-- edge inside one game.
INSERT INTO relations (project_id, relation_type_id, source_id, target_id, fields,
                       updated_by_user_id, updated_by_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('relation_type_id')::uuid,
        sqlc.arg('source_id')::uuid, sqlc.arg('target_id')::uuid, sqlc.arg('fields')::jsonb,
        sqlc.narg('updated_by_user_id')::uuid, sqlc.narg('updated_by_token_id')::uuid)
ON CONFLICT (relation_type_id, source_id, target_id) DO UPDATE
SET fields              = excluded.fields,
    updated_by_user_id  = excluded.updated_by_user_id,
    updated_by_token_id = excluded.updated_by_token_id
RETURNING *;

-- name: ListRelations :many
-- Every filter but the project is optional, and the project one is what
-- keeps a leaked entity id from listing another game's edges: source_id
-- and target_id are caller-supplied here, unlike the entity statements
-- whose composite key already determines their project.
SELECT r.* FROM relations r
WHERE r.project_id = sqlc.arg('project_id')::uuid
  AND (sqlc.narg('relation_type_id')::uuid IS NULL OR r.relation_type_id = sqlc.narg('relation_type_id')::uuid)
  AND (sqlc.narg('source_id')::uuid IS NULL OR r.source_id = sqlc.narg('source_id')::uuid)
  AND (sqlc.narg('target_id')::uuid IS NULL OR r.target_id = sqlc.narg('target_id')::uuid)
ORDER BY r.created_at, r.id
LIMIT sqlc.arg('limit')::int;

-- name: GetRelationByID :one
SELECT * FROM relations
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: DeleteRelation :execrows
DELETE FROM relations
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;
