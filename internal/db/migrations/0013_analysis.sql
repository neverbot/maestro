-- The analysis engine's schema: one column that lets a game say what its
-- relation types *mean*, one counter that says when a game's design last
-- moved, and two tables for a route -- an ordered walk through a game
-- that carries a stored verdict.
--
-- Numbered 0013 rather than the plan's 0012 because the interface
-- sub-project took 0012. goose orders by number, so nothing else moves.
-- (The number and not the name: internal/web's
-- TestNoSourceFileNamesALayoutSeed greps every source file in the
-- repository for the identifier that migration removed, allowing only
-- the two files that are its own history, and a filename quoted here
-- for convenience would have widened that guard for nothing.)
--
-- What is pinned by a test, and where. Every name below is a test in
-- internal/db/analysis_schema_test.go:
--
--   TestAnalysisTablesExist                                 routes, route_steps
--   TestTheTraitVocabularyConstraintRefusesAnUnknownTrait    23514 + control
--   TestAnEmptyTraitArrayIsRefusedAndNullIsNot               the two halves
--   TestANewRelationTypeHasNoTraitsAndThatIsNotAnError       NULL is the default
--   TestARouteStepCannotNameAnEntityFromAnotherGame          23503
--   TestARouteStepCannotBorrowAnotherGamesRoute              23503
--   TestDeletingAnEntityLeavesItsRouteStepWithATombstone     SET NULL names its column
--   TestARouteCannotRecordAnotherGamesToken                  23503
--   TestRevokingATokenNullsOnlyTheTokenColumnOfARoute        SET NULL names its column
--   TestARouteKeyIsUniquePerGameWithoutRegardToCase          routes_key_key
--   TestDeletingARouteTakesItsSteps                          CASCADE
--   TestEveryMetamodelTableCarriesTheDesignVersionTriggers   pg_trigger, both ways
--   TestEveryMetamodelWriteBumpsTheDesignVersion             12 cases
--   TestTheDesignVersionIsMonotonicAndNotACountOfChanges     per statement
--   TestDeletingAnEntityBumpsTheCounterThroughTheRelationsCascade  the cascade case
--   TestAWriteToAnotherGameDoesNotMoveThisGamesDesignVersion  isolation + control
--   TestTwoConcurrentWritesToOneGameBothLandAndTheCounterMovesTwice  the cost
--   TestDeletingAGameDoesNotFailOnItsOwnDesignVersionTrigger  the cascade onto projects
--   TestTheReachabilityWalkSeeksAnIndexRatherThanScanning     EXPLAIN, shape not name

-- +goose Up

-- ---------------------------------------------------------------------
-- 1. analysis_traits: what a relation type *does*, from a closed
--    vocabulary.
-- ---------------------------------------------------------------------
--
-- Maestro ships no built-in notion of quest or zone, and it must not
-- start now. But an analysis has to know that one relation type is a
-- prerequisite and another is containment, or it can say nothing about
-- any game. A closed vocabulary declared per relation type is how that
-- knowledge stays *declarative* -- a property of this game's own types --
-- rather than becoming a second, hard-coded metamodel.
--
-- NULL is **undeclared**: this type has never been given an opinion. An
-- empty array is refused, so "I have nothing to say about this type" and
-- "this type is deliberately inert" cannot be spelled the same way; the
-- second is `{annotation}`. That distinction is the entire reason
-- `annotation` is in the vocabulary, and it is why the constraint carries
-- `cardinality(...) > 0` and not only the `<@`.
--
-- **No separate relation_type_semantics table.** It would be strictly
-- 1:1 with relation_types, with no independent lifecycle, no independent
-- permissions and no independent history. Traits arrive on
-- relation_types.upsert, are covered by that row's existing `version` and
-- its compare-and-set, and are deleted when the type is.
--
-- **No jsonb.** The vocabulary is closed and the database should say so.
-- A jsonb array would accept `{"teleports": true}` and hand the mistake
-- to a Go reader months later.
--
-- **What a new trait costs**, stated here because §12.7's
-- mutual-exclusion gap will be argued again: a trait is (1) a migration
-- to this constraint, (2) an entry in analysis.Traits, (3) a coherence
-- rule, (4) an arm in the resolver, (5) a row in the generated tool
-- description, and (6) a `semantics_source` case. Six places. The
-- bidirectional tests in the analysis package are what make it six rather
-- than five -- without them the sixth is the one that gets forgotten.
--
-- A **mutual-exclusion** trait specifically (§12.7) would need more than
-- a seventh word here: it is a property of a *set* of edges out of one
-- node, not of one edge, so it needs either a group discriminator on the
-- edge (which lives in the relation type's field_schema, which v1
-- deliberately does not read) or a second column. Adding the word alone
-- would produce a trait no analysis could act on.
ALTER TABLE relation_types
    ADD COLUMN analysis_traits text[];

