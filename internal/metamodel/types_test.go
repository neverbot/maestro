package metamodel_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// newProject inserts a bare project row to scope a test's data.
//
// It writes SQL directly rather than calling internal/projects: that
// package would be an import cycle away today and, more to the point, a
// project is only a scope here — these tests never exercise membership,
// slugs or ownership. It is a test helper rather than a method on Service
// because production code has no business creating projects: an exported
// CreateBareProjectForTest would ship in the binary and be callable from
// the MCP surface.
func newProject(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	slug := "azeroth-" + uuid.NewString()[:8]
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`, slug).Scan(&id)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return id
}

// insertEntity writes an entity row straight to the database. Task 4 owns
// the service path that validates one; these tests only need instances to
// exist, so they make them the shortest way and stay independent of a
// write path that does not exist yet.
func insertEntity(t *testing.T, pool *pgxpool.Pool, project, typeID uuid.UUID, key string, fields map[string]any) uuid.UUID {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode fields: %v", err)
	}
	var id uuid.UUID
	err = pool.QueryRow(context.Background(),
		`INSERT INTO entities (project_id, entity_type_id, key, name, fields)
		 VALUES ($1, $2, $3, $3, $4) RETURNING id`, project, typeID, key, raw).Scan(&id)
	if err != nil {
		t.Fatalf("insert entity %q: %v", key, err)
	}
	return id
}

// entityState reads back the columns the re-validation sweep is allowed
// and not allowed to touch.
func entityState(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) (invalid bool, fields string, updatedAt string) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT invalid, fields::text, updated_at::text FROM entities WHERE id = $1`, id).
		Scan(&invalid, &fields, &updatedAt)
	if err != nil {
		t.Fatalf("read entity: %v", err)
	}
	return invalid, fields, updatedAt
}

func ptrInt32(v int32) *int32 { return &v }

// requireFieldError asserts the exact path and message of a single-problem
// validation failure. A negative test that only asserts err != nil is
// satisfied by any unrelated failure; every one below names what it wants.
func requireFieldError(t *testing.T, err error, wantPath, wantMessage string) {
	t.Helper()
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want a *metamodel.ValidationError", err)
	}
	if len(invalid.Fields) != 1 {
		t.Fatalf("got %d problems, want 1: %v", len(invalid.Fields), invalid.Fields)
	}
	if got := invalid.Fields[0].Path; got != wantPath {
		t.Fatalf("path = %q, want %q", got, wantPath)
	}
	if got := invalid.Fields[0].Message; got != wantMessage {
		t.Fatalf("message = %q, want %q", got, wantMessage)
	}
}

func TestUpsertEntityTypeIsIdempotentByKey(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	first, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}},
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("Version = %d, want 1", first.Version)
	}

	second, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema:          metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}},
		ExpectedVersion: ptrInt32(1),
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("upserting the same key created a second row")
	}
	if second.Version != 2 {
		t.Fatalf("Version = %d, want 2", second.Version)
	}
}

func TestUpsertEntityTypeStoresTheDeclaredSchemaAndTheActor(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	user := newUser(t, pool)
	row, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Description: "Something to do", Color: "#c41e3a", Icon: "scroll",
		Schema: metamodel.Schema{
			{Key: "min_level", Type: metamodel.FieldNumber, Min: ptrFloat(1), Max: ptrFloat(70)},
			{Key: "repeatable", Type: metamodel.FieldBool, HasDefault: true, Default: false},
		},
		Actor: metamodel.Actor{UserID: &user},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	stored, err := metamodel.ParseSchema(row.FieldSchema)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored %d fields, want 2", len(stored))
	}
	// The declared default has to survive storage: it is declared on the
	// wire by the presence of the "default" key alone, so a round trip that
	// lost it would drop the fact silently.
	if !stored[1].HasDefault || stored[1].Default != false {
		t.Fatalf("repeatable = %+v, want a declared default of false", stored[1])
	}
	if row.Description != "Something to do" || row.Color != "#c41e3a" || row.Icon != "scroll" {
		t.Fatalf("descriptive columns were not stored: %+v", row)
	}
	if row.UpdatedByUserID == nil || *row.UpdatedByUserID != user {
		t.Fatalf("UpdatedByUserID = %v, want %v", row.UpdatedByUserID, user)
	}
}

