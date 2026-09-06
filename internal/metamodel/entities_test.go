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

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
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
	if err != nil {
		t.Fatalf("seed quest type: %v", err)
	}
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
	if err != nil {
		t.Fatalf("read search vector: %v", err)
	}
	return hit
}

func TestUpsertEntityValidatesAgainstItsType(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
		Fields: map[string]any{"min_level": float64(10), "summary": "Kill Hogger."},
	})
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	if row.Version != 1 {
		t.Fatalf("Version = %d, want 1", row.Version)
	}

	_, err = svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "broken", Name: "Broken",
		Fields: map[string]any{"min_level": "ten"},
	})
	if !errors.Is(err, metamodel.ErrSchemaViolation) {
		t.Fatalf("err = %v, want ErrSchemaViolation", err)
	}
	if _, err := svc.EntityByKey(ctx, project, "quest", "broken"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("a refused row must store nothing, got %v", err)
	}
}

func TestUpsertEntityReportsAnUnknownType(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	project := newProject(t, pool)

	_, err := svc.UpsertEntity(context.Background(), project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Hogger",
	})
	if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	// The message has to name the type, because "not_found" alone leaves a
	// seeding agent unable to tell a missing type from a missing entity.
	if !strings.Contains(err.Error(), `no entity type "quest"`) {
		t.Fatalf("err = %v, want it to name the missing type", err)
	}
}

func TestUpsertEntityIsIdempotentAndBumpsVersion(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	in := metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
		Fields: map[string]any{"min_level": float64(10)},
	}
	first, err := svc.UpsertEntity(ctx, project, in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	in.ExpectedVersion = ptrInt32(1)
	in.Name = "Wanted: Hogger (revised)"
	second, err := svc.UpsertEntity(ctx, project, in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("a re-seed created a duplicate row")
	}
	if second.Version != 2 {
		t.Fatalf("Version = %d, want 2", second.Version)
	}
	if second.Name != "Wanted: Hogger (revised)" {
		t.Fatalf("Name = %q, want the revised one", second.Name)
	}
}

func TestUpsertEntityRejectsStaleVersion(t *testing.T) {
	pool := testutil.NewPool(t)
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
	if !errors.As(err, &conflict) || conflict.Current != 1 {
		t.Fatalf("err = %v, want a *VersionConflictError carrying version 1", err)
	}

	// A missing ExpectedVersion against an existing row is the blind
	// overwrite optimistic concurrency exists to stop, not an insert.
	_, err = svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Hogger blindly",
		Fields: map[string]any{"min_level": float64(12)},
	})
	if !errors.Is(err, metamodel.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}

	stored, err := svc.EntityByKey(ctx, project, "quest", "hogger")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if stored.Name != "Hogger" || stored.Version != 1 {
		t.Fatalf("a refused upsert changed the row: %+v", stored)
	}
}

// TestUpsertEntityRefusesARespelledKey is the entity flavour of the
// entity-type rule: entities_key_key folds case, so "hogger" addresses
// the row stored as "Hogger" and updating it under the other spelling is
// refused rather than silently overwriting a handle other rows and
// documents refer to.
func TestUpsertEntityRefusesARespelledKey(t *testing.T) {
	pool := testutil.NewPool(t)
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
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("a respelled key must read as invalid_input, got %v", err)
	}

	stored, err := svc.EntityByKey(ctx, project, "quest", "HOGGER")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if stored.Key != "Hogger" || stored.Name != "Hogger" || stored.Version != 1 {
		t.Fatalf("the refused upsert changed the row: %+v", stored)
	}
}

// TestARespelledEntityKeyIsNamedEvenWhenTheVersionIsAlsoStale pins the
// one job left to the spelling check inside the locked pre-read.
//
// The check on the row the upsert returns catches every respelling this
// one does, so deleting these lines is invisible wherever the version
// also matches. This is the case where the two disagree: a caller
// holding both a respelled key and a stale version is failing for two
// reasons at once, and the pre-read's order decides which it is told
// about. It hears the respelling — which names both spellings and both
// remedies — rather than "current version is 1", which would send it to
// retry with a version refused again for the same reason.
func TestARespelledEntityKeyIsNamedEvenWhenTheVersionIsAlsoStale(t *testing.T) {
	pool := testutil.NewPool(t)
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
	if errors.Is(err, metamodel.ErrVersionConflict) {
		t.Fatalf("err = %v, want the respelling refusal rather than the version conflict", err)
	}
	requireFieldError(t, err, "key",
		`"hogger" already exists here spelled "Hogger", and keys are matched without regard to case: `+
			`use "Hogger" to update it, or pick a key that differs by more than capitalisation`)
}

// TestARaceThatWouldLandAnEntityUnderAnotherSpellingIsRefused is the hole
// the locked pre-read cannot close, for entities.
//
// The pre-read runs before the write and only ever sees a row that is
// already committed. On the creation path there is nothing to lock, so a
// writer racing a creator sails through it, blocks on the folding unique
// index, and must be refused by something downstream — otherwise it
// lands its content on a row it never saw, under a spelling it never
// sent. conflictOnEntityKey is that something: the guard on the DO
// UPDATE is a guaranteed mismatch for a creating caller, and the re-read
// after it names both spellings.
//
// The interleaving is driven by an open rival transaction rather than a
// second goroutine, so it is the test's to choose and not the scheduler's.
func TestARaceThatWouldLandAnEntityUnderAnotherSpellingIsRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)
	typ, err := svc.EntityTypeByKey(ctx, project, "quest")
	if err != nil {
		t.Fatalf("EntityTypeByKey: %v", err)
	}

	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
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
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if row.Key != "Hogger" || row.Name != "Hogger" || row.Version != 1 {
		t.Fatalf("the losing writer changed the row: %+v", row)
	}
}

