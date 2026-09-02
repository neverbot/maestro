-- Every statement in this file carries the resolved project id: the
-- reads as a filter, the writes as the column they set. Isolation
-- between games is enforced in SQL, not in Go, so a query trusting a
-- path or an id alone would hand a caller another game's prose the
-- moment one leaked.
--
-- Which filters are load-bearing, stated rather than implied:
--
--   * On documents, every one of them is. A path is caller-supplied and
--     names no parent, and a document id is a value a previous answer
--     handed back, so nothing but the project filter keeps either inside
--     one game. TestReadingAnotherGamesDocumentIsNotFound pins it for
--     GetDocumentByPath; the FOR UPDATE read's filter is pinned by the
--     same test too, if indirectly: dropping it reddens
--     TestWritingAPathAnotherGameUsesCreatesASecondDocument immediately,
--     because a write to another game's path would otherwise lock and
--     resurrect the first game's row instead of creating its own.
--   * On document_versions, the project filter is load-bearing *as
--     well*, and this is the one place Maestro deliberately does not
--     follow the metamodel's reasoning. There, a query reaching entities
--     through an entity_type_id is covered by the composite foreign key
--     and the filter is defence in depth. Here 0007_documents.sql gives
--     the child tables the same composite key, so the same argument
--     would apply -- and the spec (§5) rejects it for these tables
--     specifically: read_version and history (Task 6) are the calls
--     where "join to the parent, then check" is one forgotten join away
--     from serving another game's prose, and the invariant must not
--     depend on anyone remembering. So the version queries carry their
--     own denormalised project_id and never join documents to establish
--     scope. InsertDocumentVersion writes it as a column rather than
--     filtering on it, and the composite key is what keeps it honest.
--
-- No write here sets updated_at. 0007_documents.sql puts a
-- set_updated_at trigger on documents, so the column has one mechanism
-- behind it rather than a trigger plus a clause every future query must
-- remember.

-- name: UpsertDocument :one
-- The compare-and-set, in one statement. Two writers cannot both read
-- current_version 3 and both succeed: the DO UPDATE is guarded by the
-- caller's expected version, so the loser matches no row and gets back
-- no row rather than an error, which Go turns into the typed conflict.
-- TestTwoConcurrentWritesLeaveOneWinnerAndNoGapInTheVersions pins it.
--
-- A creating caller passes expected_version 0, which no stored version
-- can equal -- current_version starts at 1 and only climbs -- so the
-- guard is a no-op on the insert path and a guaranteed mismatch when a
-- path the caller believed free turns out to be taken. That is the whole
-- mechanism behind `expected_version: 0` (spec §7), and it is why the
-- argument is required rather than inferred from absence.
--
-- The path column is deliberately not in the SET list: the first
-- spelling stored stands, so a re-seed under different casing addresses
-- the existing document rather than rewriting the handle other documents
-- and links refer to. That omission is also what lets Go refuse a
-- respelling *after* this statement runs -- the returned row still
-- carries the stored spelling -- which closes the creation race, where
-- there was nothing yet to lock.
--
-- kind is in the SET list, but not unconditionally: it is COALESCEd
-- against the stored value rather than overwritten by excluded.kind,
-- because kind is a property of the document -- what shelf it sits on --
-- and not a property of any one edit. A caller passes NULL to mean "say
-- nothing about kind", and every value including the empty string means
-- "set it to this"; SQL's COALESCE already treats a NULL argument as
-- "keep the left side" and a non-NULL one, empty string included, as
-- "replace it," which is exactly that rule. On the insert arm the same
-- COALESCE falls back to '' -- a new document's kind is never left NULL,
-- matching the NOT NULL DEFAULT '' the column already carries.
-- TestAnEditThatOmitsKindLeavesItUnchanged pins both directions.
--
-- created_by_* are not in the SET list either: the creator of a document
-- is a fact about its first version and does not change when someone
-- else edits it. updated_by_* do change, on every write.
--
-- deleted_at = NULL on the update arm is the resurrection rule (spec
-- §3): writing to a soft-deleted path brings the document back and
-- continues its version numbering, because the path is still taken and
-- the history is still there. Nothing in Task 3 can set deleted_at, so
-- that clause is exercised by no test until Task 4 lands the delete.
INSERT INTO documents (project_id, path, kind, title, summary, body_md, frontmatter,
                       current_version,
                       created_by_user_id, created_by_token_id,
                       updated_by_user_id, updated_by_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('path')::text,
        COALESCE(sqlc.narg('kind')::text, ''),
        sqlc.arg('title')::text, sqlc.arg('summary')::text, sqlc.arg('body_md')::text,
        sqlc.arg('frontmatter')::jsonb, 1,
        sqlc.narg('actor_user_id')::uuid, sqlc.narg('actor_token_id')::uuid,
        sqlc.narg('actor_user_id')::uuid, sqlc.narg('actor_token_id')::uuid)