ALTER TABLE relation_types
    ADD CONSTRAINT relation_types_traits_vocab CHECK (
        analysis_traits IS NULL
        OR (cardinality(analysis_traits) > 0
            AND analysis_traits <@ ARRAY['prerequisite_of','unlocks','containment',
                                         'ordering','symmetric','acyclic','annotation']::text[])
    );

-- ---------------------------------------------------------------------
-- 2. projects.design_version, maintained by triggers and not by Go.
-- ---------------------------------------------------------------------
--
-- An opaque monotonic token that moves whenever a game's *design* moves:
-- its entity types, relation types, entities or relations. A stored route
-- verdict records the value it was checked against, and a client compares
-- the two to know whether the verdict still stands.
--
-- **Why triggers and not the Go bump the design spec asks for.** The spec
-- puts the bump "in the same transaction as the write" and names the risk
-- in the same sentence: "a place a later query can silently forget."
-- There are more than twenty such write paths today across
-- internal/metamodel and the bulk driver, and the defect this repository
-- has produced at least twenty times is *a rule established correctly and
-- not carried one step along*. A Go bump is that defect with a schedule
-- attached. A trigger cannot be forgotten by a query written next month,
-- it fires for the bulk driver's set-shaped writes, and -- the case a Go
-- bump misses entirely -- it fires for a cascade.
--
-- **The cascade, corrected against the shipped schema.** The plan named
-- "removing a type takes its entities" as that case; the schema does not
-- do it. entity_types and relation_types are both ON DELETE RESTRICT
-- (0004_metamodel.sql), deliberately, so dropping a type that still has
-- instances fails loudly rather than deleting content. The cascade that
-- does exist is from an entity to its edges, and it makes the same point
-- more sharply: a Go call site that deletes one entity knows it wrote one
-- row, and does not know that Postgres also removed every edge touching
-- it -- which is exactly the change most likely to break a stored route
-- verdict. The trigger sees both statements and the counter moves twice.
-- TestDeletingAnEntityBumpsTheCounterThroughTheRelationsCascade is that
-- case, and its comment says so.
--
-- **The three costs, named here rather than discovered later.**
--
--   (a) The counter moves once per *statement*, not once per
--       transaction, so it is coarser than the spec's wording. That is
--       fine because nothing reads it as a count: it is an opaque
--       monotonic token and nothing may present it as anything else.
--       TestTheDesignVersionIsMonotonicAndNotACountOfChanges pins that.
--   (b) Every write to a game now updates that game's projects row, so
--       concurrent writers to *one game* serialise on that row for the
--       remainder of their transactions. That is a real cost and
--       TestTwoConcurrentWritesToOneGameBothLandAndTheCounterMovesTwice
--       measures it rather than reasoning about it, recording the
--       observed wall time so the next person to argue about
--       serialisation argues with data: a second writer blocked for
--       153 ms behind a transaction deliberately held open for 150 ms,
--       which is the first transaction's remaining lifetime and not a
--       cost of its own.
--
--       **What that cost actually means, found by a test rather than
--       predicted here.** An open transaction that has written *anything*
--       to a game now holds a lock on the whole game, not merely on the
--       rows it touched. internal/metamodel's
--       TestACancelledBatchDoesNotReportTheItemInFlightAsAServerFault had
--       a rival transaction hold a contended row by updating it, and the
--       batch under test then blocked on the game rather than on that
--       row -- a true report of where it stopped and the wrong fixture
--       for that test, which now takes the row lock with SELECT FOR
--       UPDATE and says why. Nothing in the product holds a write
--       transaction open across a round trip, so the exposure is bounded
--       by how long one write takes; whoever adds a long-running write
--       transaction inherits this.
--   (c) Writes to *different* games touch different rows and do not
--       contend at all.
--
-- **Rejected: re-checking every route on every write.** Seeding a game is
-- hundreds of writes, the cost is quadratic in exactly the situation that
-- matters, and the SSE stream would carry a storm of provisional
-- verdicts.
--
-- **Rejected: comparing last_checked_at against max(entities.updated_at).**
-- It costs a scan per check and misses deletions entirely, which is the
-- one change most likely to break a route.
--
-- routes itself is deliberately **not** one of the watched tables: a
-- check that marked every route in the game stale, including the one it
-- had just checked, would be a mechanism that invalidates its own output.
ALTER TABLE projects
    ADD COLUMN design_version bigint NOT NULL DEFAULT 0;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bump_design_version() RETURNS trigger AS $$
