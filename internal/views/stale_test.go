package views

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
)

// This file is staleness: a view saved against one vocabulary, run
// against the next one.
//
// **A rename is performed here with an UPDATE, because the product has no
// rename.** The plan's step 1 says to rename `requires` through
// `relation_types.upsert`, and that call cannot do it: both type upserts
// are addressed *by key* (EntityTypeInput and RelationTypeInput carry no
// id), so upserting under a new key creates a second type and leaves the
// first standing — which is a different scenario entirely, and one where
// resolution by id would never be exercised. The rename these tests need
// is the one a rename operation would perform when it is built, so it is
// performed directly on the row here. That is also why the id-first
// resolution matters before any such operation exists: it is what makes
// the operation addable without breaking every saved view in a game.

func (g *game) renameEntityType(t *testing.T, from, to string) {
	t.Helper()
	tag, err := g.pool.Exec(context.Background(),
		`UPDATE entity_types SET key = $3 WHERE project_id = $1 AND lower(key) = lower($2)`,
		g.projectID, from, to)
	if err != nil {
		t.Fatalf("rename entity type %s: %v", from, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("rename entity type %s changed %d rows", from, tag.RowsAffected())
	}
}

func (g *game) renameRelationType(t *testing.T, from, to string) {
	t.Helper()
	tag, err := g.pool.Exec(context.Background(),
		`UPDATE relation_types SET key = $3 WHERE project_id = $1 AND lower(key) = lower($2)`,
		g.projectID, from, to)
	if err != nil {
		t.Fatalf("rename relation type %s: %v", from, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("rename relation type %s changed %d rows", from, tag.RowsAffected())
	}
}

// typeIDOf reads a declared type's id, which is what a deletion is
// addressed by.
func (g *game) typeIDOf(t *testing.T, kind, key string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	if kind == KindEntityType {
		row, err := g.meta.EntityTypeByKey(ctx, g.projectID, key)
		if err != nil {
			t.Fatalf("read entity type %s: %v", key, err)
		}
		return row.ID
	}
	row, err := g.meta.RelationTypeByKey(ctx, g.projectID, key)
	if err != nil {
		t.Fatalf("read relation type %s: %v", key, err)
	}
	return row.ID
}

// save writes one saved view and returns it.
func (g *game) save(t *testing.T, key, doc string) {
	t.Helper()
	if _, err := g.views.UpsertView(context.Background(), g.projectID, saveable(key, doc)); err != nil {
		t.Fatalf("save view %s: %v", key, err)
	}
}

// diagnosticsOf reads the staleness report off a refusal.
func diagnosticsOf(t *testing.T, err error) []Diagnostic {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a refusal carrying a staleness report")
	}
	if !errors.Is(err, ErrQueryStale) {
		t.Fatalf("expected query_stale, got %v", err)
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected a *QueryError, got %T", err)
	}
	return qe.Stale
}

// wants asserts one diagnostic is in the report, with its pointer and its
// was/now pair, and says what came back when it is not.
func wants(t *testing.T, diags []Diagnostic, want Diagnostic) {
	t.Helper()
	for _, d := range diags {
		if d == want {
			return
		}
	}
	t.Fatalf("no diagnostic %+v in %+v", want, diags)
}

// codesOf is the report reduced to its codes, for the tests that care
// which things were reported and not how many.
func codesOf(diags []Diagnostic) []string {
	out := make([]string, 0, len(diags))
	for _, d := range diags {
		out = append(out, d.Code)
	}
	return out
}

// The view every rename test runs: the prerequisite chain of §3.1, which
// draws defias and the quest it requires.
const chainView = `{"v":1,"from":[{"type":"quest","as":"q",
	  "where":{"field":"@key","op":"eq","value":"defias"}}],
	"traverse":[{"from":"q","via":"requires","to_type":"quest","as":"pre"}],
	"edges":[{"from_step":"pre"}]}`

// TestARenamedRelationTypeStillRunsAndReportsItsRename is the first half
// of the whole design: a rename does not change an id, the stored
// dependency index holds the id, so the view still draws what it drew.
//
// The two assertions are independent and both are load-bearing. That the
// nodes come back says the id resolved — a by-key implementation returns
// query_stale here. That the diagnostic comes back says the spelling was
// noticed — an implementation that resolved by id and said nothing would
// leave a designer with a document that quietly disagrees with the game
// forever.
func TestARenamedRelationTypeStillRunsAndReportsItsRename(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "chain", chainView)
	g.renameRelationType(t, "requires", "depends_on")

	res, err := g.views.RunView(t.Context(), g.projectID, "chain", RunRequest{})
	if err != nil {
		t.Fatalf("a renamed relation type must still resolve by id: %v", err)
	}
	if len(res.Nodes) != 2 || len(res.Edges) != 1 {
		t.Fatalf("the picture must be the one the rename did not change: %d nodes, %d edges",
			len(res.Nodes), len(res.Edges))
	}
	wants(t, res.Stale, Diagnostic{
		Code: DiagRelationTypeRenamed, Pointer: "/traverse/0/via/0",
		Was: "requires", Now: "depends_on",
	})
	if len(res.Stale) != 1 {
		t.Fatalf("one rename is one diagnostic, got %+v", res.Stale)
	}
}

// TestARenameDoesNotRewriteTheStoredQuery is the other half of the same
// decision. Repairing the document behind the author's back would make
// optimistic concurrency lie: the next expected_version check would pass
// against a document nobody wrote.
func TestARenameDoesNotRewriteTheStoredQuery(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "chain", chainView)
	g.renameRelationType(t, "requires", "depends_on")
	if _, err := g.views.RunView(t.Context(), g.projectID, "chain", RunRequest{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := g.views.ViewByKey(t.Context(), g.projectID, "chain")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Version != 1 {
		t.Fatalf("Version = %d: a run must not move a view's version", got.Version)
	}
	var stored map[string]any
	if err := json.Unmarshal(got.Query, &stored); err != nil {
		t.Fatalf("decode the stored query: %v", err)
	}
	// Semantically as written: jsonb normalises whitespace and key order,
	// so the assertion is on the decoded value at the position that was
	// renamed, not on the bytes.
	traverse, _ := stored["traverse"].([]any)
	if len(traverse) != 1 {
		t.Fatalf("the stored document must still hold its one step: %v", stored)
	}
	step, _ := traverse[0].(map[string]any)
	if step["via"] != "requires" {
		t.Fatalf("via = %v, want the spelling the author wrote", step["via"])
	}
}

// TestARenamedEntityTypeStillJudgesTheProjectionThatDrawsIt is the rule
// carried one step along, and the step where it was first missed: the
// projection's scope is built by resolving each selector's type, and a
// scope built by key alone loses exactly the type a rename moved. What
// comes back then is not a refusal about the rename — it is
// "no field min_level is declared on the entity types this query draws",
// pointed at a projection the designer never touched.
func TestARenamedEntityTypeStillJudgesTheProjectionThatDrawsIt(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "levels", `{"v":1,"from":[{"type":"quest","as":"q"}],
		"project":{"color_by":"min_level","fields":["rank"]}}`)
	g.renameEntityType(t, "quest", "mission")

	res, err := g.views.RunView(t.Context(), g.projectID, "levels", RunRequest{})
	if err != nil {
		t.Fatalf("a renamed entity type must still resolve by id: %v", err)
	}
	if len(res.Nodes) != 3 {
		t.Fatalf("every quest must still be drawn, got %d", len(res.Nodes))
	}
	if res.Nodes[0].Attrs["color_by"] == nil {
		t.Fatalf("the projection must still find its field: %+v", res.Nodes[0].Attrs)
	}
	wants(t, res.Stale, Diagnostic{
		Code: DiagEntityTypeRenamed, Pointer: "/from/0/type", Was: "quest", Now: "mission",
	})
	if len(res.Stale) != 1 {
		t.Fatalf("the rename is the only thing that moved, got %+v", res.Stale)
	}
}

