-- A comment can no longer be written against one relation.
--
-- **It was a place to write where nothing could read.** The three other
-- targets each have a page that draws their log; an edge has none, so a
-- note left on one was reachable by `comments.list` and by nothing a
-- designer opens. The bundle warned about it, which is the shape of a
-- trap with a sign next to it: a mechanism nothing reads is a lie, and
-- the sign does not make it true.
--
-- What belongs there instead: a fact a player could meet is a field on
-- the edge, which a query, a view and the repair can all reach; a note
-- about how a kind of connection changed goes on the relation type or on
-- one of the entities, which do have pages.
--
-- Nothing is lost here. No comment in any instance named a relation when
-- this was written, which is what made it cheap to take out rather than
-- expensive to keep.

-- +goose Up
DROP INDEX comments_by_relation;

ALTER TABLE comments DROP CONSTRAINT comments_one_target;
ALTER TABLE comments DROP COLUMN relation_id;
ALTER TABLE comments ADD CONSTRAINT comments_one_target CHECK (
    (entity_id IS NOT NULL)::int
    + (entity_type_id IS NOT NULL)::int
    + (relation_type_id IS NOT NULL)::int
    = 1
);

-- The unique key existed so a comment could point at an edge, and
-- nothing else in the schema names a relation from outside.
ALTER TABLE relations DROP CONSTRAINT relations_id_project_key;

-- +goose Down
ALTER TABLE relations ADD CONSTRAINT relations_id_project_key UNIQUE (id, project_id);

ALTER TABLE comments DROP CONSTRAINT comments_one_target;
ALTER TABLE comments ADD COLUMN relation_id uuid;
ALTER TABLE comments ADD CONSTRAINT comments_relation_fk
    FOREIGN KEY (relation_id, project_id) REFERENCES relations (id, project_id) ON DELETE CASCADE;
ALTER TABLE comments ADD CONSTRAINT comments_one_target CHECK (
    (entity_id IS NOT NULL)::int
    + (relation_id IS NOT NULL)::int
    + (entity_type_id IS NOT NULL)::int
    + (relation_type_id IS NOT NULL)::int
    = 1
);
CREATE INDEX comments_by_relation ON comments (relation_id, created_at DESC) WHERE relation_id IS NOT NULL;