ON CONFLICT (project_id, lower(path)) DO UPDATE
SET kind                = COALESCE(sqlc.narg('kind')::text, documents.kind),
    title               = excluded.title,
    summary             = excluded.summary,
    body_md             = excluded.body_md,
    frontmatter         = excluded.frontmatter,
    current_version     = documents.current_version + 1,
    deleted_at          = NULL,
    updated_by_user_id  = excluded.updated_by_user_id,
    updated_by_token_id = excluded.updated_by_token_id
WHERE documents.current_version = sqlc.arg('expected_version')::integer
RETURNING *;

-- name: GetDocumentByPath :one
-- The read path. A soft-deleted document is not found unless the caller
-- asked for it: deletion is soft so nothing is lost, not so that every
-- reader has to filter. (Nothing sets deleted_at before Task 4, so the
-- include_deleted arm is unexercised until then.)
SELECT * FROM documents
WHERE project_id = sqlc.arg('project_id')::uuid
  AND lower(path) = lower(sqlc.arg('path')::text)
  AND (sqlc.arg('include_deleted')::bool OR deleted_at IS NULL);

-- name: GetDocumentByPathForUpdate :one
-- FOR UPDATE, and deliberately with no deleted_at filter: a write to a
-- deleted path resurrects it, so the writer must see the row it is about
-- to bring back, and must hold it.
--
-- **What the lock buys, measured rather than asserted.** It does not buy
-- the refusal: the guarded DO UPDATE refuses on its own, and dropping
-- FOR UPDATE from this statement leaves the whole markdown suite green
-- at -count=5. Nor does it buy the freshness of the number the loser is
-- told to merge onto -- without the lock the read sees a stale version,
-- passes the Go check, and the guarded upsert then refuses and
-- conflictAfterFailedUpsert re-reads, arriving at the same fresh answer
-- by a longer route. TestTheReportedCurrentVersionIsTheOneTheWriteWould
-- HaveMet pins that answer, and it passes either way; it pins the
-- guarantee, not this clause.
--
-- What the lock does buy is the shape of the refusal rather than its
-- existence: a conflicting writer is turned away before its body, its
-- frontmatter and its version row are written and rolled back, and the
-- pre-write spelling check in writeWith runs against a row no other
-- transaction can move under it. It also serialises this write against a
-- concurrent *delete* of the same row: without the lock, a hard delete
-- landing between this read and the upsert would let the upsert's own
-- INSERT arm fire on the freed path and silently re-create the document
-- at version 1, rather than the writer being told the row is gone. All
-- three are worth a row lock on a write that is already taking one.
-- None is observable through this package's public surface, so no test
-- here distinguishes them, and this comment says so rather than naming a
-- test that would not go red. (The third case cannot fire until Task 4
-- lands deletion, and deletion there is soft, so it is not reachable
-- even then; a *hard* delete racing this read is not something Task 3
-- or 4 need close, only something worth naming so a future hard-delete
-- feature does not skip it.)
SELECT * FROM documents
WHERE project_id = sqlc.arg('project_id')::uuid
  AND lower(path) = lower(sqlc.arg('path')::text)
FOR UPDATE;

-- name: InsertDocumentVersion :one
-- The snapshot. Full bodies, never diffs (spec §3): a thousand versions
-- of a long script is a few megabytes, which is nothing beside a diff
-- chain that must be replayed to answer "show me version 12" and that
-- corrupts every later version if one link is wrong.
--
-- project_id is written from the caller's own resolved id, in the same
-- transaction as the document write, and 0007_documents.sql's composite
-- key is what keeps it honest: a version row whose project_id disagrees
-- with its document's cannot be inserted at all
-- (documents_schema_test.go pins that refusal).
INSERT INTO document_versions (project_id, document_id, version, title, summary, body_md,
                               frontmatter, message, author_user_id, author_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('document_id')::uuid,
        sqlc.arg('version')::integer, sqlc.arg('title')::text, sqlc.arg('summary')::text,
        sqlc.arg('body_md')::text, sqlc.arg('frontmatter')::jsonb, sqlc.arg('message')::text,
        sqlc.narg('author_user_id')::uuid, sqlc.narg('author_token_id')::uuid)
RETURNING *;
