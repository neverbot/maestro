package metamodel_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// decodeStoredFields reads a row's stored jsonb back as a value map,
// which is what these assertions are about: what a repair left in the
// row, not what it returned.
func decodeStoredFields(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	fields := map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode stored fields %s: %v", raw, err)
	}
	return fields
}

// repairFixture is a quest type with no fields and three quests, which is
// where the four-step loop starts: content that fits, under a schema
// about to be narrowed.
type repairFixture struct {
	svc     *metamodel.Service
	project uuid.UUID
	typeVer int32
}

func newRepairFixture(t *testing.T, rows int) repairFixture {
	t.Helper()
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "summary", Type: metamodel.FieldText}},
	})
	if err != nil {
		t.Fatalf("declare type: %v", err)
	}
	items := make([]metamodel.EntityInput, 0, rows)
	for i := range rows {
		items = append(items, metamodel.EntityInput{
			TypeKey: "quest", Key: fmt.Sprintf("q%04d", i),
			Name:   fmt.Sprintf("Quest %04d", i),
			Fields: map[string]any{"summary": "Defeat the gnoll chieftain."},
		})
	}
	out, err := svc.UpsertEntities(ctx, project, items, metamodel.BulkAtomic)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(out.Written) != rows {
		t.Fatalf("seeded %d of %d rows", len(out.Written), rows)
	}
	return repairFixture{svc: svc, project: project, typeVer: typ.Version}
}

// setSchema edits the quest type's schema, carrying the version claim
// forward. Every schema edit in these tests goes through the ordinary
// writer, so the flagging under test is the shipped rule and not a
// fixture's imitation of it.
func (f *repairFixture) setSchema(t *testing.T, schema metamodel.Schema) {
	t.Helper()
	version := f.typeVer
	typ, err := f.svc.UpsertEntityType(context.Background(), f.project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: schema, ExpectedVersion: &version,
	})
	if err != nil {
		t.Fatalf("edit schema: %v", err)
	}
	f.typeVer = typ.Version
}

// flagged is how many quests the type's current schema rejects.
func (f repairFixture) flagged(t *testing.T) int {
	t.Helper()
	yes := true
	page, err := f.svc.ListEntities(context.Background(), f.project, metamodel.EntityFilter{
		TypeKey: "quest", Invalid: &yes, Limit: metamodel.MaxEntityPage,
	})
	if err != nil {
		t.Fatalf("list flagged: %v", err)
	}
	if page.NextCursor != "" {
		t.Fatalf("more flagged rows than one page; this fixture is too large")
	}
	return len(page.Entities)
}

