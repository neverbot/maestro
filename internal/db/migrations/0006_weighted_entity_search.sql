-- Weight the entity search vector so that a row the query *names*
-- outranks a row that merely mentions the word in a paragraph.
--
-- Task 6 shipped `search` as one flat, unweighted vector built by
-- UpsertEntity from `name || ' ' || <the row's text fields>`, and
-- `ts_rank` over it scores purely by how often a lexeme occurs. So a
-- lore paragraph saying "gnoll" three times beat the entity actually
-- called "Gnoll", and an agent whose top hit is the wrong row has no
-- recovery but to search again, harder. Task 6 deferred the fix here
-- deliberately, because it is a write-path change plus a rewrite of
-- every stored row, and left `TestSearchRanksTheStrongerMatchFirst`
-- pinning the old order as a limitation rather than a promise.
--
-- **The new vector is `setweight(name, 'A') || setweight(name ||
-- fields, 'B')`.** The B half is deliberately the *whole* old text,
-- name included, and not the fields alone. That is what makes this
-- backfill exact rather than approximate: the B half of a row written
-- from now on is byte-for-byte the vector this column already holds,
-- so the statement below is a pure function of the stored value and a
-- row migrated here ranks identically to the same row re-seeded
-- afterwards. Reconstructing a fields-only vector in SQL was the
-- alternative and it is not available: which of a row's jsonb fields
-- carry indexable text is decided by the Go validator from the type's
-- declared schema (searchTextOf, internal/metamodel/entities.go), and
-- teaching this migration that rule would put a second, drifting copy
-- of it in SQL. The cost of carrying the name in both halves is that a
-- name match scores its A weight plus its B weight instead of the A
-- weight alone, which pushes the order further in the direction this
-- migration exists to push it.
--
-- Ranking uses `ts_rank`'s default weight array, {D:0.1, C:0.2, B:0.4,
-- A:1.0} — no explicit array is passed anywhere. Whoever wants a
-- sharper or flatter separation changes it in SearchEntities
-- (metamodel.sql) and nowhere else; the weights stored here are just
-- labels.
--
-- `coalesce` because `entities.search` is nullable: every row written
-- through UpsertEntity has one, but `NULL || anything` is NULL and a
-- migration must not be the thing that empties an index.
--
-- The Down arm reverses the labelling rather than rebuilding the old
-- vector from text it does not have. `strip` removes every weight and
-- every position, which is *not* what the column held before this
-- migration — the old vector carried positions — so a downgraded
-- database ranks by presence rather than by frequency until its rows
-- are rewritten. That is stated here rather than hidden: this arm
-- exists so a downgrade leaves a working search, not so it restores a
-- byte-identical column.

-- +goose Up
UPDATE entities
SET search = setweight(to_tsvector('simple', name), 'A')
             || setweight(coalesce(search, ''::tsvector), 'B');

-- +goose Down
UPDATE entities
SET search = strip(coalesce(search, ''::tsvector));
