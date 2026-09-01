-- The four metamodel tables. Maestro ships no built-in notion of "quest"
-- or "zone": a game declares its own entity_types and relation_types,
-- each carrying a field_schema (a jsonb array of field declarations), and
-- entities and relations instance them. Every row is scoped by
-- project_id, so every query over these tables filters on it.
-- +goose Up
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
    updated_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL
);
-- Keys are the stable handle an agent re-seeds against, and they are
-- matched case-insensitively so a second run with different casing
-- collides with the existing row instead of creating a twin.
CREATE UNIQUE INDEX entity_types_key_key ON entity_types (project_id, lower(key));

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
    updated_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX relation_types_key_key ON relation_types (project_id, lower(key));

CREATE TABLE entities (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- RESTRICT, not CASCADE: dropping an entity type that still has
    -- instances must fail loudly rather than silently delete content.
    entity_type_id uuid NOT NULL REFERENCES entity_types (id) ON DELETE RESTRICT,
    key            text NOT NULL,
    name           text NOT NULL,
    fields         jsonb NOT NULL DEFAULT '{}'::jsonb,
    invalid        boolean NOT NULL DEFAULT false,
    version        integer NOT NULL DEFAULT 1,
    search         tsvector,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX entities_key_key ON entities (project_id, entity_type_id, lower(key));
CREATE INDEX entities_type_idx ON entities (entity_type_id);
CREATE INDEX entities_search_idx ON entities USING gin (search);
CREATE INDEX entities_fields_idx ON entities USING gin (fields jsonb_path_ops);

CREATE TABLE relations (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    relation_type_id uuid NOT NULL REFERENCES relation_types (id) ON DELETE RESTRICT,
    -- CASCADE on both endpoints: an edge without both of its entities is
    -- not a relation, so deleting either end takes the edge with it.
    source_id        uuid NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    target_id        uuid NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    fields           jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid REFERENCES api_tokens (id) ON DELETE SET NULL
);
-- One edge per (type, source, target). The relation type is already
-- project-scoped, so this is per-project uniqueness too.
CREATE UNIQUE INDEX relations_edge_key ON relations (relation_type_id, source_id, target_id);
CREATE INDEX relations_source_idx ON relations (source_id);
CREATE INDEX relations_target_idx ON relations (target_id);

-- +goose Down
DROP TABLE relations;
DROP TABLE entities;
DROP TABLE relation_types;
DROP TABLE entity_types;
