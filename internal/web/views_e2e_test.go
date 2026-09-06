package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/views"
	"github.com/neverbot/maestro/internal/web"
)

// This file is the views sub-project's definition of done, and it is a
// different kind of test from the ones beside it: those prove one call
// behaves, this one is the whole story an agent and a designer live
// through — seed a game, get the query wrong twice, save a view, run it,
// drag four nodes, add twelve more quests, watch the vocabulary move
// under the saved document, repair it, and finally delete a type the view
// depends on.
//
// **It is driven over the tool surface and not over the domain**, because
// what it exists to answer is "does the product work", and a step that
// reached past the transport would be answering a different question. The
// one deliberate exception is named where it is made, in step 8.
//
// **The negative halves are the point.** A walk that only asserts things
// arrive proves almost nothing: the fixture is shaped so that at every
// step something must *not* come back, and each of those absences is a
// different rule of the language being enforced.
//
//   - a level-12 quest is outside the band (the step's own where),
//   - a level-35 quest is outside it on the other side,
//   - a level-26 quest is inside the band and available to the *warrior*
//     (the traversal itself, which a band filter alone cannot test),
//   - a requires edge to a quest outside the band is not drawn
//     (edges[].between, which is the reason the spec's example has one),
//   - the seed class node comes back with no position rather than at the
//     origin,
//   - and twelve quests added afterwards arrive unpinned.
//
// **Nine review rounds of this sub-project's own history say the same
// thing about fixtures**: one too small to distinguish two policies is
// a test that passes for the wrong reason. Two zones, so `color_by` is
// not a constant; four dragged nodes and a fifth undragged one, so
// "positions came back" cannot pass by returning them all.

// The nine quests, their bands and their zones. Written as a table
// because every assertion below is about which of them came back.
type e2eQuest struct {
	key   string
	name  string
	level float64
	zone  string
	class string // the class available_to points at
}

// e2eQuests is the seeded content. Three level bands (below 20, 20–30,
// above 30), two zones, and two classes — the level-26 quest belongs to
// the warrior, which is what makes the traversal load-bearing rather
// than decorative: it satisfies the band and must still not be drawn.
var e2eQuests = []e2eQuest{
	{"cooking-for-bruises", "Cooking for Bruises", 12, "elwynn", "mage"},
	{"kobold-candles", "Kobold Candles", 15, "elwynn", "mage"},

	{"wanted-hogger", "Wanted: Hogger", 22, "elwynn", "mage"},
	{"the-defias-brotherhood", "The Defias Brotherhood", 25, "westfall", "mage"},
	{"charge-of-the-mounted", "Charge of the Mounted", 26, "elwynn", "warrior"},
	{"jangolode-mine", "The Jangolode Mine", 28, "elwynn", "mage"},
	{"westfall-stew", "Westfall Stew", 30, "westfall", "mage"},

	{"the-deadmines", "The Deadmines", 35, "westfall", "mage"},
	{"the-stockade", "The Stockade", 40, "elwynn", "mage"},
}

// e2eReachable is what the saved view must draw: the four mage quests
// between 20 and 30 inclusive. The upper bound is 30 and westfall-stew is
// exactly 30, so this list also pins that `lte` includes its bound.
var e2eReachable = []string{
	"wanted-hogger", "the-defias-brotherhood", "jangolode-mine", "westfall-stew",
}

// e2eZoneOf is the colour each reachable quest must come back with. Two
// distinct values, deliberately: a fixture whose quests all sit in one
// zone cannot tell a working one-hop colour from a hard-wired string.
var e2eZoneOf = map[string]string{
	"wanted-hogger":          "Elwynn Forest",
	"the-defias-brotherhood": "Westfall",
	"jangolode-mine":         "Elwynn Forest",
	"westfall-stew":          "Westfall",
}

// e2eQuery is the spec's own worked example, and the whole point of this
// test is that it is composed once and saved once. `class_key` is a
// parameter with a default so the view is reusable per class; the
// traverse step walks available_to *inwards* from the class to the
// quests; the second edges[] entry draws prerequisite arrows within the
// band without dragging the quests outside it into the picture.
const e2eQuery = `{
  "v": 1,
  "params": [{"key": "class_key", "type": "text", "default": "mage"}],
  "from": [
    {"type": "class", "as": "cls",
     "where": {"field": "@key", "op": "eq", "value": {"param": "class_key"}}}
  ],
  "traverse": [
    {"from": "cls", "via": "available_to", "direction": "in", "to_type": "quest",
     "depth": 1, "as": "reachable",
     "where": {"all": [
       {"field": "min_level", "op": "gte", "value": 20},
       {"field": "min_level", "op": "lte", "value": 30}
     ]}}
  ],
  "nodes": [{"set": "cls", "role": "seed"}, {"set": "reachable"}],
  "edges": [
    {"from_step": "reachable"},
    {"via": "requires", "between": ["reachable", "reachable"], "direction": "out"}
  ],
  "project": {
    "label": "@name",
    "color_by": {"related": {"via": "takes_place_in", "direction": "out",
                             "type": "zone", "attr": "@name"}},
    "fields": ["min_level"]
  },
  "limits": {"max_nodes": 500}
}`

// e2eRepairedQuery is e2eQuery with the one key the rename moved. It is
// spelled out in full rather than produced by a string replacement,
// because a repair an agent makes is a document it writes, and asserting
// against a document this test derived would be asserting against the
// derivation.
const e2eRepairedQuery = `{
  "v": 1,
  "params": [{"key": "class_key", "type": "text", "default": "mage"}],
  "from": [
    {"type": "class", "as": "cls",
     "where": {"field": "@key", "op": "eq", "value": {"param": "class_key"}}}
  ],
  "traverse": [
    {"from": "cls", "via": "usable_by", "direction": "in", "to_type": "quest",
     "depth": 1, "as": "reachable",
     "where": {"all": [
       {"field": "min_level", "op": "gte", "value": 20},
       {"field": "min_level", "op": "lte", "value": 30}
     ]}}
  ],
  "nodes": [{"set": "cls", "role": "seed"}, {"set": "reachable"}],
  "edges": [
    {"from_step": "reachable"},
    {"via": "requires", "between": ["reachable", "reachable"], "direction": "out"}
  ],
  "project": {
    "label": "@name",
    "color_by": {"related": {"via": "takes_place_in", "direction": "out",
                             "type": "zone", "attr": "@name"}},
    "fields": ["min_level"]
  },
  "limits": {"max_nodes": 500}
}`

