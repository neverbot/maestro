package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/web"
)

// seedRepairable declares a quest type with one text field and three
// quests, then narrows the schema so that all three are flagged. It is
// the state a designer is in when they reach for a repair.
func seedRepairable(t *testing.T, svc *metamodel.Service, game uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	typ, err := svc.UpsertEntityType(ctx, game, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "summary", Type: metamodel.FieldText}},
	})
	if err != nil {
		t.Fatalf("declare type: %v", err)
	}
	items := make([]metamodel.EntityInput, 0, 3)
	for i := range 3 {
		items = append(items, metamodel.EntityInput{
			TypeKey: "quest", Key: fmt.Sprintf("q%d", i), Name: fmt.Sprintf("Quest %d", i),
			Fields: map[string]any{"summary": "Defeat the gnoll chieftain."},
		})
	}
	if _, err := svc.UpsertEntities(ctx, game, items, metamodel.BulkAtomic); err != nil {
		t.Fatalf("seed entities: %v", err)
	}
	version := typ.Version
	if _, err := svc.UpsertEntityType(ctx, game, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{
			{Key: "summary", Type: metamodel.FieldText},
			{Key: "laps", Type: metamodel.FieldNumber, Required: true},
		},
		ExpectedVersion: &version,
	}); err != nil {
		t.Fatalf("narrow the schema: %v", err)
	}
}

// requireDomainFieldError asserts a refusal is the domain's own
// invalid_input at one path with one message.
//
// It is not requireMCPFieldError: a tool core returns the domain error
// unwrapped and mcpErrorFor (the transport layer) is what turns it into
// an *MCPError, so a direct call sees the *metamodel.ValidationError.
// The REST half of the same refusal, where the mapping to a status and
// a wire code *is* exercised, is in TestTheRESTMirrorRepairsToo below;
// this pins the path and the wording a caller acts on.
func requireDomainFieldError(t *testing.T, err error, wantPath, wantMessage string) {
	t.Helper()
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %#v, want a *metamodel.ValidationError", err)
	}
	if len(invalid.Fields) != 1 {
		t.Fatalf("got %d problems, want 1: %v", len(invalid.Fields), invalid.Fields)
	}
	if invalid.Fields[0].Path != wantPath || invalid.Fields[0].Message != wantMessage {
		t.Fatalf("problem = %+v, want %q: %q", invalid.Fields[0], wantPath, wantMessage)
	}
}

