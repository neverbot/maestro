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
--   * On view_positions, load-bearing on the read and on both deletes,
--     for the same reason as view_refs and with the same caveat: a view
--     id is a value a previous answer handed back, and the composite
--     foreign key says what a row may hold rather than which rows a read
--     or a DELETE may match. UpsertViewPosition is the interesting one
--     and it needs *two* mechanisms: the composite keys refuse a parent
--     from another game on the insert path, and they check nothing at
--     all on the ON CONFLICT path, where the stored row keeps its own
--     project id -- so the DO UPDATE carries a project guard of its own.
--     That is written up on the statement, because it was a real
--     cross-game overwrite before it was a comment. Each statement here
--     says which of its filters is doing work and which is redundant
--     with the one above it.
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

-- name: UpsertViewPosition :execrows
-- One node's coordinates in one view, written by views.set_positions.
--
-- ON CONFLICT rather than delete-then-insert: a drag moves a node that
-- is already placed, which is the common call, and the primary key
-- (view_id, entity_id) is exactly the identity of "this node in this
-- view". updated_at is not in the SET list because 0008_views.sql puts a
-- set_updated_at trigger on the table, the same rule the rest of this
-- file follows.
--
-- **The isolation takes two mechanisms here, and the first one alone is
-- not enough -- measured, not reasoned about.** On the insert path there
-- is nothing to filter and the guard is 0008_views.sql's two composite
-- foreign keys, (view_id, project_id) into views and
-- (entity_id, project_id) into entities: a parent from another game has
-- no matching row and the write raises 23503. On the **conflict** path
-- those keys check nothing at all. The conflict target is
-- (view_id, entity_id), project_id is not in the SET list, so the stored
-- row keeps its own project id, every foreign key stays satisfied, and a
-- caller passing another game's view id with its own project id
-- *silently overwrote that game's coordinates*. Seen: the first version
-- of this statement did exactly that, and the test that was meant to
-- catch it passed because it asserted the row count rather than the
-- coordinates.
--
-- So the DO UPDATE is guarded on the stored row's own project id, and
-- the statement is execrows rather than exec: a guard that matches
-- nothing updates nothing and reports zero, which Go turns into a
-- refusal (positions.go) instead of the silent success a bare guard
-- would give. TestPositionsOfAnotherGameAreNotReachable drives both
-- paths -- an entity with no stored position for the 23503, one with a
-- stored position for the guard -- and reads the coordinates back.
INSERT INTO view_positions (view_id, entity_id, project_id, x, y, pinned)
VALUES (sqlc.arg('view_id')::uuid, sqlc.arg('entity_id')::uuid,
        sqlc.arg('project_id')::uuid, sqlc.arg('x')::double precision,
        sqlc.arg('y')::double precision, sqlc.arg('pinned')::boolean)
ON CONFLICT (view_id, entity_id) DO UPDATE
SET x = excluded.x, y = excluded.y, pinned = excluded.pinned
WHERE view_positions.project_id = excluded.project_id;

-- name: ListViewPositions :many
-- One view's stored positions, addressed the way they were written: by
-- entity key and type key, never by id, so a caller that has never read
-- this game can act on the answer.
--
-- **p.project_id is the load-bearing filter and the other two are not.**
-- A view id is a value a previous answer handed back, so nothing but
-- that filter keeps this read inside one game; dropping it hands another
-- game's positions to a caller holding a leaked view id, which
-- TestPositionsOfAnotherGameAreNotReachable asks for directly. The two
-- join conditions -- e.project_id = p.project_id and
-- et.project_id = e.project_id -- are implied by 0008_views.sql's and
-- 0004_metamodel.sql's composite foreign keys and by the primary keys
-- they join on, so each is redundant with the filter above it and
-- deleting either alone changes no answer. They are written because a
-- join to a project-scoped table that does not carry the scope is the
-- shape this repository has lost a filter to six times, not because a
-- test can tell them apart.
SELECT et.key AS entity_type, e.key AS entity_key, p.x, p.y, p.pinned, p.updated_at
FROM view_positions p
JOIN entities e ON e.id = p.entity_id AND e.project_id = p.project_id
JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = e.project_id
WHERE p.project_id = sqlc.arg('project_id')::uuid
  AND p.view_id = sqlc.arg('view_id')::uuid
