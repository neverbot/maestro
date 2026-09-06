package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/views"
	"github.com/neverbot/maestro/internal/web"
)

// The saved-view tools, over the plain MCP* functions rather than over
// the wire. What is tested here is the surface: which tools exist, what
// they carry back, and that a token bound to one game cannot reach
// another's views through any of them. The query language itself is
// tested in internal/views, and it is not re-tested here.

type viewsFixture struct {
	pool    *pgxpool.Pool
	deps    web.MCPDeps
	caller  web.Caller
	game    uuid.UUID
	other   uuid.UUID
	srv     *web.Server
	ownerID uuid.UUID
	views   *views.Service
	meta    *metamodel.Service
}

// newViewsFixture builds a server with a views service, a token bound to
// one game, and a second game the same *user* owns — which is the case
// requireScope exists for: an instance admin gets no exemption, and
// neither does an owner reaching sideways with a token.
func newViewsFixture(t *testing.T) viewsFixture {
	t.Helper()
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
	ctx := context.Background()
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
	secret, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: owner.ID, Label: "agent",
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	caller, err := web.CallerForToken(ctx, ids, secret)
	if err != nil {
		t.Fatalf("CallerForToken: %v", err)
	}
	f := viewsFixture{
		pool:   pool,
		deps:   web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: mm, Views: vs},
		caller: caller, game: game.ID, other: other.ID, srv: srv,
		ownerID: owner.ID, views: vs, meta: mm,
	}
	f.seed(t, game.ID)
	f.seed(t, other.ID)
	return f
}

// seed declares the vocabulary every test here draws over, in whichever
// game it is given: two entity types, one relation type, and three
// quests across two zones.
func (f viewsFixture) seed(t *testing.T, game uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	for _, in := range []metamodel.EntityTypeInput{
		{Key: "quest", Label: "Quest", LabelPlural: "Quests", Schema: []metamodel.Field{
			{Key: "min_level", Type: metamodel.FieldNumber},
		}},
		{Key: "zone", Label: "Zone", LabelPlural: "Zones"},
	} {
		if _, err := f.meta.UpsertEntityType(ctx, game, in); err != nil {
			t.Fatalf("seed type %s: %v", in.Key, err)
		}
	}
	if _, err := f.meta.UpsertRelationType(ctx, game, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "Takes place in",
	}); err != nil {
		t.Fatalf("seed relation type: %v", err)
	}
	for _, in := range []metamodel.EntityInput{
		{TypeKey: "zone", Key: "elwynn", Name: "Elwynn Forest"},
		{TypeKey: "zone", Key: "westfall", Name: "Westfall"},
		{TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
			Fields: map[string]any{"min_level": 22}},
		{TypeKey: "quest", Key: "defias", Name: "The Defias Brotherhood",
			Fields: map[string]any{"min_level": 28}},
		{TypeKey: "quest", Key: "cook", Name: "Cooking for Bruises",
			Fields: map[string]any{"min_level": 12}},
	} {
		if _, err := f.meta.UpsertEntity(ctx, game, in); err != nil {
			t.Fatalf("seed entity %s: %v", in.Key, err)
		}
	}
}

const questsQuery = `{"v":1,"from":[{"type":"quest","as":"q"}]}`

func int32Of(v int32) *int32 { return &v }

// TestAViewIsReadBackThroughTheToolsWithEveryFieldItWasSavedWith is this
// task's read-back, and it is a second projection of the columns
// internal/views already reads back — which is exactly why it is here.
// A tool that stored six values and returned four would leave four of
// them write-only *on this surface* while the domain's own test stayed
// green.
//
// All six are set to non-default values, because a field that takes the
// default cannot tell a stored value from a hard-wired one.
func TestAViewIsReadBackThroughTheToolsWithEveryFieldItWasSavedWith(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()

	saved, err := web.MCPViewsUpsert(ctx, f.deps, f.caller, f.game, web.ViewsUpsertInput{
		Key: "route", Name: "The route", Description: "How a Mage gets from 20 to 30",
		Query: json.RawMessage(questsQuery), Renderer: "map",
		RendererParams:  map[string]any{"coordinate_source": "manual", "snap": float64(16)},
		LayoutMode:      "manual",
		LayoutSeed:      int32Of(0),
		ExpectedVersion: int32Of(0),
	})
	if err != nil {
		t.Fatalf("views.upsert: %v", err)
	}
	if saved.Version != 1 {
		t.Fatalf("Version = %d, want 1", saved.Version)
	}

	// A background, so background_scale and background_offset are the
	// stored values and not the column defaults.
	asset := f.upload(t, f.game)
	if _, err := web.MCPViewsSetBackground(ctx, f.deps, f.caller, f.game,
		web.ViewsSetBackgroundInput{Key: "route", AssetID: &asset,
			Scale: float64Of(2.5), Offset: &web.ViewsPointInput{X: 10, Y: -4}}); err != nil {
		t.Fatalf("views.set_background: %v", err)
	}

	got, err := web.MCPViewsGet(ctx, f.deps, f.caller, f.game, web.ViewsGetInput{Key: "route"})
	if err != nil {
		t.Fatalf("views.get: %v", err)
	}
	if got.Description != "How a Mage gets from 20 to 30" {
		t.Errorf("description = %q", got.Description)
	}
	if got.RendererParams["snap"] != float64(16) ||
		got.RendererParams["coordinate_source"] != "manual" {
		t.Errorf("renderer_params = %v, want both parameters back", got.RendererParams)
	}
	if got.LayoutMode != "manual" {
		t.Errorf("layout_mode = %q, want manual", got.LayoutMode)
	}
	// 0 is a seed a designer may deliberately choose, which is why it is
	// the one written here: a *int32 read back as a plain int32 would
	// silently answer with the column default.
	if got.LayoutSeed == nil || *got.LayoutSeed != 0 {
		t.Errorf("layout_seed = %v, want the 0 that was saved", got.LayoutSeed)
	}
	if got.BackgroundScale != 2.5 {
		t.Errorf("background_scale = %v, want 2.5", got.BackgroundScale)
	}
	if got.BackgroundOffset != (web.ViewsPointInput{X: 10, Y: -4}) {
		t.Errorf("background_offset = %+v, want {10, -4}", got.BackgroundOffset)
	}
	if got.BackgroundAssetID == nil || *got.BackgroundAssetID != asset {
		t.Errorf("background_asset_id = %v, want %s", got.BackgroundAssetID, asset)
	}
	// The query comes back as a document rather than as a string of
	// escaped JSON, which is the whole reason it is a json.RawMessage on
	// both sides.
	var document map[string]any
	if err := json.Unmarshal(got.Query, &document); err != nil {
		t.Fatalf("the query did not come back as a document: %v", err)
	}
	if document["v"] != float64(1) {
		t.Errorf("query = %v, want the document that was saved", document)
	}
}

