-- Saved views: a query plus a layout, and the state a layout
-- accumulates.
--
-- Four tables. views is the saved record; view_positions is where a
-- human dragged each node of one view; view_refs is the dependency index
-- that makes "which views did deleting this type break" one query;
-- view_assets holds background images as bytea.
--
-- Every row is scoped by project_id, and every key from one of these
-- tables to a project-scoped parent is composite, carrying project_id
-- alongside the parent id, so the database -- not Go -- refuses a
-- cross-game row. A composite key needs a UNIQUE (id, project_id) on the
-- parent to reference, which is why views and view_assets each carry one
-- on top of their primary key, exactly as 0004_metamodel.sql's types and
-- entities and 0007_documents.sql's documents do. Keys to users stay
-- single-column: users are global, not project-scoped, so there is no
-- outer scope to carry; project_id's own key to projects (id) is
-- single-column for the same reason.
--
-- What is pinned by a test, and where. Every name below is a test in
-- internal/db/views_schema_test.go:
--
--   TestViewTablesExist                                  the four tables
--   TestAViewPositionCannotCrossGames                    23503
--   TestAViewPositionCannotBorrowAnotherGamesView        23503
--   TestAViewCannotUseAnotherGamesBackground             23503
--   TestAViewRefCannotUseAnotherGamesEntityType          23503
--   TestAViewCannotRecordAnotherGamesToken               23503
--   TestAnAssetCannotRecordAnotherGamesToken             23503
--   TestDeletingAnEntityTypeNullsTheRefAndKeepsItsKey    SET NULL, ref survives
--   TestDeletingARelationTypeNullsOnlyItsOwnRefColumn    SET NULL, other arm untouched
--   TestRevokingATokenNullsOnlyTheTokenColumnOfAView     SET NULL names its column
--   TestDeletingAnAssetNullsTheBackgroundOfEveryView     SET NULL names its column
--   TestDeletingAnEntityDropsItsPositionsAndKeepsTheView CASCADE
--   TestDeletingAViewTakesItsPositionsAndRefs            CASCADE
--   TestAViewKeyIsUniquePerGameWithoutRegardToCase       views_key_key
--   TestOneRefPerPointerPerView                          view_refs_key
--   TestNonFiniteCoordinatesAreRefused                   23514
--   TestARefCannotClaimOneKindAndCarryTheOthersID        23514
--   TestAnAssetMimeOutsideTheClosedListIsRefused         23514
--   TestAnUnknownLayoutModeIsRefused                     23514
--   TestTheViewsUpdatedAtTriggerFires                    views_set_updated_at
--   TestTheViewPositionsUpdatedAtTriggerFires            view_positions_set_updated_at
--
-- Comments below that describe Go behaviour -- which renderer names are
-- legal, what layout_mode means to a client, the 8 MB asset bound -- say
-- so where they appear. They are pinned in internal/views, by tasks that
-- have not run yet, and nothing in this file's own suite exercises them.
-- +goose Up

-- Background images live in Postgres rather than on disk or in object
-- storage: a world map is one file of a few megabytes, and adding a
-- storage backend to a single-binary deployment for that is
-- disproportionate. It also means pg_dump backups already cover them,
-- which a filesystem directory would not.
--
-- The mime CHECK is a closed list on purpose and SVG is not in it: an SVG
-- served inline is a script-execution vector, and a raster map is what a
-- designer has anyway.
--
-- The 8 MB per-asset bound is applied in Go (internal/views/assets.go,
-- not written yet) rather than here, because a bytea length CHECK
-- refuses the row only after Postgres has already received and decoded
-- every byte, which is the cost the bound exists to avoid. Nothing in
-- this migration bounds an asset's size.
--
-- Assets are per-project and reusable across views, not owned by one
-- view: one world map backs a room graph and a fast-travel map alike.
CREATE TABLE view_assets (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    filename   text NOT NULL,
    mime       text NOT NULL CHECK (mime IN ('image/png', 'image/jpeg', 'image/webp')),
    width      integer NOT NULL CHECK (width > 0),
    height     integer NOT NULL CHECK (height > 0),
    bytes      bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_by_token_id uuid,
    -- Composite, so a recorded token belongs to this row's own game. The
    -- SET NULL names its column: project_id is NOT NULL, and a bare
    -- SET NULL would try to null it too -- failing at token-revocation
    -- time rather than here.
    FOREIGN KEY (created_by_token_id, project_id)
        REFERENCES api_tokens (id, project_id) ON DELETE SET NULL (created_by_token_id),
    -- The target of the composite key from views.background_asset_id.
    UNIQUE (id, project_id)
);
-- The listing's own sort order, project-leading, so a page can be sought
-- to rather than read whole and sorted. It also gives the projects
-- delete cascade an index to work from. Unlike 0005_entity_listing_index
-- this is not measured: the listing it is for does not exist yet (the
-- views plan, Task 14).
CREATE INDEX view_assets_project_idx ON view_assets (project_id, created_at, id);