ORDER BY et.key, e.key;

-- name: DeleteViewPositions :execrows
-- Every position of one view, for a views.clear_positions call that
-- named no entities.
--
-- Both filters are load-bearing, and for the reason DeleteViewRefs
-- records: a composite foreign key constrains what a row may *hold*, not
-- which rows a DELETE may *match*, so without project_id a caller
-- holding another game's view id would clear that game's arrangement.
-- execrows because the count is the answer this call gives back -- a
-- designer clearing a view is told how many placements went.
DELETE FROM view_positions
WHERE project_id = sqlc.arg('project_id')::uuid
  AND view_id = sqlc.arg('view_id')::uuid;

-- name: DeleteViewPosition :execrows
-- One named node's position. Zero rows is a normal answer: an entity
-- that was never dragged has no row, and clearing it is not an error.
-- The project filter is load-bearing exactly as it is above.
DELETE FROM view_positions
WHERE project_id = sqlc.arg('project_id')::uuid
  AND view_id = sqlc.arg('view_id')::uuid
  AND entity_id = sqlc.arg('entity_id')::uuid;

-- name: InsertViewAsset :one
-- One background image, stored whole.
--
-- **An insert, with no ON CONFLICT arm and no update path at all.** An
-- asset's bytes never change: a new image is a new asset, which is what
-- lets the serving route hand out a long Cache-Control (assets.go) and
-- what makes "this view's background is asset X" a stable claim. There
-- is nothing to conflict on either -- assets have no key, only an id --
-- so the isolation here is the column value: project_id is written from
-- the caller's resolved project and every read below filters on it.
--
-- The bytes go in as bytea and are bounded in Go, not here: a length
-- CHECK refuses the row only after Postgres has received and decoded
-- every byte, which is the cost the bound exists to avoid
-- (0008_views.sql says the same where the table is declared).
--
-- mime, width and height are the *sniffed* mime and the *decoded*
-- dimensions -- never the caller's Content-Type, filename or arguments;
-- assets.go is where that is decided and TestTheMimeIsSniffedNotTrusted
-- is what pins it. The table's CHECK on mime is a closed list that does
-- not include SVG, and is the backstop for a write path that does not
-- come through assets.go.
--
-- The returned row deliberately does not carry bytes: this statement's
-- caller has just handed those bytes in and every other reader of this
-- table but GetViewAsset avoids them, because a listing that hauled
-- megabytes per row would be a listing nobody could call.
INSERT INTO view_assets (project_id, filename, mime, width, height, bytes,
                         created_by_user_id, created_by_token_id)
VALUES (sqlc.arg('project_id')::uuid, sqlc.arg('filename')::text, sqlc.arg('mime')::text,
        sqlc.arg('width')::integer, sqlc.arg('height')::integer, sqlc.arg('bytes')::bytea,
        sqlc.narg('created_by_user_id')::uuid, sqlc.narg('created_by_token_id')::uuid)
RETURNING id, project_id, filename, mime, width, height, created_at,
          created_by_user_id, created_by_token_id;

-- name: ListViewAssets :many
-- Every asset of one game, newest last, in view_assets_project_idx's own
-- order (project_id, created_at, id) so the index serves the sort.
--
-- **The project filter is the whole mechanism.** An asset has no key and
-- names no parent, so nothing else scopes this read; without it a
-- caller lists every game's images. TestAssetsOfAnotherGameAreNotListed
-- asks for it directly, with a positive control in the same test.
--
-- **bytes is not selected, deliberately.** The cap is 8 MB an asset, so
-- a game with forty of them would answer this call with 320 MB read out
-- of the database, marshalled and thrown away by every caller but the
-- serving route. width, height and mime are what a picker needs.
SELECT id, project_id, filename, mime, width, height, created_at,
       created_by_user_id, created_by_token_id
FROM view_assets
WHERE project_id = sqlc.arg('project_id')::uuid
ORDER BY created_at, id;