// TestARenameIsReportedOnceAndNotOncePerPositionThatResolvesIt: the
// projection's scope resolves the very pointers the reference closures
// already resolved, and a second report there would tell a designer to
// repair one thing twice. The query names the type at one position and
// reads it at three.
func TestARenameIsReportedOnceAndNotOncePerPositionThatResolvesIt(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "levels", `{"v":1,"from":[{"type":"quest","as":"q"}],
		"project":{"color_by":"min_level","group_by":"rank",
		  "fields":["difficulty"]}}`)
	g.renameEntityType(t, "quest", "mission")

	res, err := g.views.RunView(t.Context(), g.projectID, "levels", RunRequest{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Stale) != 1 {
		t.Fatalf("one renamed type is one diagnostic, got %+v", res.Stale)
	}
}

// TestADeletedTypeFailsTheRunByDefault is the safety property, and it
// gets its own test because it is a *default*: a diagram that silently
// dropped its traversal looks exactly like a correct diagram.
func TestADeletedTypeFailsTheRunByDefault(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "chain", chainView)
	id := g.typeIDOf(t, KindRelationType, "requires")
	if err := g.meta.RemoveRelationType(t.Context(), g.projectID, id, true); err != nil {
		t.Fatalf("remove relation type: %v", err)
	}

	_, err := g.views.RunView(t.Context(), g.projectID, "chain", RunRequest{})
	diags := diagnosticsOf(t, err)
	wants(t, diags, Diagnostic{
		Code: DiagRelationTypeMissing, Pointer: "/traverse/0/via/0", Was: "requires",
	})
	// The refusal carries the pointer-addressed sentence too, which is
	// what internal/web publishes as details.fields[].path.
	var qe *QueryError
	if !errors.As(err, &qe) || len(qe.Fields) == 0 ||
		qe.Fields[0].Path != "/traverse/0/via/0" {
		t.Fatalf("a stale refusal must address itself: %+v", qe)
	}

	// The view is still readable, and still says what it always said:
	// refusing to run is not refusing to exist.
	got, err := g.views.ViewByKey(t.Context(), g.projectID, "chain")
	if err != nil {
		t.Fatalf("a stale view must still be readable: %v", err)
	}
	if !strings.Contains(string(got.Query), "requires") {
		t.Fatalf("the stored query must be untouched: %s", got.Query)
	}
}

// TestBestEffortDropsTheStalePartAndSaysWhatItDropped.
//
// **Non-empty is the control.** An implementation that dropped
// everything, or that returned an empty picture with a warning, satisfies
// "no error" and answers the designer's question with a lie of a
// different shape.
func TestBestEffortDropsTheStalePartAndSaysWhatItDropped(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "chain", chainView)
	id := g.typeIDOf(t, KindRelationType, "requires")
	if err := g.meta.RemoveRelationType(t.Context(), g.projectID, id, true); err != nil {
		t.Fatalf("remove relation type: %v", err)
	}

	res, err := g.views.RunView(t.Context(), g.projectID, "chain",
		RunRequest{OnStale: OnStaleBestEffort})
	if err != nil {
		t.Fatalf("best effort must draw what is left: %v", err)
	}
	if len(res.Nodes) != 1 || res.Nodes[0].Key != "defias" {
		t.Fatalf("the un-stale part is the seed selector: %+v", res.Nodes)
	}
	if len(res.Edges) != 0 {
		t.Fatalf("the edges entry drew the dropped step and must go with it: %+v", res.Edges)
	}
	wants(t, res.Stale, Diagnostic{
		Code: DiagRelationTypeMissing, Pointer: "/traverse/0/via/0", Was: "requires",
	})
}

// TestBestEffortDropsTheSetRatherThanTheConditionItCannotEvaluate is the
// rule that keeps best effort from being the thing on_stale defaults to
// fail over. A filter that cannot be resolved must not be *dropped*: the
// set would then come back wider than the document asks for, which is a
// picture that is wrong rather than one that is short.
//
// **The fixture is built so that the widening is visible**, which took
// some care and is the point of this comment. The obvious construction —
// filter on a field, then drop that field from the schema — cannot show
// it: dropping a declared key that entities hold values for marks every
// one of them invalid, so the widened set draws nothing anyway and a
// broken implementation looks exactly like a correct one. Narrowing an
// enum's options invalidates only the rows holding the option that went,
// which leaves the other two quests valid, drawable, and outside the
// filter this view asks for. Those two are what a dropped condition
// would put on the picture.
func TestBestEffortDropsTheSetRatherThanTheConditionItCannotEvaluate(t *testing.T) {
	g, _ := newGame(t)
	// One set filtered to the single epic quest, one set that is not
	// filtered at all, so there is something left to draw and something
	// to get wrong.
	g.save(t, "two", `{"v":1,"from":[
		  {"type":"quest","as":"epics","where":{"field":"rank","op":"eq","value":"epic"}},
		  {"type":"zone","as":"z"}],
		"nodes":[{"set":"epics"},{"set":"z"}]}`)
	if _, err := g.meta.UpsertEntityType(t.Context(), g.projectID, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests", ExpectedVersion: ptrInt32(1),
		Schema: metamodel.Schema{
			{Key: "min_level", Type: metamodel.FieldNumber},
			{Key: "tags", Type: metamodel.FieldListText},
			{Key: "rank", Type: metamodel.FieldEnum, Options: []string{"common", "rare"}},
		},
	}); err != nil {
		t.Fatalf("drop the epic rank: %v", err)
	}

	res, err := g.views.RunView(t.Context(), g.projectID, "two",
		RunRequest{OnStale: OnStaleBestEffort})
	if err != nil {
		t.Fatalf("best effort: %v", err)
	}
	for _, node := range res.Nodes {
		if node.Type != "zone" {
			t.Fatalf("the filtered set must be dropped whole, not run without its filter: %+v",
				res.Nodes)
		}
	}
	if len(res.Nodes) != 2 {
		t.Fatalf("the unfiltered set must survive: %+v", res.Nodes)
	}
	wants(t, res.Stale, Diagnostic{
		Code: DiagEnumOptionMissing, Pointer: "/from/0/where/value", Was: "epic",
	})

	// The control, in the same fixture: with the option still declared,
	// the filtered set draws the one quest it names.
	g2, _ := newGame(t)
	g2.save(t, "two", `{"v":1,"from":[
		  {"type":"quest","as":"epics","where":{"field":"rank","op":"eq","value":"epic"}},
		  {"type":"zone","as":"z"}],
		"nodes":[{"set":"epics"},{"set":"z"}]}`)
	control, err := g2.views.RunView(t.Context(), g2.projectID, "two", RunRequest{})
	if err != nil || len(control.Nodes) != 3 {
		t.Fatalf("the control must draw the epic quest and both zones: %v, %+v",
			err, control.Nodes)
	}
}

// TestBestEffortWithNothingLeftToDrawRefusesRatherThanDrawingNothing.
// An empty picture with a warning beside it reads as "this game has
// nothing in it", which is a different wrong answer rather than a
// smaller right one.
func TestBestEffortWithNothingLeftToDrawRefusesRatherThanDrawingNothing(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "factions", `{"v":1,"from":[{"type":"faction","as":"f"}]}`)
	id := g.typeIDOf(t, KindEntityType, "faction")
	if err := g.meta.RemoveEntityType(t.Context(), g.projectID, id, true); err != nil {
		t.Fatalf("remove entity type: %v", err)
	}

	_, err := g.views.RunView(t.Context(), g.projectID, "factions",
		RunRequest{OnStale: OnStaleBestEffort})
	wants(t, diagnosticsOf(t, err), Diagnostic{
		Code: DiagEntityTypeMissing, Pointer: "/from/0/type", Was: "faction",
	})
}