func TestUpsertEntityTypeRejectsStaleVersion(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest renamed", LabelPlural: "Quests",
		ExpectedVersion: ptrInt32(7),
	})
	if !errors.Is(err, metamodel.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	var conflict *metamodel.VersionConflictError
	if !errors.As(err, &conflict) || conflict.Current != 1 {
		t.Fatalf("the conflict must carry the current version, got %v", err)
	}

	// The refused write must not have landed.
	stored, err := svc.EntityTypeByKey(ctx, project, "quest")
	if err != nil {
		t.Fatalf("EntityTypeByKey: %v", err)
	}
	if stored.Label != "Quest" || stored.Version != 1 {
		t.Fatalf("the refused upsert changed the row: %+v", stored)
	}
}

func TestUpsertEntityTypeRejectsAMissingExpectedVersionOnAnExistingType(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// No ExpectedVersion at all: a blind overwrite of a type somebody else
	// may have edited is the thing optimistic concurrency exists to stop.
	_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest renamed", LabelPlural: "Quests",
	})
	var conflict *metamodel.VersionConflictError
	if !errors.As(err, &conflict) || conflict.Current != 1 {
		t.Fatalf("err = %v, want a *VersionConflictError carrying version 1", err)
	}
}

func TestUpsertEntityTypeRejectsBadSchema(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "difficulty", Type: metamodel.FieldEnum}},
	})
	// invalid_schema, not schema_violation: the declaration is what cannot
	// stand here, and the two sentinels exist precisely so a caller need
	// not tell them apart by reading paths.
	if !errors.Is(err, metamodel.ErrInvalidSchema) {
		t.Fatalf("err = %v, want ErrInvalidSchema", err)
	}
	if errors.Is(err, metamodel.ErrSchemaViolation) {
		t.Fatalf("err = %v must not also satisfy ErrSchemaViolation", err)
	}
	var schemaErr *metamodel.SchemaError
	if !errors.As(err, &schemaErr) || len(schemaErr.Fields) != 1 ||
		schemaErr.Fields[0].Path != "field_schema[0]" ||
		schemaErr.Fields[0].Message != "an enum field needs options" {
		t.Fatalf("err = %v, want field_schema[0]: an enum field needs options", err)
	}

	if _, err := svc.EntityTypeByKey(ctx, project, "quest"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("a rejected declaration must store nothing, got %v", err)
	}
}

func TestUpsertEntityTypeRequiresAKey(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	project := newProject(t, pool)

	_, err := svc.UpsertEntityType(context.Background(), project, metamodel.EntityTypeInput{
		Label: "Quest", LabelPlural: "Quests",
	})
	requireFieldError(t, err, "key", "is required")
}

func TestUpsertEntityTypeRejectsAKeyThatIsNotAHandle(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	project := newProject(t, pool)

	const wantMessage = "must be letters, digits, underscores or hyphens, " +
		"starting with a letter or a digit"
	for _, key := range []string{"main quest", "quest.line", "quests/all", "_quest", "-quest", "quêtes", "ques%t"} {
		t.Run(key, func(t *testing.T) {
			_, err := svc.UpsertEntityType(context.Background(), project, metamodel.EntityTypeInput{
				Key: key, Label: "Quest", LabelPlural: "Quests",
			})
			requireFieldError(t, err, "key", wantMessage)
		})
	}
}

