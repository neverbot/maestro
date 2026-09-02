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
--     specifically: GetDocumentVersion and ListDocumentVersions are the
--     calls
--     where "join to the parent, then check" is one forgotten join away
--     from serving another game's prose, and the invariant must not
--     depend on anyone remembering. So the version queries carry their
--     own denormalised project_id and never join documents to establish
--     scope. InsertDocumentVersion writes it as a column rather than
--     filtering on it, and the composite key is what keeps it honest.
--     **Stated plainly, because it is checkable and was checked:** no
--     call this package offers can make that filter matter -- History
--     and ReadVersion resolve the document inside the game first, and
--     the composite key makes a version row disagreeing with its
--     document unrepresentable, so dropping the filter from either
--     query leaves the suite green. It stays for the callers that do
--     not resolve first, which Task 11's REST mirror may be. See
--     markdown.documentForVersions, which says the same thing where a
--     reader of the Go will find it.
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
-- the history is still there. TestWritingToADeletedPathResurrectsItAnd
-- ContinuesTheNumbering is what pins it: delete the clause and the
-- resurrected document reads back as still deleted.
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
-- reader has to filter. Both arms are exercised: the filtering one by
-- TestDeletingADocumentHidesItFromReadsAndKeepsItsHistory, through
-- Read, and the include_deleted one by deleteRefusal and
-- conflictAfterFailedUpsert, which have to re-read a row that may be
-- deleted in order to say why a write or a delete was refused --
-- deleteRefusal's arm by TestDeletingTwiceIsNotFoundRatherThanASecond
-- Tombstone, and conflictAfterFailedUpsert's by
-- TestACreationRacingACreateAndDeleteIsToldTheTombstone.
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
-- feature does not skip it. Task 4 landed and deletion there is soft, as
-- predicted: SoftDeleteDocument is an UPDATE, so it takes this same row
-- lock and the two serialise on it, and the resurrection the upsert then
-- performs is the documented behaviour rather than a silent
-- re-creation.)
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
--
-- deleted is a required argument rather than a column left to its
-- DEFAULT false, and that is deliberate: Delete's tombstone is the one
-- caller that passes true, and every other caller has to say false out
-- loud. A defaulted column would let a future snapshot-writing path
-- forget the question and store a live version where a tombstone
-- belonged, silently; a required argument turns the same omission into
-- a compile error. TestDeletingADocumentHidesItFromReadsAndKeepsIts
-- History reads the true case back and Task 3's
-- TestTheFirstWriteAlsoWritesVersionOneWithItsAuthorAndMessage the
-- false one.
INSERT INTO document_versions (project_id, document_id, version, title, summary, body_md,
                               frontmatter, message, deleted, author_user_id, author_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('document_id')::uuid,
        sqlc.arg('version')::integer, sqlc.arg('title')::text, sqlc.arg('summary')::text,
        sqlc.arg('body_md')::text, sqlc.arg('frontmatter')::jsonb, sqlc.arg('message')::text,
        sqlc.arg('deleted')::boolean,
        sqlc.narg('author_user_id')::uuid, sqlc.narg('author_token_id')::uuid)
RETURNING *;

-- name: SoftDeleteDocument :one
-- Guarded by the caller's expected version, exactly as UpsertDocument
-- is, so a delete cannot race an edit: the loser matches no row and gets
-- back no row, which Go turns into the typed conflict.
-- TestDeletingWithAStaleVersionIsAConflict pins the guard and
-- TestDeletingAnotherGamesDocumentIsNotFound the project filter.
--
-- deleted_at IS NULL is part of the guard, so deleting a document twice
-- is not_found on the second call rather than a second tombstone. That
-- is the honest answer: the document is already gone, and the caller has
-- nothing to do. TestDeletingTwiceIsNotFoundRatherThanASecondTombstone
-- pins it.
--
-- current_version advances here and the caller appends the matching
-- tombstone version in the same transaction. The two are one change and
-- withTx is what keeps them so; the count assertion in
-- TestDeletingADocumentHidesItFromReadsAndKeepsItsHistory is what
-- refuses to let them drift.
--
-- There is no FOR UPDATE read before this statement, unlike the write
-- path. It would buy nothing here: this UPDATE takes the row lock
-- itself, and everything the write path reads under its lock -- the
-- stored spelling, a body to echo on a conflict -- this call either does
-- not need or reads afterwards, in deleteRefusal, once it already knows
-- it was refused.
UPDATE documents
SET deleted_at          = now(),
    current_version     = current_version + 1,
    updated_by_user_id  = sqlc.narg('actor_user_id')::uuid,
    updated_by_token_id = sqlc.narg('actor_token_id')::uuid
