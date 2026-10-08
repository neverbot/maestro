-- An image attached to an entity.
--
-- **These are documents, not art.** A map of the region a quest happens
-- in, a reference picture of what a place is supposed to feel like:
-- things a game designer puts beside a thing so the next person to open
-- it knows what they are looking at. So there is no main image, no
-- ordering that means anything and no field on the entity pointing at
-- one; there is a set of attachments, exactly as document_links is a set
-- of attached prose, and this table is that table one domain along.
--
-- **The image is not owned by the entity.** The library is the game's,
-- and an image can be attached to several entities and be a map view's
-- ground at the same time. So detaching drops this row and leaves the
-- bytes, and deleting the image drops its attachments -- which is the
-- same asymmetry document_links has, for the same reason: the content
-- outlives the thing that happened to reference it.

-- +goose Up

CREATE TABLE entity_assets (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    asset_id   uuid NOT NULL,
    entity_id  uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    created_by_user_id uuid REFERENCES users (id) ON DELETE SET NULL,

    FOREIGN KEY (asset_id, project_id)
        REFERENCES assets (id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (entity_id, project_id)
        REFERENCES entities (id, project_id) ON DELETE CASCADE,

    -- One image is attached to one entity once. Attaching it again is not
    -- an error and not a second row; it is the attachment that is already
    -- there, which is what the upsert in the queries relies on.
    UNIQUE (entity_id, asset_id)
);

-- The read is always "the images on this entity, oldest first", which is
-- the order they were attached in and the only order that means anything
-- when none of them is the main one.
CREATE INDEX entity_assets_by_entity ON entity_assets (entity_id, created_at, id);

-- And the other direction, for the deletion cascade and for answering
-- what an image is attached to before somebody removes it.
CREATE INDEX entity_assets_by_asset ON entity_assets (asset_id);

-- +goose Down

DROP TABLE entity_assets;