// TestAnEntityCreationThatLosesTheRaceForItsKeyIsRefused pins the
// compare-and-set in the upsert's own DO UPDATE.
//
// A writer creating a key it has never seen has no version to expect and
// there is no row yet to lock, so its read cannot see the rival write at
// all — and here both writers spell the key the same way, so the
// post-write spelling check has nothing to catch either. The guard on
// the DO UPDATE is the only thing left between the loser of the race and
// a silent overwrite of content it never read.
func TestAnEntityCreationThatLosesTheRaceForItsKeyIsRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)
	typ, err := svc.EntityTypeByKey(ctx, project, "quest")
	if err != nil {
		t.Fatalf("EntityTypeByKey: %v", err)
	}

	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
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
		if !errors.As(err, &conflict) || conflict.Current != 1 {
			t.Fatalf("err = %v, want a *VersionConflictError carrying version 1", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival committed")
	}

	row, err := svc.EntityByKey(ctx, project, "quest", "hogger")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if row.Name != "Theirs" || row.Version != 1 {
		t.Fatalf("the losing writer overwrote the row: %+v", row)
	}
}

// TestAMalformedEntityArgumentIsInvalidInput pins which wire code a row's
// own arguments are published under: a bad key reported as
// schema_violation sends an agent to inspect entity *values*, and a bad
// key falling through failureFor's default arm reports internal_error,
// which tells it nothing at all.
func TestAMalformedEntityArgumentIsInvalidInput(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "main quest", Name: "",
		Fields: map[string]any{"min_level": float64(1)},
	})
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if errors.Is(err, metamodel.ErrSchemaViolation) {
		t.Fatalf("err = %v must not also read as a schema violation", err)
	}
	// Both problems in one pass: an agent fixing a seed script must not
	// learn about the name only after the key is fixed.
	if err.Error() != "invalid_input: key: must be letters, digits, underscores or hyphens, "+
		"starting with a letter or a digit; name: is required" {
		t.Fatalf("err = %v, want the key and the name reported together", err)
	}

	// And the same fault inside a bulk batch carries the same code, not
	// internal_error.
	result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "main quest", Name: "A",
			Fields: map[string]any{"min_level": float64(1)}},
	}, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("UpsertEntities: %v", err)
	}
	if len(result.Failed) != 1 || result.Failed[0].Code != "invalid_input" {
		t.Fatalf("failures = %+v, want one invalid_input", result.Failed)
	}
}

func TestUpsertEntityRejectsAnOverlongName(t *testing.T) {
	pool := testutil.NewPool(t)
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
}

// TestUpsertEntityWritesAndRewritesTheSearchVector pins the one column no
// database mechanism maintains.
//
// entities.search is application-computed — it is derived from
// user-declared jsonb whose text fields only the Go validator can pick
// out — so it is written by UpsertEntity and by nothing else. A write
// path that changes name or fields without rewriting it leaves the row
// indexed under its old words, and Task 6's search then silently fails to
// find content that is plainly there.
func TestUpsertEntityWritesAndRewritesTheSearchVector(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
		Fields: map[string]any{"min_level": float64(10), "summary": "Riverpaw gnolls"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !entityMatches(t, pool, row.ID, "Hogger") {
		t.Fatal("the name is not in the search vector")
	}
	if !entityMatches(t, pool, row.ID, "gnolls") {
		t.Fatal("a text field's words are not in the search vector")
	}

	// An update rewrites it: the old words must go and the new ones must
	// arrive, on the DO UPDATE arm as much as on the insert.
	updated, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger the Gnoll",
		Fields:          map[string]any{"min_level": float64(10), "summary": "Elwynn Forest"},
		ExpectedVersion: ptrInt32(1),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if entityMatches(t, pool, updated.ID, "gnolls") {
		t.Fatal("the replaced field's words are still indexed")
	}
	if !entityMatches(t, pool, updated.ID, "Elwynn") {
		t.Fatal("the new field's words were not indexed")
	}
}

func TestBulkPartialLandsTheGoodRowsAndReportsTheRest(t *testing.T) {
	pool := testutil.NewPool(t)
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
	if err != nil {
		t.Fatalf("UpsertEntities: %v", err)
	}
	if len(result.Succeeded) != 2 {
		t.Fatalf("succeeded = %d, want 2", len(result.Succeeded))
	}
	if len(result.Failed) != 1 {
		t.Fatalf("failed = %d, want 1", len(result.Failed))
	}
	failure := result.Failed[0]
	if failure.Index != 1 {
		t.Fatalf("the failure must carry its index, got %d", failure.Index)
	}
	if failure.Key != "b" {
		t.Fatalf("the failure must carry its own key, got %q", failure.Key)
	}
	if failure.Code != "schema_violation" {
		t.Fatalf("code = %q, want schema_violation for a bad value", failure.Code)
	}
	// The message names the offending path so the caller can retry that
	// item alone, and names nothing belonging to the items around it: a
	// batch report is the one place a row's content can leak into a
	// neighbour's error.
	if !strings.Contains(failure.Message, "fields.min_level") {
		t.Fatalf("message = %q, want it to name fields.min_level", failure.Message)
	}
	for _, leaked := range []string{"alpha-secret", "gamma-secret", "Alpha", "Gamma"} {
		if strings.Contains(failure.Message, leaked) {
			t.Fatalf("message = %q leaks %q from another item", failure.Message, leaked)
		}
	}
	// The row after the failure must still have landed: item 200 landing
	// cannot depend on item 3.
	if _, err := svc.EntityByKey(ctx, project, "quest", "c"); err != nil {
		t.Fatalf("the row after the failure must still have landed: %v", err)
	}
	if _, err := svc.EntityByKey(ctx, project, "quest", "b"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("the failed row must not have landed, got %v", err)
	}
}

func TestBulkAtomicRollsBackEverythingOnOneFailure(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
		{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": "nope"}},
	}, metamodel.BulkAtomic)
	if err == nil {
		t.Fatal("an atomic batch with a bad row must fail as a whole")
	}
	// The failing item is named, and the code survives the wrapping: an
	// agent must be able to tell which row it has to fix and how.
	if !errors.Is(err, metamodel.ErrSchemaViolation) {
		t.Fatalf("err = %v, want it to still read as a schema violation", err)
	}
	if !strings.Contains(err.Error(), `item 1 ("b")`) {
		t.Fatalf("err = %v, want it to name the failing item", err)
	}
	if len(result.Succeeded) != 0 || len(result.Failed) != 0 {
		t.Fatalf("a failed atomic batch reports nothing as done: %+v", result)
	}
	if _, err := svc.EntityByKey(ctx, project, "quest", "a"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("the good row of a failed atomic batch must have been rolled back, got %v", err)
	}
}

