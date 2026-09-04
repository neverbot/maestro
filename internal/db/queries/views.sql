-- Every statement in this file carries the resolved project id: the
-- reads and the delete as a filter, the writes as the column they set.
-- Isolation between games is enforced in SQL, not in Go, so a query
-- trusting a key or an id alone would hand a caller another game's view
-- the moment one leaked.
--
-- Which filters are load-bearing, stated rather than implied, because
-- 0008_views.sql's composite foreign keys cover some of these tables and
-- a comment claiming every filter here is what isolates games would be
-- relied on as if it were true:
--
--   * On views, every one of them is. A key is caller-supplied and names
--     no parent, and a view id is a value a previous answer handed back,
--     so nothing but the project filter keeps either inside one game.
--     That includes GetViewByID: a view id in a caller's hand is not
--     authority to read it. TestReadingAnotherGamesViewIsNotFound pins
--     both, by key and by id, with a positive control in the same test.
--   * On view_refs, the filter is defence in depth *for this package's
--     own callers* and is load-bearing for anyone else's, which is a
--     narrower claim than the one this comment first made and is the
--     honest one. 0008_views.sql gives the table a composite
--     FOREIGN KEY (view_id, project_id) REFERENCES views (id, project_id),
--     so no ref row can hold a view id under one project id and a
--     different project id of its own; and UpsertView, the only caller
--     that writes here, has just written the view row in the same
--     transaction. So dropping the filter changes no answer any service
--     call can produce. It does not follow that nothing can observe it:
--     the composite key constrains what a row may *hold*, not which rows
--     a DELETE may *match*, so DeleteViewRefs called with a foreign view
--     id and this game's project id would clear another game's index.
--     TestTheViewQueriesAddressingARowByIdAreScopedToTheProject drives
--     these statements directly, because that is the only way to observe
--     a filter every service path has already made redundant — a
--     defence-in-depth claim is worth what the test behind it is worth.
--     InsertViewRef writes project_id as a column value, which is what
--     makes the composite key check anything at all.
--   * On ListViewsBrokenByType, load-bearing again, and for its own
--     caller rather than for a hypothetical one: it is handed a bare
--     type id. Its two project filters mask each other and only their
--     joint removal is observable; the statement's own comment says so
--     and names the test that asks the question across the games.
--
-- No write here sets updated_at. 0008_views.sql puts a set_updated_at
-- trigger on views, so the column has one mechanism behind it rather
-- than a trigger plus a clause every future query must remember.

-- name: UpsertView :one
-- The compare-and-set, in one statement, exactly as UpsertEntityType and
-- UpsertDocument do it; those statements' comments carry the full
-- argument and it is not restated here. In short: the DO UPDATE is
-- guarded by the caller's expected version, so two writers cannot both
-- read version 1 and both succeed, and a guard that matches nothing
-- returns no row rather than an error, which Go turns into the typed
-- conflict. A creating caller passes noVersion (-1), which no stored
-- version can equal.
--
-- **key is deliberately not in the SET list**, for the reason
-- UpsertEntityType gives: the first spelling stored stands, so a re-seed
-- cannot rewrite the handle a designer bookmarks, and the row this
-- statement returns still carries the stored spelling — which is what
-- lets Go refuse a respelling *after* the write, closing the
-- creation-races-a-creation hole its locked read cannot see.
--
-- **The three background columns are not in the SET list either, and
-- that is a decision rather than an omission.** A background is written
-- by views.set_background (the views plan, Task 14) and belongs to the
-- picture rather than to the query; listing them here would make every
-- ordinary edit of a query or a renderer parameter silently detach the
-- world map a designer put behind it, because a caller that says nothing
-- about a background would be saying "none". The same distinction
-- markdown's WriteInput.Links draws with a pointer, drawn here by the
-- column simply not being writable from this path.
-- TestAnOrdinaryUpsertLeavesABackgroundStanding writes a background
-- directly and edits the view through this statement, which is what
-- makes the decision above an assertion rather than a paragraph.
INSERT INTO views (project_id, key, name, description, query, renderer, renderer_params,
                   layout_mode, layout_seed, updated_by_user_id, updated_by_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('key')::text, sqlc.arg('name')::text,
        sqlc.arg('description')::text, sqlc.arg('query')::jsonb,
        sqlc.arg('renderer')::text, sqlc.arg('renderer_params')::jsonb,
        sqlc.arg('layout_mode')::text, sqlc.arg('layout_seed')::integer,
        sqlc.narg('updated_by_user_id')::uuid, sqlc.narg('updated_by_token_id')::uuid)