// TestADeletedAndRecreatedTypeResolvesByKey is step 2 of the resolution
// order, and the reason it exists: a designer who deletes a type and
// declares it again under the same key has fixed a mistake, not broken
// every view that named it. ON DELETE SET NULL is what leaves the key
// text standing for this to resolve against.
func TestADeletedAndRecreatedTypeResolvesByKey(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "chain", chainView)
	id := g.typeIDOf(t, KindRelationType, "requires")
	if err := g.meta.RemoveRelationType(t.Context(), g.projectID, id, true); err != nil {
		t.Fatalf("remove relation type: %v", err)
	}
	if _, err := g.meta.UpsertRelationType(t.Context(), g.projectID,
		metamodel.RelationTypeInput{Key: "requires", Label: "Requires"}); err != nil {
		t.Fatalf("declare it again: %v", err)
	}
	g.relate(t, "requires", "quest", "defias", "quest", "hogger")

	res, err := g.views.RunView(t.Context(), g.projectID, "chain", RunRequest{})
	if err != nil {
		t.Fatalf("a re-created type must resolve by key: %v", err)
	}
	if len(res.Nodes) != 2 || len(res.Edges) != 1 {
		t.Fatalf("the picture must be whole again: %d nodes, %d edges",
			len(res.Nodes), len(res.Edges))
	}
	if len(res.Stale) != 0 {
		t.Fatalf("a view that runs correctly is not stale: %+v", res.Stale)
	}
	// The control that the id really was null: the stored ref still
	// carries the key and no id, so this resolved by the second step and
	// not by the first.
	view, err := g.views.ViewByKey(t.Context(), g.projectID, "chain")
	if err != nil {
		t.Fatalf("read the view: %v", err)
	}
	refs, err := g.views.ViewRefs(t.Context(), g.projectID, view.ID)
	if err != nil {
		t.Fatalf("read the refs: %v", err)
	}
	for _, ref := range refs {
		if ref.Pointer == "/traverse/0/via/0" && ref.RelationTypeID != nil {
			t.Fatalf("the deletion must have emptied the id this test resolves without")
		}
	}
}

// TestDeletingATypeListsTheViewsItBroke.
//
// Two things are pinned, and the second is why the list is read before
// the delete rather than after it: the same ON DELETE SET NULL that lets
// a ref row survive its type empties the column this lookup matches on,
// so the answer afterwards is "nothing broke" — a wrong answer rather
// than an error.
func TestDeletingATypeListsTheViewsItBroke(t *testing.T) {
	g, other := newGame(t)
	g.save(t, "factions", `{"v":1,"from":[{"type":"faction","as":"f"}]}`)
	g.save(t, "quests", questsOnly)
	other.save(t, "factions", `{"v":1,"from":[{"type":"faction","as":"f"}]}`)
	id := g.typeIDOf(t, KindEntityType, "faction")

	broke, err := g.views.RemoveTypeReportingViews(t.Context(), g.projectID,
		KindEntityType, id, true)
	if err != nil {
		t.Fatalf("a type views depend on is deletable: %v", err)
	}
	if len(broke) != 1 {
		t.Fatalf("one view names this type in this game, got %+v", broke)
	}
	got := broke[0]
	if got.ViewKey != "factions" || got.Kind != KindEntityType || got.RefKey != "faction" ||
		got.Pointer != "/from/0/type" {
		t.Fatalf("the report must say which view and where: %+v", got)
	}

	// The ordering rule, asserted rather than trusted: asked after the
	// deletion the same question answers nothing.
	after, err := g.views.ViewsDependingOn(t.Context(), g.projectID, KindEntityType, id)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("the SET NULL empties the column this matches on: %+v", after)
	}

	// And the view is still there, still saying what it said. A deletion
	// breaks a view; it does not remove one.
	if _, err := g.views.ViewByKey(t.Context(), g.projectID, "factions"); err != nil {
		t.Fatalf("a broken view is still a view: %v", err)
	}
}

// TestAFieldDroppedFromASchemaIsAFieldMissingDiagnostic.
func TestAFieldDroppedFromASchemaIsAFieldMissingDiagnostic(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "hard", `{"v":1,"from":[{"type":"quest","as":"q",
		"where":{"field":"min_level","op":"gte","value":20}}]}`)
	if _, err := g.meta.UpsertEntityType(t.Context(), g.projectID, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("drop the whole quest schema: %v", err)
	}

	_, err := g.views.RunView(t.Context(), g.projectID, "hard", RunRequest{})
	wants(t, diagnosticsOf(t, err), Diagnostic{
		Code: DiagFieldMissing, Pointer: "/from/0/where/field", Was: "min_level",
	})

	// The control: the same view against the schema it was written for
	// runs, so this test is not passing because every run of it refuses.
	g2, _ := newGame(t)
	g2.save(t, "hard", `{"v":1,"from":[{"type":"quest","as":"q",
		"where":{"field":"min_level","op":"gte","value":20}}]}`)
	res, err := g2.views.RunView(t.Context(), g2.projectID, "hard", RunRequest{})
	if err != nil || len(res.Nodes) != 2 || len(res.Stale) != 0 {
		t.Fatalf("the control must draw the two quests over level 20: %v, %+v", err, res.Nodes)
	}
}

// TestOnStaleIsRefusedOnAnAdHocRunRatherThanIgnored: an inline query has
// no recorded past, so nothing can be resolved by an id a rename left
// alone and nothing can be reported stale. A knob accepted and ignored is
// a knob that lies.
func TestOnStaleIsRefusedOnAnAdHocRunRatherThanIgnored(t *testing.T) {
	g, _ := newGame(t)
	_, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, questsOnly), OnStale: OnStaleBestEffort})
	oneProblem(t, err, "/on_stale", "an inline query cannot be stale")

	// The control: without the switch the same query runs.
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, questsOnly)})
	if err != nil || len(res.Nodes) != 3 {
		t.Fatalf("the control must run: %v, %d nodes", err, len(res.Nodes))
	}
	if res.Stale != nil {
		t.Fatalf("an ad-hoc run has no staleness report: %+v", res.Stale)
	}
}