// TestARepairWalksTheWholeFourStepLoopTaskNineFound is the case this
// whole file exists for.
//
// Task 9 walked four steps and could not finish any of them cheaply: add
// a required field under existing rows (all flagged, correctly); find
// that nothing writable into the *type* makes them fit again, because
// Schema.Check refuses `required` with a `default` and is right to;
// rewrite every row one at a time; then take the field back out and
// watch all of them flagged a *second* time, because the value they now
// carry has become an unknown field. Both halves of that loop are one
// call each now, and the fixture walks all four steps in order rather
// than testing the two operations separately, because the second flagging
// is a consequence of the first repair and no test of `drop_unknown`
// alone would see it.
func TestARepairWalksTheWholeFourStepLoopTaskNineFound(t *testing.T) {
	f := newRepairFixture(t, 3)
	ctx := context.Background()

	// Step 1: narrow the schema. Every row is flagged, and — the half
	// that is not revisited — none of them is back-filled.
	f.setSchema(t, metamodel.Schema{
		{Key: "summary", Type: metamodel.FieldText},
		{Key: "laps", Type: metamodel.FieldNumber, Required: true},
	})
	if got := f.flagged(t); got != 3 {
		t.Fatalf("%d rows flagged after adding a required field, want 3", got)
	}

	// Step 2: the repair. One call, one decision.
	out, err := f.svc.RepairEntities(ctx, f.project, metamodel.RepairInput{
		TypeKey: "quest", Set: map[string]any{"laps": float64(10)},
	})
	if err != nil {
		t.Fatalf("RepairEntities: %v", err)
	}
	if out.Scanned != 3 || len(out.Repaired) != 3 || len(out.Failed) != 0 {
		t.Fatalf("repair scanned %d, repaired %d, failed %+v",
			out.Scanned, len(out.Repaired), out.Failed)
	}
	if got := f.flagged(t); got != 0 {
		t.Fatalf("%d rows still flagged after the repair", got)
	}

	// The values the caller named landed, and the values it did not name
	// are untouched. A repair that had rewritten the row wholesale would
	// pass the flag check and be caught here.
	row, err := f.svc.EntityByKey(ctx, f.project, "quest", "q0000")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if row.Name != "Quest 0000" {
		t.Fatalf("the repair changed a row's name to %q", row.Name)
	}
	fields := decodeStoredFields(t, row.Fields)
	if fields["laps"] != float64(10) || fields["summary"] != "Defeat the gnoll chieftain." {
		t.Fatalf("repaired fields = %v, want the set value beside the untouched one", fields)
	}
	// A repair is a write, so it advances the version like one: the row
	// was created at 1 and repaired once.
	if row.Version != 2 {
		t.Fatalf("a repaired row is at version %d, want 2", row.Version)
	}

	// Step 3: take the field back out. Every row is flagged again,
	// because the value it now carries is an unknown field — the second
	// flagging Task 9 measured and had no answer to.
	f.setSchema(t, metamodel.Schema{{Key: "summary", Type: metamodel.FieldText}})
	if got := f.flagged(t); got != 3 {
		t.Fatalf("%d rows flagged after removing the field, want 3", got)
	}

	// Step 4: the other operation. There is no other way to spell "forget
	// this value" for a field the schema no longer has a name for.
	out, err = f.svc.RepairEntities(ctx, f.project, metamodel.RepairInput{
		TypeKey: "quest", DropUnknown: true,
	})
	if err != nil {
		t.Fatalf("RepairEntities: %v", err)
	}
	if out.Scanned != 3 || len(out.Repaired) != 3 || len(out.Failed) != 0 {
		t.Fatalf("the drop pass scanned %d, repaired %d, failed %+v",
			out.Scanned, len(out.Repaired), out.Failed)
	}
	if got := f.flagged(t); got != 0 {
		t.Fatalf("%d rows still flagged after dropping the unknown field", got)
	}
	row, err = f.svc.EntityByKey(ctx, f.project, "quest", "q0000")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	fields = decodeStoredFields(t, row.Fields)
	if _, present := fields["laps"]; present {
		t.Fatalf("the dropped value is still stored: %v", fields)
	}
	if fields["summary"] != "Defeat the gnoll chieftain." {
		t.Fatalf("dropping the unknown field took a declared one with it: %v", fields)
	}
}