ON CONFLICT (project_id, lower(key)) DO UPDATE
SET name                = excluded.name,
    description         = excluded.description,
    query               = excluded.query,
    renderer            = excluded.renderer,
    renderer_params     = excluded.renderer_params,
    layout_mode         = excluded.layout_mode,
    layout_seed         = excluded.layout_seed,
    version             = views.version + 1,
    updated_by_user_id  = excluded.updated_by_user_id,
    updated_by_token_id = excluded.updated_by_token_id
WHERE views.version = sqlc.arg('expected_version')::integer
RETURNING *;

-- name: GetViewByKey :one
-- Matched without regard to case, through views_key_key
-- (project_id, lower(key)), so a caller that reads with one spelling and
-- writes with another meets one row rather than two.
SELECT * FROM views
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text);

-- name: GetViewByKeyForUpdate :one
-- The upsert's own read, taken inside its transaction with the row lock
-- held, so the spelling and the version a caller is told about are the
-- ones its own write will actually meet. Without the lock the read runs
-- against the transaction's snapshot, and a caller racing an in-flight
-- edit is told to merge onto a version that is already stale by the time
-- it retries. What the lock buys is not the refusal — the guarded DO
-- UPDATE refuses on its own — but the *number* the caller is told to
-- merge onto: TestTheReportedCurrentViewVersionIsTheOneTheWriteWouldHaveMet
-- pins exactly that.
SELECT * FROM views
WHERE project_id = sqlc.arg('project_id')::uuid AND lower(key) = lower(sqlc.arg('key')::text)
FOR UPDATE;

-- name: GetViewByID :one
-- The project filter is the whole mechanism here: an id is a value a
-- previous answer handed back and names no parent that could scope this
-- statement. Drop it and a leaked id reads another game's view.
SELECT * FROM views
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: ListViewsPage :many
-- One page of a game's saved views, keyed on (name, id).
--
-- views_project_idx, added in 0008, is (project_id, name, id): this
-- statement's filter and its whole sort key, so the position a cursor
-- carries is sought to rather than arrived at by sorting the game.
--
-- **The project filter is load-bearing.** The after_name/after_id
-- position is caller-supplied and names no parent whose composite key
-- could scope this statement, and the renderer filter is free text off
-- the same caller. Drop the project filter and a caller pages another
-- game's rows.
--
-- The keyset compares uuid to uuid rather than casting id to text, and
-- `id` is in the ORDER BY as well as in the comparison, for the reasons
-- ListEntitiesPage's own comment sets out at length: a keyset whose
-- comparison disagrees with its own sort order skips or repeats rows at
-- a page boundary and says nothing, and view names are as duplicable as
-- entity names. after_id alone guards the clause, and after_name is read
-- only when it is set, because a row comparison against a NULL half
-- yields NULL, which reads as false and would answer with an empty page
-- rather than a refusal.
--
-- **`id` in the ORDER BY is not observable by a test here, and that is
-- measured rather than assumed.** internal/metamodel found dropping it
-- from ListEntitiesPage red the moment a fixture shared one name; the
-- same mutation on this statement, against seven views sharing one name
-- paged three at a time, leaves the whole package green — views_project_idx
-- is (project_id, name, id), so the index scan that serves this
-- statement already returns tied rows in id order and the clause asks
-- for what the plan was doing anyway. What that means is that the
-- *index* is the guard today and the clause is what keeps the statement
-- correct without it: a plan that sorts instead of scanning the index
-- (a dropped index, a different planner) orders ties arbitrarily, the
-- comparison below lands nowhere near where the previous page stopped,
-- and a paged walk starts skipping and repeating rows with nothing to
-- signal it. It stays for that, not for a test.
SELECT * FROM views
WHERE project_id = sqlc.arg('project_id')::uuid
  AND (sqlc.narg('renderer')::text IS NULL OR renderer = sqlc.narg('renderer')::text)
  AND (sqlc.narg('after_id')::uuid IS NULL
       OR (name, id) > (sqlc.narg('after_name')::text, sqlc.narg('after_id')::uuid))
ORDER BY name, id
LIMIT sqlc.arg('limit')::int;

-- name: DeleteView :execrows
-- The positions and the refs go with it: both key into views with
-- ON DELETE CASCADE (0008_views.sql), so this is one statement rather
-- than three. execrows, not exec, because zero rows is the answer to
-- "was it there" — a caller that resolved the view a moment earlier and
-- deletes nothing raced another remover, and hears not_found instead of
-- a success it can publish an event about.
DELETE FROM views
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: DeleteViewRefs :exec
-- Half of the rewrite an upsert performs; InsertViewRef is the other.
-- Both run inside the upsert's own transaction, because refs that are
-- rewritten outside it drift from the query they index, which is the one
-- thing they exist not to do.
DELETE FROM view_refs
WHERE project_id = sqlc.arg('project_id')::uuid AND view_id = sqlc.arg('view_id')::uuid;

