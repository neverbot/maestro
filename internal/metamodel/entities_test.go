package metamodel_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
)

// seedQuestType declares a Quest type with a required numeric field.
func seedQuestType(t *testing.T, svc *metamodel.Service, project uuid.UUID) {
	t.Helper()
	_, err := svc.UpsertEntityType(context.Background(), project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{
			{Key: "min_level", Type: metamodel.FieldNumber, Required: true},
			{Key: "summary", Type: metamodel.FieldLongText},
		},
	})
	assert.Must(t, err == nil, "seed quest type: %v", err)
}

// entityMatches reports whether the row's stored search vector matches a
// term. It reads the column through a query rather than the returned row
// because tsvector is what the column holds and what Task 6's search will
// query; asserting on anything else would pass while the index is empty.
func entityMatches(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, term string) bool {
	t.Helper()
	var hit bool
	err := pool.QueryRow(context.Background(),
		`SELECT search @@ plainto_tsquery('simple', $2) FROM entities WHERE id = $1`,
		id, term).Scan(&hit)
	assert.Must(t, err == nil, "read search vector: %v", err)
	return hit
}

func TestEntitiesArea(t *testing.T) {
	t.Parallel()
	a := newArea(t)

	t.Run("upsert entity validates against its type", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"min_level": float64(10), "summary": "Kill Hogger."},
		})
		assert.Must(t, err == nil, "UpsertEntity: %v", err)
		assert.Must(t, row.Version == 1, "Version = %d, want 1", row.Version)

		_, err = svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "broken", Name: "Broken",
			Fields: map[string]any{"min_level": "ten"},
		})
		assert.Must(t, errors.Is(err, metamodel.ErrSchemaViolation), "err = %v, want ErrSchemaViolation", err)
		if _, err := svc.EntityByKey(ctx, project, "quest", "broken"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("a refused row must store nothing, got %v", err)
		}
	})

	t.Run("upsert entity reports an unknown type", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		project := newProject(t, pool)

		_, err := svc.UpsertEntity(context.Background(), project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger",
		})
		assert.Must(t, errors.Is(err, metamodel.ErrNotFound), "err = %v, want ErrNotFound", err)
		// The message has to name the type, because "not_found" alone leaves a
		// seeding agent unable to tell a missing type from a missing entity.
		assert.Must(t, strings.Contains(err.Error(), `no entity type "quest"`), "err = %v, want it to name the missing type", err)
	})

	t.Run("upsert entity is idempotent and bumps version", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		in := metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"min_level": float64(10)},
		}
		first, err := svc.UpsertEntity(ctx, project, in)
		assert.Must(t, err == nil, "first: %v", err)

		in.ExpectedVersion = ptrInt32(1)
		in.Name = "Wanted: Hogger (revised)"
		second, err := svc.UpsertEntity(ctx, project, in)
		assert.Must(t, err == nil, "second: %v", err)
		assert.Must(t, second.ID == first.ID, "a re-seed created a duplicate row")
		assert.Must(t, second.Version == 2, "Version = %d, want 2", second.Version)
		assert.Must(t, second.Name == "Wanted: Hogger (revised)", "Name = %q, want the revised one", second.Name)
	})

	t.Run("an upsert replaces the whole field map", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"min_level": float64(10), "summary": "Kill Hogger."},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}

		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields:          map[string]any{"min_level": float64(12)},
			ExpectedVersion: ptrInt32(1),
		})
		assert.Must(t, err == nil, "update: %v", err)
		var stored map[string]any
		if err := json.Unmarshal(row.Fields, &stored); err != nil {
			t.Fatalf("stored fields: %v", err)
		}
		_, kept := stored["summary"]
		assert.Must(t, !kept, "summary survived a write that did not name it: this is the "+
			"behaviour four tool descriptions state, so it is asserted here and not inferred")
		assert.Must(t, stored["min_level"] == float64(12), "min_level = %v, want the written 12", stored["min_level"])
	})

	t.Run("a merge writes the fields it names and keeps the rest", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"min_level": float64(10), "summary": "Kill Hogger."},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}

		// min_level is required and this item does not carry it: the write
		// lands because what the schema judges is the merged map, which is
		// the whole point of validating after the row has been read.
		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields:          map[string]any{"summary": "Kill Hogger, twice."},
			FieldsMode:      metamodel.FieldsMerge,
			ExpectedVersion: ptrInt32(1),
		})
		assert.Must(t, err == nil, "merge: %v", err)
		var stored map[string]any
		if err := json.Unmarshal(row.Fields, &stored); err != nil {
			t.Fatalf("stored fields: %v", err)
		}
		assert.Must(t, stored["min_level"] == float64(10), "min_level = %v, want the stored 10 — a merge dropped a field it did not name", stored["min_level"])
		assert.Must(t, stored["summary"] == "Kill Hogger, twice.", "summary = %v, want the written value", stored["summary"])
	})

	t.Run("a merge clears a field on an explicit null", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"min_level": float64(10), "summary": "Kill Hogger."},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}

		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields:          map[string]any{"summary": nil},
			FieldsMode:      metamodel.FieldsMerge,
			ExpectedVersion: ptrInt32(1),
		})
		assert.Must(t, err == nil, "merge: %v", err)
		var stored map[string]any
		if err := json.Unmarshal(row.Fields, &stored); err != nil {
			t.Fatalf("stored fields: %v", err)
		}
		_, kept := stored["summary"]
		assert.Must(t, !kept, "summary survived an explicit null: with omission no longer clearing a field, null is the only way left")
		assert.Must(t, stored["min_level"] == float64(10), "min_level = %v, want the stored 10", stored["min_level"])
	})

	t.Run("a merge needs no stored row to merge onto", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields:     map[string]any{"min_level": float64(10)},
			FieldsMode: metamodel.FieldsMerge,
		})
		assert.Must(t, err == nil, "create under merge: %v", err)
		assert.Must(t, row.Version == 1, "Version = %d, want 1", row.Version)
	})

	t.Run("a merge onto an invalid row is refused by its stale keys", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"min_level": float64(10), "summary": "Kill Hogger."},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
		// summary stops being declared; the row keeps its value and is
		// flagged. A merge then carries that value into the validator.
		if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
			Schema:          metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber, Required: true}},
			ExpectedVersion: ptrInt32(1),
		}); err != nil {
			t.Fatalf("shrink the schema: %v", err)
		}

		_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields:          map[string]any{"min_level": float64(11)},
			FieldsMode:      metamodel.FieldsMerge,
			ExpectedVersion: ptrInt32(1),
		})
		assert.Must(t, errors.Is(err, metamodel.ErrSchemaViolation), "err = %v, want ErrSchemaViolation", err)
		assert.Must(t, strings.Contains(err.Error(), "summary"), "err = %v, want it to name the stale key the repair has to take out", err)
	})

	t.Run("a merge clears a stale key with a null and the row is valid again", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"min_level": float64(10), "summary": "Kill Hogger."},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
			Schema:          metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber, Required: true}},
			ExpectedVersion: ptrInt32(1),
		}); err != nil {
			t.Fatalf("shrink the schema: %v", err)
		}

		// A null names the key, and naming an undeclared key is how one
		// row is taken out from under a schema edit without a repair over
		// the whole type.
		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields:          map[string]any{"summary": nil},
			FieldsMode:      metamodel.FieldsMerge,
			ExpectedVersion: ptrInt32(1),
		})
		assert.Must(t, err == nil, "merge: %v", err)
		assert.Must(t, !row.Invalid, "the row is still flagged after the stale key went")
		var stored map[string]any
		if err := json.Unmarshal(row.Fields, &stored); err != nil {
			t.Fatalf("stored fields: %v", err)
		}
		_, kept := stored["summary"]
		assert.Must(t, !kept, "the stale key survived the null that named it")
	})

	t.Run("an unknown fields mode is refused rather than read as replace", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"min_level": float64(10), "summary": "Kill Hogger."},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}

		_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields:          map[string]any{"min_level": float64(11)},
			FieldsMode:      "mrege",
			ExpectedVersion: ptrInt32(1),
		})
		assert.Must(t, err != nil, "a misspelled mode was accepted, and reading it as replace is the one mistake the argument exists to prevent")
		assert.Must(t, strings.Contains(err.Error(), "fields_mode"), "err = %v, want it to name the argument", err)
		stored, getErr := svc.EntityByKey(ctx, project, "quest", "hogger")
		assert.Must(t, getErr == nil, "read back: %v", getErr)
		assert.Must(t, stored.Version == 1, "Version = %d, want 1: a refused call wrote anyway", stored.Version)
	})

	t.Run("upsert entity rejects stale version", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(10)},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}

		_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger again",
			Fields:          map[string]any{"min_level": float64(11)},
			ExpectedVersion: ptrInt32(99),
		})
		var conflict *metamodel.VersionConflictError
		assert.Must(t, errors.As(err, &conflict) && conflict.Current == 1, "err = %v, want a *VersionConflictError carrying version 1", err)

		// A missing ExpectedVersion against an existing row is the blind
		// overwrite optimistic concurrency exists to stop, not an insert.
		_, err = svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger blindly",
			Fields: map[string]any{"min_level": float64(12)},
		})
		assert.Must(t, errors.Is(err, metamodel.ErrVersionConflict), "err = %v, want ErrVersionConflict", err)

		stored, err := svc.EntityByKey(ctx, project, "quest", "hogger")
		assert.Must(t, err == nil, "EntityByKey: %v", err)
		assert.Must(t, stored.Name == "Hogger" && stored.Version == 1, "a refused upsert changed the row: %+v", stored)
	})

	// TestEntitiesArea's "upsert entity refuses a respelled key" case is the
	// entity flavour of the entity-type rule: entities_key_key folds case, so
	// "hogger" addresses the row stored as "Hogger" and updating it under the
	// other spelling is refused rather than silently overwriting a handle
	// other rows and documents refer to.
	t.Run("upsert entity refuses a respelled key", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "Hogger", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(10)},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}

		_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "MINE",
			Fields:          map[string]any{"min_level": float64(1)},
			ExpectedVersion: ptrInt32(1),
		})
		requireFieldError(t, err, "key",
			`"hogger" already exists here spelled "Hogger", and keys are matched without regard to case: `+
				`use "Hogger" to update it, or pick a key that differs by more than capitalisation`)
		assert.Must(t, errors.Is(err, metamodel.ErrInvalidInput), "a respelled key must read as invalid_input, got %v", err)

		stored, err := svc.EntityByKey(ctx, project, "quest", "HOGGER")
		assert.Must(t, err == nil, "EntityByKey: %v", err)
		assert.Must(t, stored.Key == "Hogger" && stored.Name == "Hogger" && stored.Version == 1, "the refused upsert changed the row: %+v", stored)
	})

	// TestEntitiesArea's "a respelled entity key is named even when the
	// version is also stale" case pins the one job left to the spelling check
	// inside the locked pre-read.
	t.Run("a respelled entity key is named even when the version is also stale", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "Hogger", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(10)},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}

		_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "MINE",
			Fields:          map[string]any{"min_level": float64(1)},
			ExpectedVersion: ptrInt32(9),
		})
		assert.Must(t, !errors.Is(err, metamodel.ErrVersionConflict), "err = %v, want the respelling refusal rather than the version conflict", err)
		requireFieldError(t, err, "key",
			`"hogger" already exists here spelled "Hogger", and keys are matched without regard to case: `+
				`use "Hogger" to update it, or pick a key that differs by more than capitalisation`)
	})

	// TestEntitiesArea's "a race that would land an entity under another
	// spelling is refused" case is the hole the locked pre-read cannot close,
	// for entities.
	t.Run("a race that would land an entity under another spelling is refused", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)
		typ, err := svc.EntityTypeByKey(ctx, project, "quest")
		assert.Must(t, err == nil, "EntityTypeByKey: %v", err)

		rival, err := pool.Begin(ctx)
		assert.Must(t, err == nil, "begin: %v", err)
		defer func() { _ = rival.Rollback(ctx) }()
		if _, err := rival.Exec(ctx,
			`INSERT INTO entities (project_id, entity_type_id, key, name, fields)
			 VALUES ($1, $2, 'Hogger', 'Hogger', '{"min_level": 10}'::jsonb)`,
			project, typ.ID); err != nil {
			t.Fatalf("rival insert: %v", err)
		}

		// No version claimed: two creations racing into the folding unique
		// index, one of which loses. It used to claim the version the winner
		// lands on, which reached the post-write spelling check; a version
		// claim against a row the locked read cannot see is now refused
		// before the write, so the race this test is about is staged the way
		// it actually happens to a seeding agent.
		result := make(chan error, 1)
		go func() {
			_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
				TypeKey: "quest", Key: "hogger", Name: "MINE",
				Fields: map[string]any{"min_level": float64(1)},
			})
			result <- err
		}()

		select {
		case err := <-result:
			t.Fatalf("the upsert returned %v before the rival committed; it should have blocked", err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := rival.Commit(ctx); err != nil {
			t.Fatalf("rival commit: %v", err)
		}

		select {
		case err := <-result:
			requireFieldError(t, err, "key",
				`"hogger" already exists here spelled "Hogger", and keys are matched without regard to case: `+
					`use "Hogger" to update it, or pick a key that differs by more than capitalisation`)
		case <-time.After(10 * time.Second):
			t.Fatal("the upsert never returned after the rival committed")
		}

		// The refusal has to roll the write back, not merely report it.
		row, err := svc.EntityByKey(ctx, project, "quest", "hogger")
		assert.Must(t, err == nil, "EntityByKey: %v", err)
		assert.Must(t, row.Key == "Hogger" && row.Name == "Hogger" && row.Version == 1, "the losing writer changed the row: %+v", row)
	})

	// TestEntitiesArea's "an entity creation that loses the race for its key
	// is refused" case pins the compare-and-set in the upsert's own DO UPDATE.
	t.Run("an entity creation that loses the race for its key is refused", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)
		typ, err := svc.EntityTypeByKey(ctx, project, "quest")
		assert.Must(t, err == nil, "EntityTypeByKey: %v", err)

		rival, err := pool.Begin(ctx)
		assert.Must(t, err == nil, "begin: %v", err)
		defer func() { _ = rival.Rollback(ctx) }()
		if _, err := rival.Exec(ctx,
			`INSERT INTO entities (project_id, entity_type_id, key, name, fields)
			 VALUES ($1, $2, 'hogger', 'Theirs', '{"min_level": 10}'::jsonb)`,
			project, typ.ID); err != nil {
			t.Fatalf("rival insert: %v", err)
		}

		result := make(chan error, 1)
		go func() {
			_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
				TypeKey: "quest", Key: "hogger", Name: "Mine",
				Fields: map[string]any{"min_level": float64(1)},
			})
			result <- err
		}()

		select {
		case err := <-result:
			t.Fatalf("the upsert returned %v before the rival committed; it should have blocked", err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := rival.Commit(ctx); err != nil {
			t.Fatalf("rival commit: %v", err)
		}

		select {
		case err := <-result:
			var conflict *metamodel.VersionConflictError
			assert.Must(t, errors.As(err, &conflict) && conflict.Current == 1, "err = %v, want a *VersionConflictError carrying version 1", err)
		case <-time.After(10 * time.Second):
			t.Fatal("the upsert never returned after the rival committed")
		}

		row, err := svc.EntityByKey(ctx, project, "quest", "hogger")
		assert.Must(t, err == nil, "EntityByKey: %v", err)
		assert.Must(t, row.Name == "Theirs" && row.Version == 1, "the losing writer overwrote the row: %+v", row)
	})

	// TestEntitiesArea's "a malformed entity argument is invalid input" case
	// pins which wire code a row's own arguments are published under: a bad
	// key reported as schema_violation sends an agent to inspect entity
	// *values*, and a bad key falling through failureFor's default arm reports
	// internal_error, which tells it nothing at all.
	t.Run("a malformed entity argument is invalid input", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "main quest", Name: "",
			Fields: map[string]any{"min_level": float64(1)},
		})
		assert.Must(t, errors.Is(err, metamodel.ErrInvalidInput), "err = %v, want ErrInvalidInput", err)
		assert.Must(t, !errors.Is(err, metamodel.ErrSchemaViolation), "err = %v must not also read as a schema violation", err)
		// Both problems in one pass: an agent fixing a seed script must not
		// learn about the name only after the key is fixed.
		assert.Must(t, err.Error() == "invalid_input: key: must be letters, digits, underscores or hyphens, "+
			"starting with a letter or a digit; name: is required", "err = %v, want the key and the name reported together", err)

		// And the same fault inside a bulk batch carries the same code, not
		// internal_error.
		result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "main quest", Name: "A",
				Fields: map[string]any{"min_level": float64(1)}},
		}, metamodel.BulkPartial)
		assert.Must(t, err == nil, "UpsertEntities: %v", err)
		assert.Must(t, len(result.Failed) == 1 && result.Failed[0].Code == "invalid_input", "failures = %+v, want one invalid_input", result.Failed)
	})

	t.Run("upsert entity rejects an overlong name", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		_, err := svc.UpsertEntity(context.Background(), project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: strings.Repeat("é", 201),
			Fields: map[string]any{"min_level": float64(1)},
		})
		requireFieldError(t, err, "name", "must be at most 200 characters")

		// Exactly at the cap, counted in runes: an accented name must not be
		// cut shorter than a plain one.
		if _, err := svc.UpsertEntity(context.Background(), project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: strings.Repeat("é", 200),
			Fields: map[string]any{"min_level": float64(1)},
		}); err != nil {
			t.Fatalf("a 200-rune name must be accepted: %v", err)
		}
	})

	// TestEntitiesArea's "upsert entity writes and rewrites the search vector"
	// case pins the one column no database mechanism maintains.
	t.Run("upsert entity writes and rewrites the search vector", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"min_level": float64(10), "summary": "Riverpaw gnolls"},
		})
		assert.Must(t, err == nil, "create: %v", err)
		assert.Must(t, entityMatches(t, pool, row.ID, "Hogger"), "the name is not in the search vector")
		assert.Must(t, entityMatches(t, pool, row.ID, "gnolls"), "a text field's words are not in the search vector")

		// An update rewrites it: the old words must go and the new ones must
		// arrive, on the DO UPDATE arm as much as on the insert.
		updated, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger the Gnoll",
			Fields:          map[string]any{"min_level": float64(10), "summary": "Elwynn Forest"},
			ExpectedVersion: ptrInt32(1),
		})
		assert.Must(t, err == nil, "update: %v", err)
		assert.Must(t, !entityMatches(t, pool, updated.ID, "gnolls"), "the replaced field's words are still indexed")
		assert.Must(t, entityMatches(t, pool, updated.ID, "Elwynn"), "the new field's words were not indexed")
	})

	t.Run("bulk partial lands the good rows and reports the rest", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "Alpha",
				Fields: map[string]any{"min_level": float64(1), "summary": "alpha-secret"}},
			{TypeKey: "quest", Key: "b", Name: "Beta", Fields: map[string]any{"min_level": "nope"}},
			{TypeKey: "quest", Key: "c", Name: "Gamma",
				Fields: map[string]any{"min_level": float64(3), "summary": "gamma-secret"}},
		}, metamodel.BulkPartial)
		assert.Must(t, err == nil, "UpsertEntities: %v", err)
		assert.Must(t, len(result.Succeeded) == 2, "succeeded = %d, want 2", len(result.Succeeded))
		assert.Must(t, len(result.Failed) == 1, "failed = %d, want 1", len(result.Failed))
		failure := result.Failed[0]
		assert.Must(t, failure.Index == 1, "the failure must carry its index, got %d", failure.Index)
		assert.Must(t, failure.Key == "b", "the failure must carry its own key, got %q", failure.Key)
		assert.Must(t, failure.Code == "schema_violation", "code = %q, want schema_violation for a bad value", failure.Code)
		// The message names the offending path so the caller can retry that
		// item alone, and names nothing belonging to the items around it: a
		// batch report is the one place a row's content can leak into a
		// neighbour's error.
		assert.Must(t, strings.Contains(failure.Message, "fields.min_level"), "message = %q, want it to name fields.min_level", failure.Message)
		for _, leaked := range []string{"alpha-secret", "gamma-secret", "Alpha", "Gamma"} {
			assert.Must(t, !strings.Contains(failure.Message, leaked), "message = %q leaks %q from another item", failure.Message, leaked)
		}
		// The row after the failure must still have landed: item 200 landing
		// cannot depend on item 3.
		if _, err := svc.EntityByKey(ctx, project, "quest", "c"); err != nil {
			t.Fatalf("the row after the failure must still have landed: %v", err)
		}
		if _, err := svc.EntityByKey(ctx, project, "quest", "b"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("the failed row must not have landed, got %v", err)
		}
	})

	t.Run("bulk atomic rolls back everything on one failure", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": "nope"}},
		}, metamodel.BulkAtomic)
		assert.Must(t, err != nil, "an atomic batch with a bad row must fail as a whole")
		// The failing item is named, and the code survives the wrapping: an
		// agent must be able to tell which row it has to fix and how.
		assert.Must(t, errors.Is(err, metamodel.ErrSchemaViolation), "err = %v, want it to still read as a schema violation", err)
		assert.Must(t, strings.Contains(err.Error(), `item 1 ("b")`), "err = %v, want it to name the failing item", err)
		assert.Must(t, len(result.Succeeded) == 0 && len(result.Failed) == 0, "a failed atomic batch reports nothing as done: %+v", result)
		if _, err := svc.EntityByKey(ctx, project, "quest", "a"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("the good row of a failed atomic batch must have been rolled back, got %v", err)
		}
	})

	t.Run("bulk atomic lands every row together", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": float64(2)}},
		}, metamodel.BulkAtomic)
		assert.Must(t, err == nil, "UpsertEntities: %v", err)
		assert.Must(t, len(result.Succeeded) == 2 && len(result.Failed) == 0, "result = %+v, want two rows and no failures", result)
		for _, key := range []string{"a", "b"} {
			if _, err := svc.EntityByKey(ctx, project, "quest", key); err != nil {
				t.Fatalf("entity %q: %v", key, err)
			}
		}
	})

	// TestEntitiesArea's "bulk rejects an unknown mode" case keeps an
	// unrecognised mode from meaning "partial".
	t.Run("bulk rejects an unknown mode", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		_, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
		}, metamodel.BulkMode("atomic "))
		requireFieldError(t, err, "mode", `must be "partial" or "atomic"`)
		assert.Must(t, errors.Is(err, metamodel.ErrInvalidInput), "err = %v, want ErrInvalidInput", err)
		if _, err := svc.EntityByKey(ctx, project, "quest", "a"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("a refused batch must write nothing, got %v", err)
		}

		// The empty mode is the documented default and stays partial: an
		// omitted argument is not a typo.
		if _, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
		}, ""); err != nil {
			t.Fatalf("an omitted mode must default to partial: %v", err)
		}
		if _, err := svc.EntityByKey(ctx, project, "quest", "a"); err != nil {
			t.Fatalf("the default batch did not land: %v", err)
		}
	})

	// TestEntitiesArea's "bulk partial stops when the caller is gone" case
	// pins the one thing a partial batch owes a cancelled caller.
	t.Run("bulk partial stops when the caller is gone", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": float64(2)}},
		}, metamodel.BulkPartial)
		assert.Must(t, errors.Is(err, context.Canceled), "err = %v, want context.Canceled", err)
		assert.Must(t, len(result.Failed) == 0, "a cancelled batch reports no per-item failures, got %+v", result.Failed)
		if _, err := svc.EntityByKey(context.Background(), project, "quest", "a"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("nothing may be written after the caller is gone, got %v", err)
		}
	})

	// TestEntitiesArea's "no entity event is published for a rolled back
	// batch" case pins publish-after- commit on the path where it is easiest
	// to get wrong: the atomic batch writes and commits in different
	// functions, so a publish placed beside the write would announce rows that
	// are about to vanish.
	t.Run("no entity event is published for a rolled back batch", func(t *testing.T) {
		pool := a.pool
		hub := realtime.NewHub()
		svc := metamodel.New(pool, hub)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		sub := hub.Subscribe(project, "viewer", true)
		defer hub.Unsubscribe(sub)
		// The type declaration above published its own event before the
		// subscription existed, so the channel starts empty here.

		if _, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": "nope"}},
		}, metamodel.BulkAtomic); err == nil {
			t.Fatal("the batch must fail")
		}
		requireNothing(t, sub, "the atomic batch rolled back")

		// The subscriber is a viewer *and* a token caller: the gating stated
		// in events.go excludes neither, so a passing write reaches it.
		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)},
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if got := receive(t, sub); got.Kind != "entity.upserted" {
			t.Fatalf("Kind = %q, want entity.upserted", got.Kind)
		}
	})

	// TestEntitiesArea's "a bulk batch announces every row it landed" case
	// covers the partial path's own publication: one identity event per row
	// that landed, and none for the row that did not. A subscriber's only
	// correct reaction to an entity event is to re-read the row it names, so a
	// batch announcing a count — or announcing rows it rejected — tells it to
	// re-read the wrong thing.
	t.Run("a bulk batch announces every row it landed", func(t *testing.T) {
		pool := a.pool
		hub := realtime.NewHub()
		svc := metamodel.New(pool, hub)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		sub := hub.Subscribe(project, "viewer", true)
		defer hub.Unsubscribe(sub)

		if _, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": "nope"}},
			{TypeKey: "quest", Key: "c", Name: "C", Fields: map[string]any{"min_level": float64(3)}},
		}, metamodel.BulkPartial); err != nil {
			t.Fatalf("UpsertEntities: %v", err)
		}

		for _, wantKey := range []string{"a", "c"} {
			got := receive(t, sub)
			assert.Must(t, got.Kind == "entity.upserted", "Kind = %q, want entity.upserted", got.Kind)
			if key := payloadField(t, got, "key"); key != wantKey {
				t.Fatalf("payload key = %q, want %q", key, wantKey)
			}
		}
		requireNothing(t, sub, "the rejected row landed nothing")
	})

	// TestEntitiesArea's "entity events carry the stored identity" case pins
	// whose spelling an event reports. Keys are matched without regard to
	// case, so a caller may address type "Quest" as "quest"; the event must
	// carry the identity as stored, because a subscriber uses it to re-read a
	// row and the stored spelling is the one every other reader sees.
	t.Run("entity events carry the stored identity", func(t *testing.T) {
		pool := a.pool
		hub := realtime.NewHub()
		svc := metamodel.New(pool, hub)
		ctx := context.Background()
		project := newProject(t, pool)

		if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "Quest", Label: "Quest", LabelPlural: "Quests",
		}); err != nil {
			t.Fatalf("declare type: %v", err)
		}

		sub := hub.Subscribe(project, "viewer", true)
		defer hub.Unsubscribe(sub)

		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "Hogger", Name: "Hogger",
		})
		assert.Must(t, err == nil, "upsert: %v", err)

		got := receive(t, sub)
		assert.Must(t, got.Kind == "entity.upserted", "Kind = %q, want entity.upserted", got.Kind)
		assertIdentityPayload(t, "viewer", got, row.ID, "Hogger")
		if key := payloadField(t, got, "type_key"); key != "Quest" {
			t.Fatalf("payload type_key = %q, want the stored spelling %q", key, "Quest")
		}

		// A removal declares the same fields, so it has to fill them: an
		// event carrying "" tells a client a row keyed empty string is gone.
		if err := svc.RemoveEntity(ctx, project, "quest", "hogger"); err != nil {
			t.Fatalf("remove: %v", err)
		}
		got = receive(t, sub)
		assert.Must(t, got.Kind == "entity.removed", "Kind = %q, want entity.removed", got.Kind)
		assertIdentityPayload(t, "viewer", got, row.ID, "Hogger")
		if key := payloadField(t, got, "type_key"); key != "Quest" {
			t.Fatalf("removal payload type_key = %q, want %q", key, "Quest")
		}
	})

	t.Run("remove entity", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(10)},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := svc.RemoveEntity(ctx, project, "quest", "hogger"); err != nil {
			t.Fatalf("RemoveEntity: %v", err)
		}
		if _, err := svc.EntityByKey(ctx, project, "quest", "hogger"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound after removal", err)
		}
		if err := svc.RemoveEntity(ctx, project, "quest", "hogger"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("removing it twice: err = %v, want ErrNotFound", err)
		}
		// A key that names no row, and a type key that names no type: two
		// different not-founds, both named rather than generic, because an
		// agent told only "not found" cannot tell which of the two to fix.
		if err := svc.RemoveEntity(ctx, project, "quest", "kobold-camp"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("removing an unknown key: err = %v, want ErrNotFound", err)
		}
		if err := svc.RemoveEntity(ctx, project, "monster", "hogger"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("removing under an undeclared type: err = %v, want ErrNotFound", err)
		} else if !strings.Contains(err.Error(), `no entity type "monster"`) {
			t.Fatalf("err = %v, want it to name the type that is missing", err)
		}
	})

	// TestEntitiesArea's "schema change flags an entity written through the
	// service" case is the sweep seen from the other side. TestTypesArea's
	// "schema change flags rows invalid without touching them" case pins what
	// the sweep must not touch, over rows written straight to the database;
	// this one pins that a row written through UpsertEntity — whose values
	// have been through Validate and back out of jsonb — is judged by the same
	// sweep when a new required field appears.
	t.Run("schema change flags an entity written through the service", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(10)},
		}); err != nil {
			t.Fatalf("seed entity: %v", err)
		}

		// Add a required field: the stored row no longer satisfies the schema.
		if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
			Schema: metamodel.Schema{
				{Key: "min_level", Type: metamodel.FieldNumber, Required: true},
				{Key: "summary", Type: metamodel.FieldLongText},
				{Key: "faction", Type: metamodel.FieldText, Required: true},
			},
			ExpectedVersion: ptrInt32(1),
		}); err != nil {
			t.Fatalf("evolve schema: %v", err)
		}

		row, err := svc.EntityByKey(ctx, project, "quest", "hogger")
		assert.Must(t, err == nil, "EntityByKey: %v", err)
		assert.Must(t, row.Invalid, "the row should be flagged invalid after the schema change")
		assert.Must(t, row.Name == "Hogger", "the row's data must not have been altered")

		// Writing the row again with the missing value clears the flag: the
		// upsert resets invalid because it has just judged these values.
		fixed, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger",
			Fields:          map[string]any{"min_level": float64(10), "faction": "Alliance"},
			ExpectedVersion: ptrInt32(1),
		})
		assert.Must(t, err == nil, "fix the row: %v", err)
		assert.Must(t, !fixed.Invalid, "a row that has just been validated must not stay flagged")
	})

	t.Run("entities are scoped to their project", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		mine, theirs := newProject(t, pool), newProject(t, pool)
		seedQuestType(t, svc, mine)
		seedQuestType(t, svc, theirs)

		if _, err := svc.UpsertEntity(ctx, mine, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(10)},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}

		if _, err := svc.EntityByKey(ctx, theirs, "quest", "hogger"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("an entity must not be visible from another project, got %v", err)
		}
		// Knowing the address is not authority either.
		if err := svc.RemoveEntity(ctx, theirs, "quest", "hogger"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("an entity must not be removable from another project, got %v", err)
		}
		if _, err := svc.EntityByKey(ctx, mine, "quest", "hogger"); err != nil {
			t.Fatalf("the owning project lost its entity: %v", err)
		}

		// The same key in two games is two entities, not a collision.
		if _, err := svc.UpsertEntity(ctx, theirs, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(10)},
		}); err != nil {
			t.Fatalf("the other project must be free to use the same key: %v", err)
		}
	})

	// TestEntitiesArea's "an entity actor from another game is named" case
	// covers the database's own backstop on entities: the composite FOREIGN
	// KEY (updated_by_token_id, project_id) refuses a token scoped to another
	// game, and the refusal has to say so rather than surface a raw SQLSTATE
	// 23503 into a log nobody can act on.
	t.Run("an entity actor from another game is named", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		mine, theirs := newProject(t, pool), newProject(t, pool)
		seedQuestType(t, svc, mine)

		foreign := newToken(t, pool, theirs)
		_, err := svc.UpsertEntity(ctx, mine, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(10)},
			Actor:  metamodel.Actor{TokenID: &foreign},
		})
		assert.Must(t, errors.Is(err, metamodel.ErrActorNotInGame), "err = %v, want ErrActorNotInGame", err)
		if _, err := svc.EntityByKey(ctx, mine, "quest", "hogger"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("the refused write must store nothing, got %v", err)
		}

		// A token writing to its own game is an ordinary write, and the actor
		// is recorded: the mapping cannot be refusing token actors in general.
		own := newToken(t, pool, mine)
		row, err := svc.UpsertEntity(ctx, mine, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(10)},
			Actor:  metamodel.Actor{TokenID: &own},
		})
		assert.Must(t, err == nil, "a token writing to its own game: %v", err)
		assert.Must(t, row.UpdatedByTokenID != nil && *row.UpdatedByTokenID == own, "UpdatedByTokenID = %v, want %v", row.UpdatedByTokenID, own)
	})

	// TestEntitiesArea's "a cancelled batch does not report the item in flight
	// as a server fault" case pins the second half of the cancellation
	// contract.
	t.Run("a cancelled batch does not report the item in flight as a server fault", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		base := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		blocked, err := svc.UpsertEntity(base, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": float64(2)},
		})
		assert.Must(t, err == nil, "seed the row the batch will block on: %v", err)

		rival, err := pool.Begin(base)
		assert.Must(t, err == nil, "begin: %v", err)
		defer func() { _ = rival.Rollback(base) }()
		// **A row lock and not a write**, and the difference is
		// 0013_analysis.sql's doing. Since the design counter arrived, every
		// write to a game also updates that game's projects row, so an open
		// transaction that has *written* holds a lock on the whole game and
		// the batch below would block on item 0 rather than on item 1 --
		// which is a true report of where it stopped and the wrong fixture
		// for this test, whose subject is an item whose own row is
		// contended. SELECT FOR UPDATE takes exactly the lock this test
		// means: item 1's row, and nothing wider.
		if _, err := rival.Exec(base,
			`SELECT id FROM entities WHERE id = $1 FOR UPDATE`, blocked.ID); err != nil {
			t.Fatalf("rival row lock: %v", err)
		}

		ctx, cancel := context.WithCancel(base)
		type outcome struct {
			result metamodel.BulkResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
				{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
				{TypeKey: "quest", Key: "b", Name: "Mine",
					Fields: map[string]any{"min_level": float64(2)}, ExpectedVersion: ptrInt32(1)},
				{TypeKey: "quest", Key: "c", Name: "C", Fields: map[string]any{"min_level": float64(3)}},
			}, metamodel.BulkPartial)
			done <- outcome{result, err}
		}()

		select {
		case got := <-done:
			t.Fatalf("the batch returned (%+v, %v) while item 1's row was locked", got.result, got.err)
		case <-time.After(300 * time.Millisecond):
		}
		cancel()

		select {
		case got := <-done:
			assert.Must(t, errors.Is(got.err, context.Canceled), "err = %v, want context.Canceled", got.err)
			assert.Must(t, strings.Contains(got.err.Error(), "item 1"), "err = %v, want it to name the item the batch stopped at", got.err)
			for _, failure := range got.result.Failed {
				t.Fatalf("a cancellation must not be reported as a per-item failure, got %+v", failure)
			}
			// Partial mode's contract: what landed before the cancellation is
			// returned rather than hidden, and nothing after it ran.
			assert.Must(t, len(got.result.Succeeded) == 1 && got.result.Succeeded[0].Key == "a", "Succeeded = %+v, want only item 0", got.result.Succeeded)
		case <-time.After(10 * time.Second):
			t.Fatal("the batch never returned after the cancellation")
		}

		if _, err := svc.EntityByKey(base, project, "quest", "c"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("the item after the cancellation must not have run, got %v", err)
		}
	})

	// TestEntitiesArea's "a list of text is searchable" case pins the one
	// text-bearing field type that is not a plain string.
	t.Run("a list of text is searchable", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)

		if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
			Schema: metamodel.Schema{
				{Key: "summary", Type: metamodel.FieldLongText},
				{Key: "tags", Type: metamodel.FieldListText},
				{Key: "difficulty", Type: metamodel.FieldEnum, Options: []string{"easy", "heroic"}},
				{Key: "min_level", Type: metamodel.FieldNumber},
			},
		}); err != nil {
			t.Fatalf("declare type: %v", err)
		}

		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{
				"summary":    "Kill gnolls",
				"tags":       []any{"elite", "dungeon"},
				"difficulty": "heroic",
				"min_level":  float64(10),
			},
		})
		assert.Must(t, err == nil, "create: %v", err)
		for _, term := range []string{"Hogger", "gnolls", "elite", "dungeon", "heroic"} {
			assert.Must(t, entityMatches(t, pool, row.ID, term), "%q is not in the search vector", term)
		}

		// An update drops the old elements as it does the old words of any
		// other field: the column is rewritten, not added to.
		updated, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields:          map[string]any{"tags": []any{"raid"}},
			ExpectedVersion: ptrInt32(1),
		})
		assert.Must(t, err == nil, "update: %v", err)
		assert.Must(t, !entityMatches(t, pool, updated.ID, "elite"), "a removed element is still indexed")
		assert.Must(t, entityMatches(t, pool, updated.ID, "raid"), "the new element was not indexed")
	})

	// TestEntitiesArea's "the search vector is the same for the same values"
	// case pins the key sort in searchTextOf.
	t.Run("the search vector is the same for the same values", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)

		schema := metamodel.Schema{}
		for _, key := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"} {
			schema = append(schema, metamodel.Field{Key: key, Type: metamodel.FieldText})
		}
		if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests", Schema: schema,
		}); err != nil {
			t.Fatalf("declare type: %v", err)
		}
		fields := map[string]any{
			"alpha": "one", "bravo": "two", "charlie": "three",
			"delta": "four", "echo": "five", "foxtrot": "six",
		}

		var first string
		for i := range 6 {
			row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
				TypeKey: "quest", Key: fmt.Sprintf("q%d", i), Name: "Same", Fields: fields,
			})
			assert.Must(t, err == nil, "create %d: %v", i, err)
			got := searchColumn(t, pool, row.ID)
			if i == 0 {
				first = got
				continue
			}
			assert.Must(t, got == first, "two rows with identical values were indexed differently:\n %s\n %s", first, got)
		}
	})

	// TestEntitiesArea's "bulk events carry the stored identity" case extends
	// the single path's pin to the other two.
	t.Run("bulk events carry the stored identity", func(t *testing.T) {
		for _, mode := range []metamodel.BulkMode{metamodel.BulkPartial, metamodel.BulkAtomic} {
			t.Run(string(mode), func(t *testing.T) {
				pool := a.pool
				hub := realtime.NewHub()
				svc := metamodel.New(pool, hub)
				ctx := context.Background()
				project := newProject(t, pool)

				if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
					Key: "Quest", Label: "Quest", LabelPlural: "Quests",
				}); err != nil {
					t.Fatalf("declare type: %v", err)
				}

				sub := hub.Subscribe(project, "viewer", true)
				defer hub.Unsubscribe(sub)

				// The type is addressed as "quest" and the key as "Hogger":
				// both spellings differ from the stored ones in a way only
				// the event can get wrong.
				if _, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
					{TypeKey: "quest", Key: "Hogger", Name: "Hogger"},
				}, mode); err != nil {
					t.Fatalf("UpsertEntities: %v", err)
				}

				got := receive(t, sub)
				assert.Must(t, got.Kind == "entity.upserted", "Kind = %q, want entity.upserted", got.Kind)
				if key := payloadField(t, got, "type_key"); key != "Quest" {
					t.Fatalf("payload type_key = %q, want the stored spelling %q", key, "Quest")
				}
				if key := payloadField(t, got, "key"); key != "Hogger" {
					t.Fatalf("payload key = %q, want the stored spelling %q", key, "Hogger")
				}
			})
		}
	})

	// TestTypesArea's "a creation that loses its key to another spelling is
	// named as a respelling" case reaches conflictOnEntityKey's respelling
	// arm, the one check on the entity path that nothing else exercised.
	t.Run("an entity losing its key to another spelling is named as a respelling", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)
		typ, err := svc.EntityTypeByKey(ctx, project, "quest")
		assert.Must(t, err == nil, "EntityTypeByKey: %v", err)

		rival, err := pool.Begin(ctx)
		assert.Must(t, err == nil, "begin: %v", err)
		defer func() { _ = rival.Rollback(ctx) }()
		if _, err := rival.Exec(ctx,
			`INSERT INTO entities (project_id, entity_type_id, key, name, fields)
			 VALUES ($1, $2, 'Hogger', 'Theirs', '{"min_level": 10}'::jsonb)`,
			project, typ.ID); err != nil {
			t.Fatalf("rival insert: %v", err)
		}

		// No version claimed, so the DO UPDATE's guard (noVersion) is a
		// guaranteed mismatch and the upsert returns no row at all. It used
		// to claim a version the rival's row would not have, which reached
		// the same arm by a route a version claim can no longer take: one
		// against a row the locked read cannot see is refused before the
		// write.
		result := make(chan error, 1)
		go func() {
			_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
				TypeKey: "quest", Key: "hogger", Name: "MINE",
				Fields: map[string]any{"min_level": float64(1)},
			})
			result <- err
		}()

		select {
		case err := <-result:
			t.Fatalf("the upsert returned %v before the rival committed; it should have blocked", err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := rival.Commit(ctx); err != nil {
			t.Fatalf("rival commit: %v", err)
		}

		select {
		case err := <-result:
			requireFieldError(t, err, "key",
				`"hogger" already exists here spelled "Hogger", and keys are matched without regard to case: `+
					`use "Hogger" to update it, or pick a key that differs by more than capitalisation`)
			assert.Must(t, !errors.Is(err, metamodel.ErrVersionConflict), "err = %v must not read as a version conflict: the version is not what the "+
				"caller can act on here", err)
		case <-time.After(10 * time.Second):
			t.Fatal("the upsert never returned after the rival committed")
		}

		row, err := svc.EntityByKey(ctx, project, "quest", "hogger")
		assert.Must(t, err == nil, "EntityByKey: %v", err)
		assert.Must(t, row.Name == "Theirs" && row.Version == 1, "the losing writer changed the row: %+v", row)
	})

	// TestEntitiesArea's "the reported current entity version is the one the
	// write would have met" case pins the FOR UPDATE on
	// GetEntityByKeyForUpdate, as TestDocumentsArea's "the reported current
	// version is the one the write would have met" case does for types.
	t.Run("the reported current entity version is the one the write would have met", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(10)},
		})
		assert.Must(t, err == nil, "create: %v", err)

		rival, err := pool.Begin(ctx)
		assert.Must(t, err == nil, "begin: %v", err)
		defer func() { _ = rival.Rollback(ctx) }()
		if _, err := rival.Exec(ctx,
			`UPDATE entities SET version = version + 1, name = 'Theirs' WHERE id = $1`,
			row.ID); err != nil {
			t.Fatalf("rival update: %v", err)
		}

		// No ExpectedVersion, so the refusal is decided by the read alone and
		// the version reported is the read's answer, not the guard's.
		result := make(chan error, 1)
		go func() {
			_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
				TypeKey: "quest", Key: "hogger", Name: "Mine",
				Fields: map[string]any{"min_level": float64(1)},
			})
			result <- err
		}()

		select {
		case err := <-result:
			t.Fatalf("the upsert returned %v without waiting for the rival's row lock", err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := rival.Commit(ctx); err != nil {
			t.Fatalf("rival commit: %v", err)
		}

		select {
		case err := <-result:
			var conflict *metamodel.VersionConflictError
			assert.Must(t, errors.As(err, &conflict), "err = %v, want a *VersionConflictError", err)
			assert.Must(t, conflict.Current == 2, "Current = %d, want 2: the caller must be told the version its own write "+
				"would have met, not the one visible before the rival committed", conflict.Current)
		case <-time.After(10 * time.Second):
			t.Fatal("the upsert never returned after the rival committed")
		}
	})

	// TestEntitiesArea's "the entity queries that address a row by ID are
	// scoped to the project" case pins the two project filters
	// TestEntitiesArea's "entities are scoped to their project" case cannot
	// see.
	t.Run("the entity queries that address a row by ID are scoped to the project", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		mine, theirs := newProject(t, pool), newProject(t, pool)
		seedQuestType(t, svc, mine)

		row, err := svc.UpsertEntity(ctx, mine, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(10)},
		})
		assert.Must(t, err == nil, "create: %v", err)
		q := dbq.New(pool)

		if _, err := q.GetEntityByID(ctx, dbq.GetEntityByIDParams{
			ProjectID: theirs, ID: row.ID,
		}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("GetEntityByID handed another game's row out: err = %v, want pgx.ErrNoRows", err)
		}
		rows, err := q.DeleteEntity(ctx, dbq.DeleteEntityParams{ProjectID: theirs, ID: row.ID})
		assert.Must(t, err == nil, "DeleteEntity: %v", err)
		assert.Must(t, rows == 0, "DeleteEntity removed %d of another game's rows", rows)

		// The owning game still reads and deletes its own row, so neither
		// assertion above can be passing because the filter refuses everyone.
		if _, err := q.GetEntityByID(ctx, dbq.GetEntityByIDParams{ProjectID: mine, ID: row.ID}); err != nil {
			t.Fatalf("the owning game cannot read its own row: %v", err)
		}
		if rows, err := q.DeleteEntity(ctx, dbq.DeleteEntityParams{
			ProjectID: mine, ID: row.ID,
		}); err != nil || rows != 1 {
			t.Fatalf("the owning game cannot delete its own row: %d rows, %v", rows, err)
		}
	})

	// TestEntitiesArea's "a batch that repeats a key is diagnosed as such"
	// case covers the ordinary accident: an agent seeding from a file that
	// names one quest twice.
	t.Run("a batch that repeats a key is diagnosed as such", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "dup", Name: "First", Fields: map[string]any{"min_level": float64(2)}},
			{TypeKey: "quest", Key: "dup", Name: "Second", Fields: map[string]any{"min_level": float64(3)}},
			{TypeKey: "quest", Key: "DUP", Name: "Third", Fields: map[string]any{"min_level": float64(4)}},
			{TypeKey: "quest", Key: "c", Name: "C", Fields: map[string]any{"min_level": float64(5)}},
		}, metamodel.BulkPartial)
		assert.Must(t, err == nil, "UpsertEntities: %v", err)
		assert.Must(t, len(result.Failed) == 2, "failed = %+v, want the two repetitions", result.Failed)
		// The exact repeat and the differently-capitalised one are the same
		// fault: the folding index makes them one key, so both are reported
		// against the item that got there first.
		for i, want := range []struct {
			index int
			key   string
		}{{2, "dup"}, {3, "DUP"}} {
			failure := result.Failed[i]
			assert.Must(t, failure.Index == want.index && failure.Key == want.key, "failure = %+v, want index %d and its own key %q", failure, want.index, want.key)
			assert.Must(t, failure.Code == "invalid_input", "code = %q, want invalid_input: the caller claimed no version, and the "+
				"repetition is a fault in its own arguments", failure.Code)
			assert.Must(t, failure.Code != "version_conflict" && !strings.Contains(failure.Message, "current version"), "message = %q misdiagnoses a repeated key as a stale version", failure.Message)
			assert.Must(t, strings.Contains(failure.Message, "item 1"), "message = %q, want it to name the item the key collides with", failure.Message)
		}
		// The other three landed, and the row holds the first occurrence: a
		// duplicate must not overwrite the item it duplicates.
		assert.Must(t, len(result.Succeeded) == 3, "succeeded = %d, want the three items that were not repeated", len(result.Succeeded))
		row, err := svc.EntityByKey(ctx, project, "quest", "dup")
		assert.Must(t, err == nil, "EntityByKey: %v", err)
		assert.Must(t, row.Name == "First" && row.Version == 1, "row = %+v, want the first occurrence, written once", row)
	})

	// TestEntitiesArea's "an atomic batch that repeats a key is refused whole"
	// case is the same accident in the other mode, where it is worse: both
	// items pass their guards, both "succeed", and the caller is handed a
	// Succeeded list carrying the same row id twice — told that two rows
	// landed when one exists. An atomic batch cannot be satisfied as submitted
	// (the caller asked for N rows and at most N-1 can exist), so it is
	// refused before anything is written, which also makes the error
	// deterministic rather than dependent on which item ran first.
	t.Run("an atomic batch that repeats a key is refused whole", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "dup", Name: "First", Fields: map[string]any{"min_level": float64(2)}},
			{TypeKey: "quest", Key: "dup", Name: "Second", Fields: map[string]any{"min_level": float64(3)}},
		}, metamodel.BulkAtomic)
		assert.Must(t, errors.Is(err, metamodel.ErrInvalidInput), "err = %v, want ErrInvalidInput", err)
		requireFieldError(t, err, "items[2].key",
			`"dup" is already addressed by item 1 of this batch, and keys are matched without regard `+
				`to case: give one of the two items a different key, or merge them into one`)
		assert.Must(t, len(result.Succeeded) == 0 && len(result.Failed) == 0, "a refused atomic batch reports nothing as done: %+v", result)
		for _, key := range []string{"a", "dup"} {
			if _, err := svc.EntityByKey(ctx, project, "quest", key); !errors.Is(err, metamodel.ErrNotFound) {
				t.Fatalf("entity %q: a refused atomic batch must write nothing, got %v", key, err)
			}
		}

		// The version-chained repetition, which is where the atomic mode's
		// report goes wrong rather than merely its diagnosis: two items
		// addressing one row with the versions the other will produce both
		// pass their guards, and Succeeded comes back carrying the same row
		// id twice — the caller told two rows landed where one exists.
		chained, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "chain", Name: "Chain", Fields: map[string]any{"min_level": float64(1)},
		})
		assert.Must(t, err == nil, "seed the chained row: %v", err)
		result, err = svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "chain", Name: "Second", ExpectedVersion: ptrInt32(1),
				Fields: map[string]any{"min_level": float64(2)}},
			{TypeKey: "quest", Key: "chain", Name: "Third", ExpectedVersion: ptrInt32(2),
				Fields: map[string]any{"min_level": float64(3)}},
		}, metamodel.BulkAtomic)
		assert.Must(t, errors.Is(err, metamodel.ErrInvalidInput), "err = %v, want ErrInvalidInput; Succeeded = %+v", err, result.Succeeded)
		row, err := svc.EntityByKey(ctx, project, "quest", "chain")
		assert.Must(t, err == nil, "EntityByKey: %v", err)
		assert.Must(t, row.Version == chained.Version, "the refused batch moved the row to version %d", row.Version)

		// The same key under two different types is two rows, not a
		// repetition: what the unique index folds is (type, key).
		if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "zone", Label: "Zone", LabelPlural: "Zones",
		}); err != nil {
			t.Fatalf("declare a second type: %v", err)
		}
		if _, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "hogger", Name: "Hogger", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "zone", Key: "hogger", Name: "Hogger"},
		}, metamodel.BulkAtomic); err != nil {
			t.Fatalf("one key under two types is not a repetition: %v", err)
		}
	})

	// TestEntitiesArea's "an oversized field is stored whole and found by a
	// word near its start" case pins the one trade the search index is allowed
	// to make against a game's content.
	t.Run("an oversized field is stored whole and found by a word near its start", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		// Distinct words throughout, so every one of them takes its own
		// entry in the vector: repeated prose compresses to a handful of
		// lexemes and would never reach the cap.
		var lore strings.Builder
		for i := 0; lore.Len() < 1_500_000; i++ {
			fmt.Fprintf(&lore, "lorewordnumber%d ", i)
		}
		summary := "openingsigil " + lore.String() + "closingsigil"

		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "lore", Name: "The Long Story",
			Fields: map[string]any{"min_level": float64(1), "summary": summary},
		})
		assert.Must(t, err == nil, "a long field must not make the row unsavable: %v", err)

		stored, err := svc.EntityByKey(ctx, project, "quest", "lore")
		assert.Must(t, err == nil, "read back: %v", err)
		var fields struct {
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal(stored.Fields, &fields); err != nil {
			t.Fatalf("decode stored fields: %v", err)
		}
		assert.Must(t, fields.Summary == summary, "the stored field was altered: %d bytes stored, %d written",
			len(fields.Summary), len(summary))

		assert.Must(t, entityMatches(t, pool, row.ID, "openingsigil"), "a word near the start of a long field is not searchable")
		// The trade, asserted so it cannot be quietly widened or dropped: the
		// tail of a field this long is outside the index.
		assert.Must(t, !entityMatches(t, pool, row.ID, "closingsigil"), "the whole of an oversized field reached the index; the bound is gone")
	})

	// TestEntitiesArea's "a cancelled batch does not answer a repeated key
	// instead" case pins the order of the two checks at the top of the bulk
	// loop.
	t.Run("a cancelled batch does not answer a repeated key instead", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		base := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		ctx := &cancelWhenLanded{
			Context: base, t: t, pool: pool, project: project, key: "a",
			closed: make(chan struct{}),
		}

		result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "a", Name: "Again", Fields: map[string]any{"min_level": float64(2)}},
		}, metamodel.BulkPartial)

		assert.Must(t, errors.Is(err, context.Canceled), "err = %v, want context.Canceled", err)
		assert.Must(t, strings.Contains(err.Error(), "item 1"), "err = %v, want it to name the item the batch stopped at", err)
		for _, failure := range result.Failed {
			t.Fatalf("a batch that stopped must report no per-item failure, got %+v", failure)
		}
		// Partial mode's contract is unchanged: what landed before the
		// cancellation comes back rather than being hidden.
		assert.Must(t, len(result.Succeeded) == 1 && result.Succeeded[0].Key == "a", "Succeeded = %+v, want only item 0", result.Succeeded)
	})

	// TestEntitiesArea's "a repeat is refused even when the first occurrence
	// is doomed" case pins the corner of the repeated-key rule that costs an
	// agent a round trip, so that the core design's claim about it stays true.
	t.Run("a repeat is refused even when the first occurrence is doomed", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "x", Name: "Doomed",
				Fields: map[string]any{"min_level": "not a number"}},
			{TypeKey: "quest", Key: "x", Name: "Fine",
				Fields: map[string]any{"min_level": float64(1)}},
		}, metamodel.BulkPartial)
		assert.Must(t, err == nil, "a partial batch reports its failures rather than erroring: %v", err)
		assert.Must(t, len(result.Succeeded) == 0, "Succeeded = %+v, want nothing written", result.Succeeded)
		assert.Must(t, len(result.Failed) == 2, "Failed = %+v, want both items reported", result.Failed)
		if result.Failed[0].Index != 0 || result.Failed[0].Code != "schema_violation" {
			t.Fatalf("Failed[0] = %+v, want index 0 as schema_violation", result.Failed[0])
		}
		if result.Failed[1].Index != 1 || result.Failed[1].Code != "invalid_input" {
			t.Fatalf("Failed[1] = %+v, want index 1 as invalid_input", result.Failed[1])
		}
		if _, err := svc.EntityByKey(ctx, project, "quest", "x"); !errors.Is(err, metamodel.ErrNotFound) {
			t.Fatalf("the key must be written by neither item, got %v", err)
		}
	})

	// TestEntitiesArea's "upsert entity refuses unprintable text before it
	// reaches postgres" case pins the write-side half of the rule
	// checkSearchQuery already applies on the read side: caller-supplied text
	// must be valid UTF-8 and free of control characters, refused as the
	// caller's own argument rather than left to Postgres.
	t.Run("upsert entity refuses unprintable text before it reaches postgres", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		for _, tc := range []struct {
			name, entityName, summary, wantCode, wantSubstr string
		}{
			{"a NUL in name", "Hog\x00ger", "fine", "invalid_input", "control character"},
			{"an invalid byte in name", "Hog\xffger", "fine", "invalid_input", "valid UTF-8"},
			{"a NUL in a longtext value", "Hogger", "Kill Hog\x00ger.", "schema_violation", "control character"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
					TypeKey: "quest", Key: fmt.Sprintf("k-%s", strings.ReplaceAll(tc.name, " ", "-")),
					Name:   tc.entityName,
					Fields: map[string]any{"min_level": float64(1), "summary": tc.summary},
				})
				var ve *metamodel.ValidationError
				assert.Must(t, errors.As(err, &ve), "err = %v, want a *ValidationError", err)
				code := ve.Code
				if code == "" {
					code = "schema_violation" // the zero value; see ValidationError.code
				}
				assert.Must(t, code == tc.wantCode, "code = %q, want %q", code, tc.wantCode)
				assert.Must(t, strings.Contains(err.Error(), tc.wantSubstr), "err = %v, want it to mention %q", err, tc.wantSubstr)
			})
		}

		// The one asymmetry: a name refuses a newline, a longtext value keeps
		// it, both deliberately.
		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "newline-in-name", Name: "Hog\nger",
			Fields: map[string]any{"min_level": float64(1)},
		}); err == nil || !strings.Contains(err.Error(), "control character") {
			t.Fatalf("a newline in name: err = %v, want it refused as a control character", err)
		}

		row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "newline-in-summary", Name: "Hogger",
			Fields: map[string]any{"min_level": float64(1), "summary": "Line one.\nLine two.\tTabbed."},
		})
		assert.Must(t, err == nil, "a newline and a tab in a longtext value must be accepted: %v", err)
		assert.Must(t, row.Key == "newline-in-summary", "row.Key = %q, want it stored", row.Key)
	})

	// TestEntitiesArea's "a bulk write reports what landed in a wire shape"
	// case pins Task 7's answer to "what does a successful batch tell an
	// agent". Until Task 7, BulkResult.Succeeded was the only record of it,
	// `json:"-"` and full of database columns, so a marshalled result reported
	// failures and nothing else — a perfect four-hundred-row batch answered
	// with `{}`.
	t.Run("a bulk write reports what landed in a wire shape", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "Alpha", Fields: map[string]any{"min_level": float64(1)}},
			{TypeKey: "quest", Key: "b", Name: "Beta", Fields: map[string]any{"min_level": "nope"}},
			{TypeKey: "quest", Key: "c", Name: "Gamma", Fields: map[string]any{"min_level": float64(3)}},
		}, metamodel.BulkPartial)
		assert.Must(t, err == nil, "UpsertEntities: %v", err)
		assert.Must(t, len(result.Written) == 2, "written = %+v, want the two rows that landed", result.Written)
		if got := []string{result.Written[0].Key, result.Written[1].Key}; !equalStrings(got, []string{"a", "c"}) {
			t.Fatalf("written keys = %v, want [a c]", got)
		}
		for i, w := range result.Written {
			assert.Must(t, w.TypeKey == "quest", "written[%d].TypeKey = %q, want the stored type key", i, w.TypeKey)
			if w.ID != result.Succeeded[i].ID {
				t.Fatalf("written[%d].ID = %s, want the row's own id %s", i, w.ID, result.Succeeded[i].ID)
			}
			assert.Must(t, w.Version == 1, "written[%d].Version = %d, want 1 for a freshly created row", i, w.Version)
		}

		// A second pass over the same key moves the version, and Written is
		// where an agent reads the value its next expected_version needs —
		// which is not a convenience: an update of an existing row is
		// *refused* without a matching ExpectedVersion, so a seeding agent
		// that has only its own input has no way to edit what it just wrote
		// without a second read.
		first := result.Written[0].Version
		again, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "Alpha II",
				Fields: map[string]any{"min_level": float64(2)}, ExpectedVersion: &first},
		}, metamodel.BulkPartial)
		assert.Must(t, err == nil, "UpsertEntities again: %v", err)
		assert.Must(t, len(again.Written) == 1 && again.Written[0].Version == 2, "written = %+v, want version 2 on the second write", again.Written)
	})

	// TestEntitiesArea's "a bulk write marshals what landed" case pins the
	// same thing through JSON, which is the surface an agent actually reads:
	// the failures-only report this replaced was a marshalling fact, not a Go
	// one, so a test that only read the Go struct would not have caught it and
	// would not catch its return.
	t.Run("a bulk write marshals what landed", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)
		seedQuestType(t, svc, project)

		result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
			{TypeKey: "quest", Key: "a", Name: "Alpha", Fields: map[string]any{"min_level": float64(1)}},
		}, metamodel.BulkPartial)
		assert.Must(t, err == nil, "UpsertEntities: %v", err)

		raw, err := json.Marshal(result)
		assert.Must(t, err == nil, "marshal: %v", err)
		var decoded struct {
			Written []struct {
				TypeKey string `json:"type_key"`
				Key     string `json:"key"`
				ID      string `json:"id"`
				Version int32  `json:"version"`
			} `json:"written"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		assert.Must(t, len(decoded.Written) == 1, "marshalled result %s carries no record of the row that landed", raw)
		w := decoded.Written[0]
		assert.Must(t, w.TypeKey == "quest" && w.Key == "a" && w.Version == 1 && w.ID == result.Succeeded[0].ID.String(), "marshalled written = %+v, want the row's own address, id and version", w)
		// A database row's own columns stay off the wire: Succeeded is
		// json:"-" for the reason BulkResult records, and this pins that
		// adding Written did not quietly put them back.
		assert.Must(t, !strings.Contains(string(raw), "updated_by") && !strings.Contains(string(raw), "project_id"), "marshalled result %s carries database columns", raw)
	})
}

// payloadField reads one string field of an event payload through its
// JSON shape, which is the only thing a client ever sees.
func payloadField(t *testing.T, e realtime.Event, field string) string {
	t.Helper()
	raw, err := json.Marshal(e.Payload)
	assert.Must(t, err == nil, "marshal payload: %v", err)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode payload %s: %v", raw, err)
	}
	value, ok := got[field].(string)
	assert.Must(t, ok, "payload %s carries no string %q", raw, field)
	return value
}

// searchColumn reads the stored tsvector as text, positions included:
// two rows built from the same values must produce the same string, and
// the positions are what makes the field order observable.
// searchColumn reads the value-derived halves of a row's search vector:
// the name under label A and the name plus the flattened field text
// under label B.
func searchColumn(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var text string
	if err := pool.QueryRow(context.Background(),
		`SELECT ts_filter(search, '{a,b}')::text FROM entities WHERE id = $1`, id).Scan(&text); err != nil {
		t.Fatalf("read search column: %v", err)
	}
	return text
}

// cancelWhenLanded is a context that reports cancellation from the moment
// a given entity key exists in the database.
type cancelWhenLanded struct {
	context.Context
	t       *testing.T
	pool    *pgxpool.Pool
	project uuid.UUID
	key     string

	mu     sync.Mutex
	closed chan struct{}
	fired  bool
}

func (c *cancelWhenLanded) armed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fired {
		return true
	}
	// **Only between items.** pgx consults the context it is given all
	// the way through a statement, so this probe is also called from
	// inside item 0's own commit — and the row it watches for becomes
	// visible the instant that commit lands on the server, which can be
	// while pgx is still waiting for the reply. Arming there cancels the
	// connection under the commit, and the batch stops at item 0 with
	// the same error it should have reported for item 1: the test then
	// fails about one run in three, on nothing the product did.
	if c.pool.Stat().AcquiredConns() != 0 {
		return false
	}
	var exists bool
	if err := c.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM entities WHERE project_id = $1 AND key = $2)`,
		c.project, c.key).Scan(&exists); err != nil {
		c.t.Errorf("watch for the landed row: %v", err)
		return false
	}
	if exists {
		c.fired = true
		close(c.closed)
	}
	return exists
}

func (c *cancelWhenLanded) Done() <-chan struct{} {
	c.armed()
	return c.closed
}

func (c *cancelWhenLanded) Err() error {
	if c.armed() {
		return context.Canceled
	}
	return nil
}