WHERE project_id = sqlc.arg('project_id')::uuid
  AND lower(path) = lower(sqlc.arg('path')::text)
  AND deleted_at IS NULL
  AND current_version = sqlc.arg('expected_version')::integer
RETURNING *;

-- name: ListDocumentVersions :many
-- Version metadata only, newest first. **No bodies**: prose is the
-- largest payload in the system and agents are its main consumer, so a
-- history that carried them would blow a context window on the first
-- call against a document anyone has actually worked on (spec §7).
-- TestHistoryIsNewestFirstAndCarriesNoBodies pins the order and the
-- absence, the latter by reading the returned row type's own fields.
--
-- Keyset by version alone, which is unique within a document
-- (document_versions_key), so there is no tiebreak to add and the page
-- is an index scan. This is one of the two calls the file header names
-- as being one forgotten join away from serving another game's prose,
-- and it deliberately does not reach the parent document at all -- but
-- no call markdown.History offers can make this query's own project
-- filter matter (see markdown.documentForVersions): the document id
-- only ever reaches here already resolved inside the caller's own game,
-- and 0007_documents.sql's composite key makes a version row whose
-- project_id disagrees with its document's unrepresentable
-- (TestAVersionRowCannotClaimAGameItsDocumentDoesNotBelongTo forces
-- that refusal). What the project filter separates nothing for is
-- covered instead by TestAVersionIsAddressedByItsOwnDocument, the
-- document_id filter, and TestTwoGamesSharingOnePathKeepSeparateHistories
-- pins that a version row carries its own document's project id in the
-- first place.
SELECT id, project_id, document_id, version, title, summary, message, deleted,
       author_user_id, author_token_id, created_at
FROM document_versions
WHERE project_id = sqlc.arg('project_id')::uuid
  AND document_id = sqlc.arg('document_id')::uuid
  AND (sqlc.narg('after_version')::integer IS NULL
       OR version < sqlc.narg('after_version')::integer)
ORDER BY version DESC
LIMIT sqlc.arg('limit')::int;

-- name: GetDocumentVersion :one
-- The other call the file header names. Same rule: filtered by
-- project_id and document_id, never by version alone and never by a
-- join -- and the same caveat: no call markdown.ReadVersion offers can
-- make this query's own project filter matter, for the reason above
-- ListDocumentVersions. TestReadingAnotherGamesVersionIsNotFound is
-- refused one join earlier, in markdown.documentForVersions.
SELECT * FROM document_versions
WHERE project_id = sqlc.arg('project_id')::uuid
  AND document_id = sqlc.arg('document_id')::uuid
  AND version = sqlc.arg('version')::integer;

-- name: GetEntityTypeIDByKey :one
-- Link endpoints are resolved here rather than through
-- internal/metamodel's own EntityByKey, in two steps rather than one
-- join, for a reason that is about the *answer* and not about coupling:
-- a caller that typed "quesst" and a caller that typed "wanted-hoggerr"
-- have made two different mistakes at two different arguments, and one
-- flat "not found" makes an agent guess which. Two lookups let the
-- refusal name entity_type or entity_key, which is the discrimination
-- the spec wanted a whole extra wire code for.
-- TestAMistypedEntityTypeAndAMistypedEntityKeyAreToldApart pins that the
-- two misses are told apart, and the project filter here is what refuses
-- an entity type another game declared
-- (TestALinkToAnEntityInAnotherGameIsRefused).
SELECT id FROM entity_types
WHERE project_id = sqlc.arg('project_id')::uuid
  AND lower(key) = lower(sqlc.arg('key')::text);