BEGIN
    -- The narrowing to the games actually touched is what makes this a
    -- per-game counter rather than a global one; without it a write to
    -- one game would move every game's version and every route in the
    -- instance would read as stale.
    --
    -- A game being deleted cascades into these tables and fires this
    -- function against a projects row the same command has already
    -- removed. The UPDATE then matches nothing and the delete proceeds:
    -- TestDeletingAGameDoesNotFailOnItsOwnDesignVersionTrigger is that
    -- path, asserted rather than assumed.
    UPDATE projects p
       SET design_version = p.design_version + 1
     WHERE p.id IN (SELECT project_id FROM changed);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Statement-level with transition tables, so one statement that writes a
-- thousand rows costs one UPDATE rather than a thousand. Three triggers
-- per table because INSERT and UPDATE read NEW and DELETE reads OLD, and
-- one trigger cannot name both.

CREATE TRIGGER entity_types_design_version_ins
    AFTER INSERT ON entity_types REFERENCING NEW TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();
CREATE TRIGGER entity_types_design_version_upd
    AFTER UPDATE ON entity_types REFERENCING NEW TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();
CREATE TRIGGER entity_types_design_version_del
    AFTER DELETE ON entity_types REFERENCING OLD TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();

CREATE TRIGGER relation_types_design_version_ins
    AFTER INSERT ON relation_types REFERENCING NEW TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();
CREATE TRIGGER relation_types_design_version_upd
    AFTER UPDATE ON relation_types REFERENCING NEW TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();
CREATE TRIGGER relation_types_design_version_del
    AFTER DELETE ON relation_types REFERENCING OLD TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();

CREATE TRIGGER entities_design_version_ins
    AFTER INSERT ON entities REFERENCING NEW TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();
CREATE TRIGGER entities_design_version_upd
    AFTER UPDATE ON entities REFERENCING NEW TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();
CREATE TRIGGER entities_design_version_del
    AFTER DELETE ON entities REFERENCING OLD TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();

CREATE TRIGGER relations_design_version_ins
    AFTER INSERT ON relations REFERENCING NEW TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();
CREATE TRIGGER relations_design_version_upd
    AFTER UPDATE ON relations REFERENCING NEW TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();
CREATE TRIGGER relations_design_version_del
    AFTER DELETE ON relations REFERENCING OLD TABLE AS changed
    FOR EACH STATEMENT EXECUTE FUNCTION bump_design_version();

