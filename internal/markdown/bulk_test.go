package markdown_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

// TestABatchLandsTheGoodDocumentsAndReportsTheRest is the partial mode's
// whole contract, and its fixture is deliberately four items with the
// bad one *in the middle*.
//
// A two-item batch cannot tell partial from atomic: with the failure
// last, "everything before it landed" is also what atomic would leave if
// it did not roll back, and with the failure first there is nothing
// before it. Four items with the third one failing distinguishes both
// directions — rows before the failure are kept and rows after it are
// still attempted — and the fourth item is what proves the loop did not
// stop.
func TestABatchLandsTheGoodDocumentsAndReportsTheRest(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	result, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
		batchItem("lore/duskwood", "alpha-secret"),
		batchItem("lore/elwynn", "beta-secret"),
		{Path: "lore//westfall", Content: "# Westfall\n", ExpectedVersion: ptrInt32(0)},
		batchItem("lore/redridge", "delta-secret"),
	}, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("WriteMany: %v", err)
	}
	if len(result.Succeeded) != 3 || len(result.Written) != 3 {
		t.Fatalf("succeeded = %d / written = %d, want 3 and 3",
			len(result.Succeeded), len(result.Written))
	}
	if len(result.Failed) != 1 {
		t.Fatalf("failed = %+v, want exactly one", result.Failed)
	}
	failure := result.Failed[0]
	if failure.Index != 2 {
		t.Fatalf("the failure must carry its own index, got %d", failure.Index)
	}
	if failure.Key != "lore//westfall" {
		t.Fatalf("the failure must name the path the caller sent, got %q", failure.Key)
	}
	if failure.Code != "invalid_input" {
		t.Fatalf("code = %q, want invalid_input for a malformed path", failure.Code)
	}
	// A batch report is the one place one item's content could leak into
	// a neighbour's error.
	for _, leaked := range []string{"alpha-secret", "beta-secret", "delta-secret"} {
		if strings.Contains(failure.Message, leaked) {
			t.Fatalf("message %q leaks %q from another item", failure.Message, leaked)
		}
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
		if err != nil {
			t.Fatalf("read %q: %v", w.Path, err)
		}
		if w.ID != row.ID || w.Version != row.CurrentVersion {
			t.Fatalf("written %+v does not match the stored row (%v, v%d)",
				w, row.ID, row.CurrentVersion)
		}
	}
}

// TestAnAtomicBatchRollsBackEveryDocument is the other half of the same
// fixture: the identical four items in atomic mode leave nothing at all.
func TestAnAtomicBatchRollsBackEveryDocument(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	result, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
		batchItem("lore/duskwood", "alpha"),
		batchItem("lore/elwynn", "beta"),
		{Path: "lore//westfall", Content: "# Westfall\n", ExpectedVersion: ptrInt32(0)},
		batchItem("lore/redridge", "delta"),
	}, metamodel.BulkAtomic)
	if err == nil {
		t.Fatal("an atomic batch with a bad item must fail as a whole")
	}
	if !errors.Is(err, markdown.ErrInvalidInput) {
		t.Fatalf("err = %v, want it to still read as invalid_input through the wrapping", err)
	}
	if !strings.Contains(err.Error(), `item 2 ("lore//westfall")`) {
		t.Fatalf("err = %v, want it to name the failing item", err)
	}
	if len(result.Succeeded) != 0 || len(result.Written) != 0 || len(result.Failed) != 0 {
		t.Fatalf("a failed atomic batch reports nothing as done: %+v", result)
	}
	for _, path := range []string{"lore/duskwood", "lore/elwynn", "lore/redridge"} {
		if _, err := svc.Read(ctx, game, path); !errors.Is(err, markdown.ErrNotFound) {
			t.Fatalf("%q survived a rolled-back atomic batch: %v", path, err)
		}
	}
}