func TestBulkAtomicLandsEveryRowTogether(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
		{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": float64(2)}},
	}, metamodel.BulkAtomic)
	if err != nil {
		t.Fatalf("UpsertEntities: %v", err)
	}
	if len(result.Succeeded) != 2 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, want two rows and no failures", result)
	}
	for _, key := range []string{"a", "b"} {
		if _, err := svc.EntityByKey(ctx, project, "quest", key); err != nil {
			t.Fatalf("entity %q: %v", key, err)
		}
	}
}

// TestBulkRejectsAnUnknownMode keeps an unrecognised mode from meaning
// "partial".
//
// Task 7 builds the mode straight from an agent-supplied string, so a
// typo — "atomic ", "all-or-nothing" — would otherwise be read as the
// permissive mode and land rows a caller asked to have rolled back. A
// silent downgrade of a durability request is exactly the failure a
// caller cannot see, so it is refused at its own path instead.
func TestBulkRejectsAnUnknownMode(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	_, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
	}, metamodel.BulkMode("atomic "))
	requireFieldError(t, err, "mode", `must be "partial" or "atomic"`)
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
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
}

// TestBulkPartialStopsWhenTheCallerIsGone pins the one thing a partial
// batch owes a cancelled caller.
//
// Every item is its own transaction, so nothing stops the loop on its
// own: without the check, a cancelled context turns a 500-row batch into
// 500 failed round trips whose report nobody is left to read, and the
// call still returns a nil error, which reads as "the batch ran". The
// error is returned alongside whatever had already landed, because in
// partial mode rows landing before the cancellation is the mode's
// contract, not a bug to hide.
func TestBulkPartialStopsWhenTheCallerIsGone(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
		{TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": float64(2)}},
	}, metamodel.BulkPartial)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("a cancelled batch reports no per-item failures, got %+v", result.Failed)
	}
	if _, err := svc.EntityByKey(context.Background(), project, "quest", "a"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("nothing may be written after the caller is gone, got %v", err)
	}
}

// TestNoEntityEventIsPublishedForARolledBackBatch pins publish-after-
// commit on the path where it is easiest to get wrong: the atomic batch
// writes and commits in different functions, so a publish placed beside
// the write would announce rows that are about to vanish.
func TestNoEntityEventIsPublishedForARolledBackBatch(t *testing.T) {
	pool := testutil.NewPool(t)
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
}

// TestABulkBatchAnnouncesEveryRowItLanded covers the partial path's own
// publication: one identity event per row that landed, and none for the
// row that did not. A subscriber's only correct reaction to an entity
// event is to re-read the row it names, so a batch announcing a count —
// or announcing rows it rejected — tells it to re-read the wrong thing.
func TestABulkBatchAnnouncesEveryRowItLanded(t *testing.T) {
	pool := testutil.NewPool(t)
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
		if got.Kind != "entity.upserted" {
			t.Fatalf("Kind = %q, want entity.upserted", got.Kind)
		}
		if key := payloadField(t, got, "key"); key != wantKey {
			t.Fatalf("payload key = %q, want %q", key, wantKey)
		}
	}
	requireNothing(t, sub, "the rejected row landed nothing")
}

// TestEntityEventsCarryTheStoredIdentity pins whose spelling an event
// reports. Keys are matched without regard to case, so a caller may
// address type "Quest" as "quest"; the event must carry the identity as
// stored, because a subscriber uses it to re-read a row and the stored
// spelling is the one every other reader sees.
func TestEntityEventsCarryTheStoredIdentity(t *testing.T) {
	pool := testutil.NewPool(t)
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
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got := receive(t, sub)
	if got.Kind != "entity.upserted" {
		t.Fatalf("Kind = %q, want entity.upserted", got.Kind)
	}
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
	if got.Kind != "entity.removed" {
		t.Fatalf("Kind = %q, want entity.removed", got.Kind)
	}
	assertIdentityPayload(t, "viewer", got, row.ID, "Hogger")
	if key := payloadField(t, got, "type_key"); key != "Quest" {
		t.Fatalf("removal payload type_key = %q, want %q", key, "Quest")
	}
}