// e2eWorld is one game, the agent's token, and a second game whose token
// is used for nothing but step 12.
type e2eWorld struct {
	srv    *web.Server
	pool   *pgxpool.Pool
	deps   web.MCPDeps
	agent  web.Caller
	guest  web.Caller
	secret string         // the agent's token, for the over-the-wire steps
	vs     *views.Service // for the one thing no tool does: uploading an image
	game   uuid.UUID
	other  uuid.UUID
	typeID map[string]uuid.UUID // entity type key -> id
	entity map[string]uuid.UUID // "type/key" -> id
}

func newE2EWorld(t *testing.T) *e2eWorld {
	t.Helper()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	cfg := config.Config{
		SessionTTL: testConfig().SessionTTL,
		InviteTTL:  testConfig().InviteTTL,
		Argon2:     testConfig().Argon2,
	}
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	mm := metamodel.New(pool, nil)
	vs := views.New(pool, nil)
	srv := web.NewServer(web.Options{
		Version: "test", Config: cfg, Identity: ids, Projects: projSvc,
		Metamodel: mm, Views: vs,
	})
	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create game: %v", err)
	}
	other, err := projSvc.Create(ctx, "le-mans", "Le Mans", owner.ID)
	if err != nil {
		t.Fatalf("Create the second game: %v", err)
	}
	var agentSecret string
	mint := func(project uuid.UUID, label string) web.Caller {
		t.Helper()
		secret, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
			ProjectID: project, UserID: owner.ID, Label: label,
		})
		if err != nil {
			t.Fatalf("CreateAPIToken %s: %v", label, err)
		}
		caller, err := web.CallerForToken(ctx, ids, secret)
		if err != nil {
			t.Fatalf("CallerForToken %s: %v", label, err)
		}
		if agentSecret == "" {
			agentSecret = secret
		}
		return caller
	}
	w := &e2eWorld{
		srv:   srv,
		pool:  pool,
		deps:  web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: mm, Views: vs},
		agent: mint(game.ID, "content agent"),
		guest: mint(other.ID, "le mans agent"),
		game:  game.ID, other: other.ID,
		typeID: map[string]uuid.UUID{},
		entity: map[string]uuid.UUID{},
		vs:     vs,
	}
	w.secret = agentSecret
	w.seed(t)
	return w
}

// seed is step 1, and it goes through the same tools an agent would call:
// three entity types, three relation types, two classes, two zones and
// the nine quests, then the edges between them.
func (w *e2eWorld) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	for _, spec := range []web.TypesUpsertInput{
		{Key: "class", Label: "Class", LabelPlural: "Classes"},
		{Key: "quest", Label: "Quest", LabelPlural: "Quests", Schema: []web.FieldInput{
			{Key: "min_level", Type: "number", Min: ptrFloat(1), Max: ptrFloat(60)},
		}},
		{Key: "zone", Label: "Zone", LabelPlural: "Zones"},
	} {
		out, err := web.MCPTypesUpsert(ctx, w.deps, w.agent, w.game, spec)
		if err != nil {
			t.Fatalf("types.upsert %s: %v", spec.Key, err)
		}
		w.typeID[spec.Key] = out.ID
	}

	for _, spec := range []web.RelationTypesUpsertInput{
		{Key: "available_to", Label: "Available to",
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"class"}},
		{Key: "requires", Label: "Requires",
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"quest"}},
		{Key: "takes_place_in", Label: "Takes place in",
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"zone"}},
	} {
		if _, err := web.MCPRelationTypesUpsert(ctx, w.deps, w.agent, w.game, spec); err != nil {
			t.Fatalf("relation_types.upsert %s: %v", spec.Key, err)
		}
	}

	items := []web.EntityItemInput{
		{TypeKey: "class", Key: "mage", Name: "Mage"},
		{TypeKey: "class", Key: "warrior", Name: "Warrior"},
		{TypeKey: "zone", Key: "elwynn", Name: "Elwynn Forest"},
		{TypeKey: "zone", Key: "westfall", Name: "Westfall"},
	}
	for _, q := range e2eQuests {
		items = append(items, web.EntityItemInput{
			TypeKey: "quest", Key: q.key, Name: q.name,
			Fields: map[string]any{"min_level": q.level},
		})
	}
	w.upsertEntities(t, items)

	edges := make([]web.RelationItemInput, 0, len(e2eQuests)*2+2)
	for _, q := range e2eQuests {
		edges = append(edges,
			web.RelationItemInput{TypeKey: "available_to",
				Source: web.RefInput{TypeKey: "quest", Key: q.key},
				Target: web.RefInput{TypeKey: "class", Key: q.class}},
			web.RelationItemInput{TypeKey: "takes_place_in",
				Source: web.RefInput{TypeKey: "quest", Key: q.key},
				Target: web.RefInput{TypeKey: "zone", Key: q.zone}})
	}
	// Two prerequisites, and the second is a control: defias requires
	// hogger, which is inside the band and must be drawn; jangolode
	// requires kobold-candles, which is at level 15 and must not be,
	// because edges[].between draws only between the reachable set.
	edges = append(edges,
		web.RelationItemInput{TypeKey: "requires",
			Source: web.RefInput{TypeKey: "quest", Key: "the-defias-brotherhood"},
			Target: web.RefInput{TypeKey: "quest", Key: "wanted-hogger"}},
		web.RelationItemInput{TypeKey: "requires",
			Source: web.RefInput{TypeKey: "quest", Key: "jangolode-mine"},
			Target: web.RefInput{TypeKey: "quest", Key: "kobold-candles"}})

	out, err := web.MCPRelationsUpsert(ctx, w.deps, w.agent, w.game,
		web.RelationsUpsertInput{Items: edges})
	if err != nil {
		t.Fatalf("relations.upsert: %v", err)
	}
	if out.Count != len(edges) || len(out.Failed) != 0 {
		t.Fatalf("seeded %d of %d edges, failed %v", out.Count, len(edges), out.Failed)
	}
}

