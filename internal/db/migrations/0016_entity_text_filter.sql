-- Folding a name the way a person types it.
--
-- The catalogue's one filter matches what a designer types against an
-- entity's name and key, and both of those are the game's own words:
-- "Clérigos", "Semidiós", "Ñú". A reader typing "clerigos" is asking for
-- the same row, and the filter that answers "no match" is answering a
-- question nobody asked.
--
-- A fixed transliteration and not `unaccent`: that is a contrib
-- extension, and this product ships a Docker image whose database an
-- operator may have provided themselves. A function in the schema is a
-- thing this repository owns and migrates; an extension is a thing it
-- would have to require of somebody else's Postgres.
--
-- IMMUTABLE and not STABLE, which is what makes it indexable later: the
-- answer depends on nothing but the argument. It is also what lets the
-- planner fold it into a constant on the query side of a comparison.
--
-- lower() first, so the map carries only the lower-case accents: Postgres
-- already knows that 'Á' lowers to 'á'.

-- +goose Up
CREATE FUNCTION maestro_fold(text) RETURNS text
  LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
  RETURN translate(lower($1),
    'áàâäãåéèêëíìîïóòôöõúùûüçñ',
    'aaaaaaeeeeiiiiooooouuuucn');

-- +goose Down
DROP FUNCTION maestro_fold(text);
