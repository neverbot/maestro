-- Index a row's key, so that the handle every other tool addresses a row
-- by is a handle search can find.
--
-- Task 9's end-to-end seeding found this and pinned it as a passing
-- limitation: `search("circuit-000")` answered with nothing. The vector
-- was built from the name and the type's indexable field values, and the
-- key — the string an agent writes into `entities.get`, the string every
-- refusal quotes back, the string a designer sees on every screen — was
-- the one thing about a row that was not in it.
--
-- **The new vector is the old one plus `setweight(key, 'C')`.** That is
-- what makes this backfill exact rather than approximate, the same
-- property 0006_weighted_entity_search.sql was built for and the reason
-- this is a third part rather than more text folded into an existing
-- one. `||` shifts the right operand's positions past the left's, and
-- both this statement and UpsertEntity evaluate `(A||B) || C`, so a row
-- migrated here is byte-for-byte the vector the shipping write path
-- produces for the same row. `TestTheEntityKeyBackfillIsExact`
-- (internal/db/metamodel_schema_test.go) asserts exactly that, over the
-- same twelve shapes 0006's own exactness test uses.
--
-- **Label C, not label A.** SearchEntities leads its ORDER BY with
-- `ts_filter(search, '{a}') @@ query`, which is what makes the ranking
-- promise a guarantee: a row the query *names* outranks a row that
-- merely mentions the words, however often it mentions them. The A half
-- is the name and nothing else, and it has to stay that way. Keys minted
-- from names (`ironforge` for "Ironforge") would add nothing there, and
-- keys minted from a counter would actively break it: two hundred rows
-- keyed `race-000`…`race-199` would every one of them claim a name match
-- on the word "race". Under C the key is findable and `name_match` still
-- answers the question it was introduced for. B (0.4) outranking C (0.2)
-- costs a key-only hit nothing in practice, because an exact handle is a
-- lexeme almost no other row carries and it competes with no one.
--
-- Ranking uses `ts_rank`'s default weight array, {D:0.1, C:0.2, B:0.4,
-- A:1.0}; as 0006 records, no explicit array is passed anywhere and the
-- weights stored here are labels.
--
-- `coalesce` for the reason 0006 states: `entities.search` is nullable,
-- `NULL || anything` is NULL, and a migration must not be the thing that
-- empties an index. A row that had no vector comes out of this holding
-- its key, which is more than it had.
--
-- The Down arm removes the C-weighted positions with `ts_filter(…,
-- '{a,b}')` rather than rebuilding the old vector from text it does not
-- have. That is exact for every row this migration wrote: a lexeme
-- carried by both the key and the name keeps its A and B positions and
-- loses only the C one. It is *not* exact after 0006's own Down arm has
-- run, because `strip` leaves no weights for this filter to keep — but
-- the arms unwind in the other order, so that combination cannot arise
-- from a downgrade.

-- +goose Up
UPDATE entities
SET search = coalesce(search, ''::tsvector)
             || setweight(to_tsvector('simple', key), 'C');

-- +goose Down
UPDATE entities
SET search = ts_filter(coalesce(search, ''::tsvector), '{a,b}');
