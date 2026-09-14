-- The two indexes the entity listing's other orders sort on.
--
-- 0005 gave the listing one order — (project_id, name, id) — and its
-- comment says, with measurements, what a listing without a
-- project-leading index in its own sort order costs: the position a
-- cursor carries cannot be sought to, so every page reads the whole game
-- and sorts it. A catalogue whose column headers set the order turns
-- that one sort key into three, and the two new ones would otherwise
-- have exactly the defect 0005 measured and fixed.
--
-- **These two were not re-measured**, and this comment says so rather
-- than repeating 0005's numbers as if they had been taken again: the
-- argument for them is that they are the same shape as the index whose
-- effect was measured — the listing's whole sort key, led by the column
-- the listing is filtered on — and not a second measurement. A game
-- large enough to measure on is a fixture this repository does not have
-- standing; 0005's was built for that migration and not kept.
--
-- **Both are ascending-only b-trees and both directions use them.**
-- Postgres reads a b-tree backwards for a sort whose columns are all
-- reversed together, which is the shape of every keyset here
-- (`ORDER BY name DESC, id DESC`), so the descending half of each order
-- needs no index of its own. A mixed order — one column ascending and
-- the other descending — would, and none is offered.
--
-- entities_key_key cannot serve the key order: it is
-- (project_id, entity_type_id, lower(key)), so it delivers rows in
-- `lower(key)` order inside one type, and this listing orders by `key`
-- across the game. Two keys differing only in case cannot both exist, so
-- the two orders agree on *which rows*; they do not agree on index
-- order, and a keyset that sorts by one while seeking in the other skips
-- rows at a page boundary and says nothing.
--
-- **What they cost is a write cost, and the second one is paid by every
-- write.** `key` is immutable once the row exists, so that index is
-- maintained on insert alone; `updated_at` moves on every write, so that
-- one is maintained on every content edit, which makes it the more
-- expensive of the two.
-- +goose Up
CREATE INDEX entities_key_order_idx ON entities (project_id, key, id);
CREATE INDEX entities_updated_order_idx ON entities (project_id, updated_at, id);

-- +goose Down
DROP INDEX entities_updated_order_idx;
DROP INDEX entities_key_order_idx;