func TestRemoveEntity(t *testing.T) {
	pool := testutil.NewPool(t)
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
}

// TestSchemaChangeFlagsAnEntityWrittenThroughTheService is the sweep seen
// from the other side. TestSchemaChangeFlagsRowsInvalidWithoutTouchingThem
// pins what the sweep must not touch, over rows written straight to the
// database; this one pins that a row written through UpsertEntity — whose
// values have been through Validate and back out of jsonb — is judged by
// the same sweep when a new required field appears.
func TestSchemaChangeFlagsAnEntityWrittenThroughTheService(t *testing.T) {
	pool := testutil.NewPool(t)
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
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if !row.Invalid {
		t.Fatal("the row should be flagged invalid after the schema change")
	}
	if row.Name != "Hogger" {
		t.Fatal("the row's data must not have been altered")
	}

	// Writing the row again with the missing value clears the flag: the
	// upsert resets invalid because it has just judged these values.
	fixed, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Hogger",
		Fields:          map[string]any{"min_level": float64(10), "faction": "Alliance"},
		ExpectedVersion: ptrInt32(1),
	})
	if err != nil {
		t.Fatalf("fix the row: %v", err)
	}
	if fixed.Invalid {
		t.Fatal("a row that has just been validated must not stay flagged")
	}
}

func TestEntitiesAreScopedToTheirProject(t *testing.T) {
	pool := testutil.NewPool(t)
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
}

// TestAnEntityActorFromAnotherGameIsNamed covers the database's own
// backstop on entities: the composite
// FOREIGN KEY (updated_by_token_id, project_id) refuses a token scoped to
// another game, and the refusal has to say so rather than surface a raw
// SQLSTATE 23503 into a log nobody can act on.
func TestAnEntityActorFromAnotherGameIsNamed(t *testing.T) {
	pool := testutil.NewPool(t)
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
	if !errors.Is(err, metamodel.ErrActorNotInGame) {
		t.Fatalf("err = %v, want ErrActorNotInGame", err)
	}
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
	if err != nil {
		t.Fatalf("a token writing to its own game: %v", err)
	}
	if row.UpdatedByTokenID == nil || *row.UpdatedByTokenID != own {
		t.Fatalf("UpdatedByTokenID = %v, want %v", row.UpdatedByTokenID, own)
	}
}

// payloadField reads one string field of an event payload through its
// JSON shape, which is the only thing a client ever sees.
func payloadField(t *testing.T, e realtime.Event, field string) string {
	t.Helper()
	raw, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode payload %s: %v", raw, err)
	}
	value, ok := got[field].(string)
	if !ok {
		t.Fatalf("payload %s carries no string %q", raw, field)
	}
	return value
}

// TestACancelledBatchDoesNotReportTheItemInFlightAsAServerFault pins the
// second half of the cancellation contract.
//
// TestBulkPartialStopsWhenTheCallerIsGone cancels before item 0, so the
// loop's guard catches it between two items and nothing is in flight.
// The ordinary case is the other one: the cancellation lands *inside* an
// item's own transaction. That item then fails like any other, and
// without a second look at ctx.Err() on the failure arm it is recorded by
// failureFor as internal_error — the code reserved for a fault nobody
// planned for — so one cancellation surfaces as both a stop error and a
// per-item server fault the caller is told to report rather than retry.
//
// The rival holds item 1's row locked, so the batch is stopped where the
// interleaving is the test's rather than the scheduler's.
func TestACancelledBatchDoesNotReportTheItemInFlightAsAServerFault(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	base := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	blocked, err := svc.UpsertEntity(base, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "b", Name: "B", Fields: map[string]any{"min_level": float64(2)},
	})
	if err != nil {
		t.Fatalf("seed the row the batch will block on: %v", err)
	}

	rival, err := pool.Begin(base)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = rival.Rollback(base) }()
	if _, err := rival.Exec(base,
		`UPDATE entities SET name = 'Theirs' WHERE id = $1`, blocked.ID); err != nil {
		t.Fatalf("rival update: %v", err)
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
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", got.err)
		}
		if !strings.Contains(got.err.Error(), "item 1") {
			t.Fatalf("err = %v, want it to name the item the batch stopped at", got.err)
		}
		for _, failure := range got.result.Failed {
			t.Fatalf("a cancellation must not be reported as a per-item failure, got %+v", failure)
		}
		// Partial mode's contract: what landed before the cancellation is
		// returned rather than hidden, and nothing after it ran.
		if len(got.result.Succeeded) != 1 || got.result.Succeeded[0].Key != "a" {
			t.Fatalf("Succeeded = %+v, want only item 0", got.result.Succeeded)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the batch never returned after the cancellation")
	}

	if _, err := svc.EntityByKey(base, project, "quest", "c"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("the item after the cancellation must not have run, got %v", err)
	}
}

// TestAListOfTextIsSearchable pins the one text-bearing field type that
// is not a plain string.
//
// Tags and aliases are the archetypal thing a designer searches for, and
// a list<text> whose elements never reach the tsvector is a row that
// silently cannot be found by its own tags — with nothing to signal why,
// which is the failure UpsertEntity's SQL comment already argues against
// for the other write paths. Task 6's search inherits whatever this
// writes.
func TestAListOfTextIsSearchable(t *testing.T) {
	pool := testutil.NewPool(t)
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
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, term := range []string{"Hogger", "gnolls", "elite", "dungeon", "heroic"} {
		if !entityMatches(t, pool, row.ID, term) {
			t.Fatalf("%q is not in the search vector", term)
		}
	}

	// An update drops the old elements as it does the old words of any
	// other field: the column is rewritten, not added to.
	updated, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
		Fields:          map[string]any{"tags": []any{"raid"}},
		ExpectedVersion: ptrInt32(1),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if entityMatches(t, pool, updated.ID, "elite") {
		t.Fatal("a removed element is still indexed")
	}
	if !entityMatches(t, pool, updated.ID, "raid") {
		t.Fatal("the new element was not indexed")
	}
}