// upsertEntities writes a batch and records every id, so the deletions in
// step 11 can address entities the way entities.remove does.
func (w *e2eWorld) upsertEntities(t *testing.T, items []web.EntityItemInput) {
	t.Helper()
	out, err := web.MCPEntitiesUpsert(context.Background(), w.deps, w.agent, w.game,
		web.EntitiesUpsertInput{Items: items})
	if err != nil {
		t.Fatalf("entities.upsert: %v", err)
	}
	if out.Count != len(items) || len(out.Failed) != 0 {
		t.Fatalf("wrote %d of %d entities, failed %v", out.Count, len(items), out.Failed)
	}
	for _, written := range out.Written {
		w.entity[written.TypeKey+"/"+written.Key] = written.ID
	}
}

func (w *e2eWorld) run(t *testing.T, in web.ViewsRunInput) map[string]any {
	t.Helper()
	out, err := web.MCPViewsRun(context.Background(), w.deps, w.agent, w.game, in)
	if err != nil {
		t.Fatalf("views.run %+v: %v", in, err)
	}
	return out
}

// nodesOf reads the envelope's nodes. The tool answers with a map so the
// present/absent distinction on `positions` can be carried, which is why
// this is an assertion rather than a field access.
func nodesOf(t *testing.T, envelope map[string]any) []views.Node {
	t.Helper()
	nodes, ok := envelope["nodes"].([]views.Node)
	if !ok {
		t.Fatalf("the envelope's nodes are %T, not a node list", envelope["nodes"])
	}
	return nodes
}

func edgesOf(t *testing.T, envelope map[string]any) []views.Edge {
	t.Helper()
	edges, ok := envelope["edges"].([]views.Edge)
	if !ok {
		t.Fatalf("the envelope's edges are %T, not an edge list", envelope["edges"])
	}
	return edges
}

func e2ePositionsOf(t *testing.T, envelope map[string]any) []views.Position {
	t.Helper()
	raw, ok := envelope["positions"]
	if !ok {
		t.Fatalf("a run of a saved view carries no positions member at all")
	}
	positions, ok := raw.([]views.Position)
	if !ok {
		t.Fatalf("the envelope's positions are %T, not a position list", raw)
	}
	return positions
}

// keysOf is what most assertions below compare: which nodes came back, by
// entity key, so a failure names the content rather than a count.
func keysOf(nodes []views.Node) []string {
	keys := make([]string, 0, len(nodes))
	for _, n := range nodes {
		keys = append(keys, n.Key)
	}
	return keys
}

func drewNode(nodes []views.Node, key string) bool {
	for _, n := range nodes {
		if n.Key == key {
			return true
		}
	}
	return false
}

// queryErrorOf requires err to be a views QueryError with the given code
// and hands it back for the pointer assertions. The code is asserted here
// rather than at each call site because a stale view answered as
// query_invalid, or the reverse, is a different recovery for the agent
// reading it.
func queryErrorOf(t *testing.T, err error, code string) *views.QueryError {
	t.Helper()
	var qe *views.QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v (%T), want a views query error", err, err)
	}
	if qe.Code != code {
		t.Fatalf("code = %q, want %q (err: %v)", qe.Code, code, err)
	}
	return qe
}

// problemAt returns the addressed sentence at exactly this pointer. The
// pointer is the assertion in every step that uses it: this whole
// sub-project exists so that a broken view says *which step* broke, and a
// refusal that named the document as a whole would be the failure it was
// built to prevent.
func problemAt(t *testing.T, qe *views.QueryError, path string) string {
	t.Helper()
	for _, f := range qe.Fields {
		if f.Path == path {
			return f.Message
		}
	}
	paths := make([]string, 0, len(qe.Fields))
	for _, f := range qe.Fields {
		paths = append(paths, f.Path)
	}
	t.Fatalf("no problem at %s; the refusal addressed %v", path, paths)
	return ""
}

func diagnosticAt(t *testing.T, diagnostics []views.Diagnostic, code, path string) views.Diagnostic {
	t.Helper()
	for _, d := range diagnostics {
		if d.Code == code && d.Pointer == path {
			return d
		}
	}
	t.Fatalf("no %s at %s; the report was %+v", code, path, diagnostics)
	return views.Diagnostic{}
}