// TestPositionsAreWrittenAndReadBackThroughTheTools is the second clause
// of the read-back sweep, and `pinned` is the column it exists for: two
// nodes, one written pinned false and one taking the default, because a
// fixture where every node is pinned cannot tell a stored flag from a
// hard-wired one.
func TestPositionsAreWrittenAndReadBackThroughTheTools(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	f.save(t, "route", questsQuery, "graph")

	pinned := false
	written, err := web.MCPViewsSetPositions(ctx, f.deps, f.caller, f.game,
		web.ViewsSetPositionsInput{Key: "route", Positions: []web.ViewsPositionInput{
			{EntityType: "quest", EntityKey: "hogger", X: 12.5, Y: -40.25},
			{EntityType: "quest", EntityKey: "defias", X: 0, Y: 0, Pinned: &pinned},
		}})
	if err != nil {
		t.Fatalf("views.set_positions: %v", err)
	}
	if written.Written != 2 {
		t.Fatalf("written = %d, want 2", written.Written)
	}

	run, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.game, web.ViewsRunInput{Key: "route"})
	if err != nil {
		t.Fatalf("views.run: %v", err)
	}
	positions := positionsOf(t, run)
	if len(positions) != 2 {
		t.Fatalf("positions = %v, want the two that were dragged", positions)
	}
	byKey := map[string]map[string]any{}
	for _, p := range positions {
		byKey[p["EntityKey"].(string)] = p
	}
	if byKey["hogger"]["Pinned"] != true {
		t.Errorf("hogger came back %v, want pinned by default", byKey["hogger"])
	}
	if byKey["defias"]["Pinned"] != false {
		t.Errorf("defias came back %v, want the false it was written with", byKey["defias"])
	}

	// And the clear, both spellings, because the pointer that separates
	// them is the only thing between an empty dirty-list and a wiped
	// arrangement.
	one := []web.ViewsEntityAddressInput{{EntityType: "quest", EntityKey: "hogger"}}
	removed, err := web.MCPViewsClearPositions(ctx, f.deps, f.caller, f.game,
		web.ViewsClearPositionsInput{Key: "route", Entities: &one})
	if err != nil {
		t.Fatalf("views.clear_positions: %v", err)
	}
	if removed.Removed != 1 {
		t.Fatalf("removed = %d, want the one node named", removed.Removed)
	}
	all, err := web.MCPViewsClearPositions(ctx, f.deps, f.caller, f.game,
		web.ViewsClearPositionsInput{Key: "route"})
	if err != nil {
		t.Fatalf("views.clear_positions (whole view): %v", err)
	}
	if all.Removed != 1 {
		t.Fatalf("removed = %d, want the one that was left", all.Removed)
	}
	// An empty array is refused, which is the half a client with an empty
	// dirty-list depends on.
	empty := []web.ViewsEntityAddressInput{}
	if _, err := web.MCPViewsClearPositions(ctx, f.deps, f.caller, f.game,
		web.ViewsClearPositionsInput{Key: "route", Entities: &empty}); err == nil {
		t.Fatal("an empty entities array was accepted: said nothing is not said none")
	}
}

// TestAnAssetIsReadBackThroughTheToolWithItsDecodedShape is the third
// clause of the read-back sweep. width, height and mime are decoded from
// the bytes rather than taken from the upload's claims, and this tool is
// a second projection of those columns — the domain's own test cannot
// see it.
func TestAnAssetIsReadBackThroughTheToolWithItsDecodedShape(t *testing.T) {
	f := newViewsFixture(t)
	id := f.upload(t, f.game)

	got, err := web.MCPViewsListAssets(context.Background(), f.deps, f.caller, f.game,
		web.ViewsListAssetsInput{})
	if err != nil {
		t.Fatalf("views.list_assets: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %+v, want the one asset", got.Items)
	}
	asset := got.Items[0]
	if asset.ID != id {
		t.Errorf("id = %s, want %s", asset.ID, id)
	}
	if asset.Mime != "image/png" || asset.Width != 8 || asset.Height != 4 {
		t.Errorf("asset = %+v, want the decoded 8x4 png", asset)
	}
	// The URL is what makes the id usable at all: a client draws the
	// background from it, and it has to name this game.
	if !strings.Contains(asset.URL, "/api/games/azeroth/view-assets/"+id) {
		t.Errorf("url = %q, want this game's own serving route", asset.URL)
	}
}