CREATE TABLE views (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    key         text NOT NULL,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    -- The query document as the agent wrote it, v and all. Stored as
    -- authored: a rename never rewrites it (the views plan, Task 12), so
    -- what is read back is what was written.
    query           jsonb NOT NULL,
    -- No CHECK on renderer, deliberately, and the asymmetry with
    -- layout_mode below is the decision rather than an oversight. The
    -- renderer catalogue is Go's (internal/views/renderers.go, not
    -- written yet) and is expected to grow; a CHECK would put the
    -- catalogue in two places and make adding a renderer a migration.
    -- layout_mode's three values are a closed contract that will not
    -- grow, and the database is the right place for a closed contract.
    renderer        text NOT NULL,
    renderer_params jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- What layout_mode means is a contract with the *client*: no server
    -- code reads this value beyond validating and returning it. auto: the
    -- client lays every node out and ignores stored positions. manual:
    -- every node with a stored position uses it and the rest are placed
    -- once. mixed: pinned nodes are fixed and the rest are laid out
    -- around them.
    layout_mode text NOT NULL DEFAULT 'mixed'
                CHECK (layout_mode IN ('auto', 'manual', 'mixed')),
    -- Seeds the client's force layout so two designers opening one view
    -- see the same picture. An unseeded force layout draws a different
    -- diagram every load, which destroys the one thing a saved view is
    -- for: being recognisable.
    layout_seed integer NOT NULL DEFAULT 1,
    background_asset_id uuid,
    background_scale    double precision NOT NULL DEFAULT 1 CHECK (background_scale > 0),
    background_offset   jsonb NOT NULL DEFAULT '{"x":0,"y":0}'::jsonb,
    version    integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid,
    FOREIGN KEY (updated_by_token_id, project_id)
        REFERENCES api_tokens (id, project_id) ON DELETE SET NULL (updated_by_token_id),
    -- Composite, so a view cannot point at another game's image. The SET
    -- NULL names its column: project_id is NOT NULL and must survive the
    -- asset's deletion untouched. One image per view, deliberately (the
    -- views plan, open question O7); a second overlay layer needs a join
    -- table (view_id, asset_id, z, scale, offset) and this column would
    -- then be dropped in that migration, not widened.
    --
    -- No index leads with background_asset_id, so the SET NULL that runs
    -- when an asset is deleted scans views. That is deliberate rather
    -- than an oversight and it is not measured: a game holds tens of
    -- views, not millions, and an index nothing else reads is write cost
    -- on every view write to serve a rare delete. Whoever finds an asset
    -- deletion slow adds (project_id, background_asset_id) and measures
    -- it, the way 0005 and 0007 did.
    FOREIGN KEY (background_asset_id, project_id)
        REFERENCES view_assets (id, project_id) ON DELETE SET NULL (background_asset_id),
    -- The target of the composite keys from view_positions and view_refs.
    UNIQUE (id, project_id)
);
-- Keys are the stable handle an agent re-seeds against, matched
-- case-insensitively so a second run with different casing collides with
-- the existing row instead of creating a twin. Same rule as
-- entity_types_key_key and documents_path_key.
CREATE UNIQUE INDEX views_key_key ON views (project_id, lower(key));
-- The listing's keyset, in its own sort order. Not measured, for the
-- same reason view_assets_project_idx is not: the listing is Task 11.
-- It also gives the projects delete cascade an index to work from.
CREATE INDEX views_project_idx ON views (project_id, name, id);
CREATE TRIGGER views_set_updated_at
    BEFORE UPDATE ON views
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Where a human put each node of one view.
--
-- Per view, not per entity: the same zone sits at its real map
-- coordinates in a World map view and wherever the algorithm put it in a
-- Mage route view.
--
-- Positions are a cache of a human's arrangement and never a claim that
-- anything exists. Deleting an entity cascades its rows away and nothing
-- else happens; an entity that appears has no row, is laid out
-- automatically and joins the picture unpinned. Re-running a query never
-- rewrites positions, which is what lets a query be edited without losing
-- an afternoon of map work.
--
-- There is no view_edge_positions and no per-edge annotation, because
-- relations carry no key and no version (0004_metamodel.sql): an edge is
-- addressable only as (relation_type_id, source_id, target_id), so a
-- re-seed that deleted and recreated it would strand any row keyed on its
-- id. Nodes have keys; edges do not.
CREATE TABLE view_positions (
    view_id    uuid NOT NULL,
    entity_id  uuid NOT NULL,
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- The CHECKs reject NaN and both infinities. In Postgres NaN compares
    -- greater than every other double, so `x < 'Infinity'` is false for
    -- it and the conjunction refuses all three values with one clause.
    -- internal/views/positions.go (not written yet) is meant to refuse
    -- them first, with a field path; this is the backstop for a write
    -- path that does not go through it.
    x double precision NOT NULL
      CHECK (x > '-Infinity'::double precision AND x < 'Infinity'::double precision),
    y double precision NOT NULL
      CHECK (y > '-Infinity'::double precision AND y < 'Infinity'::double precision),
    pinned     boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (view_id, entity_id),
    FOREIGN KEY (view_id, project_id)
        REFERENCES views (id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (entity_id, project_id)
        REFERENCES entities (id, project_id) ON DELETE CASCADE
);
-- The reverse lookup and the entities delete cascade's index. Both
-- columns are equalities in the cascade's own query, so a
-- project-leading index serves it. The primary key already serves every
-- read that starts from a view, and the views cascade with it.
CREATE INDEX view_positions_entity_idx ON view_positions (project_id, entity_id);
CREATE TRIGGER view_positions_set_updated_at
    BEFORE UPDATE ON view_positions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The dependency index: one row per type reference in a view's query,
-- written on every upsert.
--
-- ON DELETE SET NULL rather than CASCADE is the whole point of this
-- table. When a type is deleted the row survives with a null id and its
-- key text intact, so "which views did that break, and where in each
-- query" is one indexed query rather than a scan that parses every stored
-- jsonb document. Resolution at execution time is by id first (a rename
-- does not change an id, so a renamed type still resolves), then by key
-- (a type deleted and re-created under the same key resolves, which is
-- the common shape of a designer fixing a mistake), then dead. That
-- resolution order is Go's, in the views plan's Task 12, and no test in
-- this file exercises it; what this file pins is that the row and its
-- key survive the deletion at all.
--
-- pointer is the JSON pointer into the query where the reference sits
-- ("/traverse/0/via"), which is what lets a staleness report name the
-- step that broke instead of the view that broke.
CREATE TABLE view_refs (
    view_id          uuid NOT NULL,
    project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- The IN list is redundant with the pairing CHECK at the foot of
    -- this table: both of that CHECK's arms name a kind, so any third
    -- value fails it whatever the id columns hold, and removing this
    -- list alone turns no test in views_schema_test.go red. It is kept
    -- for the reader, who should not have to derive the domain of a
    -- column from a constraint about two other columns. The pairing
    -- CHECK is the load-bearing one.
    kind             text NOT NULL CHECK (kind IN ('entity_type', 'relation_type')),
    ref_key          text NOT NULL,
    entity_type_id   uuid,
    relation_type_id uuid,
    pointer          text NOT NULL,
    FOREIGN KEY (view_id, project_id)
        REFERENCES views (id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (entity_type_id, project_id)
        REFERENCES entity_types (id, project_id) ON DELETE SET NULL (entity_type_id),
    FOREIGN KEY (relation_type_id, project_id)
        REFERENCES relation_types (id, project_id) ON DELETE SET NULL (relation_type_id),
    -- kind is not decoration: it says which of the two id columns this
    -- row uses, and this CHECK stops a row claiming one kind and
    -- carrying the other's id. A null id is legal on both arms -- that
    -- is what a deleted type looks like, and refusing it would make the
    -- SET NULL above unwritable. Because both arms name a kind, this is
    -- also what refuses a kind outside the two.
    CHECK ((kind = 'entity_type' AND relation_type_id IS NULL)
        OR (kind = 'relation_type' AND entity_type_id IS NULL))
);
-- One ref per pointer per view: a pointer addresses exactly one position
-- in one document, so a second row for it would be a rewrite that did not
-- delete the first. The upsert deletes this view's refs and reinserts
-- them in one transaction, and this index is what makes that a refusal
-- rather than a silent duplicate if it ever stops doing so. It is also
-- the index the views cascade above travels.
CREATE UNIQUE INDEX view_refs_key ON view_refs (view_id, pointer);
-- "Which views did deleting this type break": the two lookups the SET
-- NULL above exists to serve, each project-leading. Both columns are
-- equalities in the SET NULL's own query, so these serve it too.
CREATE INDEX view_refs_entity_type_idx ON view_refs (project_id, entity_type_id);
CREATE INDEX view_refs_relation_type_idx ON view_refs (project_id, relation_type_id);

-- Reverse traversal by relation type, for the recursive walk in
-- internal/graph (the views plan, Task 5). relations_edge_key
-- (relation_type_id, source_id, target_id) already serves a forward
-- walk. A backward one -- given a frontier of targets and one relation
-- type, find the sources -- had only relations_target_idx (target_id),
-- which finds every edge into a node whatever its type and then filters.
-- Reverse walks are not an edge case: the worked example "quests
-- available to a Mage" walks `available_to` inwards.
--
-- The metamodel plan's Task 1, correction 6, deliberately did not add
-- this index -- "an index nothing reads is pure write cost ... it belongs
-- to the task that introduces the query it serves" -- and named this
-- sub-project as the reason it was considered. This is that task, and
-- this index is measured rather than argued.
--
-- Measured on this project's own test Postgres 16, 200,000 relations
-- over 20,000 entities across 20 games, one dominant relation type
-- holding 60% of the edges, VACUUM ANALYZE run, seeking the sources of
-- one target under one type, 10,000 such lookups as a nested loop:
--
--   without: Bitmap Index Scan on relations_target_idx, the
--            relation_type_id equality left as a heap-level Filter that
--            removed 4 of every 10 rows it read; 118,894 shared buffers
--            for the 10,000 lookups, median 97.6 ms over five runs.
--   with:    Index Scan on relations_type_target_idx, both equalities
--            folded into the Index Cond and nothing rechecked; 67,170
--            shared buffers, median 57.8 ms over five runs.
--
-- Single lookup, the same shape: 15 buffers without, 11 with.
--
-- What it costs: 4,520 kB over those 200,000 rows, about 23 bytes a row,
-- and one more b-tree entry on every edge inserted. Relation endpoints
-- and types are never updated in place (an edge is addressable only as
-- the triple, 0004_metamodel.sql), so this is an insert cost and not a
-- rename cost the way entities_listing_idx's is. Measured over
-- 20,000-row bulk inserts it did not rise clearly above the noise of the
-- workload: 634 ms median with the index against 617 ms without.
--
-- 2026-09-02-analysis-engine-design.md §11 asks for a *different* index,
-- relations (project_id, relation_type_id), for a different access shape;
-- neither replaces the other. Whoever adds that one checks first: with
-- both, relations carries six indexes on the table expected to hold the
-- most rows, and every index is write cost on every seeded edge.
CREATE INDEX relations_type_target_idx ON relations (relation_type_id, target_id);

-- +goose Down
-- Children first: view_refs and view_positions both key into views, and
-- views keys into view_assets. Dropping a table takes its constraints,
-- indexes and triggers with it; set_updated_at() belongs to 0001, whose
-- Down drops it. relations survives this migration, so its index is
-- dropped by hand.
DROP INDEX relations_type_target_idx;
DROP TABLE view_refs;
DROP TABLE view_positions;
DROP TABLE views;
DROP TABLE view_assets;