// TestASchemaEditRepairsNothingByItself is the standing check that the
// back door stays shut.
//
// The flag-rather-than-back-fill decision is the one this file is built
// around and does not revisit: a schema edit re-checks rows and never
// writes to them. A repair exists precisely because that is true, so the
// way a repair could quietly undo it is by being called from the schema
// edit — at which point every schema edit becomes a back-fill and the
// designer's content is edited by a validation pass. Nothing here can
// prove a call site absent, which is why this asserts the *outcome*: a
// narrowing edit against a type with a defaulted field leaves every row
// exactly as it was, flag and all.
func TestASchemaEditRepairsNothingByItself(t *testing.T) {
	f := newRepairFixture(t, 3)
	ctx := context.Background()

	// **Direction one: narrowing.** A default is what a back-fill would
	// have to use, so the schema declares one beside a required field
	// that has none. `laps` is optional-with-default, which the schema
	// checker allows; required-with-default, which it does not, is the
	// pairing that made this whole problem.
	f.setSchema(t, metamodel.Schema{
		{Key: "summary", Type: metamodel.FieldText},
		{Key: "rank", Type: metamodel.FieldEnum, Options: []string{"gold", "silver"}, Required: true},
		{Key: "laps", Type: metamodel.FieldNumber, Default: float64(7)},
	})
	if got := f.flagged(t); got != 3 {
		t.Fatalf("%d rows flagged, want 3", got)
	}

	row, err := f.svc.EntityByKey(ctx, f.project, "quest", "q0000")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if row.Version != 1 {
		t.Fatalf("a schema edit moved a row to version %d", row.Version)
	}
	fields := decodeStoredFields(t, row.Fields)
	if _, present := fields["laps"]; present {
		t.Fatalf("a schema edit back-filled a declared default: %v", fields)
	}
	if len(fields) != 1 {
		t.Fatalf("a schema edit wrote to a row's values: %v", fields)
	}

	// **Direction two: widening, which is the constructible back-fill.**
	//
	// The direction above cannot be back-filled at all — the rows are
	// flagged for a *required* field, a required field may not declare a
	// default, and there is therefore nothing a schema edit could invent
	// that would make them fit. A test that stopped there would be a
	// fixture too small to distinguish any policy, and it was: an
	// experiment that made UpsertEntityType call RepairEntities with
	// every declared default left it green, because the repair failed
	// every row on the missing `rank`.
	//
	// Removing a field is the case that *can* be repaired automatically:
	// the rows are flagged because they carry a value the type no longer
	// declares, and dropping it fixes every one of them with no decision
	// to make. So this is the half where a schema edit that quietly
	// repaired its own rows would succeed — and must not.
	if _, err := f.svc.UpsertEntity(ctx, f.project, metamodel.EntityInput{
		TypeKey: "quest", Key: "q0000", Name: "Quest 0000",
		Fields:          map[string]any{"summary": "s", "rank": "gold", "laps": float64(3)},
		ExpectedVersion: &row.Version,
	}); err != nil {
		t.Fatalf("make a row fit: %v", err)
	}
	f.setSchema(t, metamodel.Schema{
		{Key: "summary", Type: metamodel.FieldText},
		{Key: "rank", Type: metamodel.FieldEnum, Options: []string{"gold", "silver"}, Required: true},
	})
	if got := f.flagged(t); got != 3 {
		t.Fatalf("%d rows flagged after removing a field, want 3", got)
	}
	widened, err := f.svc.EntityByKey(ctx, f.project, "quest", "q0000")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if !widened.Invalid {
		t.Fatalf("a schema edit repaired the row it had just flagged")
	}
	if widened.Version != 2 {
		t.Fatalf("a schema edit moved a row to version %d, want the 2 the hand fix left",
			widened.Version)
	}
	if got := decodeStoredFields(t, widened.Fields)["laps"]; got != float64(3) {
		t.Fatalf("a schema edit dropped the value it had just flagged: %v", got)
	}
}

// TestARepairTouchesOnlyTheRowsTheSchemaRejects is what stops a repair
// being a bulk content editor wearing a repair's name.
//
// One row is made to fit before the pass runs, by the ordinary write path
// — which is how a concurrent designer's fix arrives too. The pass must
// not see it: it is not in the selection, its version must not move, and
// the value the pass is setting must not land on it.
func TestARepairTouchesOnlyTheRowsTheSchemaRejects(t *testing.T) {
	f := newRepairFixture(t, 3)
	ctx := context.Background()

	f.setSchema(t, metamodel.Schema{
		{Key: "summary", Type: metamodel.FieldText},
		{Key: "laps", Type: metamodel.FieldNumber, Required: true},
	})
	fixed := int32(1)
	if _, err := f.svc.UpsertEntity(ctx, f.project, metamodel.EntityInput{
		TypeKey: "quest", Key: "q0001", Name: "Quest 0001",
		Fields:          map[string]any{"summary": "Fixed by hand.", "laps": float64(99)},
		ExpectedVersion: &fixed,
	}); err != nil {
		t.Fatalf("fix one row by hand: %v", err)
	}
	if got := f.flagged(t); got != 2 {
		t.Fatalf("%d rows flagged after fixing one by hand, want 2", got)
	}

	out, err := f.svc.RepairEntities(ctx, f.project, metamodel.RepairInput{
		TypeKey: "quest", Set: map[string]any{"laps": float64(10)},
	})
	if err != nil {
		t.Fatalf("RepairEntities: %v", err)
	}
	if out.Scanned != 2 || len(out.Repaired) != 2 {
		t.Fatalf("the pass scanned %d and repaired %d, want 2 and 2", out.Scanned, len(out.Repaired))
	}
	for _, w := range out.Repaired {
		if w.Key == "q0001" {
			t.Fatalf("the pass rewrote a row that already fitted: %+v", w)
		}
	}
	row, err := f.svc.EntityByKey(ctx, f.project, "quest", "q0001")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if row.Version != 2 {
		t.Fatalf("the hand-fixed row moved to version %d; the pass wrote to it", row.Version)
	}
	if got := decodeStoredFields(t, row.Fields)["laps"]; got != float64(99) {
		t.Fatalf("the pass overwrote a valid row's value with its own: %v", got)
	}
}