-- ---------------------------------------------------------------------
-- 3. routes and route_steps.
-- ---------------------------------------------------------------------
--
-- A route is a claim: "a player can walk these entities in this order."
-- Unlike a view, which stores a *question* and caches nothing, a route
-- stores a claim plus a persisted verdict about it -- which is why it is
-- its own pair of tables and not a renderer over views.
--
-- last_check is the single thing this whole sub-project caches, and it is
-- allowed to be cached for one reason only: it can say when it went out
-- of date. last_checked_design_version against projects.design_version is
-- that statement. Nothing is cached unless it can say when it went out of
-- date.
CREATE TABLE routes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    key         text NOT NULL,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    -- The route's own definition of what "holding together" means:
    -- gate, bounds, ignored types. A check reads these and not the
    -- caller's defaults, so two people checking one route get one
    -- answer.
    params      jsonb NOT NULL DEFAULT '{}'::jsonb,
    version     integer NOT NULL DEFAULT 1,
    last_checked_at             timestamptz,
    last_checked_design_version bigint,
    last_check  jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid,
    -- The target of the composite key from route_steps.
    UNIQUE (id, project_id),
    -- Composite, so a recorded token belongs to this row's own game. The
    -- SET NULL names its column: project_id is NOT NULL, and a bare SET
    -- NULL would try to null it too -- failing at token-revocation time
    -- rather than here.
    FOREIGN KEY (updated_by_token_id, project_id)
        REFERENCES api_tokens (id, project_id) ON DELETE SET NULL (updated_by_token_id)
);
-- Keys are the stable handle an agent re-seeds against, matched
-- case-insensitively so a second run with different casing collides with
-- the existing row instead of creating a twin. Same rule as
-- entity_types_key_key, documents_path_key and views_key_key.
CREATE UNIQUE INDEX routes_key_key ON routes (project_id, lower(key));
-- The listing's keyset, in its own sort order; it also gives the projects
-- delete cascade an index to work from. Not measured, for the same reason
-- views_project_idx is not: a game holds tens of routes.
CREATE INDEX routes_project_idx ON routes (project_id, name, id);
CREATE TRIGGER routes_set_updated_at
    BEFORE UPDATE ON routes
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- **Steps are rows, not a jsonb array on the route.** With jsonb,
-- deleting an entity leaves a dangling key nothing in the database knows
-- about and nothing forces anyone to re-check. With a real foreign key
-- there is a choice and the choice matters: CASCADE would silently shrink
-- the route, which is the exact failure this table exists to prevent;
-- RESTRICT would block a legitimate deletion because some old route
-- mentions the entity, making routes a liability. SET NULL with the key
-- retained as a tombstone is the honest one -- the step survives,
-- visibly, as *step 4: hogger-hunt (deleted)*, and the next check reports
-- missing_entity.
--
-- entity_type_key sits beside entity_key because an entity key is unique
-- per (project, entity_type) and not per project, so the pair is the
-- address. It is also the tombstone half a **rename** must not tidy, for
-- the reason internal/metamodel/rename.go records about view_refs: the
-- stored row and the catalogue disagreeing on the spelling is what makes
-- resolution fall to the by-id arm and report `type_renamed` rather than
-- `missing`.
--
-- **A step is an entity and never a relation** (design spec §12.4). The
-- change that would relax it is named here so whoever wants it does not
-- rediscover it: a nullable relation_id with its own composite key to
-- relations (id, project_id), a CHECK that exactly one of entity_id and
-- relation_id is set, a second tombstone pair for the edge -- which has
-- no key of its own, so it would be (relation_type_key, source_key,
-- target_key) -- and a fourth verdict in the checker. It is not reserved
-- as a nullable column now, because a column nothing writes is a column
-- every reader has to have an opinion about.
CREATE TABLE route_steps (
    route_id        uuid NOT NULL,
    project_id      uuid NOT NULL,
    position        integer NOT NULL,
    entity_id       uuid,
    entity_type_key text NOT NULL,
    entity_key      text NOT NULL,
    note            text NOT NULL DEFAULT '',
    PRIMARY KEY (route_id, position),
    FOREIGN KEY (route_id, project_id) REFERENCES routes (id, project_id) ON DELETE CASCADE,
    -- Composite, so a step cannot name another game's entity; and SET
    -- NULL names its column for the same reason as above -- a bare SET
    -- NULL would try to null project_id and fail at entity-deletion
    -- time, a failure a designer meets and a test suite does not.
    FOREIGN KEY (entity_id, project_id)
        REFERENCES entities (id, project_id) ON DELETE SET NULL (entity_id)
);
-- The index the entity-deletion SET NULL scans to find the steps it must
-- tombstone. Unlike the audit-column keys elsewhere in this schema, which
-- deliberately carry none, this one serves a path a designer takes
-- routinely -- deleting content -- rather than a rare administrative one.
CREATE INDEX route_steps_entity_idx ON route_steps (entity_id);

