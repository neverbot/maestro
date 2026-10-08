-- The game's image library stops calling itself the views'.
--
-- `view_assets` was named for the only thing that had ever read it: the
-- background a `map` view is drawn over. An entity can attach an image
-- now, from the same library and listed on the same page, so the name
-- states something that is no longer true. A name that has gone false is
-- a defect here like any other, and this one would be read by everybody
-- who opens the schema next.
--
-- `background_asset_id` on `views` keeps its name, which is still exact:
-- it is the asset a view uses as its ground, and the column says which
-- role the image plays rather than which table it came from.

-- +goose Up

ALTER TABLE view_assets RENAME TO assets;
ALTER INDEX view_assets_project_idx RENAME TO assets_project_idx;

-- **Postgres is compressing what is already compressed.** A bytea over
-- two kilobytes is TOASTed, and TOAST tries to compress it: a PNG, a
-- JPEG and a WebP all arrive compressed, so every write spends CPU to
-- save nothing and every read spends it again. EXTERNAL keeps the
-- out-of-line storage, which is what makes a row cheap to read when
-- nobody wants the bytes, and skips the compression attempt.
ALTER TABLE assets ALTER COLUMN bytes SET STORAGE EXTERNAL;

-- +goose Down

ALTER TABLE assets ALTER COLUMN bytes SET STORAGE EXTENDED;
ALTER INDEX assets_project_idx RENAME TO view_assets_project_idx;
ALTER TABLE assets RENAME TO view_assets;
