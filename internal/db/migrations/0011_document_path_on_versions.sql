-- A document can be moved, so a version row has to say where it was
-- written.
--
-- Until this migration, `documents.path` was the only place a document's
-- address lived, and that was sound because nothing ever changed it:
-- UpsertDocument deliberately leaves the path column out of its SET
-- list, so the first spelling stored stood forever. The move landing
-- alongside this migration breaks that assumption, and it breaks it in
-- the one table where it mattered most. `document_versions` is the
-- append-only record of everything a document has ever been; a move that
-- left no trace there would make the history a *lie* rather than merely
-- incomplete -- a reader walking versions 1 to 7 of `lore/duskwood`
-- would be shown four snapshots taken while the document was called
-- something else, with nothing distinguishing them.
--
-- Storing the path per version, rather than a from/to pair on a special
-- "moved" row, is the shape that answers the question a reader actually
-- has. "What was this document called at version 3" is answered by
-- reading version 3, not by replaying every move since. It is also the
-- shape that makes the move's own version row self-describing: the move
-- appends a snapshot whose content is identical to its predecessor's and
-- whose path is not, so "this version is a move" is a comparison a
-- reader makes rather than a flag it has to trust.
--
-- The column is NOT NULL with the default dropped after the backfill, on
-- the same argument InsertDocumentVersion's `deleted` argument makes: a
-- defaulted column lets a future snapshot-writing path forget the
-- question and store an empty address silently, where a required one
-- turns the same omission into a compile error in the generated
-- parameter struct.

-- +goose Up
ALTER TABLE document_versions ADD COLUMN path text NOT NULL DEFAULT '';

-- Every version that already exists was written at its document's
-- current path, because until now there was no way for a path to move.
-- That makes this backfill exact rather than a best guess.
UPDATE document_versions v
SET path = d.path
FROM documents d
WHERE d.id = v.document_id;

ALTER TABLE document_versions ALTER COLUMN path DROP DEFAULT;

-- +goose Down
ALTER TABLE document_versions DROP COLUMN path;