func TestAnAtomicBatchLandsEveryDocumentTogether(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	result, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
		batchItem("lore/duskwood", "alpha"),
		batchItem("lore/elwynn", "beta"),
		batchItem("lore/redridge", "delta"),
	}, metamodel.BulkAtomic)
	if err != nil {
		t.Fatalf("WriteMany: %v", err)
	}
	if len(result.Written) != 3 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, want three written and none failed", result)
	}
	for _, path := range []string{"lore/duskwood", "lore/elwynn", "lore/redridge"} {
		if _, err := svc.Read(ctx, game, path); err != nil {
			t.Fatalf("read %q back after an atomic batch: %v", path, err)
		}
	}
}

// TestABatchReportsAStaleVersionClaimAtItsOwnIndex is the decision
// WriteMany's doc comment argues: a batch is a list of claims about
// versions, and a claim that turned out wrong is that item's failure and
// not the batch's.
//
// It also pins the half that is easy to lose: the conflict comes back
// with the version to merge onto and *without* the current body. The
// item below asks for the echo — IncludeCurrent true — and a batch
// failure has nowhere to put one, which is why the batch tool offers no
// such argument.
func TestABatchReportsAStaleVersionClaimAtItsOwnIndex(t *testing.T) {
	svc, _, _, pool := newService(t)
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
	if err != nil {
		t.Fatalf("WriteMany: %v", err)
	}
	if len(result.Failed) != 1 {
		t.Fatalf("failed = %+v, want exactly one", result.Failed)
	}
	failure := result.Failed[0]
	if failure.Index != 1 || failure.Key != "lore/duskwood" {
		t.Fatalf("failure = %+v, want index 1 naming lore/duskwood", failure)
	}
	if failure.Code != "version_conflict" {
		t.Fatalf("code = %q, want version_conflict: a stale claim is per-item and per-item "+
			"fixable, like a schema violation", failure.Code)
	}
	if !strings.Contains(failure.Message, "version 1") {
		t.Fatalf("message = %q, want it to name the version to merge onto", failure.Message)
	}
	// Even though this item asked for the echo: four hundred conflicts
	// must not answer with four hundred bodies.
	if strings.Contains(failure.Message, stored) {
		t.Fatalf("message = %q echoes the current body; a batch never does", failure.Message)
	}
	if len(result.Written) != 2 {
		t.Fatalf("written = %+v, want the two items whose claims were true", result.Written)
	}
	// The refused item is untouched, not half-written.
	row, err := svc.Read(ctx, game, "lore/duskwood")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if row.CurrentVersion != 1 || !strings.Contains(row.BodyMd, stored) {
		t.Fatalf("the refused item moved the document: v%d, body %q",
			row.CurrentVersion, row.BodyMd)
	}
}

// TestABatchItemWithoutAnExpectedVersionFailsAlone pins the requirement
// per document rather than per call: the item that omitted its claim is
// the item that is refused.
func TestABatchItemWithoutAnExpectedVersionFailsAlone(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	result, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
		batchItem("lore/duskwood", "alpha"),
		{Path: "lore/elwynn", Content: "# Elwynn\n"},
		batchItem("lore/redridge", "delta"),
	}, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("WriteMany: %v", err)
	}
	if len(result.Failed) != 1 || result.Failed[0].Index != 1 {
		t.Fatalf("failed = %+v, want exactly item 1", result.Failed)
	}
	if code := result.Failed[0].Code; code != "invalid_input" {
		t.Fatalf("code = %q, want invalid_input", code)
	}
	if msg := result.Failed[0].Message; !strings.Contains(msg, "expected_version") {
		t.Fatalf("message = %q, want it to name expected_version", msg)
	}
	if len(result.Written) != 2 {
		t.Fatalf("written = %+v, want the other two", result.Written)
	}
}