// searchColumn reads the stored tsvector as text, positions included:
// two rows built from the same values must produce the same string, and
// the positions are what makes the field order observable.
// searchColumn reads the value-derived halves of a row's search vector:
// the name under label A and the name plus the flattened field text
// under label B.
//
// **It filters out label C, which is the row's key**, because the one
// caller compares two *different* rows and 0010_entity_key_search.sql
// made the key part of the vector. Without the filter the comparison
// below would be red for the one reason it is not asking about — the two
// rows are `q0` and `q1` and are supposed to differ there.
// TestTheEntityKeyBackfillIsExact (internal/db) is what asserts the C
// half, whole and unfiltered.
func searchColumn(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var text string
	if err := pool.QueryRow(context.Background(),
		`SELECT ts_filter(search, '{a,b}')::text FROM entities WHERE id = $1`, id).Scan(&text); err != nil {
		t.Fatalf("read search column: %v", err)
	}
	return text
}

// TestTheSearchVectorIsTheSameForTheSameValues pins the key sort in
// searchTextOf.
//
// Map iteration order is randomised per range, so without the sort two
// rows holding identical values get their words in different orders and
// the tsvector's positions differ. Nothing about search results changes,
// which is why nothing else catches it: what it costs is a stored column
// that differs between two identical writes, a diff no reader of the row
// can explain and a re-seed that looks like an edit.
func TestTheSearchVectorIsTheSameForTheSameValues(t *testing.T) {
	pool := testutil.NewPool(t)
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
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		got := searchColumn(t, pool, row.ID)
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("two rows with identical values were indexed differently:\n %s\n %s", first, got)
		}
	}
}

// TestBulkEventsCarryTheStoredIdentity extends the single path's pin to
// the other two.
//
// TestEntityEventsCarryTheStoredIdentity exercises UpsertEntity alone,
// and the bulk test that already existed asserts only the entity key —
// which the database returns whatever the caller spelled. So both bulk
// paths could publish the caller's own spelling of the *type* key and
// leave the suite green, and a subscriber would be handed an identity no
// other reader of the game sees.
func TestBulkEventsCarryTheStoredIdentity(t *testing.T) {
	for _, mode := range []metamodel.BulkMode{metamodel.BulkPartial, metamodel.BulkAtomic} {
		t.Run(string(mode), func(t *testing.T) {
			pool := testutil.NewPool(t)
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
			if got.Kind != "entity.upserted" {
				t.Fatalf("Kind = %q, want entity.upserted", got.Kind)
			}
			if key := payloadField(t, got, "type_key"); key != "Quest" {
				t.Fatalf("payload type_key = %q, want the stored spelling %q", key, "Quest")
			}
			if key := payloadField(t, got, "key"); key != "Hogger" {
				t.Fatalf("payload key = %q, want the stored spelling %q", key, "Hogger")
			}
		})
	}
}

// TestACreationThatLosesItsKeyToAnotherSpellingIsNamedAsARespelling
// reaches conflictOnEntityKey's respelling arm, the one check on the
// entity path that nothing else exercised.
//
// The other two race tests both end at the *post-write* spelling check,
// because their guard passes and the upsert returns the rival's row. This
// one holds a version the rival's row does not have, so the guarded
// DO UPDATE matches nothing and there is no row to compare: the re-read
// is the only thing left that can tell the caller its key already exists
// under another spelling. Without it the caller is told to merge onto a
// version, retries with it, and is refused again for a reason it has
// never been given — the entity twin of
// TestACreationThatLosesItsKeyToAnotherSpellingIsNamedAsARespelling in
// types_test.go.
func TestAnEntityLosingItsKeyToAnotherSpellingIsNamedAsARespelling(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)
	typ, err := svc.EntityTypeByKey(ctx, project, "quest")
	if err != nil {
		t.Fatalf("EntityTypeByKey: %v", err)
	}

	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
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
		if errors.Is(err, metamodel.ErrVersionConflict) {
			t.Fatalf("err = %v must not read as a version conflict: the version is not what the "+
				"caller can act on here", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival committed")
	}

	row, err := svc.EntityByKey(ctx, project, "quest", "hogger")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if row.Name != "Theirs" || row.Version != 1 {
		t.Fatalf("the losing writer changed the row: %+v", row)
	}
}

// TestTheReportedCurrentEntityVersionIsTheOneTheWriteWouldHaveMet pins
// the FOR UPDATE on GetEntityByKeyForUpdate, as
// TestTheReportedCurrentVersionIsTheOneTheWriteWouldHaveMet does for
// types.
//
// Deleting the lock leaves the rest of the file green: both entity race
// tests block on the unique index inside the INSERT, not on this lock,
// and the compare-and-set in the DO UPDATE refuses every lost update on
// its own. What the lock earns is the *number* the caller is told to
// merge onto. Without it the read runs against this transaction's
// snapshot and reports the version committed when it started, so a caller
// racing an in-flight edit re-issues with that version and is refused
// again — a loop it cannot leave by doing what the error said.
func TestTheReportedCurrentEntityVersionIsTheOneTheWriteWouldHaveMet(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Hogger",
		Fields: map[string]any{"min_level": float64(10)},
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
		if !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want a *VersionConflictError", err)
		}
		if conflict.Current != 2 {
			t.Fatalf("Current = %d, want 2: the caller must be told the version its own write "+
				"would have met, not the one visible before the rival committed", conflict.Current)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival committed")
	}
}

