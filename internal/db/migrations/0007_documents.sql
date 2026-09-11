-- The markdown domain: versioned prose attached to game content. Three
-- tables -- documents, their version snapshots, and the edges from a
-- document to the entities it is about.
--
-- Every row is scoped by project_id, and every key from one of these
-- tables to a project-scoped parent is composite, carrying project_id
-- alongside the parent id, so that the database -- not Go -- refuses a
-- cross-game row. That covers a version's document, a link's document,
-- a link's entity, and every *_by_token_id, since api_tokens belongs to
-- a single project (0001_identity.sql, and the api_tokens_id_project_key
-- unique constraint 0004_metamodel.sql added for exactly this). A
-- composite key needs a UNIQUE (id, project_id) on the parent to
-- reference, which is why documents carries one on top of its primary
-- key, exactly as entity_types and entities do.
--
-- Two references are deliberately not composite, for the reasons
-- 0004_metamodel.sql gives: project_id itself references projects (id),
-- which has no outer scope to carry, and the *_user_id columns reference
-- users (id), which is global rather than project-scoped.
--
-- What is pinned by a test, and where: documents_schema_test.go asserts
-- the cross-game refusals (link, version, and each audit column) with
-- their SQLSTATE, that revoking a token nulls only the token column,
-- that deleting an entity drops its links and keeps the document, that
-- deleting a document takes its versions and links, the case-insensitive
-- path key, the two unique keys, the updated_at trigger and the
-- generated search vector. That is the extent of what the test file
-- pins; the comments below also describe later tasks' Go behaviour
-- (documents.deleted_at, documents.current_version) and this file's own
-- index-access shape (correction 1 below), which no test in this file
-- exercises and which a reader should treat as forward-looking, not as
-- something this migration's own suite guards.
--
-- documents.search is GENERATED ALWAYS AS ... STORED, which
-- entities.search deliberately is not, and the asymmetry is a decision
-- rather than an inconsistency. An entity's indexed text is derived from
-- user-declared jsonb, and which of a row's fields hold text at all is
-- known only to the Go validator that has just read the type's schema
-- (searchTextOf, internal/metamodel/entities.go), so no expression over
-- the stored row can produce it. A document's is three plain text
-- columns, so Postgres can maintain it and no write path can forget to.
-- Retrofitting a generated column onto entities is not this migration's
-- job: 0006_weighted_entity_search.sql has already backfilled that
-- column, and the change would rewrite every row of every game for no
-- behaviour difference.
--
-- The configuration is 'simple', matching entities.search and
-- SearchEntities' plainto_tsquery('simple', ...) in
-- internal/db/queries/metamodel.sql. It is a literal rather than a
-- per-project setting for three reasons: a generated column requires an
-- IMMUTABLE expression and a per-project regconfig is not one; the two
-- indexes are meant to be unioned into one ranked list, and ts_rank
-- values from two different configurations are not comparable; and
-- metamodel.checkSearchQuery's "the query must contain a letter or a
-- digit" rule is tied to 'simple' having no stopword list. The cost is
-- no stemming: searching "history" does not find "histories". Recorded
-- here, not hidden; the search tool that surfaces this index is a later
-- task (.superpowers/plans/2026-09-02-markdown.md, Task 9) and says
-- so in its own description.
--
-- The weights are A for the title, B for the summary and C for the
-- body, so a document the query names outranks one that mentions the
-- word in a paragraph -- the same ordering 0006 gave entities, and read
-- by ts_rank's default weight array {D:0.1, C:0.2, B:0.4, A:1.0}. No
-- query passes an explicit array.
--
-- left(title, 131072), left(summary, 131072) and left(body_md, 131072)
-- bound every input to the expression at 131072 *characters* each, the
-- same number metamodel's searchTextLimit uses for bytes. Without a
-- bound a large value raises SQLSTATE 54000 (program_limit_exceeded) out
-- of the generated expression, which fails the INSERT or UPDATE itself
-- with a message about a tsvector the caller never mentioned; title and
-- summary need the same bound as the body; a multi-megabyte title is the
-- same "an oversized field makes the row unsavable" shape the metamodel
-- shipped and had to fix, just on a different column (correction 4).
--
-- For body_md this bound is not a backstop behind a smaller Go limit --
-- it is *tighter* than one. Task 2 sets markdown.MaxBodyBytes to 1 MiB
-- (1048576 *bytes*) against this bound of 131072 *characters*: the two
-- are not the same unit, so the ratio is 8 for ASCII prose and about 2.7
-- for CJK, and any body between 131072 characters and whatever the Go
-- bound allows is accepted by Go, written in full, and indexed by its
-- first 131072 characters only, with nothing recording the truncation.
-- The load-bearing half holds at every encoding -- the Go bound is the
-- looser one, so this truncation is what binds first; only the multiplier
-- depends on the script the game is written in. Task 9's search cannot see past that point in a long
-- document, and its tool description says so, the way the metamodel's
-- own search tool discloses its 128 KiB index bound (correction 2).