// TestABatchThatRepeatsAPathIsDiagnosedAsSuch covers the one fault the
// database cannot diagnose: two items are one document, so the second
// would silently overwrite the first and the caller would be told both
// landed.
func TestABatchThatRepeatsAPathIsDiagnosedAsSuch(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	result, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
		batchItem("lore/duskwood", "first"),
		batchItem("lore/elwynn", "beta"),
		// The same document under a different capitalisation: the unique
		// index folds case, so this is item 0 again.
		batchItem("Lore/Duskwood", "second"),
	}, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("WriteMany: %v", err)
	}
	if len(result.Failed) != 1 {
		t.Fatalf("failed = %+v, want exactly one", result.Failed)
	}
	failure := result.Failed[0]
	if failure.Index != 2 || failure.Code != "invalid_input" {
		t.Fatalf("failure = %+v, want index 2 coded invalid_input", failure)
	}
	if !strings.Contains(failure.Message, "item 0") {
		t.Fatalf("message = %q, want it to name the item that already addressed the document",
			failure.Message)
	}
	// The *first* occurrence is a perfectly good item and lands.
	row, err := svc.Read(ctx, game, "lore/duskwood")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(row.BodyMd, "first") {
		t.Fatalf("body = %q, want the first occurrence's, not the repeat's", row.BodyMd)
	}
	if row.CurrentVersion != 1 {
		t.Fatalf("version = %d: the repeat must not have written a second version",
			row.CurrentVersion)
	}
}

// TestAnAtomicBatchThatRepeatsAPathIsRefusedWhole is the same fault in
// the other mode: there is no per-item answer in a mode that lands
// everything or nothing, and the caller asked for three documents where
// at most two can exist.
func TestAnAtomicBatchThatRepeatsAPathIsRefusedWhole(t *testing.T) {
	svc, _, _, pool := newService(t)
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
}

// TestABatchRefusesAnUnknownMode pins that a typo is not read as
// "partial": doing so would land rows a caller asked to have rolled
// back, with nothing to notice by.
func TestABatchRefusesAnUnknownMode(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.WriteMany(ctx, game, []markdown.WriteInput{
		batchItem("lore/duskwood", "alpha"),
	}, metamodel.BulkMode("atomic "))
	requireFieldError(t, err, "mode", `must be "partial" or "atomic"`)
	if _, err := svc.Read(ctx, game, "lore/duskwood"); !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("a batch refused for its mode writes nothing: %v", err)
	}
}

// TestADocumentBatchIsBoundedByTheSameCeiling is the third kind's half
// of metamodel.MaxBulkItems.
//
// The bound lives in metamodel.BulkUpsert, which this domain's batch
// reaches from outside that package, so nothing in internal/metamodel's
// own tests can prove documents are bounded — and documents are the
// batch writer that shipped most recently, on the same shared machinery,
// in the same sub-project the missing bound was found in. That is
// precisely the shape of defect this repository keeps producing: a rule
// established correctly at two of its three call sites. Here is the
// third.
//
// The items are valid, unlike the metamodel-side fixture's: a document
// needs no declared parent, so the cheapest way to make the assertion
// mean something is to check that a batch which would otherwise have
// landed 501 documents landed none.
func TestADocumentBatchIsBoundedByTheSameCeiling(t *testing.T) {
	svc, _, _, pool := newService(t)
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
	if err != nil {
		t.Fatalf("a batch of exactly %d documents was refused: %v", metamodel.MaxBulkItems, err)
	}
	if len(result.Written) != metamodel.MaxBulkItems {
		t.Fatalf("a batch of exactly %d documents wrote %d", metamodel.MaxBulkItems, len(result.Written))
	}
}

// TestABatchAnnouncesEveryDocumentItLanded pins that the batch publishes
// one identity event per landed row, and that a rolled-back atomic batch
// publishes none — the rule Service.publish states and that a second
// bulk loop would have had to re-learn.
func TestABatchAnnouncesEveryDocumentItLanded(t *testing.T) {
	svc, _, hub, pool := newService(t)
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
			if !ok {
				t.Fatalf("payload = %#v, want a DocumentEvent", ev.Payload)
			}
			seen[payload.Path] = true
		case <-time.After(5 * time.Second):
			t.Fatal("a landed document was not announced within 5s")
		}
	}
	if !seen["lore/duskwood"] || !seen["lore/redridge"] {
		t.Fatalf("announced %v, want both landed documents", seen)
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("a third event %v: the refused item announced nothing", ev.Payload)
	default:
	}
}