// TestAnAtTypeOperandIsADependencyLikeEveryOtherTypeReference is the
// decision Task 4 left to the compiler and Task 6 left to this task.
// `@type eq "class"` holds that type up exactly as `from[0].type` does —
// the compiler refuses the whole view when the key names nothing — so it
// is listed in view_refs, and it resolves by id like everything else.
func TestAnAtTypeOperandIsADependencyLikeEveryOtherTypeReference(t *testing.T) {
	g, _ := newGame(t)
	doc := `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"available_to","as":"c",
		  "where":{"field":"@type","op":"eq","value":"class"}}]}`
	g.save(t, "classes", doc)

	view, err := g.views.ViewByKey(t.Context(), g.projectID, "classes")
	if err != nil {
		t.Fatalf("read the view: %v", err)
	}
	refs, err := g.views.ViewRefs(t.Context(), g.projectID, view.ID)
	if err != nil {
		t.Fatalf("read the refs: %v", err)
	}
	var found bool
	for _, ref := range refs {
		if ref.Pointer == "/traverse/0/where/value" {
			found = true
			if ref.Kind != KindEntityType || ref.RefKey != "class" || ref.EntityTypeID == nil {
				t.Fatalf("the operand's reference must be a resolved entity type: %+v", ref)
			}
		}
	}
	if !found {
		t.Fatalf("an @type operand must be indexed like any other reference: %+v", refs)
	}

	// The deletion of that type now reports this view, which is what the
	// index is for.
	id := g.typeIDOf(t, KindEntityType, "class")
	broke, err := g.views.ViewsDependingOn(t.Context(), g.projectID, KindEntityType, id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(broke) != 1 || broke[0].Pointer != "/traverse/0/where/value" {
		t.Fatalf("deleting the type must name this position: %+v", broke)
	}

	// And renaming it carries the view through, which by-key resolution
	// of the operand could not do.
	g.renameEntityType(t, "class", "profession")
	res, err := g.views.RunView(t.Context(), g.projectID, "classes", RunRequest{})
	if err != nil {
		t.Fatalf("a renamed @type operand must still resolve by id: %v", err)
	}
	if len(res.Nodes) != 4 {
		t.Fatalf("three quests and the class they reach: %+v", res.Nodes)
	}
	wants(t, res.Stale, Diagnostic{
		Code: DiagEntityTypeRenamed, Pointer: "/traverse/0/where/value",
		Was: "class", Now: "profession",
	})
}

// TestEveryDiagnosticCodeIsReachableAndCarriesItsPointer is the guard
// over the eight promises. A code nothing can produce is a documented
// mechanism that does not exist, and a code produced by a path no test
// drives is one that can stop working silently.
//
// Every case runs a *saved* view against a game that moved under it, and
// asserts the code with the pointer it is addressed at — a report with
// the right code at the wrong position sends a designer to rewrite the
// wrong line. The last clause is the vacuity check: the eight cases must
// between them cover diagnosticCodes(), so a ninth code added without a
// case fails here.
func TestEveryDiagnosticCodeIsReachableAndCarriesItsPointer(t *testing.T) {
	quest := func(schema metamodel.Schema) func(t *testing.T, g *game) {
		return func(t *testing.T, g *game) {
			t.Helper()
			if _, err := g.meta.UpsertEntityType(t.Context(), g.projectID,
				metamodel.EntityTypeInput{Key: "quest", Label: "Quest", LabelPlural: "Quests",
					Schema: schema, ExpectedVersion: ptrInt32(1)}); err != nil {
				t.Fatalf("redeclare quest: %v", err)
			}
		}
	}
	cases := []struct {
		name  string
		doc   string
		move  func(t *testing.T, g *game)
		run   RunRequest
		want  Diagnostic
		nodes int // -1 when the run is expected to refuse
	}{
		{
			name: DiagEntityTypeMissing,
			doc:  `{"v":1,"from":[{"type":"faction","as":"f"}]}`,
			move: func(t *testing.T, g *game) {
				id := g.typeIDOf(t, KindEntityType, "faction")
				if err := g.meta.RemoveEntityType(t.Context(), g.projectID, id, true); err != nil {
					t.Fatalf("remove: %v", err)
				}
			},
			want:  Diagnostic{Code: DiagEntityTypeMissing, Pointer: "/from/0/type", Was: "faction"},
			nodes: -1,
		},
		{
			name: DiagRelationTypeMissing,
			doc:  chainView,
			move: func(t *testing.T, g *game) {
				id := g.typeIDOf(t, KindRelationType, "requires")
				if err := g.meta.RemoveRelationType(t.Context(), g.projectID, id, true); err != nil {
					t.Fatalf("remove: %v", err)
				}
			},
			want: Diagnostic{Code: DiagRelationTypeMissing, Pointer: "/traverse/0/via/0",
				Was: "requires"},
			nodes: -1,
		},
		{
			name:  DiagEntityTypeRenamed,
			doc:   questsOnly,
			move:  func(t *testing.T, g *game) { g.renameEntityType(t, "quest", "mission") },
			want:  Diagnostic{Code: DiagEntityTypeRenamed, Pointer: "/from/0/type", Was: "quest", Now: "mission"},
			nodes: 3,
		},
		{
			name: DiagRelationTypeRenamed,
			doc:  chainView,
			move: func(t *testing.T, g *game) { g.renameRelationType(t, "requires", "depends_on") },
			want: Diagnostic{Code: DiagRelationTypeRenamed, Pointer: "/traverse/0/via/0",
				Was: "requires", Now: "depends_on"},
			nodes: 2,
		},
		{
			name: DiagFieldMissing,
			doc: `{"v":1,"from":[{"type":"quest","as":"q"}],
				"project":{"color_by":"min_level"}}`,
			move:  quest(metamodel.Schema{{Key: "tags", Type: metamodel.FieldListText}}),
			want:  Diagnostic{Code: DiagFieldMissing, Pointer: "/project/color_by", Was: "min_level"},
			nodes: -1,
		},
		{
			name: DiagFieldTypeChanged,
			doc: `{"v":1,"from":[{"type":"quest","as":"q",
				"where":{"field":"min_level","op":"gte","value":20}}]}`,
			// A number turned into a bool: gte is not an operator a bool
			// answers, so the refusal is at the operator rather than at
			// the value, which is the arm a coercion failure never reaches.
			move: quest(metamodel.Schema{{Key: "min_level", Type: metamodel.FieldBool}}),
			want: Diagnostic{Code: DiagFieldTypeChanged, Pointer: "/from/0/where/op",
				Was: "min_level", Now: string(metamodel.FieldBool)},
			nodes: -1,
		},
		{
			name: DiagEnumOptionMissing,
			doc: `{"v":1,"from":[{"type":"quest","as":"q",
				"where":{"field":"rank","op":"eq","value":"epic"}}]}`,
			move: quest(metamodel.Schema{{Key: "rank", Type: metamodel.FieldEnum,
				Options: []string{"common", "rare"}}}),
			want: Diagnostic{Code: DiagEnumOptionMissing, Pointer: "/from/0/where/value",
				Was: "epic"},
			nodes: -1,
		},
		{
			name: DiagParamUnbound,
			doc: `{"v":1,"params":[{"key":"floor","type":"number"}],
				"from":[{"type":"quest","as":"q",
				  "where":{"field":"min_level","op":"gte","value":{"param":"floor"}}}]}`,
			move:  func(t *testing.T, g *game) {},
			want:  Diagnostic{Code: DiagParamUnbound, Pointer: "/from/0/where/value", Was: "floor"},
			nodes: -1,
		},
	}

	covered := map[string]bool{}
	for _, tc := range cases {
		covered[tc.want.Code] = true
		t.Run(tc.name, func(t *testing.T) {
			g, _ := newGame(t)
			g.save(t, "v", tc.doc)
			tc.move(t, g)
			res, err := g.views.RunView(t.Context(), g.projectID, "v", tc.run)
			if tc.nodes < 0 {
				wants(t, diagnosticsOf(t, err), tc.want)
				return
			}
			if err != nil {
				t.Fatalf("this view still runs: %v", err)
			}
			if len(res.Nodes) != tc.nodes {
				t.Fatalf("nodes = %d, want %d: %+v", len(res.Nodes), tc.nodes, res.Nodes)
			}
			wants(t, res.Stale, tc.want)
		})
	}
	for _, code := range diagnosticCodes() {
		if !covered[code] {
			t.Errorf("the code %q is declared and no case here produces it: a code nothing "+
				"can reach is a documented mechanism that does not exist", code)
		}
	}
	if len(covered) != len(diagnosticCodes()) {
		t.Fatalf("this test covers %d codes and the vocabulary has %d",
			len(covered), len(diagnosticCodes()))
	}
}

// TestEveryDiagnosticCodeHasASentenceOfItsOwn: the codes are what a UI
// bands across the top of a picture, and the sentences are what an agent
// acts on. A code with no sentence reaches an agent as its own
// identifier, which says nothing about what to do.
func TestEveryDiagnosticCodeHasASentenceOfItsOwn(t *testing.T) {
	codes := diagnosticCodes()
	if len(codes) == 0 {
		t.Fatalf("the vocabulary is empty: this guard would pass over nothing")
	}
	seen := map[string]string{}
	for _, code := range codes {
		message := Diagnostic{Code: code, Was: "a_key", Now: "another_key"}.message()
		if message == code {
			t.Errorf("the code %q has no sentence: message() fell through to the code", code)
		}
		if !strings.Contains(message, "a_key") {
			t.Errorf("the sentence for %q does not name what the document says: %q", code, message)
		}
		if other, clash := seen[message]; clash {
			t.Errorf("%q and %q answer with the same sentence: %q", code, other, message)
		}
		seen[message] = code
	}
}

// TestARunOfAnotherGamesViewFindsNothing: RunView reads by key inside a
// game, and a view key is not a secret that carries authority across
// games. Both games seed a view under the same key here, which is what
// makes the assertion about the filter rather than about the key.
func TestARunOfAnotherGamesViewFindsNothing(t *testing.T) {
	g, other := newGame(t)
	other.save(t, "chain", chainView)
	if _, err := g.views.RunView(t.Context(), g.projectID, "chain", RunRequest{}); err == nil {
		t.Fatalf("another game's view must not be runnable here")
	}
	g.save(t, "chain", chainView)
	res, err := g.views.RunView(t.Context(), g.projectID, "chain", RunRequest{})
	if err != nil || len(res.Nodes) != 2 {
		t.Fatalf("the control must run in the game that owns it: %v, %+v", err, res.Nodes)
	}
}

// TestAStaleViewIsRefusedBeforeItReachesTheDatabase: a run that cannot
// resolve must not compile, and a compile that cannot happen must not
// open a transaction. Nothing here asserts SQL; what it asserts is that
// the refusal is the stale one rather than whatever the compiler would
// have said about a half-resolved query.
func TestAStaleViewIsRefusedBeforeItReachesTheDatabase(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "chain", chainView)
	id := g.typeIDOf(t, KindRelationType, "requires")
	if err := g.meta.RemoveRelationType(t.Context(), g.projectID, id, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	_, err := g.views.RunView(t.Context(), g.projectID, "chain", RunRequest{})
	if !errors.Is(err, ErrQueryStale) || errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("a stale view answers query_stale and nothing else: %v", err)
	}
	if got := codesOf(diagnosticsOf(t, err)); len(got) != 1 ||
		got[0] != DiagRelationTypeMissing {
		t.Fatalf("codes = %v", got)
	}
}