// TestTheEntityQueriesThatAddressARowByIDAreScopedToTheProject pins the
// two project filters TestEntitiesAreScopedToTheirProject cannot see.
//
// Both are reached only from RemoveEntity, which reads the row, then its
// type, then deletes — so each of the three filters masks the others.
// Drop GetEntityByID's and the type re-read still refuses (a type id
// belongs to one game); drop DeleteEntity's and the row read has already
// refused. Only mutating all three at once turns the service-level test
// red, which is no pin at all: it says the three together are load
// bearing without saying that any one of them is. The filters are the
// isolation between two games, so each is asserted where it lives, over
// the query itself.
func TestTheEntityQueriesThatAddressARowByIDAreScopedToTheProject(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, pool), newProject(t, pool)
	seedQuestType(t, svc, mine)

	row, err := svc.UpsertEntity(ctx, mine, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Hogger",
		Fields: map[string]any{"min_level": float64(10)},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	q := dbq.New(pool)

	if _, err := q.GetEntityByID(ctx, dbq.GetEntityByIDParams{
		ProjectID: theirs, ID: row.ID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetEntityByID handed another game's row out: err = %v, want pgx.ErrNoRows", err)
	}
	rows, err := q.DeleteEntity(ctx, dbq.DeleteEntityParams{ProjectID: theirs, ID: row.ID})
	if err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if rows != 0 {
		t.Fatalf("DeleteEntity removed %d of another game's rows", rows)
	}

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
}

// TestABatchThatRepeatsAKeyIsDiagnosedAsSuch covers the ordinary
// accident: an agent seeding from a file that names one quest twice.
//
// Keys are matched without regard to case, so the two items address one
// row. Left to the database the second is refused as a *version
// conflict* — "current version is 1" against a caller that never claimed
// a version — which sends an agent to re-read a row and retry with the
// version it is handed, at which point its second item silently
// overwrites its first. The batch is the only place the real diagnosis
// exists, so it is made here: the item is refused as invalid_input and
// the message names the item it collides with.
//
// In partial mode the duplicate is a per-item failure and nothing else
// changes. Refusing the whole batch would throw away the other
// three hundred rows over one repeated key, which is the failure partial
// mode exists to prevent, and the first occurrence is a perfectly good
// item.
func TestABatchThatRepeatsAKeyIsDiagnosedAsSuch(t *testing.T) {
	pool := testutil.NewPool(t)
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
	if err != nil {
		t.Fatalf("UpsertEntities: %v", err)
	}
	if len(result.Failed) != 2 {
		t.Fatalf("failed = %+v, want the two repetitions", result.Failed)
	}
	// The exact repeat and the differently-capitalised one are the same
	// fault: the folding index makes them one key, so both are reported
	// against the item that got there first.
	for i, want := range []struct {
		index int
		key   string
	}{{2, "dup"}, {3, "DUP"}} {
		failure := result.Failed[i]
		if failure.Index != want.index || failure.Key != want.key {
			t.Fatalf("failure = %+v, want index %d and its own key %q", failure, want.index, want.key)
		}
		if failure.Code != "invalid_input" {
			t.Fatalf("code = %q, want invalid_input: the caller claimed no version, and the "+
				"repetition is a fault in its own arguments", failure.Code)
		}
		if failure.Code == "version_conflict" || strings.Contains(failure.Message, "current version") {
			t.Fatalf("message = %q misdiagnoses a repeated key as a stale version", failure.Message)
		}
		if !strings.Contains(failure.Message, "item 1") {
			t.Fatalf("message = %q, want it to name the item the key collides with", failure.Message)
		}
	}
	// The other three landed, and the row holds the first occurrence: a
	// duplicate must not overwrite the item it duplicates.
	if len(result.Succeeded) != 3 {
		t.Fatalf("succeeded = %d, want the three items that were not repeated", len(result.Succeeded))
	}
	row, err := svc.EntityByKey(ctx, project, "quest", "dup")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if row.Name != "First" || row.Version != 1 {
		t.Fatalf("row = %+v, want the first occurrence, written once", row)
	}
}

// TestAnAtomicBatchThatRepeatsAKeyIsRefusedWhole is the same accident in
// the other mode, where it is worse: both items pass their guards, both
// "succeed", and the caller is handed a Succeeded list carrying the same
// row id twice — told that two rows landed when one exists. An atomic
// batch cannot be satisfied as submitted (the caller asked for N rows and
// at most N-1 can exist), so it is refused before anything is written,
// which also makes the error deterministic rather than dependent on which
// item ran first.
func TestAnAtomicBatchThatRepeatsAKeyIsRefusedWhole(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "a", Name: "A", Fields: map[string]any{"min_level": float64(1)}},
		{TypeKey: "quest", Key: "dup", Name: "First", Fields: map[string]any{"min_level": float64(2)}},
		{TypeKey: "quest", Key: "dup", Name: "Second", Fields: map[string]any{"min_level": float64(3)}},
	}, metamodel.BulkAtomic)
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	requireFieldError(t, err, "items[2].key",
		`"dup" is already addressed by item 1 of this batch, and keys are matched without regard `+
			`to case: give one of the two items a different key, or merge them into one`)
	if len(result.Succeeded) != 0 || len(result.Failed) != 0 {
		t.Fatalf("a refused atomic batch reports nothing as done: %+v", result)
	}
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
	if err != nil {
		t.Fatalf("seed the chained row: %v", err)
	}
	result, err = svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "chain", Name: "Second", ExpectedVersion: ptrInt32(1),
			Fields: map[string]any{"min_level": float64(2)}},
		{TypeKey: "quest", Key: "chain", Name: "Third", ExpectedVersion: ptrInt32(2),
			Fields: map[string]any{"min_level": float64(3)}},
	}, metamodel.BulkAtomic)
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput; Succeeded = %+v", err, result.Succeeded)
	}
	row, err := svc.EntityByKey(ctx, project, "quest", "chain")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if row.Version != chained.Version {
		t.Fatalf("the refused batch moved the row to version %d", row.Version)
	}

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
}

