-- 0012: drop views.layout_seed.
--
-- It was added for a force-directed layout that needed seeding to keep a
-- saved view recognisable. The interface sub-project chose a
-- deterministic engine over a stochastic one, precisely so that two
-- designers opening one view get the same picture without a seed, so
-- nothing has ever read this column: not the server (0008_views.sql said
-- so from the first day) and not the client.
--
-- If a stochastic engine is ever adopted, what it would need is a seed
-- per (view, engine) — this column was per view — and a stated rule for
-- what happens to a stored arrangement when the seed changes. Re-adding
-- a column is additive; keeping one nothing reads is a knob that lies.

-- +goose Up
ALTER TABLE views DROP COLUMN layout_seed;

-- +goose Down
-- The column comes back with 0008_views.sql's declaration, so a
-- rollback lands on the schema that was there. It comes back empty of
-- meaning too: every value that was in it was the default, because
-- nothing ever wrote a chosen one through a reader that existed.
ALTER TABLE views ADD COLUMN layout_seed integer NOT NULL DEFAULT 1;
