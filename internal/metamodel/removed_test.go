package metamodel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/metamodel"
)

// This file is one rule over four tables: **a version claim is a claim
// about a row that exists.**
func holdRemoval(t *testing.T, pool *pgxpool.Pool, table string, id uuid.UUID) (commit func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	assert.Must(t, err == nil, "begin the removing transaction: %v", err)
	done := false
	t.Cleanup(func() {
		if !done {
			_ = tx.Rollback(ctx)
		}
	})
	// The table name is a literal from this file, never caller data.
	tag, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE id = $1", id)
	assert.Must(t, err == nil, "delete from %s: %v", table, err)
	assert.Must(t, tag.RowsAffected() == 1, "the removal deleted %d rows of %s", tag.RowsAffected(), table)
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
	assert.Must(t, errors.Is(err, metamodel.ErrNotFound), "err = %v, want not_found", err)
	assert.Must(t, !errors.Is(err, metamodel.ErrVersionConflict), "err = %v, want not_found and *not* a version conflict: merging onto a "+
		"version is the one recovery that cannot work when the row is gone", err)
	assert.Must(t, strings.Contains(err.Error(), "was removed"), "err = %v, want it to say the row was removed", err)
	assert.Must(t, strings.Contains(err.Error(), address), "err = %v, want the row named as %s", err, address)
	assert.Must(t, strings.Contains(err.Error(), "no expected_version"), "err = %v, want it to name the recovery: send it again with no claim", err)
}

func TestRemovedArea(t *testing.T) {
	t.Parallel()
	a := newArea(t)

	// TestRemovedArea's "an update that loses to a committed removal is told
	// the row is gone" case is the test the whole rule exists for, and it
	// races the state rather than producing it sequentially.
	t.Run("an update that loses to a committed removal is told the row is gone", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)

		typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
		})
		assert.Must(t, err == nil, "declare: %v", err)

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
	})

	// TestRemovedArea's "a version claim against a type this game never had is
	// refused too" case is the sequential half, and it is here to pin that the
	// rule is about the *claim* and not about the interleaving: a caller that
	// states a version for a key this game has never had is making the same
	// false statement as one that lost to a removal, and hears the same thing.
	t.Run("a version claim against a type this game never had is refused too", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)

		_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
			ExpectedVersion: ptrInt32(1),
		})
		requireRemoved(t, err, `"quest"`)
	})

	// TestRemovedArea's "no version claim still creates" case is the control,
	// and without it the rule above could be implemented as "refuse every
	// insert" and every other test in this file would still pass. A caller
	// that claims nothing is claiming nothing, and gets what it asked for.
	t.Run("no version claim still creates", func(t *testing.T) {
		pool := a.pool
		svc := metamodel.New(pool, nil)
		ctx := context.Background()
		project := newProject(t, pool)

		typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
		})
		assert.Must(t, err == nil, "create: %v", err)
		assert.Must(t, typ.Version == 1, "Version = %d, want 1", typ.Version)

		// And a re-creation after a real removal, still claiming nothing,
		// still works — which is the recovery the refusal above names, so it
		// has to be a recovery that exists.
		if err := svc.RemoveEntityType(ctx, project, typ.ID, false); err != nil {
			t.Fatalf("remove: %v", err)
		}
		again, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
		})
		assert.Must(t, err == nil, "deliberate re-creation with no claim: %v", err)
		assert.Must(t, again.ID != typ.ID, "the removed row cannot have come back; this is a new one")
	})

	// TestRemovedArea's "every upsert of this shape refuses a claim against a
	// missing row" case carries the rule the one step this repository most
	// often fails to carry: the defect was found on the type upsert and on the
	// view upsert, and all four metamodel writers have the same shape. A rule
	// honoured by two of four callers is a rule that holds in two of four
	// places, and an agent meeting the other two learns that the answer
	// depends on which call it made.
	t.Run("every upsert of this shape refuses a claim against a missing row", func(t *testing.T) {
		pool := a.pool
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
			assert.Must(t, strings.Contains(err.Error(), `"defias"`) && strings.Contains(err.Error(), `"hogger"`), "err = %v, want both endpoints named", err)
		})
	})

	// TestRemovedArea's "an entity update that loses to a committed removal is
	// told the row is gone" case races the entity table, which is where the
	// loss is largest: an entity's id is named by every edge touching it and
	// by every view position placing it, so a resurrection under a new id
	// leaves a designer's arranged diagram pointing at nothing while the
	// entity list looks untouched.
	t.Run("an entity update that loses to a committed removal is told the row is gone", func(t *testing.T) {
		pool := a.pool
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
		assert.Must(t, err == nil, "seed the entity: %v", err)

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
	})

	// TestRemovedArea's "a removal refusal is not a conflict on either
	// surface" case pins the discrimination the two recoveries turn on, from
	// the error's own side: a caller matching ErrVersionConflict must not
	// catch this, and a caller matching ErrNotFound must.
	t.Run("a removal refusal is not a conflict on either surface", func(t *testing.T) {
		err := error(&metamodel.RemovedError{Subject: "entity type", Address: `"quest"`, Claimed: 4})
		assert.Must(t, errors.Is(err, metamodel.ErrNotFound), "a RemovedError must read as not_found")
		for name, sentinel := range map[string]error{
			"ErrVersionConflict": metamodel.ErrVersionConflict,
			"ErrInvalidInput":    metamodel.ErrInvalidInput,
			"ErrInUse":           metamodel.ErrInUse,
		} {
			assert.Must(t, !errors.Is(err, sentinel), "a RemovedError must not read as %s", name)
		}
		assert.Must(t, strings.Contains(err.Error(), "version 4"), "Error() = %q, want the version the caller claimed", err.Error())
	})
}