// TestARepairClearsTheFlagOnlyByRevalidating pins that there is no way
// to tell a repair "these are fine now".
//
// The pass sets a value that does not satisfy the schema. Every row goes
// through the ordinary write path, so every row is refused there, and the
// report says so at the row's own key with the schema's own complaint —
// the same code entities.upsert would have answered with. A repair that
// cleared the flag by fiat would leave this green and the game's content
// wrong.
func TestARepairClearsTheFlagOnlyByRevalidating(t *testing.T) {
	f := newRepairFixture(t, 3)
	ctx := context.Background()

	f.setSchema(t, metamodel.Schema{
		{Key: "summary", Type: metamodel.FieldText},
		{Key: "rank", Type: metamodel.FieldEnum, Options: []string{"gold", "silver"}, Required: true},
	})
	out, err := f.svc.RepairEntities(ctx, f.project, metamodel.RepairInput{
		TypeKey: "quest", Set: map[string]any{"rank": "bronze"},
	})
	if err != nil {
		t.Fatalf("RepairEntities: %v", err)
	}
	if out.Scanned != 3 || len(out.Repaired) != 0 || len(out.Failed) != 3 {
		t.Fatalf("the pass scanned %d, repaired %d, failed %d",
			out.Scanned, len(out.Repaired), len(out.Failed))
	}
	for _, fail := range out.Failed {
		if fail.Code != "schema_violation" {
			t.Fatalf("a row that could not be repaired was coded %q: %+v", fail.Code, fail)
		}
		if fail.Key == "" {
			t.Fatalf("a repair failure names no row: %+v", fail)
		}
	}
	if got := f.flagged(t); got != 3 {
		t.Fatalf("%d rows are flagged after a repair that fixed nothing, want 3", got)
	}
}

// TestARepairPassIsBoundedAndConverges pins the loop the tool
// description tells a caller to write, on more rows than one pass may
// touch.
//
// There is no cursor, and this is why there does not need to be one: a
// repaired row leaves the selection, so the next call's first page is
// what the last call did not fix. The assertion is that the loop
// terminates and that it terminates in the number of passes the bound
// implies, which is what would break if a pass ever re-read rows it had
// already repaired.
func TestARepairPassIsBoundedAndConverges(t *testing.T) {
	const rows = 250
	f := newRepairFixture(t, rows)
	ctx := context.Background()

	f.setSchema(t, metamodel.Schema{
		{Key: "summary", Type: metamodel.FieldText},
		{Key: "laps", Type: metamodel.FieldNumber, Required: true},
	})

	passes, repaired := 0, 0
	for {
		out, err := f.svc.RepairEntities(ctx, f.project, metamodel.RepairInput{
			TypeKey: "quest", Set: map[string]any{"laps": float64(10)}, Limit: 100,
		})
		if err != nil {
			t.Fatalf("RepairEntities: %v", err)
		}
		passes, repaired = passes+1, repaired+len(out.Repaired)
		if len(out.Repaired) == 0 {
			break
		}
		if out.Scanned > 100 {
			t.Fatalf("a pass limited to 100 scanned %d rows", out.Scanned)
		}
		if passes > 10 {
			t.Fatalf("the repair loop did not converge: %d passes, %d repaired", passes, repaired)
		}
	}
	if repaired != rows || passes != 4 {
		t.Fatalf("%d passes repaired %d rows, want 3 full passes plus one empty one over %d",
			passes, repaired, rows)
	}
	if got := f.flagged(t); got != 0 {
		t.Fatalf("%d rows still flagged", got)
	}

	// And the ceiling: a pass may not be asked to write more rows than a
	// batch may carry, because a pass *is* a batch.
	over, err := f.svc.RepairEntities(ctx, f.project, metamodel.RepairInput{
		TypeKey: "quest", DropUnknown: true, Limit: metamodel.MaxRepairBatch + 1,
	})
	if err != nil {
		t.Fatalf("RepairEntities: %v", err)
	}
	if over.Scanned != 0 {
		t.Fatalf("a pass over a repaired type scanned %d rows", over.Scanned)
	}
	if metamodel.MaxRepairBatch != metamodel.MaxBulkItems {
		t.Fatalf("a repair pass may write %d rows and a batch may carry %d; a pass is a batch",
			metamodel.MaxRepairBatch, metamodel.MaxBulkItems)
	}
}

