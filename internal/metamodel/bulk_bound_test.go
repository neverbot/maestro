package metamodel_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// TestABatchIsBoundedAndReportedAsTheCallersOwnArgument pins
// metamodel.MaxBulkItems at the level the bound was found missing: over
// the wire, Task 9 sent a 5,000-item atomic batch and it was accepted —
// one transaction held open for 3.1 seconds, answering with 515 KB.
// Every other caller-supplied bound on this surface is checked in Go
// before Postgres sees it (the search query at 4 KiB, a page limit
// clamped to its cap, the request body at 4 MiB); this was the one that
// was not.
//
// **Both kinds and both modes**, because the check is in the shared
// driver and a check in the shared driver is exactly the kind of thing
// that gets moved to one call site later. internal/markdown's documents
// are the third kind and are pinned in their own package, by
// TestADocumentBatchIsBoundedByTheSameCeiling, because they reach the
// same driver from outside this one.
//
// The batches here are of *invalid* items — no entity type is declared —
// so a build without the bound fails them at the item level rather than
// at the argument, and the assertion on path `items` is what tells the
// two apart. That also means nothing in the database is touched, which
// is the claim the row count at the end makes.
func TestABatchIsBoundedAndReportedAsTheCallersOwnArgument(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	entities := make([]metamodel.EntityInput, 0, metamodel.MaxBulkItems+1)
	relations := make([]metamodel.RelationInput, 0, metamodel.MaxBulkItems+1)
	for i := range metamodel.MaxBulkItems + 1 {
		entities = append(entities, metamodel.EntityInput{
			TypeKey: "quest", Key: fmt.Sprintf("q%04d", i), Name: "Quest",
		})
		relations = append(relations, metamodel.RelationInput{
			TypeKey: "takes_place_in",
			Source:  metamodel.Ref{TypeKey: "quest", Key: fmt.Sprintf("q%04d", i)},
			Target:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
		})
	}
	wantMessage := fmt.Sprintf("a batch carries at most %d items; this one carries %d — split it",
		metamodel.MaxBulkItems, metamodel.MaxBulkItems+1)

	for _, mode := range []metamodel.BulkMode{metamodel.BulkPartial, metamodel.BulkAtomic, ""} {
		t.Run("entities in mode "+string(mode), func(t *testing.T) {
			_, err := svc.UpsertEntities(ctx, project, entities, mode)
			requireFieldError(t, err, "items", wantMessage)
		})
		t.Run("relations in mode "+string(mode), func(t *testing.T) {
			_, err := svc.UpsertRelations(ctx, project, relations, mode)
			requireFieldError(t, err, "items", wantMessage)
		})
	}

	// The ceiling itself is allowed, not just the number below it: a
	// bound written with the wrong comparison refuses the batch it was
	// sized for, and nothing above would catch that. These items are
	// still invalid, so they come back as per-item failures — which is
	// the point: the batch got past the argument check and was run.
	at := entities[:metamodel.MaxBulkItems]
	out, err := svc.UpsertEntities(ctx, project, at, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("a batch of exactly %d items was refused: %v", metamodel.MaxBulkItems, err)
	}
	if len(out.Written) != metamodel.MaxBulkItems {
		t.Fatalf("a batch of exactly %d items wrote %d rows: %+v",
			metamodel.MaxBulkItems, len(out.Written), out.Failed)
	}

	// And nothing an over-large batch named exists. The refusal happens
	// before any transaction opens, so partial mode cannot have landed a
	// prefix of it.
	//
	// It is a full paged walk because the game now holds more quests than
	// one page can carry, which is itself the reason MaxBulkItems and
	// MaxEntityPage are the same number: a batch at the ceiling writes
	// exactly one page's worth.
	filter := metamodel.EntityFilter{TypeKey: "quest", Limit: metamodel.MaxEntityPage}
	total := 0
	for {
		page, err := svc.ListEntities(ctx, project, filter)
		if err != nil {
			t.Fatalf("ListEntities: %v", err)
		}
		total += len(page.Entities)
		if page.NextCursor == "" {
			break
		}
		filter.Cursor = page.NextCursor
	}
	// The two seeded quests plus the batch that was allowed through.
	if total != metamodel.MaxBulkItems+2 {
		t.Fatalf("the game holds %d quests, want %d — an over-large batch wrote rows",
			total, metamodel.MaxBulkItems+2)
	}
}