// TestARenamedRelationTypeStillJudgesTheEdgeLabelItDraws is the same rule
// as the projection's scope, at the third position that resolves a type
// key a second time: an edges[] entry written as `from_step` inherits the
// relation types of the step it draws, and Task 9 reads them straight out
// of the catalogue by key so that one reference does not become two
// TypeRefs. By key alone, a rename empties that list — and an empty list
// is an *unresolved* scope, which accepts every label_from without
// looking at a schema. So the check the entry is supposed to get is
// silently switched off by a rename somewhere else in the document.
//
// What makes it observable is a label_from that should now be refused:
// the field it names is dropped from the relation type in the same edit
// that renames it.
func TestARenamedRelationTypeStillJudgesTheEdgeLabelItDraws(t *testing.T) {
	g, _ := newGame(t)
	note := metamodel.Field{Key: "note", Type: metamodel.FieldText}
	if _, err := g.meta.UpsertRelationType(t.Context(), g.projectID, metamodel.RelationTypeInput{
		Key: "requires", Label: "Requires", Schema: metamodel.Schema{note},
		ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("declare the label's field: %v", err)
	}
	g.save(t, "chain", `{"v":1,"from":[{"type":"quest","as":"q",
		  "where":{"field":"@key","op":"eq","value":"defias"}}],
		"traverse":[{"from":"q","via":"requires","to_type":"quest","as":"pre"}],
		"edges":[{"from_step":"pre","label_from":"note"}]}`)

	// One edit that renames the type and drops the field the label reads.
	g.renameRelationType(t, "requires", "depends_on")
	if _, err := g.meta.UpsertRelationType(t.Context(), g.projectID, metamodel.RelationTypeInput{
		Key: "depends_on", Label: "Requires", ExpectedVersion: ptrInt32(2),
	}); err != nil {
		t.Fatalf("drop the field: %v", err)
	}

	_, err := g.views.RunView(t.Context(), g.projectID, "chain", RunRequest{})
	diags := diagnosticsOf(t, err)
	wants(t, diags, Diagnostic{
		Code: DiagFieldMissing, Pointer: "/edges/0/label_from", Was: "note",
	})
	wants(t, diags, Diagnostic{
		Code: DiagRelationTypeRenamed, Pointer: "/traverse/0/via/0",
		Was: "requires", Now: "depends_on",
	})

	// The control: with the field still declared, the same rename leaves
	// the label drawable and the run reports only the rename.
	g2, _ := newGame(t)
	if _, err := g2.meta.UpsertRelationType(t.Context(), g2.projectID, metamodel.RelationTypeInput{
		Key: "requires", Label: "Requires", Schema: metamodel.Schema{note},
		ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("declare the label's field: %v", err)
	}
	g2.save(t, "chain", `{"v":1,"from":[{"type":"quest","as":"q",
		  "where":{"field":"@key","op":"eq","value":"defias"}}],
		"traverse":[{"from":"q","via":"requires","to_type":"quest","as":"pre"}],
		"edges":[{"from_step":"pre","label_from":"note"}]}`)
	g2.renameRelationType(t, "requires", "depends_on")
	res, err := g2.views.RunView(t.Context(), g2.projectID, "chain", RunRequest{})
	if err != nil {
		t.Fatalf("the control must run: %v", err)
	}
	if got := codesOf(res.Stale); len(got) != 1 || got[0] != DiagRelationTypeRenamed {
		t.Fatalf("codes = %v, want the rename alone", got)
	}
}

// TestBestEffortKeepsTheProjectedFieldsItCanStillRead pins what a
// best-effort picture carries under `project.fields` when one of the
// keys has gone.
//
// It is also what showed that pruning the projection is nothing pruneStale
// has to do: resolution already walks past a key it cannot judge, so the
// resolved list handed to the compiler lacks it before anything is
// pruned. This test asserts the outcome — the surviving keys, and only
// those — rather than the mechanism, so it holds whichever pass does the
// work.
func TestBestEffortKeepsTheProjectedFieldsItCanStillRead(t *testing.T) {
	g, _ := newGame(t)
	// hogger is given a difficulty, because the fixture declares that
	// field and no entity holds a value for it — a key pruned correctly
	// and a key never written are the same empty payload, and this test
	// has to be able to tell them apart.
	if _, err := g.meta.UpsertEntity(t.Context(), g.projectID, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
		Fields: map[string]any{"min_level": 22, "rank": "rare",
			"tags": []any{"kill", "elite"}, "difficulty": 4},
		ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("give hogger a difficulty: %v", err)
	}
	// include_invalid, because dropping a declared key from a schema is
	// what marks every entity holding a value for it invalid, and a
	// picture of nothing would answer this test's question by accident.
	g.save(t, "cards", `{"v":1,"include_invalid":true,"from":[{"type":"quest","as":"q"}],
		"project":{"fields":["min_level","rank","difficulty"]}}`)
	if _, err := g.meta.UpsertEntityType(t.Context(), g.projectID, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests", ExpectedVersion: ptrInt32(1),
		Schema: metamodel.Schema{
			{Key: "rank", Type: metamodel.FieldEnum, Options: []string{"common", "rare", "epic"}},
			{Key: "difficulty", Type: metamodel.FieldNumber},
		},
	}); err != nil {
		t.Fatalf("drop min_level: %v", err)
	}

	res, err := g.views.RunView(t.Context(), g.projectID, "cards",
		RunRequest{OnStale: OnStaleBestEffort})
	if err != nil {
		t.Fatalf("best effort: %v", err)
	}
	wants(t, res.Stale, Diagnostic{
		Code: DiagFieldMissing, Pointer: "/project/fields/0", Was: "min_level",
	})
	var hogger *Node
	for i := range res.Nodes {
		if res.Nodes[i].Key == "hogger" {
			hogger = &res.Nodes[i]
		}
	}
	if hogger == nil {
		t.Fatalf("the nodes are not stale and must all be drawn: %+v", res.Nodes)
	}
	if hogger.Fields["rank"] != "rare" || hogger.Fields["difficulty"] != float64(4) {
		t.Fatalf("the two keys that survive must still be carried: %+v", hogger.Fields)
	}
	if _, gone := hogger.Fields["min_level"]; gone {
		t.Fatalf("the dropped key must not come back: %+v", hogger.Fields)
	}
	if len(hogger.Fields) != 2 {
		t.Fatalf("exactly the two keys that resolved: %+v", hogger.Fields)
	}
}

// TestARenamedTypeDoesNotSwitchOffTheProjectionsTypoCheck is what
// actually observes the scope the test above only half reaches.
//
// A projection's scope built by key alone does not *refuse* a renamed
// type's projection — it comes back holding no type at all, which
// projectionScope reads as "unresolved" and answers every key with a
// shrug. So the picture is identical and only the judgement is gone: a
// key the game no longer declares is accepted, and the run draws the
// flat one-colour picture this package refuses everywhere else. The
// difference is visible exactly where a key needs refusing.
//
// difficulty is the field dropped here because the fixture declares it
// and no entity holds a value for it, so removing it from the schema
// flags nothing invalid and this test's subject stays the projection.
func TestARenamedTypeDoesNotSwitchOffTheProjectionsTypoCheck(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "hard", `{"v":1,"from":[{"type":"quest","as":"q"}],
		"project":{"color_by":"difficulty"}}`)
	g.renameEntityType(t, "quest", "mission")
	if _, err := g.meta.UpsertEntityType(t.Context(), g.projectID, metamodel.EntityTypeInput{
		Key: "mission", Label: "Quest", LabelPlural: "Quests", ExpectedVersion: ptrInt32(1),
		Schema: metamodel.Schema{
			{Key: "min_level", Type: metamodel.FieldNumber},
			{Key: "tags", Type: metamodel.FieldListText},
			{Key: "rank", Type: metamodel.FieldEnum, Options: []string{"common", "rare", "epic"}},
		},
	}); err != nil {
		t.Fatalf("drop difficulty from the renamed type: %v", err)
	}

	_, err := g.views.RunView(t.Context(), g.projectID, "hard", RunRequest{})
	diags := diagnosticsOf(t, err)
	wants(t, diags, Diagnostic{
		Code: DiagFieldMissing, Pointer: "/project/color_by", Was: "difficulty",
	})
	wants(t, diags, Diagnostic{
		Code: DiagEntityTypeRenamed, Pointer: "/from/0/type", Was: "quest", Now: "mission",
	})

	// The control: the rename alone leaves the projection drawable, so
	// this test cannot pass by refusing every renamed view.
	g2, _ := newGame(t)
	g2.save(t, "hard", `{"v":1,"from":[{"type":"quest","as":"q"}],
		"project":{"color_by":"difficulty"}}`)
	g2.renameEntityType(t, "quest", "mission")
	res, err := g2.views.RunView(t.Context(), g2.projectID, "hard", RunRequest{})
	if err != nil {
		t.Fatalf("the control must run: %v", err)
	}
	if got := codesOf(res.Stale); len(got) != 1 || got[0] != DiagEntityTypeRenamed {
		t.Fatalf("codes = %v, want the rename alone", got)
	}
}