-- name: GetEntityIDByKey :one
-- The second half of that resolution.
--
-- **This query's own project filter separates nothing markdown.resolveEntity
-- can reach, and that is stated rather than credited to a test that would
-- not go red.** entity_type_id arrives already resolved inside the
-- caller's own game (GetEntityTypeIDByKey filters on project_id), and
-- 0004_metamodel.sql gives entities a composite key to entity_types
-- carrying project_id, so an entity whose project_id disagrees with its
-- type's is unrepresentable. Verified by mutation: neutralising this
-- filter behind a no-op `OR TRUE` leaves the package green, including
-- TestALinkToAnEntityFromAnotherGamesTypeOfTheSameNameIsRefusedAtTheKey,
-- which is refused by the entity_type_id filter instead. The filter
-- stays for the same reason ListDocumentVersions' does -- a future
-- caller that obtains a type id some other way, which Task 11's REST
-- mirror may be -- and because the file header's rule is that isolation
-- is in SQL and not in whoever remembers to resolve first.
-- What the test above does pin is the *answer*: a caller naming another
-- game's entity under a type key both games declare is refused at
-- entity_key, not at entity_type and not with somebody else's row.
SELECT id FROM entities
WHERE project_id = sqlc.arg('project_id')::uuid
  AND entity_type_id = sqlc.arg('entity_type_id')::uuid
  AND lower(key) = lower(sqlc.arg('key')::text);

-- name: UpsertDocumentLink :one
-- Idempotent by (document, entity): re-adding a link updates its role
-- rather than making a second edge, which is what keeps a re-run seeding
-- script from doubling every attachment.
-- TestReAddingALinkUpdatesItsRoleRatherThanDuplicatingIt pins it.
--
-- project_id is written, not filtered, and 0007_documents.sql's two
-- composite keys are what make a cross-game link impossible by
-- construction: this row's project_id must agree with the document's
-- *and* with the entity's, so a document in one game cannot be attached
-- to an entity in another whatever Go believes.
-- TestALinkRowCannotClaimAGameItsDocumentDoesNotBelongTo forces that
-- refusal from this package.
INSERT INTO document_links (project_id, document_id, entity_id, role)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('document_id')::uuid,
        sqlc.arg('entity_id')::uuid, sqlc.arg('role')::text)
ON CONFLICT (document_id, entity_id) DO UPDATE
SET role = excluded.role
RETURNING *;

-- name: DeleteDocumentLink :execrows
-- The row count is what tells LinkRemove that there was nothing to
-- detach, which it reports as not_found rather than as silence
-- (TestRemovingALinkLeavesTheDocumentAndTheEntity).
DELETE FROM document_links
WHERE project_id = sqlc.arg('project_id')::uuid
  AND document_id = sqlc.arg('document_id')::uuid
  AND entity_id = sqlc.arg('entity_id')::uuid;

-- name: DeleteDocumentLinksExcept :exec
-- The other half of a replacing write. An empty keep list detaches
-- everything, which is exactly what `links: []` means -- and the caller
-- must pass an empty array rather than a nil one, because pgx encodes
-- nil as SQL NULL and `NOT (x = ANY(NULL))` is NULL, which deletes
-- nothing. markdown.replaceLinks says so where the slice is built.
DELETE FROM document_links
WHERE project_id = sqlc.arg('project_id')::uuid
  AND document_id = sqlc.arg('document_id')::uuid
  AND NOT (entity_id = ANY(sqlc.arg('keep')::uuid[]));

-- name: ListDocumentLinksByDocument :many
-- Ordered by the entity's type key then its key then its id: none of the
-- first two is unique on its own, and an order that can tie reshuffles
-- its ties between two identical calls.
--
-- The project filter here separates nothing that markdown.LinksByDocument
-- can reach, and this is stated rather than credited to a test that
-- would not go red: the document id only ever arrives already resolved
-- inside the caller's own game (markdown.documentForLinks), and
-- 0007_documents.sql's composite keys make a link row whose project_id
-- disagrees with its document's unrepresentable. It stays for the same
-- reason ListDocumentVersions' does -- a caller that does not resolve
-- first, which Task 11's REST mirror may be. What is pinned is the
-- document_id filter: TestALinkIsAddressedByItsOwnDocument uses two
-- documents in one game, where the project filter separates nothing.
SELECT l.role, t.key AS entity_type_key, e.key AS entity_key,
       e.name AS entity_name, e.id AS entity_id
FROM document_links l
JOIN entities e ON e.id = l.entity_id AND e.project_id = l.project_id
JOIN entity_types t ON t.id = e.entity_type_id AND t.project_id = e.project_id
WHERE l.project_id = sqlc.arg('project_id')::uuid
  AND l.document_id = sqlc.arg('document_id')::uuid
ORDER BY t.key, e.key, e.id;