// TestEveryViewsToolRefusesAnotherGamesToken is the isolation sweep for
// this whole surface, driven from the *registered* tool list rather than
// a hand-written one: a views tool with no entry in the table below fails
// this test, so an eleventh tool added tomorrow is covered without
// anybody remembering to edit it.
//
// **views.run appears twice**, once with a saved key and once with an
// inline query, because the two take different paths to the same
// execution — RunView reads a stored document and its dependency index,
// Run resolves one that arrived in the call — and a scope check on one
// path says nothing about the other.
func TestEveryViewsToolRefusesAnotherGamesToken(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	// A view in the *other* game, so every call below names something
	// that really exists there: a refusal over a missing view would pass
	// this test while the scope check was gone.
	if _, err := f.views.UpsertView(ctx, f.other, views.ViewInput{
		Key: "route", Name: "Route", Query: []byte(questsQuery), Renderer: "graph",
	}); err != nil {
		t.Fatalf("seed a view in the other game: %v", err)
	}

	calls := map[string]func() error{
		"views.list": func() error {
			_, err := web.MCPViewsList(ctx, f.deps, f.caller, f.other, web.ViewsListInput{})
			return err
		},
		"views.get": func() error {
			_, err := web.MCPViewsGet(ctx, f.deps, f.caller, f.other, web.ViewsGetInput{Key: "route"})
			return err
		},
		"views.upsert": func() error {
			_, err := web.MCPViewsUpsert(ctx, f.deps, f.caller, f.other, web.ViewsUpsertInput{
				Key: "route", Name: "Route", Query: json.RawMessage(questsQuery),
				Renderer: "graph", ExpectedVersion: int32Of(1),
			})
			return err
		},
		"views.remove": func() error {
			_, err := web.MCPViewsRemove(ctx, f.deps, f.caller, f.other, web.ViewsRemoveInput{Key: "route"})
			return err
		},
		"views.run": func() error {
			if _, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.other,
				web.ViewsRunInput{Key: "route"}); !errors.Is(err, web.ErrScopeViolation) {
				return err
			}
			// The second path: an inline query, which never touches a
			// stored row and would be refused by nothing but the scope
			// check.
			_, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.other,
				web.ViewsRunInput{Query: json.RawMessage(questsQuery)})
			return err
		},
		"views.validate": func() error {
			_, err := web.MCPViewsValidate(ctx, f.deps, f.caller, f.other,
				web.ViewsValidateInput{Query: json.RawMessage(questsQuery)})
			return err
		},
		"views.set_positions": func() error {
			_, err := web.MCPViewsSetPositions(ctx, f.deps, f.caller, f.other,
				web.ViewsSetPositionsInput{Key: "route", Positions: []web.ViewsPositionInput{
					{EntityType: "quest", EntityKey: "hogger", X: 1, Y: 1},
				}})
			return err
		},
		"views.clear_positions": func() error {
			_, err := web.MCPViewsClearPositions(ctx, f.deps, f.caller, f.other,
				web.ViewsClearPositionsInput{Key: "route"})
			return err
		},
		"views.set_background": func() error {
			_, err := web.MCPViewsSetBackground(ctx, f.deps, f.caller, f.other,
				web.ViewsSetBackgroundInput{Key: "route"})
			return err
		},
		"views.list_assets": func() error {
			_, err := web.MCPViewsListAssets(ctx, f.deps, f.caller, f.other,
				web.ViewsListAssetsInput{})
			return err
		},
	}

	registered := 0
	for _, name := range f.srv.ScopedToolNamesForTest() {
		if !strings.HasPrefix(name, "views.") {
			continue
		}
		registered++
		call, ok := calls[name]
		if !ok {
			t.Fatalf("%s is registered and this test does not drive it: add it to the table, "+
				"or one game's agent reaches another game's views unnoticed", name)
		}
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, web.ErrScopeViolation) {
				t.Fatalf("err = %v, want a scope violation", err)
			}
		})
	}
	if registered != len(calls) {
		t.Fatalf("%d views tools are registered and the table has %d entries", registered, len(calls))
	}
	if registered == 0 {
		t.Fatal("no views tool is registered at all; this test would pass vacuously")
	}
	// The other game's view is still there: a refusal that had deleted or
	// rewritten it first would satisfy every assertion above.
	if _, err := f.views.ViewByKey(ctx, f.other, "route"); err != nil {
		t.Fatalf("the other game's view did not survive the sweep: %v", err)
	}
}

// TestTheViewsToolsAreAbsentWithoutAViewsService pins the optional half
// of MCPDeps.Views: a server built without one still starts, and
// tools/list simply does not carry the ten.
func TestTheViewsToolsAreAbsentWithoutAViewsService(t *testing.T) {
	srv := web.NewServer(web.Options{
		Version: "test", Config: testConfig(),
		Identity: identity.New(nil, config.Config{}), Projects: projects.New(nil),
	})
	for _, name := range srv.ScopedToolNamesForTest() {
		if strings.HasPrefix(name, "views.") {
			t.Errorf("%s is registered on a server built with no views service", name)
		}
	}
	// The control: the server really did register its other tools, so
	// this is not passing because nothing is registered at all.
	if len(srv.ScopedToolNamesForTest()) == 0 {
		t.Fatal("no tool at all is registered; this test would pass vacuously")
	}
}