// TestTheViewsDefinitionOfDone is the spec's §9, run in the order a real
// session runs it. Every step is the call an agent makes, and the step
// numbers are the spec's own.
func TestTheViewsDefinitionOfDone(t *testing.T) {
	w := newE2EWorld(t)
	ctx := context.Background()

	// --- Step 2: the agent misspells the relation type.
	//
	// This is the first of the two mistakes views.validate exists for,
	// and the assertion is the pointer: an agent told only "this query is
	// invalid" has to re-read a forty-line document to find the typo.

	_, err := web.MCPViewsValidate(ctx, w.deps, w.agent, w.game, web.ViewsValidateInput{
		Query: json.RawMessage(`{"v":1,
		  "from":[{"type":"class","as":"cls"}],
		  "traverse":[{"from":"cls","via":"avilable_to","direction":"in",
		              "to_type":"quest","depth":1,"as":"reachable"}],
		  "nodes":[{"set":"reachable"}]}`),
	})
	misspelled := queryErrorOf(t, err, views.CodeQueryInvalid)
	if got := problemAt(t, misspelled, "/traverse/0/via/0"); !strings.Contains(got, "avilable_to") {
		t.Errorf("the refusal at /traverse/0/via/0 is %q and does not name the key back", got)
	}

	// --- Step 3: the agent asks a number for an operator it does not
	// answer. A different mistake with a different recovery: the key is
	// right and the comparison is not.

	_, err = web.MCPViewsValidate(ctx, w.deps, w.agent, w.game, web.ViewsValidateInput{
		Query: json.RawMessage(`{"v":1,
		  "from":[{"type":"class","as":"cls"}],
		  "traverse":[{"from":"cls","via":"available_to","direction":"in",
		              "to_type":"quest","depth":1,"as":"reachable",
		              "where":{"field":"min_level","op":"contains","value":20}}],
		  "nodes":[{"set":"reachable"}]}`),
	})
	badOperator := queryErrorOf(t, err, views.CodeQueryInvalid)
	sentence := problemAt(t, badOperator, "/traverse/0/where/op")
	// The message has to carry both halves — what the field is declared
	// as, and what that declaration does answer — because an agent
	// holding only the refusal cannot guess the second.
	if !strings.Contains(sentence, "number") || !strings.Contains(sentence, "gte") ||
		!strings.Contains(sentence, "contains") {
		t.Errorf("the refusal at /traverse/0/where/op is %q; it must name the declared "+
			"type, the operators it answers and the one that was asked for", sentence)
	}

	// --- Step 4: the query the agent gets right, saved once.
	//
	// views.validate first, because that is the loop: the two refusals
	// above cost no database walk, and this pass is what tells the agent
	// the document is now saveable *with this renderer*, which is the one
	// thing a bare parse cannot answer.

	valid, err := web.MCPViewsValidate(ctx, w.deps, w.agent, w.game, web.ViewsValidateInput{
		Query: json.RawMessage(e2eQuery), Renderer: "graph",
		RendererParams: map[string]any{"color_by": "color_by"},
	})
	if err != nil {
		t.Fatalf("views.validate of the repaired document: %v", err)
	}
	if !valid.Valid {
		t.Fatal("views.validate answered valid=false without an error")
	}
	// The refs are what the deletion in step 11 will read, so they are
	// asserted here rather than taken on trust: three type references,
	// each at the position that names it.
	refAt := map[string]string{}
	for _, ref := range valid.Refs {
		refAt[ref.Pointer] = ref.Kind + ":" + ref.Key
	}
	for pointer, want := range map[string]string{
		"/from/0/type":                   "entity_type:class",
		"/traverse/0/via/0":              "relation_type:available_to",
		"/traverse/0/to_type/0":          "entity_type:quest",
		"/project/color_by/related/via":  "relation_type:takes_place_in",
		"/project/color_by/related/type": "entity_type:zone",
	} {
		if refAt[pointer] != want {
			t.Errorf("validate reported %q at %s, want %s", refAt[pointer], pointer, want)
		}
	}

	saved, err := web.MCPViewsUpsert(ctx, w.deps, w.agent, w.game, web.ViewsUpsertInput{
		Key: "mage-2030", Name: "What a Mage can reach, 20 to 30",
		Query: json.RawMessage(e2eQuery), Renderer: "graph",
		RendererParams:  map[string]any{"color_by": "color_by"},
		ExpectedVersion: int32Of(0),
	})
	if err != nil {
		t.Fatalf("views.upsert: %v", err)
	}
	if saved.Version != 1 {
		t.Fatalf("the first save landed at version %d, want 1", saved.Version)
	}

	// --- Step 5: the picture the spec asks for.

	first := w.run(t, web.ViewsRunInput{Key: "mage-2030"})
	nodes := nodesOf(t, first)
	for _, key := range e2eReachable {
		if !drewNode(nodes, key) {
			t.Errorf("%s is a mage quest between 20 and 30 and did not come back; drew %v",
				key, keysOf(nodes))
		}
	}
	// The class itself is drawn, as the seed: the document says so, and a
	// picture of quests with nothing anchoring them is not the picture
	// that was asked for.
	if !drewNode(nodes, "mage") {
		t.Errorf("the seed class did not come back; drew %v", keysOf(nodes))
	}
	// The four negatives, each a different rule.
	for key, why := range map[string]string{
		"cooking-for-bruises":   "is level 12 and below the band",
		"the-deadmines":         "is level 35 and above the band",
		"charge-of-the-mounted": "is level 26 but available to the warrior, not the mage",
		"warrior":               "is a class this run's parameter did not name",
	} {
		if drewNode(nodes, key) {
			t.Errorf("%s came back and %s; drew %v", key, why, keysOf(nodes))
		}
	}
	if len(nodes) != len(e2eReachable)+1 {
		t.Errorf("drew %d nodes (%v), want the four reachable quests and the seed class",
			len(nodes), keysOf(nodes))
	}
	// Each quest carries its own zone's name, and the two zones differ:
	// a colour that came back the same for every node would satisfy a
	// "colour is present" assertion and mean nothing.
	for _, n := range nodes {
		want, isQuest := e2eZoneOf[n.Key]
		if !isQuest {
			// The class has no takes_place_in, so it carries no colour at
			// all rather than an empty one — the distinction
			// jsonb_strip_nulls exists to keep.
			if _, ok := n.Attrs["color_by"]; ok {
				t.Errorf("%s has no zone and came back with color_by = %v", n.Key, n.Attrs["color_by"])
			}
			continue
		}
		if got := n.Attrs["color_by"]; got != want {
			t.Errorf("%s came back coloured %v, want %q", n.Key, got, want)
		}
		if n.Ambiguous {
			t.Errorf("%s is in one zone and was reported ambiguous", n.Key)
		}
	}
	// The prerequisite arrow inside the band is drawn and the one leaving
	// it is not: `between` is what the spec's example has a second edges[]
	// entry for, and an assertion that only counted edges would pass
	// either way.
	edges := edgesOf(t, first)
	var withinBand, leavingBand int
	for _, e := range edges {
		if e.Type != "requires" {
			continue
		}
		if e.Source == w.entity["quest/the-defias-brotherhood"] &&
			e.Target == w.entity["quest/wanted-hogger"] {
			withinBand++
		}
		if e.Target == w.entity["quest/kobold-candles"] {
			leavingBand++
		}
	}
	if withinBand != 1 {
		t.Errorf("the requires edge inside the band was drawn %d times, want once", withinBand)
	}
	if leavingBand != 0 {
		t.Errorf("a requires edge to a level-15 quest outside the band was drawn %d times",
			leavingBand)
	}

	stats, ok := first["stats"].(views.Stats)
	if !ok {
		t.Fatalf("the envelope's stats are %T", first["stats"])
	}
	// Step 13: this number is the evidence the open question about
	// indexed jsonb fields is to be re-argued with, so it is logged
	// rather than merely asserted about.
	t.Logf("step 5: %d nodes, %d edges, duration_ms = %d",
		stats.Nodes, stats.Edges, stats.DurationMS)
	if stats.Nodes != len(nodes) || stats.Edges != len(edges) {
		t.Errorf("stats say %d nodes and %d edges; the envelope holds %d and %d",
			stats.Nodes, stats.Edges, len(nodes), len(edges))
	}
	if truncated, ok := first["truncated"].(views.Truncated); !ok || truncated.Nodes ||
		truncated.Edges || truncated.Depth {
		t.Errorf("truncated = %+v over a nine-quest game at max_nodes 500", first["truncated"])
	}
	// A saved run always carries a positions member, and nobody has
	// dragged anything yet: empty, not absent, so a client can tell
	// "nothing is placed" from "placement does not apply here".
	if placed := e2ePositionsOf(t, first); len(placed) != 0 {
		t.Errorf("positions = %+v before anything was dragged", placed)
	}

	// --- Step 6: the designer drags the four quests.

	drag := make([]web.ViewsPositionInput, 0, len(e2eReachable))
	for i, key := range e2eReachable {
		drag = append(drag, web.ViewsPositionInput{
			EntityType: "quest", EntityKey: key,
			X: float64(100 * i), Y: float64(-25 * i),
			// One of the four is dragged unpinned, so the read-back
			// cannot pass by hard-wiring the column default.
			Pinned: boolPtr(i != 2),
		})
	}
	written, err := web.MCPViewsSetPositions(ctx, w.deps, w.agent, w.game,
		web.ViewsSetPositionsInput{Key: "mage-2030", Positions: drag})
	if err != nil {
		t.Fatalf("views.set_positions: %v", err)
	}
	if written.Written != len(drag) {
		t.Fatalf("wrote %d positions, want %d", written.Written, len(drag))
	}

	afterDrag := w.run(t, web.ViewsRunInput{Key: "mage-2030"})
	placed := e2ePositionsOf(t, afterDrag)
	byKey := map[string]views.Position{}
	for _, p := range placed {
		byKey[p.EntityKey] = p
	}
	for i, key := range e2eReachable {
		got, ok := byKey[key]
		if !ok {
			t.Fatalf("%s was dragged and came back with no position", key)
		}
		if got.X != float64(100*i) || got.Y != float64(-25*i) {
			t.Errorf("%s came back at (%v, %v), want (%v, %v)",
				key, got.X, got.Y, float64(100*i), float64(-25*i))
		}
		if want := i != 2; got.Pinned != want {
			t.Errorf("%s came back pinned=%v, want %v", key, got.Pinned, want)
		}
	}
	// The other half, and the one the spec asks for by name: the seed
	// class was never dragged, and it comes back *without* a position
	// rather than at the origin. A renderer handed (0, 0) would stack
	// every unplaced node on top of each other and look like a layout.
	if _, ok := byKey["mage"]; ok {
		t.Errorf("the class was never dragged and came back placed at %+v", byKey["mage"])
	}
	if len(placed) != len(e2eReachable) {
		t.Errorf("%d positions came back for %d dragged nodes: %+v",
			len(placed), len(e2eReachable), placed)
	}

	// --- Step 7: the agent adds twelve more quests, and the afternoon of
	// map work survives it.

	more := make([]web.EntityItemInput, 0, 12)
	for i := 1; i <= 12; i++ {
		more = append(more, web.EntityItemInput{
			TypeKey: "quest", Key: fmt.Sprintf("errand-%02d", i),
			Name:   fmt.Sprintf("Errand %02d", i),
			Fields: map[string]any{"min_level": float64(20 + i%8)},
		})
	}
	w.upsertEntities(t, more)
	newEdges := make([]web.RelationItemInput, 0, 24)
	for i := 1; i <= 12; i++ {
		key := fmt.Sprintf("errand-%02d", i)
		newEdges = append(newEdges,
			web.RelationItemInput{TypeKey: "available_to",
				Source: web.RefInput{TypeKey: "quest", Key: key},
				Target: web.RefInput{TypeKey: "class", Key: "mage"}},
			web.RelationItemInput{TypeKey: "takes_place_in",
				Source: web.RefInput{TypeKey: "quest", Key: key},
				Target: web.RefInput{TypeKey: "zone", Key: "elwynn"}})
	}
	if _, err := web.MCPRelationsUpsert(ctx, w.deps, w.agent, w.game,
		web.RelationsUpsertInput{Items: newEdges}); err != nil {
		t.Fatalf("relations.upsert for the twelve: %v", err)
	}

	widened := w.run(t, web.ViewsRunInput{Key: "mage-2030"})
	widenedNodes := nodesOf(t, widened)
	// Eleven of the twelve are inside the band (20 + i%8 gives 20..27 and
	// one 20), so the picture grew; what matters is that it grew *and*
	// the four coordinates did not move.
	if len(widenedNodes) <= len(nodes) {
		t.Fatalf("the twelve new quests did not widen the picture: %d nodes, was %d",
			len(widenedNodes), len(nodes))
	}
	widenedPlaced := e2ePositionsOf(t, widened)
	if len(widenedPlaced) != len(e2eReachable) {
		t.Errorf("%d positions after adding twelve quests, want the four that were dragged: %+v",
			len(widenedPlaced), widenedPlaced)
	}
	for _, p := range widenedPlaced {
		before, ok := byKey[p.EntityKey]
		if !ok {
			t.Errorf("%s arrived with a position and was never dragged", p.EntityKey)
			continue
		}
		if p.X != before.X || p.Y != before.Y || p.Pinned != before.Pinned {
			t.Errorf("%s moved from %+v to %+v when twelve quests were added",
				p.EntityKey, before, p)
		}
	}
	// The twelve arrive unpinned — which here means carrying no position
	// at all, because a position is what pinning is stored on. A new node
	// that arrived placed would be a layout the designer never made.
	for _, n := range widenedNodes {
		if _, dragged := byKey[n.Key]; dragged {
			continue
		}
		if _, ok := byKey[n.Key]; ok {
			continue
		}
		for _, p := range widenedPlaced {
			if p.EntityKey == n.Key {
				t.Errorf("%s was added after the drag and came back placed at %+v", n.Key, p)
			}
		}
	}

	// --- Step 8: the vocabulary moves under the saved document.
	//
	// **The plan's step 8 says to rename available_to through
	// relation_types.upsert, and that call still cannot do it**: both
	// type upserts are addressed by key and idempotent by it, so writing
	// a different key creates a *second* type and leaves the first
	// standing. What this walk does instead is the call that was built
	// for the job — `relation_types.rename`, over the same MCP surface
	// as every other step here, with the version it read.
	//
	// It used to be an UPDATE on the row, because no rename existed and
	// the state had to be reached somehow. Driving the real tool is the
	// point of this walk: the honesty of the whole file is that an agent
	// could have made every call in it.
	//
	// **Only the catalogue moves.** The stored document and its ref row
	// both keep the old spelling, which is what a rename must leave
	// behind: tidying view_refs.ref_key to the new key while the
	// documents keep the old one is read as a torn index, deliberately,
	// and would report the type missing instead.
	//
	// The deletion branch — the other thing an agent can do to a type —
	// is step 11, and it is where on_stale earns its two spellings.
	beforeRename, err := web.MCPRelationTypesGet(ctx, w.deps, w.agent, w.game,
		web.RelationTypesGetInput{Key: "available_to"})
	if err != nil {
		t.Fatalf("read available_to before renaming it: %v", err)
	}
	if _, err := web.MCPRelationTypesRename(ctx, w.deps, w.agent, w.game,
		web.RelationTypesRenameInput{
			From: "available_to", To: "usable_by",
			ExpectedVersion: &beforeRename.Version,
		}); err != nil {
		t.Fatalf("rename available_to: %v", err)
	}

	// **A rename is reported and still drawn, and the plan's own step 8
	// is wrong about that.** It asks for query_stale by default. What
	// ships refuses only a reference that no longer resolves *at all*: a
	// renamed type still resolves by id, so the picture is correct and
	// aborting would be refusing a right answer — the spec says so in as
	// many words ("it runs correctly in the meantime — the id
	// resolved"), and internal/views deliberately has no predicate over
	// the diagnostic codes for exactly this reason. What the run owes the
	// designer is the pointer, and it is the pointer this step asserts.
	renamedRun := w.run(t, web.ViewsRunInput{Key: "mage-2030"})
	renamedDiagnostics, ok := renamedRun["stale"].([]views.Diagnostic)
	if !ok {
		t.Fatalf("the run after the rename reported stale = %T, want the diagnostics",
			renamedRun["stale"])
	}
	renamed := diagnosticAt(t, renamedDiagnostics, views.DiagRelationTypeRenamed,
		"/traverse/0/via/0")
	if renamed.Was != "available_to" || renamed.Now != "usable_by" {
		t.Errorf("the rename diagnostic is %+v, want was=available_to now=usable_by", renamed)
	}
	// The picture is the one it was before the rename, node for node.
	// This is the half that makes the diagnostic a *warning* rather than
	// a refusal, and a test that only read the diagnostic could not tell
	// a correct picture from an empty one.
	if got, want := len(nodesOf(t, renamedRun)), len(widenedNodes); got != want {
		t.Errorf("the renamed view drew %d nodes, want the %d it drew before the rename",
			got, want)
	}
	for _, key := range e2eReachable {
		if !drewNode(nodesOf(t, renamedRun), key) {
			t.Errorf("the renamed view lost %s", key)
		}
	}
	// And the stored document is untouched: a rename that rewrote it
	// would edit an author's document underneath them with no version
	// bump, which makes the next expected_version check pass against a
	// document nobody wrote.
	stored, err := web.MCPViewsGet(ctx, w.deps, w.agent, w.game, web.ViewsGetInput{Key: "mage-2030"})
	if err != nil {
		t.Fatalf("views.get after the rename: %v", err)
	}
	if !strings.Contains(string(stored.Query), "available_to") ||
		strings.Contains(string(stored.Query), "usable_by") {
		t.Errorf("the rename rewrote the stored query: %s", stored.Query)
	}
	if stored.Version != 1 {
		t.Errorf("the rename moved the view to version %d; it must move nothing", stored.Version)
	}

	// --- Step 9: the agent repairs the view, and the repair is what the
	// diagnostic told it to do.

	repaired, err := web.MCPViewsUpsert(ctx, w.deps, w.agent, w.game, web.ViewsUpsertInput{
		Key: "mage-2030", Name: "What a Mage can reach, 20 to 30",
		Query: json.RawMessage(e2eRepairedQuery), Renderer: "graph",
		RendererParams:  map[string]any{"color_by": "color_by"},
		ExpectedVersion: int32Of(1),
	})
	if err != nil {
		t.Fatalf("views.upsert of the repair: %v", err)
	}
	if repaired.Version != 2 {
		t.Fatalf("the repair landed at version %d, want 2", repaired.Version)
	}
	clean := w.run(t, web.ViewsRunInput{Key: "mage-2030"})
	if _, reported := clean["stale"]; reported {
		t.Errorf("the repaired view still reports staleness: %v", clean["stale"])
	}
	cleanNodes := nodesOf(t, clean)
	for _, key := range e2eReachable {
		if !drewNode(cleanNodes, key) {
			t.Errorf("the repaired view lost %s; drew %v", key, keysOf(cleanNodes))
		}
	}
	// The repair is an edit of the query and not of the placement: the
	// four coordinates are still there, which is what makes "repair by
	// re-saving" something a designer will actually do.
	if len(e2ePositionsOf(t, clean)) != len(e2eReachable) {
		t.Errorf("the repair lost the arrangement: %+v", e2ePositionsOf(t, clean))
	}

	// --- Step 10: the designer deletes the zone type, and is told what
	// it broke.
	//
	// This is the branch an agent can really drive, and the spec is
	// explicit that the deletion is *allowed*: a view is derived and can
	// be rewritten in one call, so making a type undeletable because a
	// six-month-old diagram mentions it would push designers into
	// deleting views in order to delete types. What they are owed
	// instead is the list.

	for _, zone := range []string{"elwynn", "westfall"} {
		if _, err := web.MCPEntitiesRemove(ctx, w.deps, w.agent, w.game,
			web.EntitiesRemoveInput{TypeKey: "zone", Key: zone}); err != nil {
			t.Fatalf("entities.remove %s: %v", zone, err)
		}
	}
	removed, err := web.MCPTypesRemove(ctx, w.deps, w.agent, w.game, web.TypesRemoveInput{
		Key: "zone",
	})
	if err != nil {
		t.Fatalf("types.remove zone: %v", err)
	}
	if !removed.Removed {
		t.Fatal("types.remove answered removed=false without an error")
	}
	// The response lists what it broke, with the pointer into the
	// projection: the colour is what the zone type was holding up, and a
	// designer who is not told which views lose their colours finds out
	// the next time they open one.
	var listed bool
	for _, broke := range removed.BrokeViews {
		if broke.ViewKey == "mage-2030" && broke.Pointer == "/project/color_by/related/type" {
			listed = true
			if broke.Key != "zone" {
				t.Errorf("the broken reference is reported as %q, want the key the document "+
					"spells: zone", broke.Key)
			}
		}
	}
	if !listed {
		t.Errorf("the deletion reported %+v; the view it broke is mage-2030 at "+
			"/project/color_by/related/type", removed.BrokeViews)
	}

	// --- Step 11: the two spellings of on_stale, over a reference that
	// really is dead.
	//
	// This is where the default earns its argument: a diagram that
	// silently dropped its colours looks exactly like a correct diagram,
	// and a designer will believe it.

	_, err = web.MCPViewsRun(ctx, w.deps, w.agent, w.game, web.ViewsRunInput{Key: "mage-2030"})
	broken := queryErrorOf(t, err, views.CodeQueryStale)
	diagnosticAt(t, broken.Stale, views.DiagEntityTypeMissing, "/project/color_by/related/type")
	// The refusal is addressed as well as coded: an agent acts on
	// details.fields[].path and a banner reads the codes, and losing
	// either leaves one of the two readers with nothing.
	if got := problemAt(t, broken, "/project/color_by/related/type"); got == "" {
		t.Error("the stale refusal carried no addressed sentence")
	}

	// And the designer who wants the picture anyway gets it, with the
	// warning and without the colour — which is the cost of best effort
	// made visible rather than described.
	best := w.run(t, web.ViewsRunInput{Key: "mage-2030", OnStale: views.OnStaleBestEffort})
	bestNodes := nodesOf(t, best)
	if len(bestNodes) == 0 {
		t.Fatal("best_effort drew nothing; the point of it is that most of the graph survives")
	}
	for _, key := range e2eReachable {
		if !drewNode(bestNodes, key) {
			t.Errorf("best_effort lost %s, which the dead colour reference does not reach", key)
		}
	}
	bestStale, ok := best["stale"].([]views.Diagnostic)
	if !ok {
		t.Fatalf("best_effort answered with stale = %T, want the diagnostics as a banner",
			best["stale"])
	}
	diagnosticAt(t, bestStale, views.DiagEntityTypeMissing, "/project/color_by/related/type")
	for _, n := range bestNodes {
		if _, coloured := n.Attrs["color_by"]; coloured {
			t.Errorf("%s came back coloured after the zone type was deleted: %v",
				n.Key, n.Attrs["color_by"])
		}
	}

	// --- Step 12: the isolation sweep, over the walk's own state.
	//
	// Which tools exist and that every one of them refuses a foreign
	// token is asserted by the registration-driven sweep in
	// mcp_views_test.go, which enumerates the registry and fails when a
	// tool it does not drive is added. What that sweep cannot say is that
	// a real arrangement survives the attempt, so this one drives the
	// calls that would *change* something and then re-reads the walk's
	// own picture.

	foreign := map[string]func() error{
		"views.get": func() error {
			_, err := web.MCPViewsGet(ctx, w.deps, w.guest, w.game,
				web.ViewsGetInput{Key: "mage-2030"})
			return err
		},
		"views.run": func() error {
			_, err := web.MCPViewsRun(ctx, w.deps, w.guest, w.game,
				web.ViewsRunInput{Key: "mage-2030"})
			return err
		},
		"views.upsert": func() error {
			_, err := web.MCPViewsUpsert(ctx, w.deps, w.guest, w.game, web.ViewsUpsertInput{
				Key: "mage-2030", Name: "stolen", Query: json.RawMessage(e2eRepairedQuery),
				Renderer: "graph", ExpectedVersion: int32Of(2),
			})
			return err
		},
		"views.set_positions": func() error {
			_, err := web.MCPViewsSetPositions(ctx, w.deps, w.guest, w.game,
				web.ViewsSetPositionsInput{Key: "mage-2030", Positions: []web.ViewsPositionInput{
					{EntityType: "quest", EntityKey: "wanted-hogger", X: 999, Y: 999},
				}})
			return err
		},
		"views.clear_positions": func() error {
			_, err := web.MCPViewsClearPositions(ctx, w.deps, w.guest, w.game,
				web.ViewsClearPositionsInput{Key: "mage-2030"})
			return err
		},
		"views.remove": func() error {
			_, err := web.MCPViewsRemove(ctx, w.deps, w.guest, w.game,
				web.ViewsRemoveInput{Key: "mage-2030"})
			return err
		},
	}
	for name, call := range foreign {
		if err := call(); !errors.Is(err, web.ErrScopeViolation) {
			t.Errorf("%s with the other game's token: err = %v, want a scope violation", name, err)
		}
	}
	survived := w.run(t, web.ViewsRunInput{Key: "mage-2030", OnStale: views.OnStaleBestEffort})
	if got := len(e2ePositionsOf(t, survived)); got != len(e2eReachable) {
		t.Errorf("the arrangement holds %d positions after the sweep, want %d",
			got, len(e2eReachable))
	}
	for _, p := range e2ePositionsOf(t, survived) {
		before := byKey[p.EntityKey]
		if p.X != before.X || p.Y != before.Y {
			t.Errorf("%s moved to %+v during the isolation sweep, was %+v", p.EntityKey, p, before)
		}
	}
	still, err := web.MCPViewsGet(ctx, w.deps, w.agent, w.game, web.ViewsGetInput{Key: "mage-2030"})
	if err != nil {
		t.Fatalf("the view did not survive the isolation sweep: %v", err)
	}
	if still.Version != 2 {
		t.Errorf("the view is at version %d after the sweep, want the 2 the repair left",
			still.Version)
	}
}