func TestUpsertEntityTypeAcceptsTheKeysAGameActuallyWrites(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	project := newProject(t, pool)

	// Row keys are deliberately wider than field keys: capitals and a
	// leading digit are ordinary in a game's own vocabulary, and the
	// case-folding unique index means capitals cannot produce the
	// near-duplicate keys the field-key rule exists to prevent.
	for _, key := range []string{"Quest", "quest-line", "Elwynn_Forest", "1999", "500-miles"} {
		t.Run(key, func(t *testing.T) {
			row, err := svc.UpsertEntityType(context.Background(), project, metamodel.EntityTypeInput{
				Key: key, Label: key, LabelPlural: key + "s",
			})
			if err != nil {
				t.Fatalf("UpsertEntityType(%q): %v", key, err)
			}
			if row.Key != key {
				t.Fatalf("stored key = %q, want %q", row.Key, key)
			}
		})
	}
}

func TestUpsertEntityTypeRejectsAnOverlongKey(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	project := newProject(t, pool)

	_, err := svc.UpsertEntityType(context.Background(), project, metamodel.EntityTypeInput{
		Key: strings.Repeat("q", 65), Label: "Quest", LabelPlural: "Quests",
	})
	requireFieldError(t, err, "key", "must be at most 64 characters")

	// Sixty-four exactly is still a key, so the cap cannot drift to >=.
	if _, err := svc.UpsertEntityType(context.Background(), project, metamodel.EntityTypeInput{
		Key: strings.Repeat("q", 64), Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("a 64-character key must be accepted: %v", err)
	}
}

func TestUpsertEntityTypeRefusesAKeyThatDiffersOnlyByCase(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "Quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The uniqueness index folds case, so "quest" addresses the row stored
	// as "Quest". Updating it under the other spelling is refused with a
	// message naming both spellings, rather than silently overwriting the
	// row or surfacing a raw unique violation.
	_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Something else", LabelPlural: "Something elses",
		ExpectedVersion: ptrInt32(1),
	})
	requireFieldError(t, err, "key",
		`"quest" already exists here spelled "Quest", and keys are matched without regard to case: `+
			`use "Quest" to update it, or pick a key that differs by more than capitalisation`)

	stored, err := svc.EntityTypeByKey(ctx, project, "QUEST")
	if err != nil {
		t.Fatalf("EntityTypeByKey: %v", err)
	}
	if stored.Key != "Quest" || stored.Label != "Quest" || stored.Version != 1 {
		t.Fatalf("the refused upsert changed the row: %+v", stored)
	}
}