// --- helpers ---

func float64Of(v float64) *float64 { return &v }

// save stores a view through the tool, for the tests that need one and
// do not care what it holds.
func (f viewsFixture) save(t *testing.T, key, query, renderer string) {
	t.Helper()
	if _, err := web.MCPViewsUpsert(context.Background(), f.deps, f.caller, f.game,
		web.ViewsUpsertInput{Key: key, Name: key, Query: json.RawMessage(query),
			Renderer: renderer, ExpectedVersion: int32Of(0)}); err != nil {
		t.Fatalf("save view %q: %v", key, err)
	}
}

// upload stores one 8x4 PNG through the domain — there is no upload tool
// and deliberately so — and hands back its id as the string the
// set_background tool takes.
func (f viewsFixture) upload(t *testing.T, game uuid.UUID) string {
	t.Helper()
	asset, err := f.views.CreateAsset(context.Background(), game, views.Actor{},
		"world.png", bytes.NewReader(testPNG(t, 8, 4)))
	if err != nil {
		t.Fatalf("upload an asset: %v", err)
	}
	return asset.ID.String()
}

// positionsOf reads the arrangement out of a run's envelope, which is a
// map[string]any because a node's own shape is a property of the query
// that drew it (viewsRunOutputSchema says why).
func positionsOf(t *testing.T, run map[string]any) []map[string]any {
	t.Helper()
	raw, ok := run["positions"]
	if !ok {
		t.Fatal("the run carried no positions member at all")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal positions: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("decode positions: %v", err)
	}
	return out
}

// TestARunNamesEitherASavedViewOrAQueryAndNeverBoth pins the one
// argument this tool arbitrates. A precedence rule would draw a picture
// the caller did not ask for and say nothing, which is the silent-wrong-
// answer failure the whole sub-project is built to refuse.
func TestARunNamesEitherASavedViewOrAQueryAndNeverBoth(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	f.save(t, "route", questsQuery, "graph")

	both, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.game, web.ViewsRunInput{
		Key: "route", Query: json.RawMessage(questsQuery),
	})
	if err == nil {
		t.Fatalf("naming both was accepted and drew %v", both)
	}
	if !strings.Contains(err.Error(), "one or the other") {
		t.Errorf("err = %v, want it to say which of the two to send", err)
	}
	if _, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.game, web.ViewsRunInput{}); err == nil {
		t.Fatal("naming neither was accepted: this tool cannot run nothing")
	}
	// The two controls, so the refusal is about the combination and not
	// about either argument.
	if _, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.game,
		web.ViewsRunInput{Key: "route"}); err != nil {
		t.Fatalf("a saved key alone: %v", err)
	}
	if _, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.game,
		web.ViewsRunInput{Query: json.RawMessage(questsQuery)}); err != nil {
		t.Fatalf("an inline query alone: %v", err)
	}
}

// TestAnInlineRunCarriesNoPositionsMemberAndASavedOneAlwaysDoes is the
// distinction the domain's own envelope cannot carry, because
// `omitempty` collapses "nothing was dragged" into "positions do not
// apply". An agent calls one tool either way and has to be able to tell
// the two apart: absent means an inline query, empty means a saved view
// nobody has arranged yet.
func TestAnInlineRunCarriesNoPositionsMemberAndASavedOneAlwaysDoes(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	f.save(t, "route", questsQuery, "graph")

	inline, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.game,
		web.ViewsRunInput{Query: json.RawMessage(questsQuery)})
	if err != nil {
		t.Fatalf("inline run: %v", err)
	}
	if _, present := inline["positions"]; present {
		t.Errorf("an inline run carried positions = %v: there is no saved view for one to "+
			"belong to", inline["positions"])
	}

	saved, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.game, web.ViewsRunInput{Key: "route"})
	if err != nil {
		t.Fatalf("saved run: %v", err)
	}
	raw, present := saved["positions"]
	if !present {
		t.Fatal("a saved run carried no positions member: absent means \"not applicable\", " +
			"and this view simply has none yet")
	}
	if got := positionsOf(t, saved); len(got) != 0 {
		t.Fatalf("positions = %v, want an empty list on a view nobody has dragged", raw)
	}
	// And the two runs drew the same picture, so the difference really is
	// the member and not the query.
	if len(saved["nodes"].([]views.Node)) != len(inline["nodes"].([]views.Node)) {
		t.Fatalf("the two runs drew different pictures; the control does not hold")
	}
}