// TestASavedViewArrivesOverTheRealTransport is the walk's last step and
// the one the plan asks to be done by hand: a real MCP client, over
// HTTP, with a real token, saving a view and running it — and reading
// the envelope out of StructuredContent, which is what a client actually
// consumes.
//
// **It is a different test from the walk above, and the difference is
// the point.** Every other assertion in this file calls the MCP*
// functions directly, which is where the isolation invariant is pinned
// and where a story can be told; nothing there passes through the tool
// registration, the input schemas, the output schemas or the SDK's own
// marshalling. Three of those four can be wrong while every test in this
// package is green, and one of them was: views.run's declared output
// schema required two stats members the envelope has never carried
// (node_count, edge_count) and omitted the two it does (nodes, edges).
// Nothing read the schema, so nothing said so.
func TestASavedViewArrivesOverTheRealTransport(t *testing.T) {
	w := newE2EWorld(t)
	ctx := context.Background()
	httpSrv := httptest.NewServer(w.srv)
	defer httpSrv.Close()
	session := connectMCP(t, httpSrv.URL, w.secret)

	upsert, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "views.upsert",
		Arguments: map[string]any{
			"key": "mage-2030", "name": "What a Mage can reach, 20 to 30",
			"query": json.RawMessage(e2eQuery), "renderer": "graph",
			"renderer_params":  map[string]any{"color_by": "color_by"},
			"expected_version": 0,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(views.upsert): %v", err)
	}
	if upsert.IsError {
		var msg any
		decodeToolText(t, upsert, &msg)
		t.Fatalf("views.upsert over the wire: %v", msg)
	}

	run, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "views.run",
		Arguments: map[string]any{"key": "mage-2030"},
	})
	if err != nil {
		t.Fatalf("CallTool(views.run): %v", err)
	}
	if run.IsError {
		var msg any
		decodeToolText(t, run, &msg)
		t.Fatalf("views.run over the wire: %v", msg)
	}
	var envelope struct {
		Nodes []struct {
			Key   string         `json:"key"`
			Attrs map[string]any `json:"attrs"`
		} `json:"nodes"`
		Stats struct {
			Nodes      int   `json:"nodes"`
			Edges      int   `json:"edges"`
			DurationMS int64 `json:"duration_ms"`
		} `json:"stats"`
		Positions []map[string]any `json:"positions"`
	}
	decodeStructured(t, run, &envelope)
	got := map[string]string{}
	for _, n := range envelope.Nodes {
		if colour, ok := n.Attrs["color_by"].(string); ok {
			got[n.Key] = colour
		}
	}
	for _, key := range e2eReachable {
		if got[key] != e2eZoneOf[key] {
			t.Errorf("over the wire %s came back coloured %q, want %q", key, got[key], e2eZoneOf[key])
		}
	}
	if envelope.Stats.Nodes != len(envelope.Nodes) {
		t.Errorf("the wire's stats say %d nodes over %d in the envelope",
			envelope.Stats.Nodes, len(envelope.Nodes))
	}
	// A saved view always carries the member, empty here because nothing
	// has been dragged in this world.
	if envelope.Positions == nil {
		t.Error("a run of a saved view arrived over the wire with no positions member")
	}
}