// TestAnOversizedFieldIsStoredWholeAndFoundByAWordNearItsStart pins the
// one trade the search index is allowed to make against a game's content.
//
// to_tsvector refuses to build a vector larger than 1,048,575 bytes
// (SQLSTATE 54000), and nothing bounds the length of a text or longtext
// value: a designer pasting a lore document of a megabyte or two had the
// whole row refused, reported as internal_error — the code reserved for
// what nobody planned for, which tells an agent to give up on a call it
// could have fixed. The row is the product and the index is a
// convenience, so the index is what gives way: searchTextOf bounds what
// it hands the vector, `fields` is stored untouched, and the documented
// consequence is that the tail of a very long field is not searchable.
func TestAnOversizedFieldIsStoredWholeAndFoundByAWordNearItsStart(t *testing.T) {
	pool := testutil.NewPool(t)
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
	if err != nil {
		t.Fatalf("a long field must not make the row unsavable: %v", err)
	}

	stored, err := svc.EntityByKey(ctx, project, "quest", "lore")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var fields struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(stored.Fields, &fields); err != nil {
		t.Fatalf("decode stored fields: %v", err)
	}
	if fields.Summary != summary {
		t.Fatalf("the stored field was altered: %d bytes stored, %d written",
			len(fields.Summary), len(summary))
	}

	if !entityMatches(t, pool, row.ID, "openingsigil") {
		t.Fatal("a word near the start of a long field is not searchable")
	}
	// The trade, asserted so it cannot be quietly widened or dropped: the
	// tail of a field this long is outside the index.
	if entityMatches(t, pool, row.ID, "closingsigil") {
		t.Fatal("the whole of an oversized field reached the index; the bound is gone")
	}
}

// cancelWhenLanded is a context that reports cancellation from the moment
// a given entity key exists in the database.
//
// The finding it exists for is an ordering one: in UpsertEntities the
// cancellation guard must be consulted before anything else the loop
// does with an item, and a repeated key is the one thing that used to be
// answered ahead of it. Forcing that ordering to matter needs a
// cancellation landing *after* the last item that writes has committed
// and *before* the loop reaches a trailing repeat — a window a
// concurrent cancel() cannot be aimed at.
//
// So the context arms itself, synchronously, from inside Err(): the row
// item 0 writes is visible to a second connection only once item 0's
// transaction has committed, which is exactly the edge wanted. It cannot
// disturb the item it observes — nothing in that item's transaction can
// see the row before the commit that ends it — and it needs no timing
// assumption at all.
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

// TestACancelledBatchDoesNotAnswerARepeatedKeyInstead pins the order of
// the two checks at the top of the bulk loop.
//
// A repeated key is a per-item failure and a cancellation stops the
// batch, so which is consulted first decides what the caller is told
// when both apply. With the repeat check first, a batch whose trailing
// item is a repeat reported that repeat and returned a nil error —
// which reads as "the batch ran" — even though the caller had gone
// before the loop reached it. That is precisely the failure the
// cancellation guard exists to prevent, escaping through the one arm
// that never consulted it.
func TestACancelledBatchDoesNotAnswerARepeatedKeyInstead(t *testing.T) {
	pool := testutil.NewPool(t)
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

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "item 1") {
		t.Fatalf("err = %v, want it to name the item the batch stopped at", err)
	}
	for _, failure := range result.Failed {
		t.Fatalf("a batch that stopped must report no per-item failure, got %+v", failure)
	}
	// Partial mode's contract is unchanged: what landed before the
	// cancellation comes back rather than being hidden.
	if len(result.Succeeded) != 1 || result.Succeeded[0].Key != "a" {
		t.Fatalf("Succeeded = %+v, want only item 0", result.Succeeded)
	}
}

// TestARepeatIsRefusedEvenWhenTheFirstOccurrenceIsDoomed pins the corner
// of the repeated-key rule that costs an agent a round trip, so that the
// core design's claim about it stays true.
//
// The first occurrence fails validation and the second is well formed,
// so a caller could reasonably expect the good one to land. It does not:
// the repeat is decided from the batch as submitted, before any item
// runs, and the key ends up written by neither. Falling back to
// "whichever survived validation" would make one item's outcome depend
// on another item's mistakes, in a batch that never said which of the
// two rows it meant.
func TestARepeatIsRefusedEvenWhenTheFirstOccurrenceIsDoomed(t *testing.T) {
	pool := testutil.NewPool(t)
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
	if err != nil {
		t.Fatalf("a partial batch reports its failures rather than erroring: %v", err)
	}
	if len(result.Succeeded) != 0 {
		t.Fatalf("Succeeded = %+v, want nothing written", result.Succeeded)
	}
	if len(result.Failed) != 2 {
		t.Fatalf("Failed = %+v, want both items reported", result.Failed)
	}
	if result.Failed[0].Index != 0 || result.Failed[0].Code != "schema_violation" {
		t.Fatalf("Failed[0] = %+v, want index 0 as schema_violation", result.Failed[0])
	}
	if result.Failed[1].Index != 1 || result.Failed[1].Code != "invalid_input" {
		t.Fatalf("Failed[1] = %+v, want index 1 as invalid_input", result.Failed[1])
	}
	if _, err := svc.EntityByKey(ctx, project, "quest", "x"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("the key must be written by neither item, got %v", err)
	}
}