// TestARepairThatStatesNoOperationIsRefused pins the refusal that keeps
// this from becoming a back-fill by the back door.
//
// A pass with neither `set` nor `drop_unknown` rewrites every flagged row
// with the values it already holds — which either changes nothing, or,
// where the schema has since grown a default, injects that default into
// every flagged row. The second reading is exactly the back-fill the
// schema-evolution rule refuses, arrived at by a call that looks like it
// does nothing.
func TestARepairThatStatesNoOperationIsRefused(t *testing.T) {
	f := newRepairFixture(t, 3)
	ctx := context.Background()

	f.setSchema(t, metamodel.Schema{
		{Key: "summary", Type: metamodel.FieldText},
		{Key: "rank", Type: metamodel.FieldEnum, Options: []string{"gold"}, Required: true},
		{Key: "laps", Type: metamodel.FieldNumber, Default: float64(7)},
	})
	_, err := f.svc.RepairEntities(ctx, f.project, metamodel.RepairInput{TypeKey: "quest"})
	requireFieldError(t, err, "set",
		"a repair must state what to change: give set, drop_unknown, or both. "+
			"A pass with neither would rewrite every flagged row with the values it already holds")

	// Nothing was read and nothing was written: the refusal is before the
	// selection, so the default is not in any row.
	row, err := f.svc.EntityByKey(ctx, f.project, "quest", "q0000")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if _, present := decodeStoredFields(t, row.Fields)["laps"]; present {
		t.Fatalf("a refused repair back-filled a default")
	}
}

// TestARepairRefusesASetKeyTheTypeDoesNotDeclare keeps a caller's own
// mistake from being reported two hundred times as a property of the
// game's content.
func TestARepairRefusesASetKeyTheTypeDoesNotDeclare(t *testing.T) {
	f := newRepairFixture(t, 3)
	ctx := context.Background()

	f.setSchema(t, metamodel.Schema{
		{Key: "summary", Type: metamodel.FieldText},
		{Key: "laps", Type: metamodel.FieldNumber, Required: true},
	})
	_, err := f.svc.RepairEntities(ctx, f.project, metamodel.RepairInput{
		TypeKey: "quest", Set: map[string]any{"lap": float64(10)},
	})
	requireFieldError(t, err, "set.lap",
		"no such field on this type; a repair writes declared values only")

	if got := f.flagged(t); got != 3 {
		t.Fatalf("%d rows flagged after a refused repair, want 3", got)
	}
}

// TestRepairingAnUndeclaredTypeIsNotFound: the address is a key, like
// every other read on this surface, and a key that names nothing is
// not_found rather than a pass over zero rows.
func TestRepairingAnUndeclaredTypeIsNotFound(t *testing.T) {
	f := newRepairFixture(t, 1)
	ctx := context.Background()

	if _, err := f.svc.RepairEntities(ctx, f.project, metamodel.RepairInput{
		TypeKey: "monster", DropUnknown: true,
	}); err == nil {
		t.Fatalf("repairing an undeclared entity type was accepted")
	} else if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want not_found", err)
	}
	if _, err := f.svc.RepairRelations(ctx, f.project, metamodel.RepairInput{
		TypeKey: "eats", DropUnknown: true,
	}); err == nil {
		t.Fatalf("repairing an undeclared relation type was accepted")
	} else if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want not_found", err)
	}
}