// TestEveryViewsToolIsCallableOverTheRealTransport drives all ten tools
// through the mounted MCP endpoint, with arguments that are meant to
// succeed.
//
// **It is a schema test wearing a smoke test's clothes**, and that is
// the point of driving every tool rather than one. The SDK validates a
// call's arguments against the registered *input* schema before any
// handler runs, and the handler's answer against the registered *output*
// schema before it reaches the wire; both schemas are inferred from Go
// types or written by hand, and neither is read by any test that calls
// the MCP* functions directly. Two were wrong at once when this test was
// written — the query document declared as an array of bytes on three
// tools, and views.run's stats declaring two members the envelope has
// never carried — and the whole package was green.
//
// The table is checked against the registry, so a tool added without a
// call here fails rather than going unexercised.
func TestEveryViewsToolIsCallableOverTheRealTransport(t *testing.T) {
	w := newE2EWorld(t)
	ctx := context.Background()
	httpSrv := httptest.NewServer(w.srv)
	defer httpSrv.Close()
	session := connectMCP(t, httpSrv.URL, w.secret)

	// Uploading is REST-only and browser-only by design, so there is no
	// tool for it and this one setup step goes through the service.
	asset, err := w.vs.CreateAsset(ctx, w.game, views.Actor{}, "world.png",
		bytes.NewReader(testPNG(t, 8, 4)))
	if err != nil {
		t.Fatalf("upload a background: %v", err)
	}

	calls := map[string]map[string]any{
		"views.upsert": {
			"key": "mage-2030", "name": "What a Mage can reach, 20 to 30",
			// map, because views.set_background is in this sweep and only a
			// renderer that draws a background accepts one.
			"query": json.RawMessage(e2eQuery), "renderer": "map",
			"renderer_params":  map[string]any{"coordinate_source": "manual"},
			"expected_version": 0,
		},
		"views.list":     {},
		"views.get":      {"key": "mage-2030"},
		"views.run":      {"key": "mage-2030"},
		"views.validate": {"query": json.RawMessage(e2eQuery)},
		"views.set_positions": {"key": "mage-2030", "positions": []map[string]any{
			{"entity_type": "quest", "entity_key": "wanted-hogger", "x": 1.5, "y": -2.5},
		}},
		"views.clear_positions": {"key": "mage-2030"},
		"views.list_assets":     {},
		"views.set_background": {"key": "mage-2030", "asset_id": asset.ID.String(),
			"scale": 2.5, "offset": map[string]any{"x": 10, "y": -4}},
		// Last, deliberately: it takes the view every other call needs.
		"views.remove": {"key": "mage-2030"},
	}
	// The order matters — a view has to exist before it can be run — so
	// the map is driven through a list and the map is what the registry
	// is compared against.
	//
	// **views.run comes after views.set_positions**, so the run whose
	// answer the output schema judges actually carries a stored position.
	// Run first, the `positions` array is empty and an array of nothing
	// satisfies any item schema at all — which is how a misspelled
	// position member would have gone on passing this test the day the
	// schema started naming them.
	order := []string{
		"views.upsert", "views.list", "views.get", "views.validate",
		"views.set_positions", "views.run", "views.clear_positions", "views.list_assets",
		"views.set_background", "views.remove",
	}
	if len(order) != len(calls) {
		t.Fatalf("the ordering names %d tools and the table has %d", len(order), len(calls))
	}
	registered := 0
	for _, name := range w.srv.ScopedToolNamesForTest() {
		if !strings.HasPrefix(name, "views.") {
			continue
		}
		registered++
		if _, ok := calls[name]; !ok {
			t.Fatalf("%s is registered and this test does not call it: add it, or its "+
				"input and output schemas are asserted by nothing at all", name)
		}
	}
	if registered != len(calls) {
		t.Fatalf("%d views tools are registered and the table has %d", registered, len(calls))
	}
	if registered == 0 {
		t.Fatal("no views tool is registered; this test would pass vacuously")
	}

	for _, name := range order {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: name, Arguments: calls[name],
			})
			if err != nil {
				t.Fatalf("CallTool(%s): %v", name, err)
			}
			if result.IsError {
				var msg any
				decodeToolText(t, result, &msg)
				t.Fatalf("%s over the wire: %v", name, msg)
			}
			// Every one of these tools declares an output schema, so a
			// successful call must carry structured content: an answer
			// that arrived only as prose is one no client can read.
			if result.StructuredContent == nil {
				t.Fatalf("%s answered with no structured content", name)
			}
		})
	}
}
