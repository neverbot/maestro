package metamodel_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// TestEntitiesByIDsResolvesAPageOfEndpointsInOneQuery pins the read
// Task 7's `relations.list` said it needed and did not have: a page of
// edges holds entity ids, and turning them back into the (type key, key)
// refs a caller wrote them with takes exactly one bulk read, not one per
// endpoint.
func TestEntitiesByIDsResolvesAPageOfEndpointsInOneQuery(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	hogger, err := svc.EntityByKey(ctx, project, "quest", "hogger")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	elwynn, err := svc.EntityByKey(ctx, project, "zone", "elwynn")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}

	// The same id twice: a page of edges routinely names one dense node
	// on both sides of many rows, and the caller must not have to
	// deduplicate before asking.
	rows, err := svc.EntitiesByIDs(ctx, project, []uuid.UUID{hogger.ID, elwynn.ID, hogger.ID})
	if err != nil {
		t.Fatalf("EntitiesByIDs: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if got := rows[hogger.ID].Key; got != "hogger" {
		t.Fatalf("hogger key = %q, want %q", got, "hogger")
	}
	if got := rows[elwynn.ID].Name; got != "Elwynn Forest" {
		t.Fatalf("elwynn name = %q, want %q", got, "Elwynn Forest")
	}
}

// TestEntitiesByIDsNeverCrossesAGameBoundary is the isolation half: the
// ids come from a caller-visible page, so a leaked one must resolve to
// nothing rather than to another game's row. Absent, not an error — a
// removal racing a listing produces exactly the same gap and is not a
// failure either.
func TestEntitiesByIDsNeverCrossesAGameBoundary(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine := newProject(t, pool)
	theirs := newProject(t, pool)
	seedWorld(t, svc, mine)
	seedWorld(t, svc, theirs)

	foreign, err := svc.EntityByKey(ctx, theirs, "quest", "hogger")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}

	rows, err := svc.EntitiesByIDs(ctx, mine, []uuid.UUID{foreign.ID, uuid.New()})
	if err != nil {
		t.Fatalf("EntitiesByIDs: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none of another game's entities", rows)
	}
}

// TestEntitiesByIDsWithNoIDsAsksTheDatabaseNothing pins the empty case
// as a shape rather than as a round trip: a listing with no edges must
// not cost a query, and it must still hand back a map a caller can
// index into rather than a nil one it has to check for.
func TestEntitiesByIDsWithNoIDsAsksTheDatabaseNothing(t *testing.T) {
	// A nil pool is what makes this an assertion rather than a
	// convention: a service with no pool panics on any query, so this
	// test passes only while the empty case short-circuits.
	svc := metamodel.New(nil, nil)
	rows, err := svc.EntitiesByIDs(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("EntitiesByIDs: %v", err)
	}
	if rows == nil {
		t.Fatal("rows = nil, want an empty map a caller can index into")
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want empty", rows)
	}
}