// TestBestEffortDropsWhatDependedOnWhatItDropped is the other half of
// the pruning rule, and the half nothing else in this file reaches: a
// step reads from a set by name and an edges[] entry draws between sets
// by name, so dropping a set that either of them names leaves a document
// referring to something that is no longer there. What comes back then
// is not a smaller picture — it is `no set named "f" is declared`, a
// query_invalid about a set the designer never removed, from a run they
// explicitly asked to do its best.
//
// Both dependants are in one document on purpose: they fail the same way
// and a fixture holding one of them would leave the other unwatched. The
// second step reads from the first, which is what makes the propagation
// a fixed point rather than one pass: a step reading from a step reading
// from a dropped set survives a single sweep.
func TestBestEffortDropsWhatDependedOnWhatItDropped(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "web", `{"v":1,"from":[
		  {"type":"faction","as":"f"},
		  {"type":"quest","as":"q"}],
		"traverse":[{"from":"f","via":"available_to","to_type":"quest","as":"reach"},
		  {"from":"reach","via":"requires","to_type":"quest","as":"more"}],
		"edges":[{"via":"available_to","between":["q","f"]}],
		"nodes":[{"set":"f"},{"set":"q"},{"set":"reach"},{"set":"more"}]}`)
	id := g.typeIDOf(t, KindEntityType, "faction")
	if err := g.meta.RemoveEntityType(t.Context(), g.projectID, id, true); err != nil {
		t.Fatalf("remove the seed type: %v", err)
	}

	res, err := g.views.RunView(t.Context(), g.projectID, "web",
		RunRequest{OnStale: OnStaleBestEffort})
	if err != nil {
		t.Fatalf("best effort must draw the set that still resolves: %v", err)
	}
	if len(res.Nodes) != 3 {
		t.Fatalf("the quests are what is left: %+v", res.Nodes)
	}
	for _, node := range res.Nodes {
		if node.Set != "q" {
			t.Fatalf("only the surviving set may draw: %+v", res.Nodes)
		}
	}
	if len(res.Edges) != 0 {
		t.Fatalf("the entry drew between a set that is gone: %+v", res.Edges)
	}
	wants(t, res.Stale, Diagnostic{
		Code: DiagEntityTypeMissing, Pointer: "/from/0/type", Was: "faction",
	})
}

// TestARunsOwnBadArgumentsAreNotBlamedOnTheView: a parameter name the
// document does not declare is the caller's mistake, made now, in this
// call — not something the game moved out from under a saved document.
// It answers query_invalid at its own pointer, and a run that reported it
// as staleness would send a designer to repair a view that is fine.
func TestARunsOwnBadArgumentsAreNotBlamedOnTheView(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "levelled", `{"v":1,"params":[{"key":"floor","type":"number","default":20}],
		"from":[{"type":"quest","as":"q",
		  "where":{"field":"min_level","op":"gte","value":{"param":"floor"}}}]}`)

	_, err := g.views.RunView(t.Context(), g.projectID, "levelled",
		RunRequest{Params: map[string]any{"flor": 5}})
	oneProblem(t, err, "/params", `no parameter named "flor"`)
	if errors.Is(err, ErrQueryStale) {
		t.Fatalf("a run's own argument is not a stale view: %v", err)
	}

	// The control: the parameter spelled right runs, and the default it
	// overrides shows the value really was read.
	res, err := g.views.RunView(t.Context(), g.projectID, "levelled",
		RunRequest{Params: map[string]any{"floor": 5}})
	if err != nil || len(res.Nodes) != 3 {
		t.Fatalf("the control must run every quest over level 5: %v, %+v", err, res.Nodes)
	}
}

// TestATypeThatIsGoneIsReportedOnceAndNotAgainByTheFieldsItDeclared is
// the second position where one broken thing could be reported twice.
//
// A step whose to_type has gone reaches entities of no known type, so
// every field its `where` names is undeclared *there* — and answering
// that as field_missing beside the type's own diagnostic would tell a
// designer to repair a field that never moved. fieldScope says which of
// its refusals is staleness and which is a consequence of one already
// reported, and this is the case where the distinction shows.
func TestATypeThatIsGoneIsReportedOnceAndNotAgainByTheFieldsItDeclared(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "narrow", `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"available_to","to_type":"faction","as":"f",
		  "where":{"field":"rank","op":"eq","value":"rare"}}]}`)
	id := g.typeIDOf(t, KindEntityType, "faction")
	if err := g.meta.RemoveEntityType(t.Context(), g.projectID, id, true); err != nil {
		t.Fatalf("remove the destination type: %v", err)
	}

	_, err := g.views.RunView(t.Context(), g.projectID, "narrow", RunRequest{})
	diags := diagnosticsOf(t, err)
	wants(t, diags, Diagnostic{
		Code: DiagEntityTypeMissing, Pointer: "/traverse/0/to_type/0", Was: "faction",
	})
	if len(diags) != 1 {
		t.Fatalf("one thing moved and it is reported once: %+v", diags)
	}
	// The predicate's own refusal is still carried, because it is still
	// true and an agent reading the addressed sentences should see every
	// position that has to change. What it is not is a second thing to
	// repair.
	var qe *QueryError
	if !errors.As(err, &qe) || len(qe.Fields) != 2 {
		t.Fatalf("both positions are addressed: %+v", qe)
	}
}

// TestARefIdIsTrustedOnlyWhenItsKeyIsTheOneTheDocumentSpells is the
// pairing check the id-first road needs to be safe.
//
// `RunView` reads the document and the dependency index in two separate
// pool statements, with no transaction and no version check between them.
// Task 11 writes the pair in *one* transaction, so they are consistent on
// disk; a read does not pair them. An upsert committing between the two
// statements hands a run document version N beside refs version N+1, and
// every ref whose pointer survived the edit then redirects by id to the
// type the *new* document names.
//
// The state that race produces for an instant is exactly the row edit
// below — the same kind of direct edit the rename tests use, for the same
// reason: the product has no way to produce it on purpose. Trusting the
// id there draws the wrong types, returns success, and calls the
// disagreement a rename, which is the wrong-picture-that-looks-right the
// default policy exists to prevent, arriving with a diagnostic that
// misdescribes it.
//
// The check that closes it is one comparison: trust the stored id only
// when the ref's own key is the key the document spells at that pointer.
// After a rename the document and the ref still agree — both hold the old
// spelling, since neither is rewritten — and only the catalogue differs,
// so every rename test above is untouched.
func TestARefIdIsTrustedOnlyWhenItsKeyIsTheOneTheDocumentSpells(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "quests", `{"v":1,"from":[{"type":"quest","as":"q"}]}`)
	zone := g.typeIDOf(t, KindEntityType, "zone")
	tag, err := g.pool.Exec(t.Context(),
		`UPDATE view_refs SET ref_key = 'zone', entity_type_id = $2
		 WHERE project_id = $1 AND pointer = '/from/0/type'`, g.projectID, zone)
	if err != nil {
		t.Fatalf("repoint the ref row: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("repointing changed %d rows", tag.RowsAffected())
	}

	res, err := g.views.RunView(t.Context(), g.projectID, "quests", RunRequest{})
	if err != nil {
		t.Fatalf("the document names a live type and must run: %v", err)
	}
	drawn := make([]string, 0, len(res.Nodes))
	for _, n := range res.Nodes {
		drawn = append(drawn, n.Type+"/"+n.Key)
	}
	if len(res.Nodes) != 3 {
		t.Fatalf("the document says quest and the picture must be the quests, drew %v", drawn)
	}
	for _, n := range res.Nodes {
		if n.Type != "quest" {
			t.Fatalf("drew a %s: the index does not describe this document, so its id "+
				"must not be trusted, %v", n.Type, drawn)
		}
	}
	if len(res.Stale) != 0 {
		t.Fatalf("nothing in this game moved under this view, got %+v", res.Stale)
	}
}