-- ---------------------------------------------------------------------
-- 4. No relations_project_type_idx, and the measurement that says so.
-- ---------------------------------------------------------------------
--
-- The design spec asks for CREATE INDEX relations_project_type_idx ON
-- relations (project_id, relation_type_id), justified by the claim that
-- no existing index leads with project_id. That claim is false and the
-- spec says so itself: relations_project_idx is (project_id, created_at)
-- and does lead with it, and relations_edge_key and
-- relations_type_target_idx both lead with relation_type_id. relations is
-- the table that holds a real game's whole graph and already carries five
-- indexes; a sixth is write cost on every edge write.
--
-- So the index is **measured before it is added** rather than added on a
-- justification that does not hold. The statement measured is the one the
-- reachability walk emits -- internal/graph's WalkCTE with the
-- normalising edge predicate, over a game of twenty relation types and a
-- few thousand edges -- and the plan observed on this project's Postgres
-- 16 was:
--
--   Subquery Scan on w_out
--     CTE w
--       ->  Recursive Union
--             ->  Nested Loop
--                   ->  Index Scan using entities_key_key on entities
--                   ->  Bitmap Heap Scan on entities anchor
--                         ->  Bitmap Index Scan on entities_listing_idx
--             ->  Nested Loop
--                   ->  Nested Loop
--                         ->  WorkTable Scan on w w_1
--                         ->  Materialize
--                               ->  Bitmap Heap Scan on relations r
--                                     ->  BitmapOr
--                                           ->  Bitmap Index Scan on relations_type_target_idx
--                                           ->  Bitmap Index Scan on relations_type_target_idx
--                                           ->  Bitmap Index Scan on relations_type_target_idx
--                   ->  Bitmap Heap Scan on entities far
--                         ->  Bitmap Index Scan on entities_listing_idx
--     ->  Limit
--           ->  Sort
--                 ->  CTE Scan on w
--
-- No sequential scan on relations anywhere in it: the walk seeks the edge
-- indexes that already exist, per node, which is the access pattern a
-- recursive walk actually has -- it never asks for "every edge of type t
-- in this game", which is the only question the proposed index would have
-- answered better. TestTheReachabilityWalkSeeksAnIndexRatherThanScanning
-- runs that EXPLAIN and asserts the *shape* -- no Seq Scan on relations
-- -- and not an index name, exactly as
-- TestTheEdgeSweepSeeksAnIndexRatherThanScanning does: which of five
-- indexes the planner picks is its business.

-- +goose Down

DROP INDEX IF EXISTS route_steps_entity_idx;
DROP TABLE IF EXISTS route_steps;
DROP TRIGGER IF EXISTS routes_set_updated_at ON routes;
DROP INDEX IF EXISTS routes_project_idx;
DROP INDEX IF EXISTS routes_key_key;
DROP TABLE IF EXISTS routes;

DROP TRIGGER IF EXISTS relations_design_version_del ON relations;
DROP TRIGGER IF EXISTS relations_design_version_upd ON relations;
DROP TRIGGER IF EXISTS relations_design_version_ins ON relations;
DROP TRIGGER IF EXISTS entities_design_version_del ON entities;
DROP TRIGGER IF EXISTS entities_design_version_upd ON entities;
DROP TRIGGER IF EXISTS entities_design_version_ins ON entities;
DROP TRIGGER IF EXISTS relation_types_design_version_del ON relation_types;
DROP TRIGGER IF EXISTS relation_types_design_version_upd ON relation_types;
DROP TRIGGER IF EXISTS relation_types_design_version_ins ON relation_types;
DROP TRIGGER IF EXISTS entity_types_design_version_del ON entity_types;
DROP TRIGGER IF EXISTS entity_types_design_version_upd ON entity_types;
DROP TRIGGER IF EXISTS entity_types_design_version_ins ON entity_types;
DROP FUNCTION IF EXISTS bump_design_version();
ALTER TABLE projects DROP COLUMN design_version;

ALTER TABLE relation_types DROP CONSTRAINT relation_types_traits_vocab;
ALTER TABLE relation_types DROP COLUMN analysis_traits;