-- +goose Up
CREATE TABLE documents (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    path            text NOT NULL,
    kind            text NOT NULL DEFAULT '',
    title           text NOT NULL DEFAULT '',
    summary         text NOT NULL DEFAULT '',
    body_md         text NOT NULL DEFAULT '',
    frontmatter     jsonb NOT NULL DEFAULT '{}'::jsonb,
    current_version integer NOT NULL DEFAULT 1,
    -- A soft delete: the row and its history stay, so that writing to
    -- the path again continues the same document rather than starting a
    -- second one.
    deleted_at      timestamptz,
    search          tsvector GENERATED ALWAYS AS (
                        setweight(to_tsvector('simple', left(title, 131072)), 'A')
                     || setweight(to_tsvector('simple', left(summary, 131072)), 'B')
                     || setweight(to_tsvector('simple', left(body_md, 131072)), 'C')
                    ) STORED,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    created_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_by_token_id uuid,
    updated_by_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_token_id uuid,
    -- Composite, so a recorded token belongs to this row's own game. The
    -- SET NULL names its column: project_id is NOT NULL, and a bare
    -- SET NULL would try to null it too -- failing at token-revocation
    -- time rather than here.
    FOREIGN KEY (created_by_token_id, project_id)
        REFERENCES api_tokens (id, project_id) ON DELETE SET NULL (created_by_token_id),
    FOREIGN KEY (updated_by_token_id, project_id)
        REFERENCES api_tokens (id, project_id) ON DELETE SET NULL (updated_by_token_id),
    -- The target of the composite keys from document_versions and
    -- document_links.
    UNIQUE (id, project_id)
);
-- The path is the key, unique per game and matched case-insensitively,
-- exactly as entity_types_key_key and entities_key_key fold a key. It is
-- what makes a re-run of a seeding script update rather than duplicate.
-- Soft-deleted rows are covered too -- the index has no WHERE clause --
-- so a deleted path stays taken.
CREATE UNIQUE INDEX documents_path_key ON documents (project_id, lower(path));
CREATE INDEX documents_search_idx ON documents USING gin (search);
-- Created in the order the document listing is planned to sort by,
-- (project_id, path, id), so that a keyset page can be an index scan
-- rather than a sort of the whole game. Unlike 0005_entity_listing_index
-- this is not measured: the listing it is for does not exist yet
-- (.superpowers/plans/2026-09-02-markdown.md, Task 8), and whoever
-- writes it should check the plan rather than assume this index is in
-- it. It also gives the projects delete cascade an index to work from.
CREATE INDEX documents_listing_idx ON documents (project_id, path, id);
CREATE TRIGGER documents_set_updated_at
    BEFORE UPDATE ON documents
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE document_versions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Denormalised deliberately: reading one version, or a document's
    -- history, is where "join to the parent, then check" is one
    -- forgotten join away from serving another game's prose, and the
    -- isolation filter must not depend on anyone remembering it. The
    -- composite key below is what keeps this column honest -- a version
    -- row whose project_id disagrees with its document's cannot be
    -- inserted. It carries no FOREIGN KEY of its own to projects
    -- (correction 1): the composite key to documents below already
    -- forces it to name a real project, and a project's own cascade to
    -- documents already reaches every version transitively through the
    -- FOREIGN KEY (document_id, project_id) below, which cascades on
    -- document_id -- the index this table already carries. A second,
    -- direct FOREIGN KEY (project_id) REFERENCES projects would only add
    -- a second cascade path Postgres would run for the same rows, one
    -- with no project_id-leading index to serve it.
    project_id   uuid NOT NULL,
    document_id  uuid NOT NULL,
    version      integer NOT NULL,
    title        text NOT NULL DEFAULT '',
    summary      text NOT NULL DEFAULT '',
    body_md      text NOT NULL DEFAULT '',
    frontmatter  jsonb NOT NULL DEFAULT '{}'::jsonb,
    message      text NOT NULL DEFAULT '',
    -- A tombstone: the snapshot a soft delete appends, so that
    -- current_version never disagrees with the newest version row.
    deleted      boolean NOT NULL DEFAULT false,
    -- No updated_at, and so no trigger: a version is a snapshot and is
    -- never rewritten.
    author_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    author_token_id uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (document_id, project_id)
        REFERENCES documents (id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (author_token_id, project_id)
        REFERENCES api_tokens (id, project_id) ON DELETE SET NULL (author_token_id)
);
-- The second line of defence behind documents.current_version: two
-- writers cannot both insert version 4, whatever Go believes. It is also
-- the index every read of this table travels, since a version is only
-- ever addressed within its document. There is deliberately no
-- project_id-leading index (correction 1): project_id has no FOREIGN KEY
-- of its own that would need one, is a check on an already narrow row
-- set for every query this task's tests exercise, and this is the
-- fastest-growing of the three tables here -- one row per edit, forever
-- -- so an index only a rare project deletion would use is pure write
-- cost. That deletion instead reaches this table by cascading through
-- documents, using this same document_id-leading index; measured at
-- 50,000 rows across 20 games, that path took 1996us end to end against
-- 5835us with the direct FOREIGN KEY this correction removed.
CREATE UNIQUE INDEX document_versions_key ON document_versions (document_id, version);