// nestedView draws the prerequisite chain and reads `requires` twice: once
// in the query, once as the containment relation of the renderer that
// draws it. The second position is the one no run judged.
const nestedView = `{"v":1,"from":[{"type":"quest","as":"q"}],
	"traverse":[{"from":"q","via":"requires","to_type":"quest","as":"pre"}],
	"edges":[{"from_step":"pre"}]}`

// saveNested saves a view whose renderer names a relation type.
func (g *game) saveNested(t *testing.T, key, doc, via string, version *int32) {
	t.Helper()
	in := ViewInput{Key: key, Name: "Nest", Query: []byte(doc), ExpectedVersion: version,
		Renderer: RendererNested, RendererParams: map[string]any{"contain_via": via}}
	if _, err := g.views.UpsertView(context.Background(), g.projectID, in); err != nil {
		t.Fatalf("save view %s: %v", key, err)
	}
}

// TestARenamedRelationTypeIsReportedAtTheRendererParameterThatNamesIt is
// the rule carried to the fourth position that turns a key into a
// declared thing — and the one where its absence made the *rename
// diagnostic itself* prescribe a repair the product then refused.
//
// A renderer parameter naming a relation type is a reference exactly as
// `via` is: deleting the type breaks the view, and a rename has to be
// carried by the id. It was resolved only by `CheckRenderer`, which is
// called from the upsert and never from a run, so a rename left the
// document reported and the parameter silent — and re-saving with the
// new spelling, which is what the rename diagnostic says to do, came back
// refused at `/renderer_params/contain_via` with advice that would
// recreate the type the designer had just renamed away from. The view was
// uneditable until they guessed.
//
// The whole loop is asserted here, because the loop is the finding: the
// run names both positions, repairing both is accepted, and the repaired
// view runs with nothing reported.
func TestARenamedRelationTypeIsReportedAtTheRendererParameterThatNamesIt(t *testing.T) {
	g, _ := newGame(t)
	g.saveNested(t, "nest", nestedView, "requires", nil)
	g.renameRelationType(t, "requires", "depends_on")

	res, err := g.views.RunView(t.Context(), g.projectID, "nest", RunRequest{})
	if err != nil {
		t.Fatalf("a rename does not change this picture and must not refuse it: %v", err)
	}
	if len(res.Nodes) != 3 {
		t.Fatalf("the picture the rename did not change, got %d nodes", len(res.Nodes))
	}
	wants(t, res.Stale, Diagnostic{
		Code: DiagRelationTypeRenamed, Pointer: "/traverse/0/via/0",
		Was: "requires", Now: "depends_on",
	})
	wants(t, res.Stale, Diagnostic{
		Code: DiagRelationTypeRenamed, Pointer: "/renderer_params/contain_via",
		Was: "requires", Now: "depends_on",
	})

	// The repair the diagnostics prescribe, at both pointers they name.
	repaired := `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"depends_on","to_type":"quest","as":"pre"}],
		"edges":[{"from_step":"pre"}]}`
	one := int32(1)
	g.saveNested(t, "nest", repaired, "depends_on", &one)

	res, err = g.views.RunView(t.Context(), g.projectID, "nest", RunRequest{})
	if err != nil {
		t.Fatalf("the repaired view must run: %v", err)
	}
	if len(res.Stale) != 0 {
		t.Fatalf("a repaired view reports nothing, got %+v", res.Stale)
	}
}

// TestARendererParameterNamingADeletedRelationTypeIsReported is the other
// half: the parameter's reference dies with the type, and the run says so
// at the parameter rather than drawing the flat row of boxes that a
// containment renderer with no containment relation draws.
func TestARendererParameterNamingADeletedRelationTypeIsReported(t *testing.T) {
	g, _ := newGame(t)
	g.saveNested(t, "nest", nestedView, "requires", nil)
	id := g.typeIDOf(t, KindRelationType, "requires")
	if err := g.meta.RemoveRelationType(t.Context(), g.projectID, id, true); err != nil {
		t.Fatalf("remove relation type: %v", err)
	}

	_, err := g.views.RunView(t.Context(), g.projectID, "nest", RunRequest{})
	wants(t, diagnosticsOf(t, err), Diagnostic{
		Code: DiagRelationTypeMissing, Pointer: "/renderer_params/contain_via", Was: "requires",
	})
}

// TestARendererParameterIsARecordedReference is what makes the rename
// above resolve at all: without a `view_refs` row at the parameter's own
// pointer there is no id to resolve by, so a run would look the renamed
// type up by key, find nothing, and report a type *missing* that the
// rename left standing — refusing a view whose picture had not changed.
//
// It is asserted on the stored index rather than only through the run,
// because the run is green either way for one round: a by-key lookup that
// happens to find the type says nothing is wrong, and the difference
// shows only once something has moved.
//
// **The deletion report gains nothing measurable from this row**, and
// that is worth saying rather than claiming otherwise: `contain_via` is
// the only type-naming renderer parameter, and the nested renderer
// structurally requires the query to draw an edges[] entry of that same
// relation type — so a view naming it in the parameter already named it
// in the query, and already appeared in the report. The row is here for
// the id, not for the list.
func TestARendererParameterIsARecordedReference(t *testing.T) {
	g, _ := newGame(t)
	g.saveNested(t, "nest", nestedView, "requires", nil)

	view, err := g.views.ViewByKey(t.Context(), g.projectID, "nest")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	refs, err := g.views.ViewRefs(t.Context(), g.projectID, view.ID)
	if err != nil {
		t.Fatalf("read refs: %v", err)
	}
	for _, ref := range refs {
		if ref.Pointer != "/renderer_params/contain_via" {
			continue
		}
		if ref.Kind != KindRelationType || ref.RefKey != "requires" ||
			ref.RelationTypeID == nil {
			t.Fatalf("the parameter's reference must carry its kind, its key and its "+
				"id: %+v", ref)
		}
		return
	}
	t.Fatalf("no reference recorded at the renderer parameter, got %+v", refs)
}

// TestARendererParameterReadingADroppedFieldIsReported is the field half
// of the same position: a parameter that names a declared field key is a
// second lookup exactly as one that names a type, and a schema narrowing
// under a saved view left it unjudged too.
func TestARendererParameterReadingADroppedFieldIsReported(t *testing.T) {
	g, _ := newGame(t)
	in := ViewInput{Key: "levels", Name: "Levels",
		Query: []byte(`{"v":1,"from":[{"type":"quest","as":"q"}],
			"project":{"fields":["min_level"]}}`),
		Renderer:       RendererTimeline,
		RendererParams: map[string]any{"axis_field": "min_level"},
	}
	if _, err := g.views.UpsertView(t.Context(), g.projectID, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	// The narrowing: quest is redeclared without min_level.
	if _, err := g.meta.UpsertEntityType(t.Context(), g.projectID, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests", ExpectedVersion: ptrInt32(1),
		Schema: metamodel.Schema{
			{Key: "tags", Type: metamodel.FieldListText},
			{Key: "rank", Type: metamodel.FieldEnum,
				Options: []string{"common", "rare", "epic"}},
		},
	}); err != nil {
		t.Fatalf("drop min_level: %v", err)
	}

	_, err := g.views.RunView(t.Context(), g.projectID, "levels", RunRequest{})
	wants(t, diagnosticsOf(t, err), Diagnostic{
		Code: DiagFieldMissing, Pointer: "/renderer_params/axis_field", Was: "min_level",
	})
}