-- name: InsertViewRef :exec
-- One reference from a query to a declared type, at the JSON pointer
-- that addresses it.
--
-- **A plain INSERT, with no ON CONFLICT arm, deliberately.** A pointer
-- addresses exactly one position in one document, so two rows for one
-- pointer would be a rewrite that did not delete the first;
-- view_refs_key is UNIQUE (view_id, pointer) and 0008_views.sql records
-- that the index exists to make that a refusal rather than a silent
-- duplicate. Swallowing it here would put the silence back.
--
-- kind says which of the two id columns the row uses and the table's own
-- CHECK refuses a row that claims one kind and carries the other's id,
-- so Go passes a null for the column the kind does not name.
INSERT INTO view_refs (view_id, project_id, kind, ref_key, entity_type_id,
                       relation_type_id, pointer)
VALUES (sqlc.arg('view_id')::uuid, sqlc.arg('project_id')::uuid, sqlc.arg('kind')::text,
        sqlc.arg('ref_key')::text, sqlc.narg('entity_type_id')::uuid,
        sqlc.narg('relation_type_id')::uuid, sqlc.arg('pointer')::text);

-- name: ListViewRefs :many
-- One view's dependency list, in document order. The sort is the pointer
-- rather than the insertion order because a table has no insertion
-- order, and a staleness report that listed one query's broken
-- references in an order Postgres chose would move about between calls.
SELECT * FROM view_refs
WHERE project_id = sqlc.arg('project_id')::uuid AND view_id = sqlc.arg('view_id')::uuid
ORDER BY pointer;

-- name: ListViewsBrokenByType :many
-- "Which views does this type hold up, and where in each query" — the
-- lookup ON DELETE SET NULL on view_refs exists to serve, and the reason
-- that column is nullable rather than cascading.
--
-- **It is asked with the type's id, so it must be asked before the type
-- is deleted**, inside the deleting transaction: the SET NULL is what
-- makes the row survive its type, and it also empties the column this
-- statement matches on. Asking afterwards finds nothing and reports that
-- nothing broke, which is the wrong answer rather than an error. Task 12
-- is the caller that reports the list in a deletion's answer.
--
-- One of the two arguments is set and the other is null: an entity type
-- and a relation type are different vocabularies and a call naming both
-- would be asking two questions. view_refs_entity_type_idx and
-- view_refs_relation_type_idx are (project_id, entity_type_id) and
-- (project_id, relation_type_id), so each arm is an index lookup rather
-- than a scan over every stored query document, which is the whole point
-- of the table.
--
-- **Two project filters, and they mask each other.** The join equates
-- v.project_id with r.project_id, so each filter implies the other and
-- deleting either one alone changes no answer and leaves the whole
-- package green. Removing both does not: the caller supplies the type id
-- directly and 0008_views.sql's composite foreign keys do not defend
-- this one — a ref row legitimately holds another game's type id under
-- that game's project id — so with neither filter the statement does not
-- care who is asking, and one game asking about another's type is handed
-- that game's views. Either filter alone is therefore sufficient and
-- neither is individually necessary; what is load-bearing is that at
-- least one survives, which is why both are written and this note exists
-- rather than a claim that each is doing its own work.
--
-- **This is not defence in depth**, unlike the view_refs filters above:
-- views.ViewsDependingOn takes a bare type id and resolves nothing
-- inside the game first, so the isolation this statement enforces is the
-- only isolation its own caller has.
-- TestViewsDependingOnATypeAreFoundByIdWithTheirPointers asks the other
-- game about this game's type id, on both arms, with the positive
-- control in the same test — the discrimination in the rest of that test
-- comes from the two games' type ids differing, which passes either way.
SELECT v.id, v.key, v.name, r.kind, r.ref_key, r.pointer
FROM view_refs r
JOIN views v ON v.id = r.view_id AND v.project_id = r.project_id
WHERE r.project_id = sqlc.arg('project_id')::uuid
  AND v.project_id = sqlc.arg('project_id')::uuid
  AND ((sqlc.narg('entity_type_id')::uuid IS NOT NULL
        AND r.entity_type_id = sqlc.narg('entity_type_id')::uuid)
    OR (sqlc.narg('relation_type_id')::uuid IS NOT NULL
        AND r.relation_type_id = sqlc.narg('relation_type_id')::uuid))
ORDER BY v.name, v.id, r.pointer;
