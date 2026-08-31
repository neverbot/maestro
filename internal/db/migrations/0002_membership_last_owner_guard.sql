-- Task 8's Go-level ErrLastOwner guard (projects.SetRole, RemoveMember)
-- covers the only two paths this package offers to remove or demote an
-- owner, but memberships.user_id is ON DELETE CASCADE: deleting a user
-- row deletes their memberships underneath the Go layer entirely, and a
-- sole owner deleted that way leaves a project with zero owners — the
-- unrecoverable state the Go guard exists to prevent. Nothing deletes
-- users today, so this is latent, but it goes live the moment a user
-- deletion endpoint lands, and by then any orphans it created are already
-- committed. This migration closes it at the one layer the cascade cannot
-- bypass: the database itself.
-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION memberships_require_owner() RETURNS trigger AS $$
BEGIN
    -- Skip the check when the project itself is gone. Deleting a project
    -- cascades to every one of its memberships, including its last
    -- owner's, and that is a legitimate way for an owner membership row
    -- to disappear — without this escape hatch, the final DELETE in a
    -- project's own deletion cascade would trip this trigger and abort
    -- the deletion it is not meant to guard against.
    IF NOT EXISTS (SELECT 1 FROM projects WHERE id = OLD.project_id) THEN
        RETURN NULL;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM memberships
        WHERE project_id = OLD.project_id AND role = 'owner'
    ) THEN
        RAISE EXCEPTION 'project % must keep at least one owner', OLD.project_id
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- A constraint trigger, not a plain one, and DEFERRABLE INITIALLY
-- DEFERRED: checking at end-of-transaction rather than after each
-- individual row change is what lets a legitimate multi-statement
-- transaction (demote the old owner, promote a new one, in either order)
-- pass through a momentary zero-owner state without tripping this guard,
-- the same forgiving-until-commit behaviour Postgres's own deferrable
-- foreign keys and unique constraints already give every other invariant
-- in this schema.
CREATE CONSTRAINT TRIGGER memberships_require_owner_trigger
    AFTER DELETE OR UPDATE ON memberships
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    WHEN (OLD.role = 'owner')
    EXECUTE FUNCTION memberships_require_owner();

-- +goose Down
DROP TRIGGER memberships_require_owner_trigger ON memberships;
DROP FUNCTION memberships_require_owner();