-- name: GetViewAsset :one
-- One asset with its bytes, for the serving route and for nothing else.
--
-- The project filter is load-bearing for the reason GetViewByID's is: an
-- id is a value a previous answer handed back and names no parent, so a
-- leaked asset id would otherwise serve another game's world map to
-- anyone holding it. TestAnAssetOfAnotherGameIsNotServed pins it.
SELECT * FROM view_assets
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: GetViewAssetMeta :one
-- The same row without its bytes, for the callers that only need to know
-- the asset exists inside this game: SetBackground's own lookup, and the
-- REST layer's after-delete check. Separate from GetViewAsset rather
-- than a column list chosen in Go, because sqlc decides a statement's
-- columns and a caller that "just ignores" a bytea has still read it.
SELECT id, project_id, filename, mime, width, height, created_at,
       created_by_user_id, created_by_token_id
FROM view_assets
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: ClearBackgroundKnobsForAsset :exec
-- Reset the scale and the offset of every view backed by one asset, run
-- immediately before that asset is deleted.
--
-- **background_asset_id is deliberately not in this SET list**: nulling
-- it is 0008_views.sql's ON DELETE SET NULL job, and doing it here as
-- well would make the constraint unobservable through this package --
-- the placement of that action is exactly the kind of thing only a
-- constraint pins, and a Go statement that quietly did the same work
-- would leave a dropped SET NULL green.
-- TestDeletingAnAssetNullsTheBackgroundOfEveryViewUsingIt asserts the
-- null the constraint writes and the two defaults this statement writes,
-- which is why the division of labour is legible from the test.
--
-- What it is for: a scale and an offset with no image behind them are
-- values nothing reads, which is what SetBackground refuses to store in
-- the first place. A deletion must not be able to create the state the
-- setter refuses.
--
-- Both filters are load-bearing. project_id scopes the statement -- an
-- asset id is a value a previous answer handed back -- and
-- background_asset_id is what makes this one asset's views rather than
-- the whole game's.
UPDATE views
SET background_scale = 1, background_offset = '{"x":0,"y":0}'::jsonb
WHERE project_id = sqlc.arg('project_id')::uuid
  AND background_asset_id = sqlc.arg('background_asset_id')::uuid;

-- name: DeleteViewAsset :execrows
-- One asset. Every view pointing at it keeps its row and loses its
-- background: 0008_views.sql's composite FOREIGN KEY carries
-- ON DELETE SET NULL (background_asset_id), so the picture goes and the
-- view does not.
--
-- execrows because zero rows is the answer to "was it there": a caller
-- that resolved the asset a moment earlier and deleted nothing raced
-- another remover, and hears not_found rather than a success.
--
-- The project filter is load-bearing exactly as GetViewAsset's is.
DELETE FROM view_assets
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;

-- name: SetViewBackground :execrows
-- The three background columns of one view, and the only statement that
-- writes them.
--
-- UpsertView deliberately leaves them out of its own SET list, so that
-- an ordinary edit of a query or a renderer parameter cannot silently
-- detach the world map a designer put behind it; this is the other half
-- of that decision. It writes all three together and never one of them,
-- because a scale without an image and an image without a scale are both
-- half a background.
--
-- **Two mechanisms keep the asset inside the game, and neither is
-- redundant.** The project filter scopes which *view* is written -- a
-- view id is a value a previous answer handed back, so without it a
-- caller repaints another game's picture. 0008_views.sql's composite
-- FOREIGN KEY (background_asset_id, project_id) into view_assets is what
-- refuses another game's *asset*: this statement's own WHERE cannot see
-- that argument at all, so the constraint is the only thing standing
-- between a leaked asset id and a cross-game image.
-- TestAnAssetOfAnotherGameCannotBecomeThisViewsBackground drives both,
-- through the service for the message and through this statement for the
-- 23503, each with its positive control.
--
-- execrows: a view id that matches nothing is a refusal, not a silent
-- success. version is untouched -- a background is not an edit of the
-- query document the version guards (positions.go makes the same call
-- for a drag), and updated_at moves on its own through
-- views_set_updated_at.
UPDATE views
SET background_asset_id = sqlc.narg('background_asset_id')::uuid,
    background_scale    = sqlc.arg('background_scale')::double precision,
    background_offset   = sqlc.arg('background_offset')::jsonb
WHERE project_id = sqlc.arg('project_id')::uuid AND id = sqlc.arg('id')::uuid;