CREATE TABLE document_links (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    document_id uuid NOT NULL,
    entity_id   uuid NOT NULL,
    role        text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (document_id, project_id)
        REFERENCES documents (id, project_id) ON DELETE CASCADE,
    -- ON DELETE CASCADE, so cutting a quest drops its links and leaves
    -- the document -- which is the point: the prose survives for
    -- whatever replaces the quest.
    FOREIGN KEY (entity_id, project_id)
        REFERENCES entities (id, project_id) ON DELETE CASCADE
);
-- One document has exactly one role on one entity: re-adding a link
-- updates the role rather than making a second edge. role is not part of
-- the key, which is the decision
-- .superpowers/plans/2026-09-02-markdown.md records and argues.
CREATE UNIQUE INDEX document_links_key ON document_links (document_id, entity_id);
-- The reverse lookup, for reading every document attached to one entity.
-- project_id leads it because every query filters on that first, and
-- both columns are equalities, so it also serves the entities delete
-- cascade.
CREATE INDEX document_links_entity_idx ON document_links (project_id, entity_id);

-- +goose Down
-- Dropping the three tables takes their constraints, indexes and the
-- trigger with them; set_updated_at() belongs to 0001, whose Down drops
-- it. The order is children first: document_links and document_versions
-- both key into documents.
DROP TABLE document_links;
DROP TABLE document_versions;
DROP TABLE documents;