// TestAnEdgeSchemaEditIsRepairedTheSameWay carries the rule one step
// along, which is where this repository's most repeated defect lives.
//
// 0009 gave relations a field schema's invalid flag and a version, and
// revalidate is one function for both kinds precisely so that a schema
// edit flags edges exactly as it flags entities. A repair that covered
// only entities would leave a designer who narrows a *relation* type's
// schema with the two-hundred-round-trip loop this file exists to close.
func TestAnEdgeSchemaEditIsRepairedTheSameWay(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	// The endpoint lists are entity type *ids*, which is what a caller of
	// this domain has to resolve for itself today.
	types, err := svc.ListEntityTypes(ctx, project)
	if err != nil {
		t.Fatalf("ListEntityTypes: %v", err)
	}
	byKey := map[string]uuid.UUID{}
	for _, row := range types {
		byKey[row.Key] = row.ID
	}
	relType, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"zone"},
	})
	if err != nil {
		t.Fatalf("declare relation type: %v", err)
	}
	for _, quest := range []string{"hogger", "kobold-camp"} {
		if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
			TypeKey: "takes_place_in",
			Source:  metamodel.Ref{TypeKey: "quest", Key: quest},
			Target:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
		}); err != nil {
			t.Fatalf("seed edge %s: %v", quest, err)
		}
	}

	version := relType.Version
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeKeys:  []string{"quest"},
		TargetTypeKeys:  []string{"zone"},
		Schema:          metamodel.Schema{{Key: "act", Type: metamodel.FieldNumber, Required: true}},
		ExpectedVersion: &version,
	}); err != nil {
		t.Fatalf("narrow the edge schema: %v", err)
	}

	yes := true
	flagged, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{
		TypeKey: "takes_place_in", Invalid: &yes, Limit: metamodel.MaxRelationPage,
	})
	if err != nil {
		t.Fatalf("list flagged edges: %v", err)
	}
	if len(flagged.Relations) != 2 {
		t.Fatalf("%d edges flagged, want 2", len(flagged.Relations))
	}

	out, err := svc.RepairRelations(ctx, project, metamodel.RepairInput{
		TypeKey: "takes_place_in", Set: map[string]any{"act": float64(1)},
	})
	if err != nil {
		t.Fatalf("RepairRelations: %v", err)
	}
	if out.Scanned != 2 || len(out.Repaired) != 2 || len(out.Failed) != 0 {
		t.Fatalf("the edge pass scanned %d, repaired %d, failed %+v",
			out.Scanned, len(out.Repaired), out.Failed)
	}

	still, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{
		TypeKey: "takes_place_in", Invalid: &yes, Limit: metamodel.MaxRelationPage,
	})
	if err != nil {
		t.Fatalf("list flagged edges: %v", err)
	}
	if len(still.Relations) != 0 {
		t.Fatalf("%d edges still flagged after the repair", len(still.Relations))
	}

	// The endpoints survived the round trip. An edge is written by its
	// address, and the repair rebuilds that address from the stored row;
	// a join that resolved the wrong end would move an edge rather than
	// repair it, and no flag check would see it.
	edge, err := svc.RelationByEdge(ctx, project, "takes_place_in",
		metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"})
	if err != nil {
		t.Fatalf("RelationByEdge: %v", err)
	}
	if got := decodeStoredFields(t, edge.Fields)["act"]; got != float64(1) {
		t.Fatalf("repaired edge fields = %v", got)
	}
	if edge.Version != 2 {
		t.Fatalf("a repaired edge is at version %d, want 2", edge.Version)
	}
	// And the game still holds two edges, not four: a repair writes the
	// rows it read rather than creating new ones.
	all, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{
		TypeKey: "takes_place_in", Limit: metamodel.MaxRelationPage,
	})
	if err != nil {
		t.Fatalf("list edges: %v", err)
	}
	if len(all.Relations) != 2 {
		t.Fatalf("the game holds %d takes_place_in edges, want 2", len(all.Relations))
	}
}
