package metamodel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// This file is one rule over four tables: **a version claim is a claim
// about a row that exists.**
//
// Every upsert in this package used to read an `expected_version` that
// reached its insert path as no claim at all. The locked read found
// nothing, the call took the creation path, the guard on the DO UPDATE
// was never evaluated, and a brand-new row appeared under a new id at
// version 1 with the call returning nil. What that costs is not the
// content — the caller was resending it anyway — but every relation,
// view position, saved-view reference and attachment that named the row
// that was removed: each of them now names nothing, while a row with the
// same key sits there looking fine.
//
// The refusal says the row was **removed** and never that the version is
// stale, because the two have different recoveries: stale means re-read
// and merge, and removed means decide whether to re-create the row
// deliberately, with no claim, accepting a new row with a new id.

// holdRemoval opens a transaction of the test's own, deletes one row from
// one table, and holds the lock until the returned function commits it.
//
// **This is what makes the refusal a raced test rather than a sequential
// one.** The state the rule is about is produced by an update *parking
// behind a committed removal*: the writer's locked read blocks on the row
// lock the deletion holds, and only when the deletion commits does that
// read come back empty and hand the writer the creation path. A test
// that simply removes a row and then calls the upsert reaches the same
// branch without ever proving the race, so a fix that only worked
// sequentially would look identical. Staging one side as plain SQL is
// the technique lock_order_test.go's holdEntityTypeRow uses, and for the
// same reason: the interleaving is the test's to choose and not the
// scheduler's.
func holdRemoval(t *testing.T, pool *pgxpool.Pool, table string, id uuid.UUID) (commit func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the removing transaction: %v", err)
	}
	done := false
	t.Cleanup(func() {
		if !done {
			_ = tx.Rollback(ctx)
		}
	})
	// The table name is a literal from this file, never caller data.
	tag, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE id = $1", id)
	if err != nil {
		t.Fatalf("delete from %s: %v", table, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("the removal deleted %d rows of %s", tag.RowsAffected(), table)
	}
	return func() {
		done = true
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit the removal: %v", err)
		}
	}
}

// requireRemoved asserts the refusal a version claim against a missing
// row gets: not_found, saying the row was removed, and never a version
// conflict.
func requireRemoved(t *testing.T, err error, address string) {
	t.Helper()
	if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want not_found", err)
	}
	if errors.Is(err, metamodel.ErrVersionConflict) {
		t.Fatalf("err = %v, want not_found and *not* a version conflict: merging onto a "+
			"version is the one recovery that cannot work when the row is gone", err)
	}
	if !strings.Contains(err.Error(), "was removed") {
		t.Fatalf("err = %v, want it to say the row was removed", err)
	}
	if !strings.Contains(err.Error(), address) {
		t.Fatalf("err = %v, want the row named as %s", err, address)
	}
	if !strings.Contains(err.Error(), "no expected_version") {
		t.Fatalf("err = %v, want it to name the recovery: send it again with no claim", err)
	}
}

// TestAnUpdateThatLosesToACommittedRemovalIsToldTheRowIsGone is the test
// the whole rule exists for, and it races the state rather than
// producing it sequentially.
//
// A designer removes the type; an agent that read version 1 a moment
// earlier writes to it. The write's locked read parks on the row lock the
// removal holds. When the removal commits, that read comes back empty —
// and before this rule the write took the creation path from there, its
// version claim never evaluated, and stored the type again under a **new
// id**, silently undoing the removal and orphaning every entity, edge and
// saved-view reference that named the id that is gone.
//
// The three assertions are separate on purpose: that the call is
// refused, that it is refused as *removed* rather than as a stale
// version, and that nothing was written — a refusal reported after the
// row landed would be worse than no refusal, because the caller would
// stop looking.
func TestAnUpdateThatLosesToACommittedRemovalIsToldTheRowIsGone(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	})
	if err != nil {
		t.Fatalf("declare: %v", err)
	}

	commitRemoval := holdRemoval(t, pool, "entity_types", typ.ID)

	result := make(chan error, 1)
	go func() {
		_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
			Description:     "an edit written against the version the designer removed",
			ExpectedVersion: &typ.Version,
		})
		result <- err
	}()

	// The write must be *parked*, not merely slow: if it returned here it
	// would have run entirely before the removal committed, and the state
	// this test is about would never have existed.
	waitFor(t, "the update to park on the row the removal holds", func() bool {
		return lockWaiters(t, standaloneConn(t, pool)) >= 1
	})
	select {
	case err := <-result:
		t.Fatalf("the update returned %v before the removal committed; it should have "+
			"parked on the row lock, which is the interleaving this test is about", err)
	default:
	}

	commitRemoval()

	err = <-result
	requireRemoved(t, err, `"quest"`)

	// And nothing was written: no row came back under a new id.
	if _, err := svc.EntityTypeByKey(ctx, project, "quest"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("the type is back: %v — the refusal has to be a refusal, not a report "+
			"made after the resurrection landed", err)
	}
}

// TestAVersionClaimAgainstATypeThisGameNeverHadIsRefusedToo is the
// sequential half, and it is here to pin that the rule is about the
// *claim* and not about the interleaving: a caller that states a version
// for a key this game has never had is making the same false statement
// as one that lost to a removal, and hears the same thing.
//
// It replaces TestAVersionClaimAgainstAMissingTypeCreatesItRatherThanRefusing,
// which pinned the opposite outcome. That test's argument was that
// nothing is overwritten and the returned Version of 1 tells the caller
// it created — true, and beside the point: what is lost is not the
// content but every reference to the id that went away.
func TestAVersionClaimAgainstATypeThisGameNeverHadIsRefusedToo(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		ExpectedVersion: ptrInt32(1),
	})
	requireRemoved(t, err, `"quest"`)
}

