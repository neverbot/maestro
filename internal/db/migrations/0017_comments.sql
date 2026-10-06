-- The log a game's designers and their agents keep beside the content.
--
-- **It is not part of the metamodel and it is not a fifth primitive.** A
-- game's own model is four things and nothing here changes that: a
-- comment says nothing a player can be, go to, do or unlock. It is meta
-- — how a thing was imported, what was rewritten and why, an idea about
-- the philosophy of a type, something worth doing later — written in
-- markdown by whoever is working on the game, agent or person.
--
-- **It carries no state, and that is the line.** No status, no assignee,
-- no due date, no "done": the day this table grows one it has become a
-- project tracker, which is the one thing this product says it is not.
-- A log is what it is for.
--
-- **Append-only.** There is no update: a comment is what somebody said
-- at a moment, and a log that can be rewritten is not a log. It can be
-- removed, because a note written against the wrong thing is worse than
-- a gap.
--
-- **One target, by a real foreign key rather than a (kind, id) pair.**
-- Four nullable columns with a CHECK that exactly one is set costs a
-- column and buys what a polymorphic id cannot: the database refuses a
-- comment on a row that is not there, and a removed entity takes its
-- comments with it instead of leaving orphans nothing will ever read.
-- The composite keys carry project_id for the reason every other table
-- here does: an isolation filter that depends on somebody remembering a
-- join is an isolation filter that is one forgotten join from serving
-- another game's content.

-- +goose Up

-- A composite foreign key needs a UNIQUE (id, project_id) on the parent
-- to point at. The other three tables here were given one when they were
-- created; relations was not, because nothing had ever needed to name an
-- edge from another table.
ALTER TABLE relations ADD CONSTRAINT relations_id_project_key UNIQUE (id, project_id);

CREATE TABLE comments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    entity_id           uuid,
    relation_id         uuid,
    entity_type_id      uuid,
    relation_type_id    uuid,

    body       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    created_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_by_token_id uuid,

    FOREIGN KEY (created_by_token_id, project_id)
        REFERENCES api_tokens (id, project_id) ON DELETE SET NULL (created_by_token_id),
    FOREIGN KEY (entity_id, project_id)
        REFERENCES entities (id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (relation_id, project_id)
        REFERENCES relations (id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (entity_type_id, project_id)
        REFERENCES entity_types (id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (relation_type_id, project_id)
        REFERENCES relation_types (id, project_id) ON DELETE CASCADE,

    CONSTRAINT comments_one_target CHECK (
        (entity_id IS NOT NULL)::int
        + (relation_id IS NOT NULL)::int
        + (entity_type_id IS NOT NULL)::int
        + (relation_type_id IS NOT NULL)::int
        = 1
    ),
    CONSTRAINT comments_body_not_blank CHECK (btrim(body) <> '')
);

-- One index per target, because every read is "the comments on this one
-- thing, newest first" and a scan of a game's whole log to answer it is
-- the shape that stops working at the size this product is for.
CREATE INDEX comments_by_entity ON comments (entity_id, created_at DESC) WHERE entity_id IS NOT NULL;
CREATE INDEX comments_by_relation ON comments (relation_id, created_at DESC) WHERE relation_id IS NOT NULL;
CREATE INDEX comments_by_entity_type ON comments (entity_type_id, created_at DESC) WHERE entity_type_id IS NOT NULL;
CREATE INDEX comments_by_relation_type ON comments (relation_type_id, created_at DESC) WHERE relation_type_id IS NOT NULL;
-- And the game's whole log, which is the other read: what has been
-- thought about this game lately, whatever it was about.
CREATE INDEX comments_by_project ON comments (project_id, created_at DESC);

-- +goose Down
DROP TABLE comments;
ALTER TABLE relations DROP CONSTRAINT relations_id_project_key;