// TestTheViewsToolDescriptionsAreGeneratedRatherThanRestated is the
// guard Task 10's finding 12 asked this task for, and it reads what an
// agent reads rather than what this package could have written.
//
// **The renderer half must be the catalogue's own generated text,
// verbatim.** A hand-written paragraph about a renderer is a paragraph
// that goes false the first time a parameter moves, and the agent finds
// out by being refused at a pointer nobody showed it. The same argument
// holds for the operator table, which *is* the query language's contract.
//
// It also carries Task 14's rule one step along: the description must
// name nothing only this repository knows. That test lives in
// internal/views for the renderer text; the tools' own prose is written
// here and had never been swept.
func TestTheViewsToolDescriptionsAreGeneratedRatherThanRestated(t *testing.T) {
	f := newViewsFixture(t)
	descriptions := f.srv.ToolDescriptionsForTest()

	renderers := views.RendererDescription()
	operators := views.OperatorDescription()
	if len(renderers) < 200 || len(operators) < 200 {
		t.Fatalf("the generated tables are %d and %d characters; this test would pass over "+
			"nothing", len(renderers), len(operators))
	}

	// **Both tools that judge a renderer carry the catalogue**, and
	// views.validate is the one that would be missed: it is the tool
	// that exists so a renderer parameter can be iterated, its own text
	// says naming a renderer "is the only way a renderer parameter can
	// be judged", and it named no renderer and no parameter. A loop
	// requiring the catalogue on the upsert alone is a guard against
	// exactly the drift it let through.
	for _, name := range []string{"views.upsert", "views.validate"} {
		if strings.Count(descriptions[name], renderers) != 1 {
			t.Errorf("%s does not carry the generated renderer catalogue verbatim, exactly "+
				"once: an agent choosing a renderer or a parameter is then reading a "+
				"hand-written copy that can lie", name)
		}
	}

	if _, ok := descriptions["views.upsert"]; !ok {
		t.Fatal("views.upsert is not registered")
	}
	for _, name := range []string{"views.upsert", "views.run", "views.validate"} {
		if strings.Count(descriptions[name], operators) != 1 {
			t.Errorf("%s does not carry the generated operator table verbatim", name)
		}
	}

	// The hand-written half is the rules a table cannot state, and each
	// of these is a rule a review round found an agent would otherwise
	// meet as an empty picture rather than as a refusal.
	for _, want := range []struct{ tool, phrase, why string }{
		{"views.run", "draws nothing silently",
			"the @type split: equality refuses a misspelling and the pattern operators do not"},
		{"views.validate", "draws nothing silently", "the same split, on the tool that exists to catch it"},
		{"views.run", "switches off typo detection for the whole `project` stage",
			"an untyped step costs every typo check in the projection"},
		{"views.run", "about the *traversal*, not about the picture",
			"what truncated.depth measures"},
		{"views.run", "never sets it", "a one-hop step is a neighbour query and is not probed"},
		{"views.run", "not guaranteed to appear in `nodes`",
			"an edge's endpoints may be outside the drawn node set"},
		{"views.run", "keyed by the projection *slot*", "where a renderer reads an attribute"},
		{"views.run", "text, number or bool", "a parameter cannot feed an enum or a list field"},
		{"views.run", "Every other comparison against a declared field — equality included",
			"what a condition actually costs"},
		{"views.upsert", "is not_found saying it was removed",
			"a version claim against a view that is gone is refused, not a quiet " +
				"re-creation that discards the arrangement"},
		{"views.upsert", "A view's key is permanent", "there is no rename for a view"},
		{"views.upsert", "types.rename", "a *type* key can be renamed, and an agent told " +
			"otherwise would take the several-call workaround for a one-call job"},
		{"views.get", "A view's key is permanent", "the tool a caller reads a key from says so too"},
	} {
		if !strings.Contains(descriptions[want.tool], want.phrase) {
			t.Errorf("%s does not say %q — %s", want.tool, want.phrase, want.why)
		}
	}

	// Nothing only this repository knows, on every views tool. Task 14
	// swept the renderer catalogue for exactly this and the tools' own
	// prose is written one layer up, where the same mistake is available.
	//
	// **Patterns and not substrings**, which is the correction a review
	// round made after the first version of this guard was measured: a
	// list of the literals that happened to have leaked ("Task 1",
	// "Task 6", "§5") catches those and the numbers they are prefixes
	// of, and lets through every task number and section nobody had
	// leaked yet. A guard against a *kind* of mistake has to describe
	// the kind.
	repositoryOnly := []struct {
		what  string
		re    *regexp.Regexp
		leaks string
	}{
		{"a task number", regexp.MustCompile(`(?i)\btasks?\s+\d+`), "Task 9"},
		{"a spec section", regexp.MustCompile(`§\s*\d`), "§3.2"},
		{"a numbered finding", regexp.MustCompile(`(?i)\bfindings?\s+\d+`), "finding 11"},
		{"a source filename", regexp.MustCompile(`(?i)\b[\w-]+\.(?:go|sql|md)\b`), "positions.go"},
		{"this repository's sub-project word",
			regexp.MustCompile(`(?i)\bsub-projects?\b`), "sub-project 5"},
		{"an open question", regexp.MustCompile(`(?i)\bopen questions?\b|\bO[1-9]\d*\b`), "O3"},
		{"a plan or spec document",
			regexp.MustCompile(`(?i)\bthe (?:plan|spec|design doc)\b`), "the plan"},
	}
	// Each pattern is checked against the leak it is written for, so a
	// regexp that matches nothing at all cannot sit here looking like
	// coverage — the failure mode of the substring list it replaces,
	// one level up.
	for _, guard := range repositoryOnly {
		if !guard.re.MatchString(guard.leaks) {
			t.Errorf("the guard for %s does not match %q, so it guards nothing",
				guard.what, guard.leaks)
		}
	}
	swept := 0
	for name, text := range descriptions {
		if !strings.HasPrefix(name, "views.") {
			continue
		}
		swept++
		for _, guard := range repositoryOnly {
			if found := guard.re.FindString(text); found != "" {
				t.Errorf("%s's description names %q — %s, which an agent has never seen",
					name, found, guard.what)
			}
		}
	}
	if swept == 0 {
		t.Fatal("no views tool description was swept; this test would pass vacuously")
	}
}

