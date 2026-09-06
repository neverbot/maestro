-- Relations get `invalid` and `version`, the two columns entities have
-- had since 0004 and edges did not.
--
-- **Why the asymmetry could not stand.** A relation type carries a
-- `field_schema` exactly as an entity type does, and an edge's fields are
-- judged against it by the same Schema.Validate on the write path. The
-- core design's schema-evolution rule is "flag the rows rather than
-- reject the edit or back-fill the data" -- and until this migration that
-- rule was implementable for entities only: editing a relation type's
-- schema left every existing edge silently unjudged, because there was no
-- column to record a verdict in, no way to query for the rows that had
-- stopped fitting, and no way to tell an edge that had been re-checked
-- from one that never had. Two rows judged by the same rules must be
-- findable by the same question.
--
-- **version** comes with it rather than in a later migration, and for a
-- reason of its own: edge writes had no optimistic concurrency at all.
-- `UpsertRelation` was last-writer-wins, documented as such, and the
-- documentation named the exposure -- an edge's own fields are where
-- `relations_edge_key` pushes multiplicity, so `passages: ["door",
-- "vent"]` extended by two designers at once lost one of them silently.
-- Every other write in this repository is a compare-and-set. The two
-- columns also travel together because both are retrofits over a table
-- that will hold a real game's whole graph: doing them in two migrations
-- means two rewrites of every edge.
--
-- **Both defaults are the value a fresh write produces**, so the existing
-- rows need no back-fill pass and this migration is a pure DDL change.
-- `invalid false` says an edge is presumed to fit until something judges
-- it, which is the state every edge already written was in; `version 1`
-- is what an INSERT lands on, so an existing edge reads as never edited
-- and the first compare-and-set against it expects 1. Neither claim is a
-- guess about the data: an edge written before this migration was
-- validated against its type's schema at write time (upsertRelationWith
-- has always called Schema.Validate), so "presumed valid" is what the
-- write path actually established. What was missing is only the
-- re-judging after a schema edit, which the sweep this migration enables
-- now does on the next edit of the type.
--
-- **No new index.** The sweep reads `relations WHERE project_id = $1 AND
-- relation_type_id = $2`, and `relations_edge_key` -- the UNIQUE index on
-- (relation_type_id, source_id, target_id) that 0004 created to make a
-- re-seed idempotent -- leads with exactly that column, so the sweep
-- seeks it. Verified on this project's own Postgres rather than reasoned
-- about: with 5,000 edges of one type among 20,000, the sweep's statement
-- plans as `Index Scan using relations_edge_key`, not a sequential scan.
-- entities needed `entities_type_idx` for its own sweep because
-- `entities_key_key` leads with project_id and only then entity_type_id;
-- relations' unique index happens to lead the other way, so the index
-- this migration would otherwise add already exists under another name.
--
-- The invalid *listing* likewise gets no partial index, and that is
-- symmetry rather than an omission: `ListEntitiesPage`'s own invalid
-- filter has none either, and a game's invalid rows are the small set a
-- designer works through after a schema edit, not a hot path. Whoever
-- measures one of the two into needing an index adds it for both.

-- +goose Up
ALTER TABLE relations
    ADD COLUMN invalid boolean NOT NULL DEFAULT false,
    ADD COLUMN version integer NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE relations
    DROP COLUMN invalid,
    DROP COLUMN version;
