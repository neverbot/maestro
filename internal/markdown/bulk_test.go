package markdown_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
)

// batchItem is one ordinary write of a batch: a distinct path, a body
// that names itself, and the claim that the document does not exist yet.
func batchItem(path, body string) markdown.WriteInput {
	return markdown.WriteInput{
		Path: path, Content: "# " + path + "\n\n" + body + "\n",
		ExpectedVersion: ptrInt32(0),
	}
}

func TestBulkArea(t *testing.T) {
	t.Parallel()
	a := newArea(t)

	// TestBulkArea's "a batch lands the good documents and reports the rest"
	// case is the partial mode's whole contract, and its fixture is
	// deliberately four items with the bad one *in the middle*.
	t.Run("a batch lands the good documents and reports the rest", func(t *testing.T) {
		svc, _, _, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")

		result, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
			batchItem("lore/duskwood", "alpha-secret"),
			batchItem("lore/elwynn", "beta-secret"),
			{Path: "lore//westfall", Content: "# Westfall\n", ExpectedVersion: ptrInt32(0)},
			batchItem("lore/redridge", "delta-secret"),
		}, metamodel.BulkPartial)
		assert.Must(t, err == nil, "WriteMany: %v", err)
		assert.Must(t, len(result.Succeeded) == 3 && len(result.Written) == 3, "succeeded = %d / written = %d, want 3 and 3",
			len(result.Succeeded), len(result.Written))
		assert.Must(t, len(result.Failed) == 1, "failed = %+v, want exactly one", result.Failed)
		failure := result.Failed[0]
		assert.Must(t, failure.Index == 2, "the failure must carry its own index, got %d", failure.Index)
		assert.Must(t, failure.Key == "lore//westfall", "the failure must name the path the caller sent, got %q", failure.Key)
		assert.Must(t, failure.Code == "invalid_input", "code = %q, want invalid_input for a malformed path", failure.Code)
		// A batch report is the one place one item's content could leak into
		// a neighbour's error.
		for _, leaked := range []string{"alpha-secret", "beta-secret", "delta-secret"} {
			assert.Must(t, !strings.Contains(failure.Message, leaked), "message %q leaks %q from another item", failure.Message, leaked)
		}
		// Every landed item is readable, the one after the failure included:
		// item 4 landing cannot depend on item 3.
		for _, path := range []string{"lore/duskwood", "lore/elwynn", "lore/redridge"} {
			if _, err := svc.Read(ctx, game, path); err != nil {
				t.Fatalf("read %q back after the batch: %v", path, err)
			}
		}
		if _, err := svc.Read(ctx, game, "lore/westfall"); !errors.Is(err, markdown.ErrNotFound) {
			t.Fatalf("the failed item must not have landed under any spelling, got %v", err)
		}
		// The report is what a caller edits from next, so every field of it
		// has to be true of the stored row.
		for _, w := range result.Written {
			row, err := svc.Read(ctx, game, w.Path)
			assert.Must(t, err == nil, "read %q: %v", w.Path, err)
			assert.Must(t, w.ID == row.ID && w.Version == row.CurrentVersion, "written %+v does not match the stored row (%v, v%d)",
				w, row.ID, row.CurrentVersion)
		}
	})

	// TestBulkArea's "an atomic batch rolls back every document" case is the
	// other half of the same fixture: the identical four items in atomic mode
	// leave nothing at all.
	t.Run("an atomic batch rolls back every document", func(t *testing.T) {
		svc, _, _, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")

		result, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
			batchItem("lore/duskwood", "alpha"),
			batchItem("lore/elwynn", "beta"),
			{Path: "lore//westfall", Content: "# Westfall\n", ExpectedVersion: ptrInt32(0)},
			batchItem("lore/redridge", "delta"),
		}, metamodel.BulkAtomic)
		assert.Must(t, err != nil, "an atomic batch with a bad item must fail as a whole")
		assert.Must(t, errors.Is(err, markdown.ErrInvalidInput), "err = %v, want it to still read as invalid_input through the wrapping", err)
		assert.Must(t, strings.Contains(err.Error(), `item 2 ("lore//westfall")`), "err = %v, want it to name the failing item", err)
		assert.Must(t, len(result.Succeeded) == 0 && len(result.Written) == 0 && len(result.Failed) == 0, "a failed atomic batch reports nothing as done: %+v", result)
		for _, path := range []string{"lore/duskwood", "lore/elwynn", "lore/redridge"} {
			if _, err := svc.Read(ctx, game, path); !errors.Is(err, markdown.ErrNotFound) {
				t.Fatalf("%q survived a rolled-back atomic batch: %v", path, err)
			}
		}
	})

	t.Run("an atomic batch lands every document together", func(t *testing.T) {
		svc, _, _, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")

		result, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
			batchItem("lore/duskwood", "alpha"),
			batchItem("lore/elwynn", "beta"),
			batchItem("lore/redridge", "delta"),
		}, metamodel.BulkAtomic)
		assert.Must(t, err == nil, "WriteMany: %v", err)
		assert.Must(t, len(result.Written) == 3 && len(result.Failed) == 0, "result = %+v, want three written and none failed", result)
		for _, path := range []string{"lore/duskwood", "lore/elwynn", "lore/redridge"} {
			if _, err := svc.Read(ctx, game, path); err != nil {
				t.Fatalf("read %q back after an atomic batch: %v", path, err)
			}
		}
	})

	// TestBulkArea's "a batch reports a stale version claim at its own index"
	// case is the decision WriteMany's doc comment argues: a batch is a list
	// of claims about versions, and a claim that turned out wrong is that
	// item's failure and not the batch's.
	t.Run("a batch reports a stale version claim at its own index", func(t *testing.T) {
		svc, _, _, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")

		const stored = "the-body-a-conflict-must-not-echo"
		if _, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: "lore/duskwood", Content: "# Duskwood\n\n" + stored + "\n",
			ExpectedVersion: ptrInt32(0),
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}

		result, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
			batchItem("lore/elwynn", "beta"),
			{
				Path: "lore/duskwood", Content: "# Duskwood\n\nrewritten\n",
				// The caller is holding a version that has moved on: it read
				// nothing and guessed, which is what a re-seed does.
				ExpectedVersion: ptrInt32(7), IncludeCurrent: true,
			},
			batchItem("lore/redridge", "delta"),
		}, metamodel.BulkPartial)
		assert.Must(t, err == nil, "WriteMany: %v", err)
		assert.Must(t, len(result.Failed) == 1, "failed = %+v, want exactly one", result.Failed)
		failure := result.Failed[0]
		assert.Must(t, failure.Index == 1 && failure.Key == "lore/duskwood", "failure = %+v, want index 1 naming lore/duskwood", failure)
		assert.Must(t, failure.Code == "version_conflict", "code = %q, want version_conflict: a stale claim is per-item and per-item "+
			"fixable, like a schema violation", failure.Code)
		assert.Must(t, strings.Contains(failure.Message, "version 1"), "message = %q, want it to name the version to merge onto", failure.Message)
		// Even though this item asked for the echo: four hundred conflicts
		// must not answer with four hundred bodies.
		assert.Must(t, !strings.Contains(failure.Message, stored), "message = %q echoes the current body; a batch never does", failure.Message)
		assert.Must(t, len(result.Written) == 2, "written = %+v, want the two items whose claims were true", result.Written)
		// The refused item is untouched, not half-written.
		row, err := svc.Read(ctx, game, "lore/duskwood")
		assert.Must(t, err == nil, "read: %v", err)
		assert.Must(t, row.CurrentVersion == 1 && strings.Contains(row.BodyMd, stored), "the refused item moved the document: v%d, body %q",
			row.CurrentVersion, row.BodyMd)
	})

	// TestBulkArea's "a batch item without an expected version fails alone"
	// case pins the requirement per document rather than per call: the item
	// that omitted its claim is the item that is refused.
	t.Run("a batch item without an expected version fails alone", func(t *testing.T) {
		svc, _, _, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")

		result, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
			batchItem("lore/duskwood", "alpha"),
			{Path: "lore/elwynn", Content: "# Elwynn\n"},
			batchItem("lore/redridge", "delta"),
		}, metamodel.BulkPartial)
		assert.Must(t, err == nil, "WriteMany: %v", err)
		assert.Must(t, len(result.Failed) == 1 && result.Failed[0].Index == 1, "failed = %+v, want exactly item 1", result.Failed)
		if code := result.Failed[0].Code; code != "invalid_input" {
			t.Fatalf("code = %q, want invalid_input", code)
		}
		if msg := result.Failed[0].Message; !strings.Contains(msg, "expected_version") {
			t.Fatalf("message = %q, want it to name expected_version", msg)
		}
		assert.Must(t, len(result.Written) == 2, "written = %+v, want the other two", result.Written)
	})

	// TestBulkArea's "a batch that repeats a path is diagnosed as such" case
	// covers the one fault the database cannot diagnose: two items are one
	// document, so the second would silently overwrite the first and the
	// caller would be told both landed.
	t.Run("a batch that repeats a path is diagnosed as such", func(t *testing.T) {
		svc, _, _, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")

		result, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
			batchItem("lore/duskwood", "first"),
			batchItem("lore/elwynn", "beta"),
			// The same document under a different capitalisation: the unique
			// index folds case, so this is item 0 again.
			batchItem("Lore/Duskwood", "second"),
		}, metamodel.BulkPartial)
		assert.Must(t, err == nil, "WriteMany: %v", err)
		assert.Must(t, len(result.Failed) == 1, "failed = %+v, want exactly one", result.Failed)
		failure := result.Failed[0]
		assert.Must(t, failure.Index == 2 && failure.Code == "invalid_input", "failure = %+v, want index 2 coded invalid_input", failure)
		assert.Must(t, strings.Contains(failure.Message, "item 0"), "message = %q, want it to name the item that already addressed the document",
			failure.Message)
		// The *first* occurrence is a perfectly good item and lands.
		row, err := svc.Read(ctx, game, "lore/duskwood")
		assert.Must(t, err == nil, "read: %v", err)
		assert.Must(t, strings.Contains(row.BodyMd, "first"), "body = %q, want the first occurrence's, not the repeat's", row.BodyMd)
		assert.Must(t, row.CurrentVersion == 1, "version = %d: the repeat must not have written a second version",
			row.CurrentVersion)
	})

	// TestBulkArea's "an atomic batch that repeats a path is refused whole"
	// case is the same fault in the other mode: there is no per-item answer in
	// a mode that lands everything or nothing, and the caller asked for three
	// documents where at most two can exist.
	t.Run("an atomic batch that repeats a path is refused whole", func(t *testing.T) {
		svc, _, _, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")

		_, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
			batchItem("lore/duskwood", "first"),
			batchItem("lore/elwynn", "beta"),
			batchItem("Lore/Duskwood", "second"),
		}, metamodel.BulkAtomic)
		requireFieldError(t, err, "items[2].path", "already addressed by item 0")
		if _, err := svc.Read(ctx, game, "lore/elwynn"); !errors.Is(err, markdown.ErrNotFound) {
			t.Fatalf("nothing may land from a batch refused before it is written: %v", err)
		}
	})

	// TestBulkArea's "a batch refuses an unknown mode" case pins that a typo
	// is not read as "partial": doing so would land rows a caller asked to
	// have rolled back, with nothing to notice by.
	t.Run("a batch refuses an unknown mode", func(t *testing.T) {
		svc, _, _, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")

		_, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
			batchItem("lore/duskwood", "alpha"),
		}, metamodel.BulkMode("atomic "))
		requireFieldError(t, err, "mode", `must be "partial" or "atomic"`)
		if _, err := svc.Read(ctx, game, "lore/duskwood"); !errors.Is(err, markdown.ErrNotFound) {
			t.Fatalf("a batch refused for its mode writes nothing: %v", err)
		}
	})

	// TestBulkArea's "a document batch is bounded by the same ceiling" case is
	// the third kind's half of metamodel.MaxBulkItems.
	t.Run("a document batch is bounded by the same ceiling", func(t *testing.T) {
		svc, _, _, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")

		items := make([]markdown.WriteInput, 0, metamodel.MaxBulkItems+1)
		for i := range metamodel.MaxBulkItems + 1 {
			items = append(items, batchItem(fmt.Sprintf("lore/zone-%04d", i), "alpha"))
		}
		wantMessage := fmt.Sprintf("a batch carries at most %d items; this one carries %d — split it",
			metamodel.MaxBulkItems, metamodel.MaxBulkItems+1)

		for _, mode := range []metamodel.BulkMode{metamodel.BulkPartial, metamodel.BulkAtomic, ""} {
			_, err := svc.WriteMany(ctx, game, items, mode)
			requireFieldError(t, err, "items", wantMessage)
		}
		if _, err := svc.Read(ctx, game, "lore/zone-0000"); !errors.Is(err, markdown.ErrNotFound) {
			t.Fatalf("an over-large batch landed its first document: %v", err)
		}

		// The ceiling itself is allowed. Without this, a bound written with
		// the wrong comparison would refuse the batch it was sized for and
		// every assertion above would still pass.
		result, err := svc.WriteMany(ctx, game, items[:metamodel.MaxBulkItems], metamodel.BulkAtomic)
		assert.Must(t, err == nil, "a batch of exactly %d documents was refused: %v", metamodel.MaxBulkItems, err)
		assert.Must(t, len(result.Written) == metamodel.MaxBulkItems, "a batch of exactly %d documents wrote %d", metamodel.MaxBulkItems, len(result.Written))
	})

	// TestBulkArea's "a batch announces every document it landed" case pins
	// that the batch publishes one identity event per landed row, and that a
	// rolled-back atomic batch publishes none — the rule Service.publish
	// states and that a second bulk loop would have had to re-learn.
	t.Run("a batch announces every document it landed", func(t *testing.T) {
		svc, _, hub, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")

		sub := hub.Subscribe(game, "viewer", false)
		defer hub.Unsubscribe(sub)

		if _, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
			batchItem("lore/duskwood", "alpha"),
			{Path: "lore//westfall", Content: "# W\n", ExpectedVersion: ptrInt32(0)},
			batchItem("lore/redridge", "delta"),
		}, metamodel.BulkPartial); err != nil {
			t.Fatalf("WriteMany: %v", err)
		}

		seen := map[string]bool{}
		for range 2 {
			select {
			case ev := <-sub.C:
				payload, ok := ev.Payload.(markdown.DocumentEvent)
				assert.Must(t, ok, "payload = %#v, want a DocumentEvent", ev.Payload)
				seen[payload.Path] = true
			case <-time.After(5 * time.Second):
				t.Fatal("a landed document was not announced within 5s")
			}
		}
		assert.Must(t, seen["lore/duskwood"] && seen["lore/redridge"], "announced %v, want both landed documents", seen)
		select {
		case ev := <-sub.C:
			t.Fatalf("a third event %v: the refused item announced nothing", ev.Payload)
		default:
		}
	})
}
