-- The four metamodel tables. Maestro ships no built-in notion of "quest"
-- or "zone": a game declares its own entity_types and relation_types,
-- each carrying a field_schema (a jsonb array of field declarations), and
-- entities and relations instance them. Every row is scoped by
-- project_id, so every query over these tables filters on it.
--
-- Isolation between games is enforced here, in SQL, not in Go: every key
-- from one of these tables to a project-scoped parent is composite,
-- carrying project_id alongside the parent id, so no row can point at a
-- parent owned by another game. That covers the entity type of an
-- entity, the relation type and both endpoints of a relation, and
-- updated_by_token_id on all four tables, since api_tokens belongs to a
-- single project. A composite key needs a UNIQUE (id, project_id) on the
-- parent to reference, which is why the types and entities carry one on
-- top of their primary key, and why api_tokens gains one below.
--
-- Two references are deliberately *not* composite:
--
--   * project_id itself references projects (id), which is already a
--     primary key on its own; there is no outer scope to carry.
--   * updated_by_user_id references users (id). Users are global, not
--     project-scoped: the same account edits content in every game it is
--     a member of, so there is nothing to scope the key by.
-- +goose Up
-- api_tokens is project-scoped (0001_identity.sql), so the composite
-- updated_by_token_id keys below need a unique (id, project_id) target
-- to reference. It lives here rather than in a later migration because
-- this is where the referencing keys are created.
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_id_project_key UNIQUE (id, project_id);

CREATE TABLE entity_types (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    key          text NOT NULL,
    label        text NOT NULL,
    label_plural text NOT NULL,
    description  text NOT NULL DEFAULT '',
    color        text NOT NULL DEFAULT '',
    icon         text NOT NULL DEFAULT '',
    field_schema jsonb NOT NULL DEFAULT '[]'::jsonb,
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid,
    -- Composite, so the recorded token belongs to this row's own game.
    -- The SET NULL names its column: project_id is NOT NULL and must
    -- survive the token's deletion untouched.
    FOREIGN KEY (updated_by_token_id, project_id)
        REFERENCES api_tokens (id, project_id) ON DELETE SET NULL (updated_by_token_id),
    -- The target of the composite key from entities.
    UNIQUE (id, project_id)
);
-- Keys are the stable handle an agent re-seeds against, and they are
-- matched case-insensitively so a second run with different casing
-- collides with the existing row instead of creating a twin.
CREATE UNIQUE INDEX entity_types_key_key ON entity_types (project_id, lower(key));
CREATE TRIGGER entity_types_set_updated_at
    BEFORE UPDATE ON entity_types
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE relation_types (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    key            text NOT NULL,
    label          text NOT NULL,
    description    text NOT NULL DEFAULT '',
    source_type_ids uuid[] NOT NULL DEFAULT '{}',
    target_type_ids uuid[] NOT NULL DEFAULT '{}',
    -- Nullable: a relation type need not classify itself. When it does,
    -- the value is one of the roles the views are built on.
    semantic_role  text CHECK (semantic_role IN
                     ('prerequisite','unlock','containment','spatial','availability','reward')),
    field_schema   jsonb NOT NULL DEFAULT '[]'::jsonb,
    version        integer NOT NULL DEFAULT 1,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid,
    FOREIGN KEY (updated_by_token_id, project_id)
        REFERENCES api_tokens (id, project_id) ON DELETE SET NULL (updated_by_token_id),
    -- The target of the composite key from relations.
    UNIQUE (id, project_id)
);
CREATE UNIQUE INDEX relation_types_key_key ON relation_types (project_id, lower(key));
CREATE TRIGGER relation_types_set_updated_at
    BEFORE UPDATE ON relation_types
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE entities (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    entity_type_id uuid NOT NULL,
    key            text NOT NULL,
    name           text NOT NULL,
    fields         jsonb NOT NULL DEFAULT '{}'::jsonb,
    invalid        boolean NOT NULL DEFAULT false,
    version        integer NOT NULL DEFAULT 1,
    search         tsvector,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid,
    FOREIGN KEY (updated_by_token_id, project_id)
        REFERENCES api_tokens (id, project_id) ON DELETE SET NULL (updated_by_token_id),
    -- RESTRICT, not CASCADE: dropping an entity type that still has
    -- instances must fail loudly rather than silently delete content.
    -- Composite, so the type has to belong to the entity's own game.
    FOREIGN KEY (entity_type_id, project_id)
        REFERENCES entity_types (id, project_id) ON DELETE RESTRICT,
    -- The target of the composite keys from relations' two endpoints.
    UNIQUE (id, project_id)
);
CREATE UNIQUE INDEX entities_key_key ON entities (project_id, entity_type_id, lower(key));
CREATE INDEX entities_type_idx ON entities (entity_type_id);
CREATE INDEX entities_search_idx ON entities USING gin (search);
CREATE INDEX entities_fields_idx ON entities USING gin (fields jsonb_path_ops);
CREATE TRIGGER entities_set_updated_at
    BEFORE UPDATE ON entities
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE relations (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    relation_type_id uuid NOT NULL,
    source_id        uuid NOT NULL,
    target_id        uuid NOT NULL,
    fields           jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid,
    FOREIGN KEY (updated_by_token_id, project_id)
        REFERENCES api_tokens (id, project_id) ON DELETE SET NULL (updated_by_token_id),
    FOREIGN KEY (relation_type_id, project_id)
        REFERENCES relation_types (id, project_id) ON DELETE RESTRICT,
    -- CASCADE on both endpoints: an edge without both of its entities is
    -- not a relation, so deleting either end takes the edge with it. Both
    -- are composite, so an edge cannot straddle two games.
    FOREIGN KEY (source_id, project_id)
        REFERENCES entities (id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (target_id, project_id)
        REFERENCES entities (id, project_id) ON DELETE CASCADE
);
-- One edge per (type, source, target). The relation type is already
-- project-scoped, so this is per-project uniqueness too.
CREATE UNIQUE INDEX relations_edge_key ON relations (relation_type_id, source_id, target_id);
-- The unfiltered "show me this game's edges" listing, ordered by
-- created_at, is the most common read on the table expected to hold the
-- most rows; without a project-leading index it is a sequential scan
-- across every game's relations plus a sort. It also gives the
-- projects delete cascade an index to work from.
CREATE INDEX relations_project_idx ON relations (project_id, created_at);
CREATE INDEX relations_source_idx ON relations (source_id);
CREATE INDEX relations_target_idx ON relations (target_id);
CREATE TRIGGER relations_set_updated_at
    BEFORE UPDATE ON relations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
-- Dropping the four tables takes their constraints, indexes and triggers
-- with them; set_updated_at() itself belongs to 0001, whose Down drops
-- it. Only the constraint added to the pre-existing api_tokens has to be
-- undone by hand.
DROP TABLE relations;
DROP TABLE entities;
DROP TABLE relation_types;
DROP TABLE entity_types;
ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_id_project_key;