// TestUpsertEntityRefusesUnprintableTextBeforeItReachesPostgres pins the
// write-side half of the rule checkSearchQuery already applies on the
// read side: caller-supplied text must be valid UTF-8 and free of
// control characters, refused as the caller's own argument rather than
// left to Postgres.
//
// Proved live before this test existed, all of them through
// UpsertEntity: a NUL inside `name` returned `ERROR: invalid byte
// sequence for encoding "UTF8" (SQLSTATE 22021)`, untyped; the same NUL
// inside a `longtext` value returned `ERROR: unsupported Unicode escape
// sequence (SQLSTATE 22P05)`, untyped; a NUL substitute — a lone
// continuation byte, 0xff, not valid UTF-8 at all — returned the first
// error again; and a newline inside `name` was accepted and stored,
// which is the one of these five that stays accepted, deliberately: see
// textProblem's doc comment for why `longtext` keeps its newlines and
// `name` does not.
func TestUpsertEntityRefusesUnprintableTextBeforeItReachesPostgres(t *testing.T) {
	pool := testutil.NewPool(t)
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
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a *ValidationError", err)
			}
			code := ve.Code
			if code == "" {
				code = "schema_violation" // the zero value; see ValidationError.code
			}
			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantSubstr)
			}
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
	if err != nil {
		t.Fatalf("a newline and a tab in a longtext value must be accepted: %v", err)
	}
	if row.Key != "newline-in-summary" {
		t.Fatalf("row.Key = %q, want it stored", row.Key)
	}
}

// TestABulkWriteReportsWhatLandedInAWireShape pins Task 7's answer to
// "what does a successful batch tell an agent". Until Task 7,
// BulkResult.Succeeded was the only record of it, `json:"-"` and full of
// database columns, so a marshalled result reported failures and nothing
// else — a perfect four-hundred-row batch answered with `{}`.
//
// Written is the wire half. It carries, per row that landed, the three
// things an agent cannot derive from what it sent: the row's id (which
// is what entities.remove takes), its version (which is what
// expected_version takes on the next edit), and — with type_key and key,
// which it *can* derive — the address that says which of its own items
// this row is.
func TestABulkWriteReportsWhatLandedInAWireShape(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "a", Name: "Alpha", Fields: map[string]any{"min_level": float64(1)}},
		{TypeKey: "quest", Key: "b", Name: "Beta", Fields: map[string]any{"min_level": "nope"}},
		{TypeKey: "quest", Key: "c", Name: "Gamma", Fields: map[string]any{"min_level": float64(3)}},
	}, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("UpsertEntities: %v", err)
	}
	if len(result.Written) != 2 {
		t.Fatalf("written = %+v, want the two rows that landed", result.Written)
	}
	if got := []string{result.Written[0].Key, result.Written[1].Key}; !equalStrings(got, []string{"a", "c"}) {
		t.Fatalf("written keys = %v, want [a c]", got)
	}
	for i, w := range result.Written {
		if w.TypeKey != "quest" {
			t.Fatalf("written[%d].TypeKey = %q, want the stored type key", i, w.TypeKey)
		}
		if w.ID != result.Succeeded[i].ID {
			t.Fatalf("written[%d].ID = %s, want the row's own id %s", i, w.ID, result.Succeeded[i].ID)
		}
		if w.Version != 1 {
			t.Fatalf("written[%d].Version = %d, want 1 for a freshly created row", i, w.Version)
		}
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
	if err != nil {
		t.Fatalf("UpsertEntities again: %v", err)
	}
	if len(again.Written) != 1 || again.Written[0].Version != 2 {
		t.Fatalf("written = %+v, want version 2 on the second write", again.Written)
	}
}

// TestABulkWriteMarshalsWhatLanded pins the same thing through JSON,
// which is the surface an agent actually reads: the failures-only report
// this replaced was a marshalling fact, not a Go one, so a test that
// only read the Go struct would not have caught it and would not catch
// its return.
func TestABulkWriteMarshalsWhatLanded(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	result, err := svc.UpsertEntities(ctx, project, []metamodel.EntityInput{
		{TypeKey: "quest", Key: "a", Name: "Alpha", Fields: map[string]any{"min_level": float64(1)}},
	}, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("UpsertEntities: %v", err)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
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
	if len(decoded.Written) != 1 {
		t.Fatalf("marshalled result %s carries no record of the row that landed", raw)
	}
	w := decoded.Written[0]
	if w.TypeKey != "quest" || w.Key != "a" || w.Version != 1 || w.ID != result.Succeeded[0].ID.String() {
		t.Fatalf("marshalled written = %+v, want the row's own address, id and version", w)
	}
	// A database row's own columns stay off the wire: Succeeded is
	// json:"-" for the reason BulkResult records, and this pins that
	// adding Written did not quietly put them back.
	if strings.Contains(string(raw), "updated_by") || strings.Contains(string(raw), "project_id") {
		t.Fatalf("marshalled result %s carries database columns", raw)
	}
}