// TestNoVersionClaimStillCreates is the control, and without it the rule
// above could be implemented as "refuse every insert" and every other
// test in this file would still pass. A caller that claims nothing is
// claiming nothing, and gets what it asked for.
func TestNoVersionClaimStillCreates(t *testing.T) {
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
	if typ.Version != 1 {
		t.Fatalf("Version = %d, want 1", typ.Version)
	}

	// And a re-creation after a real removal, still claiming nothing,
	// still works — which is the recovery the refusal above names, so it
	// has to be a recovery that exists.
	if err := svc.RemoveEntityType(ctx, project, typ.ID, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	again, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	})
	if err != nil {
		t.Fatalf("deliberate re-creation with no claim: %v", err)
	}
	if again.ID == typ.ID {
		t.Fatal("the removed row cannot have come back; this is a new one")
	}
}

// TestEveryUpsertOfThisShapeRefusesAClaimAgainstAMissingRow carries the
// rule the one step this repository most often fails to carry: the
// defect was found on the type upsert and on the view upsert, and all
// four metamodel writers have the same shape. A rule honoured by two of
// four callers is a rule that holds in two of four places, and an agent
// meeting the other two learns that the answer depends on which call it
// made.
func TestEveryUpsertOfThisShapeRefusesAClaimAgainstAMissingRow(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("seed the entity type: %v", err)
	}
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "Requires",
	}); err != nil {
		t.Fatalf("seed the relation type: %v", err)
	}
	for _, key := range []string{"defias", "hogger"} {
		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: key, Name: key,
		}); err != nil {
			t.Fatalf("seed entity %s: %v", key, err)
		}
	}

	t.Run("relation type", func(t *testing.T) {
		_, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
			Key: "unlocks", Label: "Unlocks", ExpectedVersion: ptrInt32(1),
		})
		requireRemoved(t, err, `"unlocks"`)
	})

	t.Run("entity", func(t *testing.T) {
		_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "vanished", Name: "Vanished",
			ExpectedVersion: ptrInt32(1),
		})
		requireRemoved(t, err, `"vanished"`)
	})

	t.Run("relation", func(t *testing.T) {
		_, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
			TypeKey:         "requires",
			Source:          metamodel.Ref{TypeKey: "quest", Key: "defias"},
			Target:          metamodel.Ref{TypeKey: "quest", Key: "hogger"},
			ExpectedVersion: ptrInt32(1),
		})
		requireRemoved(t, err, `"requires"`)
		// The edge is named by both ends as well as by its type: an edge
		// has no key of its own, so this is the only address a caller
		// could compare against what it holds.
		if !strings.Contains(err.Error(), `"defias"`) || !strings.Contains(err.Error(), `"hogger"`) {
			t.Fatalf("err = %v, want both endpoints named", err)
		}
	})
}

// TestAnEntityUpdateThatLosesToACommittedRemovalIsToldTheRowIsGone races
// the entity table, which is where the loss is largest: an entity's id
// is named by every edge touching it and by every view position placing
// it, so a resurrection under a new id leaves a designer's arranged
// diagram pointing at nothing while the entity list looks untouched.
//
// The race is the same one, staged against `entities`.
func TestAnEntityUpdateThatLosesToACommittedRemovalIsToldTheRowIsGone(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("seed the type: %v", err)
	}
	row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
	})
	if err != nil {
		t.Fatalf("seed the entity: %v", err)
	}

	commitRemoval := holdRemoval(t, pool, "entities", row.ID)

	result := make(chan error, 1)
	go func() {
		_, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger, revised",
			ExpectedVersion: &row.Version,
		})
		result <- err
	}()

	waitFor(t, "the entity write to park on the row the removal holds", func() bool {
		return lockWaiters(t, standaloneConn(t, pool)) >= 1
	})
	select {
	case err := <-result:
		t.Fatalf("the write returned %v before the removal committed", err)
	default:
	}

	commitRemoval()

	requireRemoved(t, <-result, `"hogger"`)
	if _, err := svc.EntityByKey(ctx, project, "quest", "hogger"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("the entity is back: %v", err)
	}
}

// TestARemovalRefusalIsNotAConflictOnEitherSurface pins the discrimination
// the two recoveries turn on, from the error's own side: a caller
// matching ErrVersionConflict must not catch this, and a caller matching
// ErrNotFound must.
//
// The Go-level assertion is here rather than only in internal/web
// because it is the domain's promise: internal/web's mcpErrorFor and
// writeDomainError both dispatch on these sentinels, so getting Is wrong
// would publish a not_found as a version_conflict on both surfaces at
// once with nothing in either of them to see.
func TestARemovalRefusalIsNotAConflictOnEitherSurface(t *testing.T) {
	err := error(&metamodel.RemovedError{Subject: "entity type", Address: `"quest"`, Claimed: 4})
	if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatal("a RemovedError must read as not_found")
	}
	for name, sentinel := range map[string]error{
		"ErrVersionConflict": metamodel.ErrVersionConflict,
		"ErrInvalidInput":    metamodel.ErrInvalidInput,
		"ErrInUse":           metamodel.ErrInUse,
	} {
		if errors.Is(err, sentinel) {
			t.Fatalf("a RemovedError must not read as %s", name)
		}
	}
	if !strings.Contains(err.Error(), "version 4") {
		t.Fatalf("Error() = %q, want the version the caller claimed", err.Error())
	}
}