func TestACreationThatLosesTheRaceForItsKeyIsRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	// A writer creating a key it has never seen has no version to expect,
	// and there is no row yet to lock, so the read this upsert takes cannot
	// see the rival write at all. The compare-and-set therefore has to live
	// in the INSERT ... ON CONFLICT itself: an unguarded DO UPDATE turns the
	// loser of the race into a silent overwrite of a type it never read.
	//
	// The rival is an open transaction rather than a second goroutine, so
	// the interleaving is the test's to choose and not the scheduler's.
	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = rival.Rollback(ctx) }()
	if _, err := rival.Exec(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'quest', 'Quest', 'Quests')`, project); err != nil {
		t.Fatalf("rival insert: %v", err)
	}

	// The upsert blocks on the uncommitted row's unique index until the
	// rival commits, which is exactly the window the guard exists for.
	result := make(chan error, 1)
	go func() {
		_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Mine", LabelPlural: "Mine",
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

	var conflict *metamodel.VersionConflictError
	select {
	case err := <-result:
		if !errors.As(err, &conflict) || conflict.Current != 1 {
			t.Fatalf("err = %v, want a *VersionConflictError carrying version 1", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival committed")
	}

	row, err := svc.EntityTypeByKey(ctx, project, "quest")
	if err != nil {
		t.Fatalf("EntityTypeByKey: %v", err)
	}
	if row.Label != "Quest" || row.Version != 1 {
		t.Fatalf("the losing writer overwrote the row: %+v", row)
	}
}

func TestSchemaChangeFlagsRowsInvalidWithoutTouchingThem(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}},
	})
	if err != nil {
		t.Fatalf("declare type: %v", err)
	}

	complete := insertEntity(t, pool, project, typ.ID, "hogger", map[string]any{"min_level": 10, "summary": "x"})
	sparse := insertEntity(t, pool, project, typ.ID, "kobold", map[string]any{"min_level": 3})
	_, sparseFieldsBefore, sparseUpdatedBefore := entityState(t, pool, sparse)

	// "summary" was never declared, so hogger is already invalid under the
	// schema it was written against; only the sweep decides that.
	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema:          metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}},
		ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("re-declare: %v", err)
	}

	if invalid, _, _ := entityState(t, pool, complete); !invalid {
		t.Fatal("an entity carrying an undeclared field must be flagged invalid")
	}
	invalid, fieldsAfter, updatedAfter := entityState(t, pool, sparse)
	if invalid {
		t.Fatal("an entity that still fits its schema must not be flagged")
	}
	if fieldsAfter != sparseFieldsBefore {
		t.Fatalf("the sweep rewrote stored values: %s -> %s", sparseFieldsBefore, fieldsAfter)
	}
	// A row whose verdict has not changed must not be rewritten at all: a
	// validation pass is not an edit, and a moved updated_at says it was.
	if updatedAfter != sparseUpdatedBefore {
		t.Fatalf("the sweep touched updated_at: %s -> %s", sparseUpdatedBefore, updatedAfter)
	}
}

func TestSchemaChangeDoesNotBackFillDeclaredDefaults(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}},
	})
	if err != nil {
		t.Fatalf("declare type: %v", err)
	}
	entity := insertEntity(t, pool, project, typ.ID, "hogger", map[string]any{"min_level": 10})

	// The new field declares a default. The sweep must judge the row, not
	// edit it: back-filling here would write a value the designer never
	// chose into content they own.
	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{
			{Key: "min_level", Type: metamodel.FieldNumber},
			{Key: "repeatable", Type: metamodel.FieldBool, HasDefault: true, Default: false},
		},
		ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("re-declare: %v", err)
	}

	invalid, fields, _ := entityState(t, pool, entity)
	if invalid {
		t.Fatal("a row missing a field that has a default still fits the schema")
	}
	if strings.Contains(fields, "repeatable") {
		t.Fatalf("the sweep back-filled the default: %s", fields)
	}
}

func TestSchemaChangeClearsTheFlagWhenTheRowFitsAgain(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}},
	})
	if err != nil {
		t.Fatalf("declare type: %v", err)
	}
	entity := insertEntity(t, pool, project, typ.ID, "hogger", map[string]any{
		"min_level": 10, "summary": "Kill Hogger.",
	})

	narrowed := metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}}
	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: narrowed, ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("narrow: %v", err)
	}
	if invalid, _, _ := entityState(t, pool, entity); !invalid {
		t.Fatal("the row must be flagged while summary is undeclared")
	}

	// Declaring the missing field is how a designer fixes the flag, so the
	// sweep has to clear it as readily as it sets it.
	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{
			{Key: "min_level", Type: metamodel.FieldNumber},
			{Key: "summary", Type: metamodel.FieldLongText},
		},
		ExpectedVersion: ptrInt32(2),
	}); err != nil {
		t.Fatalf("widen: %v", err)
	}
	if invalid, _, _ := entityState(t, pool, entity); invalid {
		t.Fatal("the row fits the widened schema and must not stay flagged")
	}
}

func TestRemoveEntityTypeRefusesWhenInUse(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	})
	if err != nil {
		t.Fatalf("upsert type: %v", err)
	}
	insertEntity(t, pool, project, typ.ID, "hogger", map[string]any{})

	if err := svc.RemoveEntityType(ctx, project, typ.ID, false); !errors.Is(err, metamodel.ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse", err)
	}
	// The refusal must leave both the type and its content alone.
	if _, err := svc.EntityTypeByKey(ctx, project, "quest"); err != nil {
		t.Fatalf("the refused removal deleted the type: %v", err)
	}

	if err := svc.RemoveEntityType(ctx, project, typ.ID, true); err != nil {
		t.Fatalf("cascade removal: %v", err)
	}
	if _, err := svc.EntityTypeByKey(ctx, project, "quest"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound after removal", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM entities WHERE project_id = $1`, project).Scan(&remaining); err != nil {
		t.Fatalf("count entities: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("%d entities survived the cascade", remaining)
	}
}