// TestANegatedTypeComparisonIsADependencyLikeAnyOther pins the one place
// the `@type` dependency costs more than it buys, so the asymmetry stays
// a decision rather than becoming a surprise.
//
// A deleted type cannot change what `@type neq` selects: nothing is of a
// type this game no longer has, so the view below would draw exactly what
// it drew. It is still reported broken and still refused, for the three
// reasons written at typeOperand — chiefly that the no-op is a property
// of today's rows and not of the document, and re-declaring the key
// (which resolution's step 2 exists for) makes the negation narrow again
// with nothing said.
func TestANegatedTypeComparisonIsADependencyLikeAnyOther(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "notclasses", `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":["available_to","takes_place_in"],"as":"t",
		  "where":{"field":"@type","op":"neq","value":"class"}}]}`)
	res, err := g.views.RunView(t.Context(), g.projectID, "notclasses", RunRequest{})
	if err != nil || len(res.Nodes) != 5 {
		t.Fatalf("the control: three quests and the two zones, got %d nodes, %v",
			len(res.Nodes), err)
	}

	id := g.typeIDOf(t, KindEntityType, "class")
	broke, err := g.views.RemoveTypeReportingViews(t.Context(), g.projectID,
		KindEntityType, id, true)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(broke) != 1 || broke[0].ViewKey != "notclasses" {
		t.Fatalf("a view naming the type in a negation names it, got %+v", broke)
	}

	_, err = g.views.RunView(t.Context(), g.projectID, "notclasses", RunRequest{})
	wants(t, diagnosticsOf(t, err), Diagnostic{
		Code: DiagEntityTypeMissing, Pointer: "/traverse/0/where/value", Was: "class",
	})
}

// TestAnAdHocRunAcceptsTheFailPolicyItAlreadyImplements is the other side
// of the refusal above. `fail` is what an ad-hoc run does — an
// unresolvable document is refused — so a caller that says so is stating
// a true fact, and a REST layer or a UI that fills the field in on every
// request from a form whose default is the default would be refused for
// having a default.
func TestAnAdHocRunAcceptsTheFailPolicyItAlreadyImplements(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, questsOnly), OnStale: OnStaleFail})
	if err != nil || len(res.Nodes) != 3 {
		t.Fatalf("fail is what this run already does: %v, %d nodes", err, len(res.Nodes))
	}
	if res.Stale != nil {
		t.Fatalf("an ad-hoc run still has no staleness report: %+v", res.Stale)
	}

	// And a value that is neither is still refused, rather than read as
	// one of the two.
	_, err = g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, questsOnly), OnStale: "whatever"})
	oneProblem(t, err, "/on_stale", "an inline query cannot be stale")
}

// TestBestEffortRefusesAProblemPruningCannotActOn is the guard against
// the silent widening by the one road nothing watched: a broken pointer
// outside the three document positions the pruner knows how to drop.
//
// It prunes nothing, so the document runs whole with a resolution problem
// standing against it — a picture drawn as if the problem were not there,
// with a warning beside it saying that it is. A renderer parameter naming
// a deleted relation type is that pointer, which is what makes this a
// live arm rather than defence in depth: the containment renderer without
// its containment relation draws one flat row of boxes, and drawing that
// under a warning is exactly the wrong-picture-that-looks-right.
func TestBestEffortRefusesAProblemPruningCannotActOn(t *testing.T) {
	g, _ := newGame(t)
	g.saveNested(t, "nest", nestedView, "requires", nil)
	id := g.typeIDOf(t, KindRelationType, "requires")
	if err := g.meta.RemoveRelationType(t.Context(), g.projectID, id, true); err != nil {
		t.Fatalf("remove relation type: %v", err)
	}

	_, err := g.views.RunView(t.Context(), g.projectID, "nest",
		RunRequest{OnStale: OnStaleBestEffort})
	wants(t, diagnosticsOf(t, err), Diagnostic{
		Code: DiagRelationTypeMissing, Pointer: "/renderer_params/contain_via", Was: "requires",
	})
}

// TestBestEffortCanStillAnswerWithAnEmptyPictureItDidNotPrune records
// what the "an empty picture reads as this game has nothing" guard does
// *not* cover, because the sentence invites the wrong reading.
//
// The guard is about the query — every seed set pruned away — and not
// about the result. Emptiness also arrives by a road pruning cannot see:
// narrowing a schema invalidates the rows that held a value for what it
// dropped, so a seed set that survives whole comes back with no rows at
// all. Refusing empty results is not the fix; a legitimately empty query
// is a thing a designer asks for. This pins the measurement rather than a
// wish: zero nodes, no error, and the warning attached.
func TestBestEffortCanStillAnswerWithAnEmptyPictureItDidNotPrune(t *testing.T) {
	g, _ := newGame(t)
	// Two selectors, so nothing here is the "every seed set is gone"
	// case: quests filtered on an enum option, plus the zones. The
	// projection is what breaks, at /project, which prunes nothing.
	g.save(t, "epics", `{"v":1,"from":[
		  {"type":"quest","as":"epics","where":{"field":"rank","op":"eq","value":"epic"}}],
		"project":{"color_by":"difficulty"}}`)
	// quest keeps rank exactly as the filter needs it, and loses
	// min_level and difficulty. Every quest holds a min_level, so every
	// quest is now an invalid row: the seed set survives whole, its
	// condition still resolves, and it selects nothing. What breaks is
	// the projection, at /project/color_by, which prunes nothing.
	if _, err := g.meta.UpsertEntityType(t.Context(), g.projectID, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests", ExpectedVersion: ptrInt32(1),
		Schema: metamodel.Schema{
			{Key: "tags", Type: metamodel.FieldListText},
			{Key: "rank", Type: metamodel.FieldEnum,
				Options: []string{"common", "rare", "epic"}},
		},
	}); err != nil {
		t.Fatalf("narrow quest: %v", err)
	}

	res, err := g.views.RunView(t.Context(), g.projectID, "epics",
		RunRequest{OnStale: OnStaleBestEffort})
	if err != nil {
		t.Fatalf("best effort has a seed set to draw and must not refuse: %v", err)
	}
	if len(res.Nodes) != 0 {
		t.Fatalf("the rows the narrowing invalidated are gone, got %d nodes", len(res.Nodes))
	}
	if len(res.Stale) == 0 {
		t.Fatalf("the empty picture must at least arrive with its warning")
	}
}

// TestTheDependencyListIsOneRowPerReferenceAndNotPerView pins the shape
// of the deletion report, which nothing asserted and which a caller can
// read exactly one way too many.
//
// It is one row per *reference*, so a view naming the type at three
// positions comes back three times. That is intentional and it is what
// the pointers are for — a designer told to repair a view wants the three
// positions, not the view's name three times — but it means a caller
// counting rows and reporting "three views broken" is wrong, and nothing
// said so. Now something does.
func TestTheDependencyListIsOneRowPerReferenceAndNotPerView(t *testing.T) {
	g, _ := newGame(t)
	g.save(t, "thrice", `{"v":1,"from":[
		  {"type":"quest","as":"a","where":{"field":"@type","op":"eq","value":"quest"}}],
		"traverse":[{"from":"a","via":"requires","to_type":"quest","as":"pre"}]}`)

	id := g.typeIDOf(t, KindEntityType, "quest")
	deps, err := g.views.ViewsDependingOn(t.Context(), g.projectID, KindEntityType, id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	at := map[string]bool{}
	for _, dep := range deps {
		if dep.ViewKey != "thrice" {
			t.Fatalf("another view came back: %+v", deps)
		}
		at[dep.Pointer] = true
	}
	for _, want := range []string{"/from/0/type", "/from/0/where/value",
		"/traverse/0/to_type/0"} {
		if !at[want] {
			t.Fatalf("no row at %s: %+v", want, deps)
		}
	}
	if len(deps) != 3 {
		t.Fatalf("one row per reference, and this view holds three: %+v", deps)
	}
}