// TestARepairPassAnswersOverTheToolSurface reads the domain's repair back
// through the tool an agent actually calls, including the two shapes a
// direct-function test of the domain cannot see: the empty slices on the
// wire, and the refusals arriving as MCP errors.
func TestARepairPassAnswersOverTheToolSurface(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()
	seedRepairable(t, f.deps.Metamodel, f.game)

	out, err := web.MCPEntitiesRepair(ctx, f.deps, f.caller, f.game, web.EntitiesRepairInput{
		TypeKey: "quest", Set: map[string]any{"laps": float64(10)},
	})
	if err != nil {
		t.Fatalf("entities.repair: %v", err)
	}
	if out.Scanned != 3 || len(out.Repaired) != 3 || len(out.Failed) != 0 {
		t.Fatalf("the pass scanned %d, repaired %d, failed %+v",
			out.Scanned, len(out.Repaired), out.Failed)
	}
	row, err := web.MCPEntitiesGet(ctx, f.deps, f.caller, f.game,
		web.EntitiesGetInput{TypeKey: "quest", Key: "q0"})
	if err != nil {
		t.Fatalf("entities.get: %v", err)
	}
	if row.Invalid || row.Fields["laps"] != float64(10) ||
		row.Fields["summary"] != "Defeat the gnoll chieftain." {
		t.Fatalf("repaired row = %+v", row)
	}

	// A second pass finds nothing, and says so with arrays rather than
	// nulls: "the pass repaired nothing" and "the pass reported nothing"
	// must not look the same to a client walking either list.
	again, err := web.MCPEntitiesRepair(ctx, f.deps, f.caller, f.game, web.EntitiesRepairInput{
		TypeKey: "quest", Set: map[string]any{"laps": float64(10)},
	})
	if err != nil {
		t.Fatalf("entities.repair: %v", err)
	}
	if again.Scanned != 0 || len(again.Repaired) != 0 {
		t.Fatalf("a second pass over a repaired type did work: %+v", again)
	}
	raw, err := json.Marshal(again)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"repaired":[]`) ||
		!strings.Contains(string(raw), `"failed":[]`) {
		t.Fatalf("an empty repair answer marshalled as %s", raw)
	}

	// The two refusals, as the wire sees them.
	if _, err := web.MCPEntitiesRepair(ctx, f.deps, f.caller, f.game,
		web.EntitiesRepairInput{TypeKey: "quest"}); err == nil {
		t.Fatalf("a repair stating no operation was accepted")
	} else {
		requireDomainFieldError(t, err, "set",
			"a repair must state what to change: give set, drop_unknown, or both. "+
				"A pass with neither would rewrite every flagged row with the values it "+
				"already holds")
	}
	if _, err := web.MCPEntitiesRepair(ctx, f.deps, f.caller, f.game, web.EntitiesRepairInput{
		TypeKey: "quest", Set: map[string]any{"lap": float64(1)},
	}); err == nil {
		t.Fatalf("a set key the type does not declare was accepted")
	} else {
		requireDomainFieldError(t, err, "set.lap",
			"no such field on this type; a repair writes declared values only")
	}
}

// TestTheRESTMirrorRepairsToo is the other public surface. The two are
// one core each (entitiesRepair, relationsRepair), and a mirror that was
// never called is a mirror that compiles.
func TestTheRESTMirrorRepairsToo(t *testing.T) {
	f := newRESTFixture(t)
	seedRepairable(t, f.mm, f.game)

	rec := f.as(t, http.MethodPost, "/entities/repair", map[string]any{
		"type_key": "quest",
		"set":      map[string]any{"laps": float64(10)},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("repair = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Scanned  int `json:"scanned"`
		Repaired []struct {
			Key string `json:"key"`
		} `json:"repaired"`
		Failed []json.RawMessage `json:"failed"`
	}
	decodeBody(t, rec, &out)
	if out.Scanned != 3 || len(out.Repaired) != 3 || len(out.Failed) != 0 {
		t.Fatalf("REST repair = %s", rec.Body.String())
	}

	// A pass stating no operation is a status, not a report: it is a
	// refusal of the call rather than of some of its rows.
	refused := f.as(t, http.MethodPost, "/entities/repair", map[string]any{"type_key": "quest"})
	if refused.Code != http.StatusBadRequest ||
		!strings.Contains(refused.Body.String(), "invalid_input") {
		t.Fatalf("an empty repair over REST = %d %s", refused.Code, refused.Body.String())
	}

	// And the edge route exists, which is the half a mirror most easily
	// forgets: relation types carry a field schema and an invalid flag
	// exactly as entity types do.
	edges := f.as(t, http.MethodPost, "/relations/repair", map[string]any{
		"type_key": "quest", "drop_unknown": true,
	})
	if edges.Code != http.StatusNotFound {
		t.Fatalf("repairing an undeclared relation type = %d %s", edges.Code, edges.Body.String())
	}
}

// TestTheRepairWireTypesCarryExactlyTheseKeys is the search suite's
// standing check, applied to the four shapes this change put on the
// wire. A wire type that grows a field silently is a decision nobody
// made.
func TestTheRepairWireTypesCarryExactlyTheseKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  []string
	}{
		{"web.EntitiesRepairInput", web.EntitiesRepairInput{
			TypeKey: "quest", Set: map[string]any{"a": 1}, DropUnknown: true, Limit: 1,
		}, []string{"drop_unknown", "limit", "set", "type_key"}},
		{"web.RelationsRepairInput", web.RelationsRepairInput{
			TypeKey: "takes_place_in", Set: map[string]any{"a": 1}, DropUnknown: true, Limit: 1,
		}, []string{"drop_unknown", "limit", "set", "type_key"}},
		{"web.EntitiesRepairOutput", web.EntitiesRepairOutput{},
			[]string{"failed", "repaired", "scanned"}},
		{"web.RelationsRepairOutput", web.RelationsRepairOutput{},
			[]string{"failed", "repaired", "scanned"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var keyed map[string]json.RawMessage
			if err := json.Unmarshal(raw, &keyed); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			got := make([]string, 0, len(keyed))
			for k := range keyed {
				got = append(got, k)
			}
			slices.Sort(got)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("wire keys = %v, want %v — a repair wire type grew a field; decide "+
					"whether an agent should see it, then update this list and the "+
					"hand-written output schema beside it", got, tc.want)
			}
		})
	}
}

// TestAnOverLargeBatchIsRefusedOnBothSurfaces carries the batch bound to
// the wire, which is where Task 9 measured its absence: a 5,000-item
// atomic batch was accepted over MCP, held one transaction open for 3.1
// seconds and answered with 515 KB. The domain test pins the rule for
// all three kinds; this pins that a caller reaching it through a tool or
// a URL is told the same thing, as its own argument and not as a slow
// success.
func TestAnOverLargeBatchIsRefusedOnBothSurfaces(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	if _, err := web.MCPTypesUpsert(ctx, f.deps, f.caller, f.game, web.TypesUpsertInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("types.upsert: %v", err)
	}
	items := make([]web.EntityItemInput, 0, metamodel.MaxBulkItems+1)
	for i := range metamodel.MaxBulkItems + 1 {
		items = append(items, web.EntityItemInput{
			TypeKey: "quest", Key: fmt.Sprintf("q%04d", i), Name: "Quest",
		})
	}
	wantMessage := fmt.Sprintf("a batch carries at most %d items; this one carries %d — split it",
		metamodel.MaxBulkItems, metamodel.MaxBulkItems+1)

	_, err := web.MCPEntitiesUpsert(ctx, f.deps, f.caller, f.game, web.EntitiesUpsertInput{
		Mode: string(metamodel.BulkAtomic), Items: items,
	})
	requireDomainFieldError(t, err, "items", wantMessage)

	// Nothing landed. A bound that refused the report but ran the batch
	// would leave this green.
	page, err := web.MCPEntitiesList(ctx, f.deps, f.caller, f.game,
		web.EntitiesListInput{TypeKey: "quest", Limit: metamodel.MaxEntityPage})
	if err != nil {
		t.Fatalf("entities.list: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("an over-large batch landed %d rows", len(page.Items))
	}

	// And the bound is stated in the tool descriptions, over the real
	// transport, which is what keeps a caller from discovering it by
	// tripping over it. A bound a caller cannot read is a bound a caller
	// trips over — the rule MaxSearchQuery established and the reason
	// MaxBulkItems is exported at all.
	httpSrv := httptest.NewServer(f.srv)
	defer httpSrv.Close()
	session := connectMCP(t, httpSrv.URL, f.token)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	stated := map[string]bool{}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Description, fmt.Sprint(metamodel.MaxBulkItems)) {
			stated[tool.Name] = true
		}
	}
	for _, name := range []string{"entities.upsert", "relations.upsert", "docs.write_many"} {
		if !stated[name] {
			t.Fatalf("%s does not state the %d-item batch ceiling in its description",
				name, metamodel.MaxBulkItems)
		}
	}
}