func TestRemoveEntityTypeReportsAnUnknownID(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	project := newProject(t, pool)

	if err := svc.RemoveEntityType(context.Background(), project, uuid.New(), false); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestTypesAreScopedToTheirProject(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, pool), newProject(t, pool)

	typ, err := svc.UpsertEntityType(ctx, mine, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := svc.EntityTypeByKey(ctx, theirs, "quest"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("a type must not be visible from another project, got %v", err)
	}
	// Holding the id is not authority either: the query filters on the
	// project, so a leaked id reads as not found.
	if _, err := svc.EntityTypeByID(ctx, theirs, typ.ID); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("a type id must not resolve from another project, got %v", err)
	}
	if err := svc.RemoveEntityType(ctx, theirs, typ.ID, true); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("a type must not be removable from another project, got %v", err)
	}
	if _, err := svc.EntityTypeByKey(ctx, mine, "quest"); err != nil {
		t.Fatalf("the owning project lost its type: %v", err)
	}

	types, err := svc.ListEntityTypes(ctx, theirs)
	if err != nil {
		t.Fatalf("ListEntityTypes: %v", err)
	}
	if len(types) != 0 {
		t.Fatalf("the other project sees %d types, want 0", len(types))
	}

	// The same key in two games is two types, not a collision.
	if _, err := svc.UpsertEntityType(ctx, theirs, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("the other project must be free to use the same key: %v", err)
	}
}

func TestListEntityTypesIsOrderedByLabel(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	// The keys are deliberately in a different order from the labels, so a
	// listing ordered by key — or by insertion — cannot pass this by
	// accident.
	for _, typ := range []struct{ key, label string }{
		{"c", "Zone"}, {"b", "Class"}, {"a", "Quest"},
	} {
		if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: typ.key, Label: typ.label, LabelPlural: typ.label + "s",
		}); err != nil {
			t.Fatalf("upsert %q: %v", typ.label, err)
		}
	}

	types, err := svc.ListEntityTypes(ctx, project)
	if err != nil {
		t.Fatalf("ListEntityTypes: %v", err)
	}
	var got []string
	for _, typ := range types {
		got = append(got, typ.Label)
	}
	want := []string{"Class", "Quest", "Zone"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("labels = %v, want %v", got, want)
	}
}

func ptrFloat(v float64) *float64 { return &v }

// newUser inserts a user row, so a write can record an actor.
func newUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, display_name, password_hash)
		 VALUES ($1, 'Designer', 'x') RETURNING id`,
		uuid.NewString()[:8]+"@example.test").Scan(&id)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

// TestARaceThatWouldLandUnderAnotherSpellingIsRefused is the regression
// test for a hole the pre-read alone could not close.
//
// The spelling refusal used to live only in the locked read at the top of
// UpsertEntityType, which happens *before* the guarded write and only sees
// a row that is already visible. On the creation path there is no row to
// lock, so a writer racing a creator — and carrying an ExpectedVersion
// that happens to match the version the winner lands on — sailed straight
// through the ON CONFLICT ... DO UPDATE WHERE version = @expected_version
// and updated a row it never read, stored under a different spelling. The
// call returned no error at all, which is the one outcome correction 4
// rules out.
//
// The interleaving is driven by an open rival transaction rather than a
// second goroutine, so it is the test's to choose and not the scheduler's.
func TestARaceThatWouldLandUnderAnotherSpellingIsRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = rival.Rollback(ctx) }()
	if _, err := rival.Exec(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'Hogger', 'Hogger', 'Hoggers')`, project); err != nil {
		t.Fatalf("rival insert: %v", err)
	}

	// The rival's row is invisible to this upsert's own locked read — an
	// uncommitted row is not there to be seen or locked — so it takes the
	// creation path and then blocks on the unique index. ExpectedVersion 1
	// is exactly the version the rival's insert lands on, so the guard on
	// the DO UPDATE cannot refuse this write either.
	result := make(chan error, 1)
	go func() {
		_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "hogger", Label: "MINE", LabelPlural: "MINE",
			ExpectedVersion: ptrInt32(1),
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

	// The refusal has to roll the write back, not merely report it: the
	// bypass this test exists for landed the label under the rival's
	// spelling and bumped the version while returning nil.
	row, err := svc.EntityTypeByKey(ctx, project, "hogger")
	if err != nil {
		t.Fatalf("EntityTypeByKey: %v", err)
	}
	if row.Key != "Hogger" || row.Label != "Hogger" || row.Version != 1 {
		t.Fatalf("the losing writer changed the row: %+v", row)
	}
}