-- name: ListDocumentLinksByEntity :many
-- The reverse direction, which is how the UI builds an entity page and
-- how an agent asked to rewrite the Hogger dialogue finds the document
-- from the quest instead of guessing a path.
--
-- Soft-deleted documents are excluded: an entity page listing a document
-- nobody can read would be a dead link on every quest it was attached
-- to. TestAnEntityStopsListingADocumentThatWasDeleted pins it, and pins
-- the other half of the same rule in its second act: nothing cascades on
-- a soft delete, so the link row survives and comes back with its role
-- when the document is written to again.
SELECT l.role, d.id AS document_id, d.path, d.title, d.kind
FROM document_links l
JOIN documents d ON d.id = l.document_id AND d.project_id = l.project_id
WHERE l.project_id = sqlc.arg('project_id')::uuid
  AND l.entity_id = sqlc.arg('entity_id')::uuid
  AND d.deleted_at IS NULL
ORDER BY d.path, d.id;

-- name: ListDocumentsPage :many
-- One page of a game's documents, keyset-ordered by (path, id).
--
-- **No body_md and no frontmatter in the select list.** They are the two
-- largest columns in the schema and a listing never needs either; a
-- SELECT * here would quietly put a megabyte of prose per row on a page
-- of fifty. markdown.DocumentSummary carries no field for either, which
-- is what makes the rule a fact about the type rather than a promise
-- about this select list (TestAListingRowCarriesNoBodyAtAll).
--
-- **starts_with, not LIKE, and this is a correctness choice rather than
-- a style one.** A path may legally contain `_`, which is LIKE's
-- single-character wildcard, so `path_prefix: "lore_x"` under LIKE would
-- also match `loreax` -- a filter silently answering a question the
-- caller did not ask, which is the class of defect this whole read
-- surface is most exposed to. Escaping it correctly is possible and is
-- one forgotten backslash away from the same bug.
-- TestAPathPrefixFilterDoesNotTreatUnderscoreAsAWildcard pins it. The
-- cost is that starts_with cannot use documents_listing_idx for the
-- prefix, so a prefixed listing scans the game's documents; a game has
-- hundreds of documents, not millions, and the ordering half of the
-- index still applies.
--
-- The prefix and the kind are folded, like every other path comparison
-- in this file, so `Lore/` and `lore/` name the same subtree and
-- `SCRIPT` and `script` name the same shelf
-- (TestAListingFiltersByKindAndByEntity).
--
-- The entity filter is an EXISTS rather than a join, so a document
-- attached to one entity appears once and the page's row count is the
-- number of documents rather than the number of links.
-- TestADocumentAttachedToTwoEntitiesAppearsOnceInAFilteredListing pins
-- that, with a document attached to two entities of which one is
-- filtered on; a JOIN would answer with the same row twice.
--
-- The project filter is load-bearing here, unlike the ones the header
-- describes as defence in depth: a listing resolves nothing beforehand,
-- so this filter is the only thing keeping one game's prose out of
-- another's page. TestAListingNeverCrossesGames pins it.
SELECT d.id, d.project_id, d.path, d.kind, d.title, d.summary, d.current_version,
       d.deleted_at, d.created_at, d.updated_at,
       d.created_by_user_id, d.created_by_token_id,
       d.updated_by_user_id, d.updated_by_token_id
FROM documents d
WHERE d.project_id = sqlc.arg('project_id')::uuid
  AND (sqlc.arg('include_deleted')::bool OR d.deleted_at IS NULL)
  AND (sqlc.narg('path_prefix')::text IS NULL
       OR starts_with(lower(d.path), lower(sqlc.narg('path_prefix')::text)))
  AND (sqlc.narg('kind')::text IS NULL OR lower(d.kind) = lower(sqlc.narg('kind')::text))
  AND (sqlc.narg('entity_id')::uuid IS NULL
       OR EXISTS (SELECT 1 FROM document_links l
                   WHERE l.project_id = d.project_id
                     AND l.document_id = d.id
                     AND l.entity_id = sqlc.narg('entity_id')::uuid))
  AND (sqlc.narg('after_id')::uuid IS NULL
       OR (d.path, d.id) > (sqlc.narg('after_path')::text, sqlc.narg('after_id')::uuid))
ORDER BY d.path, d.id
LIMIT sqlc.arg('limit')::int;
