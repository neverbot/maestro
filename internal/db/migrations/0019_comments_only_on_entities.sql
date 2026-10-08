-- A comment can no longer be written against a type, of either kind.
--
-- **A type is a declaration, and a note about a declaration is a note
-- about the things that instance it.** The log is read beside content a
-- designer is working on: an ability, a place, a mission. A band on the
-- page that lists three hundred of them is a note nobody is looking for
-- while they are looking at any one of them, and the thing it would
-- usefully say ("these were imported from the old engine") belongs on
-- the rows it is about or in the type's own fields.
--
-- Entity type and relation type go together because the metamodel makes
-- them a pair: a log on one kind of declaration and not the other is an
-- asymmetry neither the screens nor the bundle could explain.
--
-- **This drops rows.** Unlike 0018, which took out a target no instance
-- had ever used, a comment on an entity type is something a person can
-- have written by hand. The deletion is first and deliberate: the
-- columns cannot go while rows still point through them, and a log kept
-- where nothing draws it is the trap 0018 exists to close.

-- +goose Up

DELETE FROM comments WHERE entity_type_id IS NOT NULL OR relation_type_id IS NOT NULL;

DROP INDEX comments_by_entity_type;
DROP INDEX comments_by_relation_type;

ALTER TABLE comments DROP CONSTRAINT comments_one_target;
ALTER TABLE comments DROP COLUMN entity_type_id;
ALTER TABLE comments DROP COLUMN relation_type_id;

-- One target left, so the rule is the column itself rather than a count
-- of how many of several are set. A CHECK over one nullable column is a
-- NOT NULL with extra words.
ALTER TABLE comments ALTER COLUMN entity_id SET NOT NULL;

-- +goose Down

ALTER TABLE comments ALTER COLUMN entity_id DROP NOT NULL;
ALTER TABLE comments ADD COLUMN entity_type_id uuid;
ALTER TABLE comments ADD COLUMN relation_type_id uuid;
ALTER TABLE comments ADD CONSTRAINT comments_entity_type_fk
    FOREIGN KEY (entity_type_id, project_id) REFERENCES entity_types (id, project_id) ON DELETE CASCADE;
ALTER TABLE comments ADD CONSTRAINT comments_relation_type_fk
    FOREIGN KEY (relation_type_id, project_id) REFERENCES relation_types (id, project_id) ON DELETE CASCADE;
ALTER TABLE comments ADD CONSTRAINT comments_one_target CHECK (
    (entity_id IS NOT NULL)::int
    + (entity_type_id IS NOT NULL)::int
    + (relation_type_id IS NOT NULL)::int
    = 1
);
CREATE INDEX comments_by_entity_type ON comments (entity_type_id, created_at DESC) WHERE entity_type_id IS NOT NULL;
CREATE INDEX comments_by_relation_type ON comments (relation_type_id, created_at DESC) WHERE relation_type_id IS NOT NULL;