// TestAViewWithNoRendererParametersReadsBackAsAnObjectAndNotNull is the
// wire half of the domain's own "{} and never null" rule. The domain
// stores an empty object; this tool builds its own map, and a nil one
// marshals to `null` — so a client reading a view back would have two
// spellings of "no parameters" to handle, which is the exact thing the
// storage rule exists to prevent.
//
// It needs a view with **no** parameters, which is why it is its own
// test: the read-back sweep saves two of them, and a map that is
// unmarshalled into is allocated on the way, so that fixture cannot see
// this at all.
func TestAViewWithNoRendererParametersReadsBackAsAnObjectAndNotNull(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	f.save(t, "plain", questsQuery, "graph")

	got, err := web.MCPViewsGet(ctx, f.deps, f.caller, f.game, web.ViewsGetInput{Key: "plain"})
	if err != nil {
		t.Fatalf("views.get: %v", err)
	}
	if got.RendererParams == nil {
		t.Fatal("renderer_params is a nil map and will marshal as null")
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"renderer_params":{}`) {
		t.Errorf("on the wire: %s", encoded)
	}
}

// TestTheListingFlagsAStaleViewAndNotAHealthyOne is the control the
// walk-through test cannot be: `stale` false is what a listing answers
// whether or not anything computes it, so a fixture with no stale view
// in it cannot tell a working flag from a hard-wired false.
func TestTheListingFlagsAStaleViewAndNotAHealthyOne(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	f.save(t, "healthy", questsQuery, "graph")
	f.save(t, "broken", `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"takes_place_in","to_type":"zone","as":"z"}]}`, "graph")

	// The relation type the second view walks, deleted: the game moved
	// under a document nobody edited, which is what stale means.
	relation, err := f.meta.RelationTypeByKey(ctx, f.game, "takes_place_in")
	if err != nil {
		t.Fatalf("read the relation type: %v", err)
	}
	if err := f.meta.RemoveRelationType(ctx, f.game, relation.ID, false); err != nil {
		t.Fatalf("delete the relation type: %v", err)
	}

	listing, err := web.MCPViewsList(ctx, f.deps, f.caller, f.game, web.ViewsListInput{})
	if err != nil {
		t.Fatalf("views.list: %v", err)
	}
	flags := map[string]bool{}
	for _, item := range listing.Items {
		flags[item.Key] = item.Stale
	}
	if len(flags) != 2 {
		t.Fatalf("listing = %+v, want both views", listing.Items)
	}
	if !flags["broken"] {
		t.Error("the view whose relation type was deleted is not flagged stale")
	}
	if flags["healthy"] {
		t.Error("a view nothing touched is flagged stale: the flag says nothing if it says " +
			"the same for both")
	}
}

// --- The arguments a description promises and the transport has to
// carry ---
//
// Each of the four tests below was written because dropping one
// documented argument on the way into the domain left the whole web
// suite green. The domain tests every one of these behaviours; what
// nothing asserted is that the argument an agent sends *arrives*, which
// is the only thing this layer can be wrong about and the failure a
// caller would meet as a knob that silently does nothing.

// TestARunCarriesItsStalePolicyAndReportsWhatItPruned drives on_stale
// through the tool, and it is two claims in one because the two are
// unobservable apart: best_effort has to reach RunView, and what
// best_effort learned has to reach the caller.
//
// The control is the same view run with no policy at all, which is
// refused — so an answer cannot pass here by being empty, and the
// policy is what makes the difference rather than the query.
func TestARunCarriesItsStalePolicyAndReportsWhatItPruned(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	f.save(t, "route", `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"takes_place_in","to_type":"zone","as":"z"}]}`, "graph")

	// The game moves under a document nobody edited: the relation type
	// the second step walks is gone.
	relation, err := f.meta.RelationTypeByKey(ctx, f.game, "takes_place_in")
	if err != nil {
		t.Fatalf("read the relation type: %v", err)
	}
	if err := f.meta.RemoveRelationType(ctx, f.game, relation.ID, false); err != nil {
		t.Fatalf("delete the relation type: %v", err)
	}

	// The default policy: refused, with the code that says why.
	if _, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.game,
		web.ViewsRunInput{Key: "route"}); err == nil {
		t.Fatal("a stale view ran under the default policy: the control does not hold")
	}

	run, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.game,
		web.ViewsRunInput{Key: "route", OnStale: "best_effort"})
	if err != nil {
		t.Fatalf("best_effort was not honoured: %v", err)
	}
	// What survived the pruning: the quests are still drawn, so this is
	// a picture and not an empty answer that satisfies the next
	// assertion by drawing nothing.
	nodes, ok := run["nodes"].([]views.Node)
	if !ok || len(nodes) == 0 {
		t.Fatalf("best_effort drew nothing: %v", run["nodes"])
	}
	// And what it pruned reaches the caller, which is the other half:
	// a run that quietly drew three-quarters of a picture and said
	// nothing is the silent-wrong-answer this whole surface refuses.
	stale, present := run["stale"]
	if !present {
		t.Fatal("the envelope carried no stale member: best_effort pruned a step and " +
			"told the caller nothing")
	}
	encoded, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal stale: %v", err)
	}
	if !strings.Contains(string(encoded), "takes_place_in") {
		t.Errorf("stale = %s, want the relation type that broke", encoded)
	}
}

// TestARunCarriesItsIncludeFieldsFlag: the flag selects each node's
// declared fields into the envelope, and the same run with it off is the
// control — a test asserting only the presence would pass against a
// transport that hard-wired the flag on.
func TestARunCarriesItsIncludeFieldsFlag(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	f.save(t, "route", questsQuery, "graph")

	with, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.game,
		web.ViewsRunInput{Key: "route", IncludeFields: true})
	if err != nil {
		t.Fatalf("views.run with fields: %v", err)
	}
	nodes, ok := with["nodes"].([]views.Node)
	if !ok || len(nodes) == 0 {
		t.Fatalf("the run drew nothing: %v", with["nodes"])
	}
	found := false
	for _, node := range nodes {
		if _, has := node.Fields["min_level"]; has {
			found = true
		}
	}
	if !found {
		t.Error("include_fields was sent and no node carried its declared fields")
	}

	without, err := web.MCPViewsRun(ctx, f.deps, f.caller, f.game,
		web.ViewsRunInput{Key: "route"})
	if err != nil {
		t.Fatalf("views.run without fields: %v", err)
	}
	for _, node := range without["nodes"].([]views.Node) {
		if len(node.Fields) != 0 {
			t.Errorf("a run that did not ask for fields carried %v: the flag says nothing "+
				"if the answer is the same either way", node.Fields)
		}
	}
}

// TestValidateJudgesTheRendererAndTheParametersTheCallSent is the tool's
// own reason to exist, asserted on the wire.
//
// views.validate's description says naming a renderer "is the only way a
// renderer parameter can be judged". That is a promise about two
// arguments arriving, and both were droppable: without the renderer the
// domain skips the check entirely, and without the parameters it judges
// a call nobody made — the map renderer's default mode requires nothing,
// so an empty map validates whatever the caller sent.
//
// The verdict flips on the parameters alone, with the renderer fixed,
// which is what makes this a test of the binding rather than of the
// catalogue.
func TestValidateJudgesTheRendererAndTheParametersTheCallSent(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()

	// The query projects the field the coordinates are read from, which
	// is what the map renderer's "fields" mode requires of the document.
	const projected = `{"v":1,"from":[{"type":"quest","as":"q"}],
		"project":{"fields":["min_level"]}}`

	// "fields" mode needs two declared number fields named, and this
	// call names neither: the refusal the description promises.
	_, err := web.MCPViewsValidate(ctx, f.deps, f.caller, f.game, web.ViewsValidateInput{
		Query: json.RawMessage(projected), Renderer: "map",
		RendererParams: map[string]any{"coordinate_source": "fields"},
	})
	if err == nil {
		t.Fatal("a map view in \"fields\" mode naming no coordinate fields validated")
	}
	if !strings.Contains(err.Error(), "x_field") {
		t.Errorf("err = %v, want it to name the parameter that is missing", err)
	}

	// The same renderer, the same query, two more parameters: accepted.
	// The verdict moved on the binding, so the binding arrived.
	if _, err := web.MCPViewsValidate(ctx, f.deps, f.caller, f.game, web.ViewsValidateInput{
		Query: json.RawMessage(projected), Renderer: "map",
		RendererParams: map[string]any{"coordinate_source": "fields",
			"x_field": "min_level", "y_field": "min_level"},
	}); err != nil {
		t.Fatalf("the repaired parameters were refused: %v", err)
	}

	// And the same refused parameters with no renderer named: accepted,
	// because there is nothing to judge them against. That is the
	// control for the renderer argument itself — the refusal above is
	// the renderer arriving, not the parameters being wrong on their own.
	if _, err := web.MCPViewsValidate(ctx, f.deps, f.caller, f.game, web.ViewsValidateInput{
		Query:          json.RawMessage(projected),
		RendererParams: map[string]any{"coordinate_source": "fields"},
	}); err != nil {
		t.Fatalf("a query validated on its own was refused: %v", err)
	}
}

// TestAnUpsertRecordsTheCallerWhoMadeIt is the attribution Task 14's
// round reasoned about, asserted where it is put on the wire. The
// domain stores whatever Actor it is handed; what nothing asserted is
// that this surface hands it the caller's own, so an upsert by an agent
// was free to land with no attribution at all.
func TestAnUpsertRecordsTheCallerWhoMadeIt(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	f.save(t, "route", questsQuery, "graph")

	var user, token *uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`SELECT updated_by_user_id, updated_by_token_id FROM views
		 WHERE project_id = $1 AND key = 'route'`, f.game).Scan(&user, &token); err != nil {
		t.Fatalf("read the audit columns: %v", err)
	}
	if token == nil {
		t.Fatal("a view saved by an agent has no updated_by_token_id: the tool dropped " +
			"the caller on the way into the domain")
	}
	if f.caller.TokenID == nil || *token != *f.caller.TokenID {
		t.Errorf("updated_by_token_id = %v, want this call's own token %v", token, f.caller.TokenID)
	}
	if user == nil || *user != f.ownerID {
		t.Errorf("updated_by_user_id = %v, want the token's owner %s", user, f.ownerID)
	}
}

// TestTheViewListingFiltersByRendererAndPagesOnBothSurfaces is a
// transport test and not a re-test of the domain, which covers the pair
// well. What nothing asserted is that the two arguments the description
// promises ever leave this layer: reducing the listing input to its
// limit alone left the whole web suite green, and "a cursor belongs to
// the game and the filter it was issued for" is a wire claim with no
// wire test.
func TestTheViewListingFiltersByRendererAndPagesOnBothSurfaces(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	f.save(t, "one", questsQuery, "graph")
	f.save(t, "two", questsQuery, "graph")
	f.save(t, "atlas", questsQuery, "map")

	only, err := web.MCPViewsList(ctx, f.deps, f.caller, f.game,
		web.ViewsListInput{Renderer: "map"})
	if err != nil {
		t.Fatalf("views.list filtered: %v", err)
	}
	if len(only.Items) != 1 || only.Items[0].Key != "atlas" {
		t.Fatalf("the renderer filter answered %+v, want only the map view", only.Items)
	}
	// The control in the same test: the two graph views really are
	// there, so the assertion above cannot pass on an empty listing.
	whole, err := web.MCPViewsList(ctx, f.deps, f.caller, f.game, web.ViewsListInput{})
	if err != nil {
		t.Fatalf("views.list: %v", err)
	}
	if len(whole.Items) != 3 {
		t.Fatalf("the unfiltered listing answered %+v, want all three", whole.Items)
	}

	// The cursor, through the filter it was issued for.
	page, err := web.MCPViewsList(ctx, f.deps, f.caller, f.game,
		web.ViewsListInput{Renderer: "graph", Limit: 1})
	if err != nil {
		t.Fatalf("views.list paged: %v", err)
	}
	if len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatalf("page one = %+v with cursor %v, want one row and a cursor",
			page.Items, page.NextCursor)
	}
	next, err := web.MCPViewsList(ctx, f.deps, f.caller, f.game,
		web.ViewsListInput{Renderer: "graph", Limit: 1, Cursor: *page.NextCursor})
	if err != nil {
		t.Fatalf("views.list page two: %v", err)
	}
	if len(next.Items) != 1 || next.Items[0].Key == page.Items[0].Key {
		t.Fatalf("page two = %+v, want the other graph view", next.Items)
	}

	// And the claim the description makes about a cursor: it belongs to
	// the filter it was issued for. A page-one cursor replayed against a
	// different renderer is refused rather than answered from a listing
	// the caller is not walking.
	if _, err := web.MCPViewsList(ctx, f.deps, f.caller, f.game,
		web.ViewsListInput{Renderer: "map", Cursor: *page.NextCursor}); err == nil {
		t.Error("a cursor issued for the graph filter was accepted against the map one")
	}
}

// TestTheAssetListingToolPagesWithItsOwnCursor is the gap the REST twin
// hides. The browser's asset listing is its own handler (api_view_assets.go)
// and is paged by its own test; this tool's core is a second
// implementation of the same read, and dropping its cursor and limit
// left the whole web suite green — an agent asking for a page would
// have been handed the whole listing, and the next_cursor it sent back
// would have been ignored in silence.
func TestTheAssetListingToolPagesWithItsOwnCursor(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	all := map[string]bool{}
	for i := 0; i < 3; i++ {
		all[f.upload(t, f.game)] = true
	}

	whole, err := web.MCPViewsListAssets(ctx, f.deps, f.caller, f.game,
		web.ViewsListAssetsInput{})
	if err != nil {
		t.Fatalf("views.list_assets: %v", err)
	}
	if len(whole.Items) != 3 || whole.NextCursor != nil {
		t.Fatalf("the unpaged listing = %d items with cursor %v, want all three and none",
			len(whole.Items), whole.NextCursor)
	}

	page, err := web.MCPViewsListAssets(ctx, f.deps, f.caller, f.game,
		web.ViewsListAssetsInput{Limit: 2})
	if err != nil {
		t.Fatalf("views.list_assets paged: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("page one carried %d assets, want the limit of 2 honoured", len(page.Items))
	}
	if page.NextCursor == nil {
		t.Fatal("page one carried no next_cursor and there is a third asset to reach")
	}
	next, err := web.MCPViewsListAssets(ctx, f.deps, f.caller, f.game,
		web.ViewsListAssetsInput{Limit: 2, Cursor: *page.NextCursor})
	if err != nil {
		t.Fatalf("views.list_assets page two: %v", err)
	}
	if len(next.Items) != 1 {
		t.Fatalf("page two carried %d assets, want the one that was left", len(next.Items))
	}
	// The walk saw each asset once: a cursor that started over would
	// satisfy every count above and hand the same page back forever.
	seen := map[string]bool{}
	for _, item := range append(append([]web.ViewAssetOutput{}, page.Items...), next.Items...) {
		if seen[item.ID] {
			t.Fatalf("asset %s came back on both pages", item.ID)
		}
		seen[item.ID] = true
	}
	if len(seen) != len(all) {
		t.Fatalf("the paged walk saw %d assets, the unpaged listing %d", len(seen), len(all))
	}
}

// TestRemovingAViewSaysSoAndThenTheViewIsGone. views.remove's whole
// answer is one boolean, and nothing asserted it: hard-wiring it to
// false left the web suite green, so an agent could not have told a
// deletion from a refusal.
func TestRemovingAViewSaysSoAndThenTheViewIsGone(t *testing.T) {
	f := newViewsFixture(t)
	ctx := context.Background()
	f.save(t, "route", questsQuery, "graph")

	out, err := web.MCPViewsRemove(ctx, f.deps, f.caller, f.game,
		web.ViewsRemoveInput{Key: "route"})
	if err != nil {
		t.Fatalf("views.remove: %v", err)
	}
	if !out.Removed {
		t.Error("views.remove answered removed false for a view it deleted")
	}
	// And it really is gone, so the boolean is not the only thing being
	// asserted here.
	if _, err := web.MCPViewsGet(ctx, f.deps, f.caller, f.game,
		web.ViewsGetInput{Key: "route"}); err == nil {
		t.Fatal("the view survived views.remove")
	}
	// A key that names no view is not_found rather than a cheerful
	// removed true.
	if _, err := web.MCPViewsRemove(ctx, f.deps, f.caller, f.game,
		web.ViewsRemoveInput{Key: "route"}); err == nil {
		t.Fatal("removing a view twice was accepted the second time")
	}
}
