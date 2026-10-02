-- The language a person reads this product in.
--
-- It is a column and not a cookie because the preference is the person's
-- and not the browser's: a designer who picks Spanish on their laptop
-- reads Spanish on the machine beside the engine too. The browser keeps
-- a copy in localStorage, but only as a cache of this, so the first
-- request for a catalogue can go out before /api/me has answered.
--
-- Empty means "not chosen", which is a different fact from "chose
-- English": an unset preference follows the browser's own languages, and
-- a chosen one does not. A NOT NULL DEFAULT '' keeps both readable
-- without a nullable column and without a second flag.
--
-- The value is a BCP 47 tag the product admits, checked in Go against
-- the catalogues that ship, never here: a CHECK constraint naming the
-- locales would have to be migrated every time one is added, and the
-- list already lives in the one place that can be wrong about it.

-- +goose Up
ALTER TABLE users ADD COLUMN locale text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN locale;