// TestACreationThatLosesItsKeyToAnotherSpellingIsNamedAsARespelling
// covers conflictOnEntityTypeKey's other branch: the guarded upsert
// matched no row *and* the winner took the key under a different
// spelling.
//
// TestACreationThatLosesTheRaceForItsKeyIsRefused above exercises the
// same re-read but with matching spellings, so it can only ever observe
// the version-conflict branch; deleting the respelling branch left the
// whole suite green. A designer who loses this race must be told what
// actually stands in the way — a key already spelled differently, which
// they can address — not a version conflict on a row they never created
// and whose spelling they cannot see.
func TestACreationThatLosesItsKeyToAnotherSpellingIsNamedAsARespelling(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = rival.Rollback(ctx) }()
	if _, err := rival.Exec(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'Hogger', 'Hogger', 'Hoggers')`, project); err != nil {
		t.Fatalf("rival insert: %v", err)
	}

	// No ExpectedVersion at all, so the guard passes noVersion and the
	// DO UPDATE is a guaranteed mismatch once the rival's row appears:
	// this is the path that reaches conflictOnEntityTypeKey.
	result := make(chan error, 1)
	go func() {
		_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "hogger", Label: "MINE", LabelPlural: "MINE",
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
		if errors.Is(err, metamodel.ErrVersionConflict) {
			t.Fatalf("err = %v must not read as a version conflict", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival committed")
	}
}

// TestTheReportedCurrentVersionIsTheOneTheWriteWouldHaveMet pins the
// FOR UPDATE on GetEntityTypeByKeyForUpdate.
//
// Removing the lock leaves every other test in this file green, because
// the compare-and-set in the upsert's own DO UPDATE still refuses every
// lost update on its own. What the lock earns is the *number* a caller is
// told to merge onto. Without it the read runs against this
// transaction's snapshot and returns whatever version was committed when
// it started, so a caller racing an in-flight edit is told "current
// version is 1", re-issues with 1, and is refused again — a loop it
// cannot get out of by doing what the error said.
//
// The rival is an open transaction, so the interleaving is the test's.
func TestTheReportedCurrentVersionIsTheOneTheWriteWouldHaveMet(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = rival.Rollback(ctx) }()
	if _, err := rival.Exec(ctx,
		`UPDATE entity_types SET version = version + 1, label = 'Theirs' WHERE id = $1`,
		typ.ID); err != nil {
		t.Fatalf("rival update: %v", err)
	}

	// No ExpectedVersion, so the refusal is decided by the read alone and
	// the version it reports is the read's answer, not the guard's.
	result := make(chan error, 1)
	go func() {
		_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Mine", LabelPlural: "Mine",
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
		if !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want a *VersionConflictError", err)
		}
		if conflict.Current != 2 {
			t.Fatalf("Current = %d, want 2: the caller must be told the version its own "+
				"write would have met, not the one visible before the rival committed",
				conflict.Current)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival committed")
	}
}
