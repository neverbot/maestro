package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/analysis"
	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/web"
)

// The analysis engine end to end, over **two games that share not one
// vocabulary key**.
//
// That second game is not decoration and not a courtesy to the racing
// genre: genericity is the product, and every isolation assertion below
// runs against a game whose entity types, relation types and traits have
// nothing in common with the first. A code path that special-cased
// "quest" would pass every test in internal/analysis and fail here.
//
// Everything is driven through the tools an agent calls. The refusals in
// TestTheAnalysisRefusalsHoldOverTheTransport go over the mounted MCP
// endpoint itself, because a refusal that only exists in Go is a refusal
// no client has ever met.

// twoGames is the fixture: one MMORPG-shaped game, one racing career,
// one server, one token each.
type twoGames struct {
	srv   *web.Server
	deps  web.MCPDeps
	mmo   uuid.UUID
	race  uuid.UUID
	agent web.Caller
	// racer is the racing game's own token, which every isolation
	// assertion uses: a token bound to a game whose vocabulary shares no
	// key with the other one.
	racer web.Caller
	// admin is an instance admin's token, bound to the racing game. An
	// admin is not exempt from a token's binding and this is what proves
	// it on this surface.
	admin       web.Caller
	mmoSecret   string
	racerSecret string
	adminSecret string
	hub         *realtime.Hub
	mmoSlug     string
	racerSlug   string
}

func newTwoGames(t *testing.T) *twoGames {
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
	hub := realtime.NewHub()
	mm := metamodel.New(pool, hub)
	an := analysis.New(pool, hub)
	srv := web.NewServer(web.Options{
		Version: "test", Config: cfg, Identity: ids, Projects: projSvc,
		Metamodel: mm, Analysis: an, Hub: hub,
	})
	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "designer@studio.com", DisplayName: "Designer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	admin, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "admin@studio.com", DisplayName: "Admin", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser admin: %v", err)
	}
	if err := ids.SetAdmin(ctx, admin.ID, true); err != nil {
		t.Fatalf("SetAdmin: %v", err)
	}
	mmo, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("create the MMORPG: %v", err)
	}
	race, err := projSvc.Create(ctx, "le-mans", "Le Mans", owner.ID)
	if err != nil {
		t.Fatalf("create the racing career: %v", err)
	}
	mint := func(project, user uuid.UUID, label string) (web.Caller, string) {
		t.Helper()
		secret, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
			ProjectID: project, UserID: user, Label: label,
		})
		if err != nil {
			t.Fatalf("CreateAPIToken %s: %v", label, err)
		}
		caller, err := web.CallerForToken(ctx, ids, secret)
		if err != nil {
			t.Fatalf("CallerForToken %s: %v", label, err)
		}
		return caller, secret
	}
	agent, mmoSecret := mint(mmo.ID, owner.ID, "azeroth agent")
	racer, racerSecret := mint(race.ID, owner.ID, "le mans agent")
	adminCaller, adminSecret := mint(race.ID, admin.ID, "an admin's le mans token")

	w := &twoGames{
		srv: srv, deps: web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: mm, Analysis: an},
		mmo: mmo.ID, race: race.ID,
		agent: agent, racer: racer, admin: adminCaller,
		mmoSecret: mmoSecret, racerSecret: racerSecret, adminSecret: adminSecret,
		hub: hub, mmoSlug: mmo.Slug, racerSlug: race.Slug,
	}
	w.seedMMORPG(t)
	w.seedRacingCareer(t)
	return w
}

// --- Step 1: an MMORPG-shaped game, seeded over the tools ---

// mmoQuestChain is the eight quests a route walks, in order, each
// unlocking the next. It is the progression the later tests break in
// three different ways.
//
// The green baseline is not a test of its own: it is the opening
// assertion of TestATypoFixMakesARouteStaleAndAReCheckMakesItGreen,
// which refuses to go on unless the route is green the moment it is
// first checked. That is deliberate rather than an omission — a
// standalone happy-path test would assert the same call and then be
// the one test nobody reads when a break test starts failing, whereas
// folded in it is a precondition every one of those runs re-proves.
var mmoQuestChain = []string{
	"q-tutorial", "q-hogger", "q-westfall", "q-defias",
	"q-deadmines", "q-vancleef", "q-stormwind", "q-duskwood",
}

func (w *twoGames) seedMMORPG(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	for _, spec := range []web.TypesUpsertInput{
		{Key: "class", Label: "Class", LabelPlural: "Classes"},
		{Key: "zone", Label: "Zone", LabelPlural: "Zones"},
		{Key: "quest", Label: "Quest", LabelPlural: "Quests"},
		{Key: "talent", Label: "Talent", LabelPlural: "Talents"},
		// The type nothing gates: it is content a designer writes and no
		// player "reaches", so an unreachable report that named every
		// piece of lore in the game would be a report nobody reads.
		{Key: "lore", Label: "Lore", LabelPlural: "Lore"},
	} {
		if _, err := web.MCPTypesUpsert(ctx, w.deps, w.agent, w.mmo, spec); err != nil {
			t.Fatalf("types.upsert %s: %v", spec.Key, err)
		}
	}

	for _, spec := range []web.RelationTypesUpsertInput{
		{Key: "requires", Label: "Requires", AnalysisTraits: []string{"prerequisite_of"},
			SourceTypeKeys: []string{"quest", "talent"}, TargetTypeKeys: []string{"quest", "talent"}},
		{Key: "unlocks", Label: "Unlocks", AnalysisTraits: []string{"unlocks"},
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"quest", "talent"}},
		{Key: "connects_to", Label: "Connects to", AnalysisTraits: []string{"symmetric"},
			SourceTypeKeys: []string{"zone"}, TargetTypeKeys: []string{"zone"}},
		{Key: "contains", Label: "Contains", AnalysisTraits: []string{"containment"},
			SourceTypeKeys: []string{"zone"}, TargetTypeKeys: []string{"quest"}},
		{Key: "follows", Label: "Follows", AnalysisTraits: []string{"ordering"},
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"quest"}},
		{Key: "is_illustrated_by", Label: "Is illustrated by",
			AnalysisTraits: []string{"annotation"},
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"lore"}},
		// The type that carries the flagged edge below. It is declared a
		// gate, deliberately: an edge of a type the engine does not walk
		// is an edge the engine never *follows*, so flagging one would
		// leave `invalid_edges_followed` at zero and the whole
		// invalid-edge decision unexercised. A type declared inert or
		// undeclared would have made this fixture too small to
		// distinguish the two policies -- which is a defect this
		// repository has produced before, and which the assertion on
		// that count is what caught here.
		{Key: "reveals", Label: "Reveals", AnalysisTraits: []string{"unlocks"},
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"quest"}},
	} {
		if _, err := web.MCPRelationTypesUpsert(ctx, w.deps, w.agent, w.mmo, spec); err != nil {
			t.Fatalf("relation_types.upsert %s: %v", spec.Key, err)
		}
	}

	items := []web.EntityItemInput{
		{TypeKey: "class", Key: "mage", Name: "Mage"},
		{TypeKey: "class", Key: "warrior", Name: "Warrior"},
	}
	for _, zone := range []string{"elwynn", "westfall", "duskwood", "redridge", "stranglethorn"} {
		items = append(items, web.EntityItemInput{TypeKey: "zone", Key: zone, Name: zone})
	}
	for _, key := range mmoQuestChain {
		items = append(items, web.EntityItemInput{TypeKey: "quest", Key: key, Name: key})
	}
	for _, key := range []string{
		// The three-cycle.
		"q-loop-a", "q-loop-b", "q-loop-c",
		// The self-loop.
		"q-ouroboros",
		// The two quests nothing can unlock, each gated behind the cycle.
		"q-behind-the-loop", "q-behind-it-too",
		// The entity whose only edge is an annotation: an orphan.
		"q-decorated",
		// The entity whose only edge is flagged invalid: **not** an
		// orphan, and not unreachable either.
		"q-flagged", "q-flagger",
		// Filler, so the game is a game and not a diagram of one.
		"q-filler-1", "q-filler-2", "q-filler-3", "q-filler-4",
		"q-filler-5", "q-filler-6", "q-filler-7",
	} {
		items = append(items, web.EntityItemInput{TypeKey: "quest", Key: key, Name: key})
	}
	for _, key := range []string{"t-fireball", "t-frostbolt", "t-arcane", "t-fury", "t-arms"} {
		items = append(items, web.EntityItemInput{TypeKey: "talent", Key: key, Name: key})
	}
	for _, key := range []string{"l-legend", "l-history", "l-song"} {
		items = append(items, web.EntityItemInput{TypeKey: "lore", Key: key, Name: key})
	}
	if _, err := web.MCPEntitiesUpsert(ctx, w.deps, w.agent, w.mmo,
		web.EntitiesUpsertInput{Items: items}); err != nil {
		t.Fatalf("entities.upsert: %v", err)
	}

	var edges []web.RelationItemInput
	quest := func(key string) web.RefInput { return web.RefInput{TypeKey: "quest", Key: key} }
	zone := func(key string) web.RefInput { return web.RefInput{TypeKey: "zone", Key: key} }
	talent := func(key string) web.RefInput { return web.RefInput{TypeKey: "talent", Key: key} }
	lore := func(key string) web.RefInput { return web.RefInput{TypeKey: "lore", Key: key} }
	edge := func(typeKey string, source, target web.RefInput) {
		edges = append(edges, web.RelationItemInput{
			TypeKey: typeKey, Source: source, Target: target,
		})
	}

	// The main story: each quest unlocks the next, and each follows the
	// one before it, so the route below is both reachable and in order.
	for i := 0; i+1 < len(mmoQuestChain); i++ {
		edge("unlocks", quest(mmoQuestChain[i]), quest(mmoQuestChain[i+1]))
		edge("follows", quest(mmoQuestChain[i]), quest(mmoQuestChain[i+1]))
	}
	// The prerequisite three-cycle: a gate nobody can open.
	edge("requires", quest("q-loop-a"), quest("q-loop-b"))
	edge("requires", quest("q-loop-b"), quest("q-loop-c"))
	edge("requires", quest("q-loop-c"), quest("q-loop-a"))
	// The self-loop, which is a cycle of length one and the shape an
	// earlier walk could not show at all.
	edge("requires", quest("q-ouroboros"), quest("q-ouroboros"))
	// The two quests behind the cycle.
	edge("requires", quest("q-behind-the-loop"), quest("q-loop-a"))
	edge("requires", quest("q-behind-it-too"), quest("q-loop-b"))
	// The legitimate connects_to loop: a map is not a hierarchy, and
	// three zones joined in a ring is a map that works.
	edge("connects_to", zone("elwynn"), zone("westfall"))
	edge("connects_to", zone("westfall"), zone("duskwood"))
	edge("connects_to", zone("duskwood"), zone("elwynn"))
	edge("connects_to", zone("redridge"), zone("elwynn"))
	edge("connects_to", zone("stranglethorn"), zone("duskwood"))
	// Containment: a zone holds its quests.
	for i, key := range mmoQuestChain {
		edge("contains", zone([]string{"elwynn", "westfall", "duskwood",
			"redridge", "stranglethorn"}[i%5]), quest(key))
	}
	// Talents, gated by quests.
	for i, key := range []string{"t-fireball", "t-frostbolt", "t-arcane", "t-fury", "t-arms"} {
		edge("unlocks", quest(mmoQuestChain[i]), talent(key))
	}
	// The annotation edge, and nothing else, on q-decorated.
	edge("is_illustrated_by", quest("q-decorated"), lore("l-legend"))
	edge("is_illustrated_by", quest("q-tutorial"), lore("l-history"))
	// The edge that will be flagged invalid: q-flagged's only edge, of a
	// type the engine walks, so it is followed by default and counted.
	edge("reveals", quest("q-flagger"), quest("q-flagged"))
	// Filler edges, so the walk has work to do.
	for i := 1; i <= 7; i++ {
		edge("unlocks", quest("q-tutorial"), quest(fmt.Sprintf("q-filler-%d", i)))
	}
	if _, err := web.MCPRelationsUpsert(ctx, w.deps, w.agent, w.mmo,
		web.RelationsUpsertInput{Items: edges}); err != nil {
		t.Fatalf("relations.upsert: %v", err)
	}

	// The flagged edge, made invalid the way the product makes one: a
	// required field added to its type's schema, so an existing row stops
	// fitting its own declaration. Forging `invalid = true` by hand would
	// test a state the writer cannot produce.
	current, err := web.MCPRelationTypesGet(ctx, w.deps, w.agent, w.mmo,
		web.RelationTypesGetInput{Key: "reveals"})
	if err != nil {
		t.Fatalf("relation_types.get reveals: %v", err)
	}
	version := current.Version
	if _, err := web.MCPRelationTypesUpsert(ctx, w.deps, w.agent, w.mmo,
		web.RelationTypesUpsertInput{
			Key: "reveals", Label: "Reveals", ExpectedVersion: &version,
			// The traits are carried over: an upsert replaces the row, so
			// redeclaring without them would clear the column and the type
			// would stop being a gate -- a different fixture from the one
			// this seeds.
			AnalysisTraits: []string{"unlocks"},
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"quest"},
			Schema: []web.FieldInput{{Key: "why", Type: "text", Required: true}},
		}); err != nil {
		t.Fatalf("invalidate the reveals edges: %v", err)
	}
}

// --- Step 2: a racing career, in the same instance ---

// It exists for one reason: **genericity is the product.** Not one key
// below appears in the game above, and every analysis is expected to
// find something here through the same code path.
func (w *twoGames) seedRacingCareer(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	for _, spec := range []web.TypesUpsertInput{
		{Key: "driver", Label: "Driver", LabelPlural: "Drivers"},
		{Key: "car", Label: "Car", LabelPlural: "Cars"},
		{Key: "circuit", Label: "Circuit", LabelPlural: "Circuits"},
		{Key: "race", Label: "Race", LabelPlural: "Races"},
		{Key: "championship", Label: "Championship", LabelPlural: "Championships"},
	} {
		if _, err := web.MCPTypesUpsert(ctx, w.deps, w.racer, w.race, spec); err != nil {
			t.Fatalf("types.upsert %s: %v", spec.Key, err)
		}
	}
	for _, spec := range []web.RelationTypesUpsertInput{
		// `groups` and not `contains`: **not one key below appears in the
		// game above**, which is what makes every assertion over this
		// game evidence about a second vocabulary rather than about a
		// second copy of the first. A shared key would weaken every one
		// of them silently, so the disjointness is asserted as well as
		// intended.
		{Key: "groups", Label: "Groups", AnalysisTraits: []string{"containment"},
			SourceTypeKeys: []string{"championship"}, TargetTypeKeys: []string{"race"}},
		{Key: "next_race", Label: "Next race", AnalysisTraits: []string{"unlocks"},
			SourceTypeKeys: []string{"race"}, TargetTypeKeys: []string{"race"}},
	} {
		if _, err := web.MCPRelationTypesUpsert(ctx, w.deps, w.racer, w.race, spec); err != nil {
			t.Fatalf("relation_types.upsert %s: %v", spec.Key, err)
		}
	}
	items := []web.EntityItemInput{
		{TypeKey: "driver", Key: "rookie", Name: "Rookie"},
		{TypeKey: "car", Key: "gt3", Name: "GT3"},
		{TypeKey: "circuit", Key: "sarthe", Name: "La Sarthe"},
		{TypeKey: "championship", Key: "season-one", Name: "Season One"},
	}
	for _, key := range []string{"r-opener", "r-second", "r-final",
		"r-loop-x", "r-loop-y", "r-behind"} {
		items = append(items, web.EntityItemInput{TypeKey: "race", Key: key, Name: key})
	}
	if _, err := web.MCPEntitiesUpsert(ctx, w.deps, w.racer, w.race,
		web.EntitiesUpsertInput{Items: items}); err != nil {
		t.Fatalf("entities.upsert: %v", err)
	}
	race := func(key string) web.RefInput { return web.RefInput{TypeKey: "race", Key: key} }
	edges := []web.RelationItemInput{
		{TypeKey: "next_race", Source: race("r-opener"), Target: race("r-second")},
		{TypeKey: "next_race", Source: race("r-second"), Target: race("r-final")},
		// The racing game's own two-cycle, so it has findings of its own
		// and the "both games produce findings" claim is not one game's.
		{TypeKey: "next_race", Source: race("r-loop-x"), Target: race("r-loop-y")},
		{TypeKey: "next_race", Source: race("r-loop-y"), Target: race("r-loop-x")},
		{TypeKey: "next_race", Source: race("r-loop-x"), Target: race("r-behind")},
		{TypeKey: "groups", Source: web.RefInput{TypeKey: "championship", Key: "season-one"},
			Target: race("r-opener")},
	}
	if _, err := web.MCPRelationsUpsert(ctx, w.deps, w.racer, w.race,
		web.RelationsUpsertInput{Items: edges}); err != nil {
		t.Fatalf("relations.upsert: %v", err)
	}
}

// --- helpers ---

func (w *twoGames) cycles(t *testing.T, caller web.Caller, game uuid.UUID) analysis.CyclesResult {
	t.Helper()
	got, err := web.MCPAnalysisCycles(context.Background(), w.deps, caller, game,
		web.AnalysisCyclesInput{})
	if err != nil {
		t.Fatalf("analysis.cycles: %v", err)
	}
	return got
}

func (w *twoGames) unreachable(t *testing.T, caller web.Caller, game uuid.UUID,
	in web.AnalysisUnreachableInput) analysis.UnreachableResult {
	t.Helper()
	got, err := web.MCPAnalysisUnreachable(context.Background(), w.deps, caller, game, in)
	if err != nil {
		t.Fatalf("analysis.unreachable: %v", err)
	}
	return got
}

func (w *twoGames) orphans(t *testing.T, caller web.Caller, game uuid.UUID) analysis.OrphansResult {
	t.Helper()
	got, err := web.MCPAnalysisOrphans(context.Background(), w.deps, caller, game,
		web.AnalysisOrphansInput{})
	if err != nil {
		t.Fatalf("analysis.orphans: %v", err)
	}
	return got
}

// writeMainStoryRoute writes the eight-step route over the main story.
func (w *twoGames) writeMainStoryRoute(t *testing.T) analysis.Route {
	t.Helper()
	steps := make([]web.RoutesStepInput, 0, len(mmoQuestChain))
	for _, key := range mmoQuestChain {
		steps = append(steps, web.RoutesStepInput{EntityType: "quest", Key: key})
	}
	route, err := web.MCPRoutesUpsert(context.Background(), w.deps, w.agent, w.mmo,
		web.RoutesUpsertInput{
			Key: "main-story", Name: "The main story", Steps: steps,
			ExpectedVersion: new(int32),
		})
	if err != nil {
		t.Fatalf("routes.upsert: %v", err)
	}
	return route
}

func (w *twoGames) checkMainStory(t *testing.T) analysis.RouteCheck {
	t.Helper()
	got, err := web.MCPRoutesCheck(context.Background(), w.deps, w.agent, w.mmo,
		web.RoutesCheckInput{Key: "main-story"})
	if err != nil {
		t.Fatalf("routes.check: %v", err)
	}
	return got
}

func cycleContains(cycles []analysis.Cycle, keys ...string) bool {
	want := map[string]bool{}
	for _, key := range keys {
		want[key] = true
	}
	for _, cycle := range cycles {
		if len(cycle.Entities) != len(want) {
			continue
		}
		matched := 0
		for _, node := range cycle.Entities {
			if want[node.Key] {
				matched++
			}
		}
		if matched == len(want) {
			return true
		}
	}
	return false
}

func findingKeys(findings []analysis.Finding) map[string]analysis.Reason {
	out := make(map[string]analysis.Reason, len(findings))
	for _, finding := range findings {
		out[finding.Key] = finding.Reason
	}
	return out
}

func orphanKeys(orphans []analysis.Orphan) map[string]bool {
	out := make(map[string]bool, len(orphans))
	for _, orphan := range orphans {
		out[orphan.Key] = true
	}
	return out
}

// --- Step 3: the four analyses, each asserted on both halves ---

// TestTheFourAnalysesFindWhatIsWrongAndNotWhatIsNot.
//
// Each of the four is asserted twice: on what it must find, and on what
// it must **not**. The second half is where every one of these
// analyses is most easily wrong, because a walk that followed one edge
// too many produces a longer list that still contains the right answer.
func TestTheFourAnalysesFindWhatIsWrongAndNotWhatIsNot(t *testing.T) {
	t.Parallel()
	w := newTwoGames(t)

	// Cycles: the three-cycle and the self-loop, and **not** the
	// legitimate connects_to ring. A map joined in a circle is a map that
	// works; reporting it would send a designer to cut a road.
	got := w.cycles(t, w.agent, w.mmo)
	if !cycleContains(got.Cycles, "q-loop-a", "q-loop-b", "q-loop-c") {
		t.Errorf("the three-cycle is not reported: %+v", got.Cycles)
	}
	if !cycleContains(got.Cycles, "q-ouroboros") {
		t.Errorf("the self-loop is not reported as a cycle of length one: %+v", got.Cycles)
	}
	for _, cycle := range got.Cycles {
		for _, edge := range cycle.Edges {
			if edge.RelationType == "connects_to" {
				t.Errorf("a connects_to ring is reported as a prerequisite cycle: %+v", cycle)
			}
			if edge.RelationType == "" {
				t.Error("a cycle edge names no relation type, which is the field this " +
					"whole widening of internal/graph exists for")
			}
		}
	}
	if got.EdgesWalked == 0 {
		t.Error("the cycle report walked no edge, so its findings came from nowhere")
	}

	// Unreachable: the two quests behind the loop, and **not** the lore
	// (ignored) and **not** the quest behind the flagged edge, which is
	// followed by default.
	report := w.unreachable(t, w.agent, w.mmo, web.AnalysisUnreachableInput{
		IgnoreEntityTypes: []string{"lore"},
		MaxResults:        analysis.MaxMaxResults,
		Limit:             200,
	})
	reasons := findingKeys(report.Findings)
	for _, key := range []string{"q-behind-the-loop", "q-behind-it-too"} {
		if _, found := reasons[key]; !found {
			t.Errorf("%s is gated behind a prerequisite loop and is not reported "+
				"unreachable: %v", key, reasons)
		}
	}
	for _, key := range []string{"l-legend", "l-history", "l-song"} {
		if _, found := reasons[key]; found {
			t.Errorf("%s is lore and ignore_entity_types excluded its type, so it must "+
				"not be a finding", key)
		}
	}
	if _, found := reasons["q-flagged"]; found {
		t.Error("q-flagged's only edge is flagged invalid and this engine follows " +
			"invalid edges by default: a field-schema edit on `reveals` must not make " +
			"a quest report as unreachable")
	}
	if report.ReachableTotal == 0 || len(report.PerType) == 0 {
		t.Errorf("the report counts no reachable entity and no type: %+v", report)
	}
	if report.InvalidEdgesFollowed == 0 {
		t.Error("the report followed no invalid edge and says so, but one exists: " +
			"silence about a real limit is the correct-and-unasserted defect with the " +
			"sign flipped")
	}

	// Orphans: the annotation-only entity, and **not** the one whose only
	// edge is flagged. Calling the second an orphan would send a designer
	// to delete content that has edges.
	orphaned := orphanKeys(w.orphans(t, w.agent, w.mmo).Findings)
	if !orphaned["q-decorated"] {
		t.Errorf("q-decorated's only edge is an annotation and it is not reported as an "+
			"orphan: %v", orphaned)
	}
	if orphaned["q-flagged"] {
		t.Error("q-flagged has an edge, flagged invalid, and is reported as an orphan: " +
			"an entity whose only edge is invalid is not an orphan")
	}
	if orphaned["q-tutorial"] {
		t.Error("the first quest of the main story is reported as an orphan")
	}

	// Routes: eight steps over the main story, ok throughout.
	w.writeMainStoryRoute(t)
	check := w.checkMainStory(t)
	if !check.Holds || check.StepsOK != len(mmoQuestChain) {
		t.Fatalf("the main story does not hold: %+v", check.Steps)
	}
}

// --- Step 4: the negative halves, all five at once ---

// TestTheNegativeHalvesAreCountedAndNotMerelyEmpty is the table from the
// plan's own preamble, asserted in one place.
//
// **An empty findings list is what a broken engine returns too.** So
// every clean answer here is asserted twice: empty, *and* over a
// non-zero count of work actually done. A walk that started nowhere and
// found nothing produces the first and never the second.
func TestTheNegativeHalvesAreCountedAndNotMerelyEmpty(t *testing.T) {
	t.Parallel()
	w := newTwoGames(t)
	ctx := context.Background()

	// A hand-built clean game: same instance, its own vocabulary, no
	// loops, nothing unreachable, nothing orphaned.
	clean, err := w.deps.Projects.Create(ctx, "clean", "Clean", w.agent.UserID)
	if err != nil {
		t.Fatalf("create the clean game: %v", err)
	}
	cleanAgent := w.mintFor(t, clean.ID, "clean agent")
	if _, err := web.MCPTypesUpsert(ctx, w.deps, cleanAgent, clean.ID,
		web.TypesUpsertInput{Key: "step", Label: "Step", LabelPlural: "Steps"}); err != nil {
		t.Fatalf("types.upsert: %v", err)
	}
	if _, err := web.MCPRelationTypesUpsert(ctx, w.deps, cleanAgent, clean.ID,
		web.RelationTypesUpsertInput{Key: "leads_to", Label: "Leads to",
			AnalysisTraits: []string{"unlocks"},
			SourceTypeKeys: []string{"step"}, TargetTypeKeys: []string{"step"}}); err != nil {
		t.Fatalf("relation_types.upsert: %v", err)
	}
	chain := []string{"one", "two", "three", "four", "five"}
	items := make([]web.EntityItemInput, 0, len(chain))
	for _, key := range chain {
		items = append(items, web.EntityItemInput{TypeKey: "step", Key: key, Name: key})
	}
	if _, err := web.MCPEntitiesUpsert(ctx, w.deps, cleanAgent, clean.ID,
		web.EntitiesUpsertInput{Items: items}); err != nil {
		t.Fatalf("entities.upsert: %v", err)
	}
	var edges []web.RelationItemInput
	for i := 0; i+1 < len(chain); i++ {
		edges = append(edges, web.RelationItemInput{TypeKey: "leads_to",
			Source: web.RefInput{TypeKey: "step", Key: chain[i]},
			Target: web.RefInput{TypeKey: "step", Key: chain[i+1]}})
	}
	if _, err := web.MCPRelationsUpsert(ctx, w.deps, cleanAgent, clean.ID,
		web.RelationsUpsertInput{Items: edges}); err != nil {
		t.Fatalf("relations.upsert: %v", err)
	}

	// 1. An acyclic game reports zero cycles **and says how many edges it
	//    walked**.
	cycles := w.cycles(t, cleanAgent, clean.ID)
	if len(cycles.Cycles) != 0 || len(cycles.ContainmentCycles) != 0 {
		t.Errorf("an acyclic game reports cycles: %+v", cycles)
	}
	if cycles.EdgesWalked == 0 || cycles.SeedTotal == 0 {
		t.Errorf("the acyclic report walked %d edges from %d seeds: zero of either makes "+
			"\"no cycles\" the answer of a walk that never ran",
			cycles.EdgesWalked, cycles.SeedTotal)
	}

	// 2. Everything is reachable, **and every entity was counted**.
	reach := w.unreachable(t, cleanAgent, clean.ID, web.AnalysisUnreachableInput{})
	if len(reach.Findings) != 0 {
		t.Errorf("a game where everything is reachable reports %+v", reach.Findings)
	}
	if reach.ReachableTotal != len(chain) {
		t.Errorf("reachable_total = %d, want %d: an empty findings list over a game "+
			"nothing was counted in is the same JSON as a clean bill of health",
			reach.ReachableTotal, len(chain))
	}
	if reach.EdgesWalked == 0 {
		t.Error("the reachability report walked no edge")
	}

	// 3. No orphans, **and every entity considered**.
	orphans, err := web.MCPAnalysisOrphans(ctx, w.deps, cleanAgent, clean.ID,
		web.AnalysisOrphansInput{})
	if err != nil {
		t.Fatalf("analysis.orphans: %v", err)
	}
	if len(orphans.Findings) != 0 {
		t.Errorf("a game with no orphans reports %+v", orphans.Findings)
	}
	if orphans.ConsideredTotal != int64(len(chain)) {
		t.Errorf("considered_total = %d, want %d", orphans.ConsideredTotal, len(chain))
	}

	// 4. A route that holds, **and it names its seed set and its step
	//    count**.
	steps := make([]web.RoutesStepInput, 0, len(chain))
	for _, key := range chain {
		steps = append(steps, web.RoutesStepInput{EntityType: "step", Key: key})
	}
	if _, err := web.MCPRoutesUpsert(ctx, w.deps, cleanAgent, clean.ID, web.RoutesUpsertInput{
		Key: "the-walk", Name: "The walk", Steps: steps, ExpectedVersion: new(int32),
	}); err != nil {
		t.Fatalf("routes.upsert: %v", err)
	}
	check, err := web.MCPRoutesCheck(ctx, w.deps, cleanAgent, clean.ID,
		web.RoutesCheckInput{Key: "the-walk"})
	if err != nil {
		t.Fatalf("routes.check: %v", err)
	}
	if !check.Holds || check.StepsChecked != len(chain) {
		t.Errorf("the clean route does not hold: %+v", check)
	}
	if check.Seeds.Total == 0 {
		t.Error("the verdict names no seed set, so \"it holds\" rests on a walk that " +
			"started nowhere")
	}

	// 5. A game that declared nothing is **refused**, carrying the
	//    catalogue, on every analysis that walks an edge.
	bare, err := w.deps.Projects.Create(ctx, "bare", "Bare", w.agent.UserID)
	if err != nil {
		t.Fatalf("create the bare game: %v", err)
	}
	bareAgent := w.mintFor(t, bare.ID, "bare agent")
	if _, err := web.MCPTypesUpsert(ctx, w.deps, bareAgent, bare.ID,
		web.TypesUpsertInput{Key: "thing", Label: "Thing", LabelPlural: "Things"}); err != nil {
		t.Fatalf("types.upsert: %v", err)
	}
	if _, err := web.MCPRelationTypesUpsert(ctx, w.deps, bareAgent, bare.ID,
		web.RelationTypesUpsertInput{Key: "relates_to", Label: "Relates to",
			SourceTypeKeys: []string{"thing"}, TargetTypeKeys: []string{"thing"}}); err != nil {
		t.Fatalf("relation_types.upsert: %v", err)
	}
	if _, err := web.MCPAnalysisCycles(ctx, w.deps, bareAgent, bare.ID,
		web.AnalysisCyclesInput{}); err == nil {
		t.Error("an analysis over a game that declared nothing answered rather than " +
			"refusing: a clean bill of health from an engine with nothing to read is " +
			"the worst output this package can produce")
	} else if !strings.Contains(err.Error(), "semantics_undeclared") ||
		!strings.Contains(err.Error(), "relates_to") {
		t.Errorf("the refusal is %q; it must carry the code and the catalogue a caller "+
			"needs to fix it", err)
	}
	// The control, and the one asymmetry: orphans **answers** an
	// undeclared game, because a game with no annotation type is a
	// perfectly meaningful input rather than an engine with nothing to
	// read.
	if _, err := web.MCPAnalysisOrphans(ctx, w.deps, bareAgent, bare.ID,
		web.AnalysisOrphansInput{}); err != nil {
		t.Errorf("analysis.orphans refused an undeclared game: %v", err)
	}
}

// mintFor makes a token for a game created inside a test.
func (w *twoGames) mintFor(t *testing.T, project uuid.UUID, label string) web.Caller {
	t.Helper()
	secret, _, err := w.deps.Identity.CreateAPIToken(context.Background(),
		identity.CreateAPITokenRequest{
			ProjectID: project, UserID: w.agent.UserID, Label: label,
		})
	if err != nil {
		t.Fatalf("CreateAPIToken %s: %v", label, err)
	}
	caller, err := web.CallerForToken(context.Background(), w.deps.Identity, secret)
	if err != nil {
		t.Fatalf("CallerForToken %s: %v", label, err)
	}
	return caller
}

// --- Step 2 (assertion): genericity is the product ---

// TestTheSameEngineAnalysesAnMMORPGAndARacingCareer.
//
// The two games share **not one vocabulary key**: `quest` against
// `race`, `requires` against `next_race`, `takes_place_in` against
// `contains`. Both produce findings, through the same call, and nothing
// in the engine distinguishes them.
func TestTheSameEngineAnalysesAnMMORPGAndARacingCareer(t *testing.T) {
	t.Parallel()
	w := newTwoGames(t)

	mmoCycles := w.cycles(t, w.agent, w.mmo)
	raceCycles := w.cycles(t, w.racer, w.race)
	if len(mmoCycles.Cycles) == 0 {
		t.Error("the MMORPG reports no cycle")
	}
	if len(raceCycles.Cycles) == 0 {
		t.Error("the racing career reports no cycle, so this test asserts nothing about " +
			"a second vocabulary")
	}
	if !cycleContains(raceCycles.Cycles, "r-loop-x", "r-loop-y") {
		t.Errorf("the racing career's own two-cycle is not the one reported: %+v",
			raceCycles.Cycles)
	}

	// The vocabularies really are disjoint, asserted rather than assumed:
	// a fixture that accidentally shared a key would make every
	// assertion above weaker without saying so.
	mmoTypes := map[string]bool{}
	for _, entry := range mmoCycles.SemanticsSource {
		mmoTypes[entry.Key] = true
	}
	shared := 0
	for _, entry := range raceCycles.SemanticsSource {
		if mmoTypes[entry.Key] {
			shared++
		}
	}
	if shared != 0 {
		t.Errorf("%d relation type keys are shared between the two games; this test's "+
			"whole claim is that they are not", shared)
	}

	// And the racing game finds its own unreachable content.
	raceReach := w.unreachable(t, w.racer, w.race, web.AnalysisUnreachableInput{})
	if len(raceReach.Findings) == 0 {
		t.Error("the racing career reports nothing unreachable, though a race sits " +
			"behind its own loop")
	}
}

// --- Step 5: the design counter, end to end ---

// TestATypoFixMakesARouteStaleAndAReCheckMakesItGreen.
//
// The counter is **coarse and that is accepted**: any write anywhere in
// the game moves it, so fixing one word in one entity's name marks every
// route stale. The alternative is a per-route dependency set maintained
// on every write, which is wrong the moment a *new* relation makes a
// previously irrelevant entity relevant — and a staleness signal that is
// subtly wrong is worse than one that is bluntly right, because
// re-checking is cheap and believing a stale green is not.
//
// The acceptance is asserted as behaviour here rather than left in a
// comment, which is the difference between a decision and an intention.
func TestATypoFixMakesARouteStaleAndAReCheckMakesItGreen(t *testing.T) {
	t.Parallel()
	w := newTwoGames(t)
	ctx := context.Background()
	w.writeMainStoryRoute(t)

	if check := w.checkMainStory(t); !check.Holds || check.Status != analysis.RouteChecked {
		t.Fatalf("the route is not green immediately after a check: %+v", check.Status)
	}

	// One word, in one entity the route does not even name. The version
	// is read and sent, because an upsert is a compare-and-set: an
	// unguarded rewrite of an existing row is refused, and a fixture that
	// sent none would be writing nothing and asserting staleness against
	// a game that never moved -- which is exactly what this test caught
	// on its first run.
	current, err := web.MCPEntitiesGet(ctx, w.deps, w.agent, w.mmo,
		web.EntitiesGetInput{TypeKey: "lore", Key: "l-song"})
	if err != nil {
		t.Fatalf("entities.get: %v", err)
	}
	version := current.Version
	if _, err := web.MCPEntitiesUpsert(ctx, w.deps, w.agent, w.mmo, web.EntitiesUpsertInput{
		Items: []web.EntityItemInput{{TypeKey: "lore", Key: "l-song", Name: "The Song",
			ExpectedVersion: &version}},
	}); err != nil {
		t.Fatalf("entities.upsert: %v", err)
	}

	route, err2 := web.MCPRoutesGet(ctx, w.deps, w.agent, w.mmo,
		web.RoutesGetInput{Key: "main-story"})
	if err2 != nil {
		t.Fatalf("routes.get: %v", err2)
	}
	if route.Status != analysis.RouteStale {
		t.Fatalf("status = %q after an unrelated write, want %q: the counter is coarse "+
			"and this is what that means", route.Status, analysis.RouteStale)
	}
	// **Stale is neither green nor red**: the stored verdict is still
	// there and still says the route held, and what has changed is that
	// it is about a game that has since moved.
	if len(route.LastCheck) == 0 {
		t.Error("a stale route lost its stored verdict; stale is not broken")
	}

	if check := w.checkMainStory(t); !check.Holds || check.Status != analysis.RouteChecked {
		t.Fatalf("a re-check did not make the route green again: %+v", check.Status)
	}
}

// --- Step 6: deleting an entity a route names ---

// TestDeletingAStepsEntityLeavesATombstoneAndAStaleRoute — three facts,
// three assertions: the step survives with its keys, the check reports
// missing_entity, and the route is stale as well.
func TestDeletingAStepsEntityLeavesATombstoneAndAStaleRoute(t *testing.T) {
	t.Parallel()
	w := newTwoGames(t)
	ctx := context.Background()
	w.writeMainStoryRoute(t)
	if check := w.checkMainStory(t); !check.Holds {
		t.Fatalf("the route did not hold before the deletion: %+v", check.Steps)
	}

	deleted := mmoQuestChain[3]
	if _, err := web.MCPEntitiesRemove(ctx, w.deps, w.agent, w.mmo,
		web.EntitiesRemoveInput{TypeKey: "quest", Key: deleted}); err != nil {
		t.Fatalf("entities.remove: %v", err)
	}

	route, err := web.MCPRoutesGet(ctx, w.deps, w.agent, w.mmo,
		web.RoutesGetInput{Key: "main-story"})
	if err != nil {
		t.Fatalf("routes.get: %v", err)
	}
	if len(route.Steps) != len(mmoQuestChain) {
		t.Fatalf("the route has %d steps, want %d: a step must not disappear with its "+
			"entity", len(route.Steps), len(mmoQuestChain))
	}
	step := route.Steps[3]
	if step.EntityID != nil || step.Key != deleted || step.EntityType != "quest" {
		t.Errorf("the tombstone reads %+v, want a null id beside quest/%s", step, deleted)
	}
	if route.Status != analysis.RouteStale {
		t.Errorf("status = %q after a deletion, want %q", route.Status, analysis.RouteStale)
	}

	check := w.checkMainStory(t)
	if check.Steps[3].Verdict != analysis.VerdictMissingEntity {
		t.Errorf("step 3 = %q, want missing_entity", check.Steps[3].Verdict)
	}
	if check.Steps[3].Key != deleted {
		t.Errorf("the verdict names %q, want the key the step was written with, %q",
			check.Steps[3].Key, deleted)
	}
}

// --- Step 7: renaming a type a route names ---

// TestRenamingQuestToMissionLeavesTheRouteHealthyAndTheTraitsIntact.
//
// **This is the decision-taken-since-the-spec carried all the way to the
// surface.** internal/metamodel/rename.go moves the catalogue row and
// nothing else; a route step keeps spelling the old key, resolves by id,
// and is still `ok`. Three things are asserted together, because the
// rename lesson is only carried if all three hold: the verdict, the
// diagnostic beside it, and that the traits on `requires` — which live on
// a different row entirely — were not touched, so cycles still finds the
// three-cycle afterwards.
func TestRenamingQuestToMissionLeavesTheRouteHealthyAndTheTraitsIntact(t *testing.T) {
	t.Parallel()
	w := newTwoGames(t)
	ctx := context.Background()
	w.writeMainStoryRoute(t)
	before := w.checkMainStory(t)
	if !before.Holds || before.Steps[0].TypeRenamed != nil {
		t.Fatalf("before the rename: holds=%v renamed=%+v", before.Holds,
			before.Steps[0].TypeRenamed)
	}

	current, err := web.MCPTypesGet(ctx, w.deps, w.agent, w.mmo,
		web.TypesGetInput{Key: "quest"})
	if err != nil {
		t.Fatalf("types.get: %v", err)
	}
	version := current.Version
	if _, err := web.MCPTypesRename(ctx, w.deps, w.agent, w.mmo, web.TypesRenameInput{
		From: "quest", To: "mission", ExpectedVersion: &version,
	}); err != nil {
		t.Fatalf("types.rename: %v", err)
	}

	check := w.checkMainStory(t)
	if !check.Holds {
		t.Fatalf("a rename broke a healthy route: %+v", check.Steps)
	}
	for _, step := range check.Steps {
		if step.EntityType != "quest" {
			t.Errorf("step %d spells its type %q: a rename does not rewrite a stored step",
				step.Position, step.EntityType)
		}
		if step.TypeRenamed == nil {
			t.Errorf("step %d says nothing about the key having moved", step.Position)
			continue
		}
		if step.TypeRenamed.Was != "quest" || step.TypeRenamed.Now != "mission" {
			t.Errorf("step %d reports %+v, want quest → mission", step.Position,
				step.TypeRenamed)
		}
	}

	// The traits live on the relation type's row, which a rename of an
	// entity type does not touch, so the engine reads the game exactly as
	// it did before.
	got := w.cycles(t, w.agent, w.mmo)
	if !cycleContains(got.Cycles, "q-loop-a", "q-loop-b", "q-loop-c") {
		t.Errorf("after renaming an entity type the three-cycle is gone: %+v", got.Cycles)
	}
}

// --- Step 8: the refusals, over the transport ---

// TestTheAnalysisRefusalsHoldOverTheTransport drives every refusal this
// sub-project owes through the mounted MCP endpoint.
//
// **Over the transport and not through Go calls**, because a refusal
// that has only ever been produced by a direct call is a refusal no
// client has met: it has never crossed an input schema, and an argument
// the schema rejects first never reaches the code that would refuse it
// with a message a caller can act on.
func TestTheAnalysisRefusalsHoldOverTheTransport(t *testing.T) {
	t.Parallel()
	w := newTwoGames(t)
	ctx := context.Background()
	httpSrv := httptest.NewServer(w.srv)
	defer httpSrv.Close()
	session := connectMCP(t, httpSrv.URL, w.mmoSecret)

	tooManySteps := make([]map[string]any, 0, analysis.MaxRouteSteps+1)
	for i := range analysis.MaxRouteSteps + 1 {
		tooManySteps = append(tooManySteps, map[string]any{
			"entity_type": "quest", "key": mmoQuestChain[i%len(mmoQuestChain)],
		})
	}

	cases := []struct {
		name     string
		tool     string
		args     map[string]any
		code     string
		mentions []string
	}{
		{
			name: "a route of five hundred and one steps is refused and not clamped",
			tool: "routes.upsert",
			args: map[string]any{"key": "too-long", "name": "Too long",
				"steps": tooManySteps, "expected_version": 0},
			code: "invalid_input",
			// The count the caller sent and the cap, so the refusal says
			// what to change rather than that something is wrong.
			mentions: []string{"501", fmt.Sprint(analysis.MaxRouteSteps), "/steps"},
		},
		{
			name:     "a max_depth above the cap is limit_exceeded naming the cap",
			tool:     "analysis.cycles",
			args:     map[string]any{"max_depth": 500},
			code:     "limit_exceeded",
			mentions: []string{fmt.Sprint(analysis.MaxMaxDepth), "max_depth"},
		},
		{
			name: "a version claim against a route that does not exist says it was removed",
			tool: "routes.upsert",
			args: map[string]any{"key": "never-existed", "name": "Never existed",
				"steps": []map[string]any{}, "expected_version": 3},
			code:     "not_found",
			mentions: []string{"removed"},
		},
		{
			name: "a seed key from the other game is not found in this one",
			tool: "analysis.unreachable",
			args: map[string]any{
				"seed_entities":   []map[string]any{{"entity_type": "race", "key": "r-opener"}},
				"include_ungated": false,
			},
			code:     "not_found",
			mentions: []string{"race"},
		},
		{
			name: "an empty seed set with include_ungated off is refused rather than answered",
			tool: "analysis.unreachable",
			args: map[string]any{"include_ungated": false},
			code: "invalid_input",
			// The path is the caller's own argument, which is the whole
			// reason this is invalid_input and not a new code.
			mentions: []string{"seed_entities"},
		},
		{
			name:     "an unknown argument is refused rather than ignored",
			tool:     "routes.get",
			args:     map[string]any{"key": "main-story", "keyy": "main-story"},
			code:     "",
			mentions: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: tc.tool, Arguments: tc.args,
			})
			if tc.code == "" {
				// The SDK's own argument validation refuses an unknown
				// member before any handler runs, with prose and no code
				// — a documented exception this repository already
				// records (TestMCPInputValidationFailuresAreProseNotACode).
				// What matters here is that it is refused at all.
				if err == nil && (result == nil || !result.IsError) {
					t.Fatal("an unknown argument was accepted: a typo'd seed member would " +
						"answer \"your whole game is unreachable\" to a caller that did " +
						"supply seeds")
				}
				return
			}
			if err != nil {
				t.Fatalf("CallTool(%s): %v", tc.tool, err)
			}
			if !result.IsError {
				t.Fatalf("%s answered rather than refusing", tc.tool)
			}
			var body map[string]any
			decodeToolText(t, result, &body)
			if body["error"] != tc.code {
				t.Fatalf("code = %v, want %q: %v", body["error"], tc.code, body["message"])
			}
			message, _ := body["message"].(string)
			for _, want := range tc.mentions {
				if !strings.Contains(message, want) {
					t.Errorf("the refusal does not name %q: %s", want, message)
				}
			}
		})
	}
}

// TestAnAnalysisWithTheWrongGamesTokenIsAScopeViolationEvenForAnAdmin.
//
// **An instance admin is not exempt from a token's binding.** That is
// the invariant requireScope exists for, and this is the analysis
// surface's own instance of it — driven over the transport, with the
// racing game's token pointed at the MMORPG's slug.
func TestAnAnalysisWithTheWrongGamesTokenIsAScopeViolationEvenForAnAdmin(t *testing.T) {
	t.Parallel()
	w := newTwoGames(t)
	ctx := context.Background()
	httpSrv := httptest.NewServer(w.srv)
	defer httpSrv.Close()

	for _, tc := range []struct {
		name   string
		secret string
	}{
		{"an ordinary token", w.racerSecret},
		{"an instance admin's token", w.adminSecret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := connectMCP(t, httpSrv.URL, tc.secret)
			// The control: the token works on its own game, so the
			// refusal below is about the binding rather than about the
			// token.
			if result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "analysis.cycles",
			}); err != nil || result.IsError {
				t.Fatalf("the token cannot analyse its own game: %v %+v", err, result)
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "analysis.cycles", Arguments: map[string]any{"game": w.mmoSlug},
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !result.IsError {
				t.Fatal("a token bound to another game analysed this one")
			}
			var body map[string]any
			decodeToolText(t, result, &body)
			if body["error"] != "scope_violation" {
				t.Fatalf("code = %v, want scope_violation: %v", body["error"], body["message"])
			}
		})
	}
}

// TestTheRESTMirrorAnswersTheSameAnalysisAsTheTool.
//
// The two surfaces call one core each, and this is the assertion that
// makes that claim checkable rather than architectural: the same game,
// the same question, the same answer.
func TestTheRESTMirrorAnswersTheSameAnalysisAsTheTool(t *testing.T) {
	t.Parallel()
	w := newTwoGames(t)
	httpSrv := httptest.NewServer(w.srv)
	defer httpSrv.Close()

	overTheTool := w.cycles(t, w.agent, w.mmo)

	req, err := http.NewRequest(http.MethodPost,
		httpSrv.URL+"/api/games/"+w.mmoSlug+"/analysis/cycles", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.mmoSecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST the analysis: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var overREST analysis.CyclesResult
	if err := json.NewDecoder(resp.Body).Decode(&overREST); err != nil {
		t.Fatalf("decode the REST answer: %v", err)
	}
	if len(overREST.Cycles) != len(overTheTool.Cycles) {
		t.Fatalf("the tool found %d cycles and the REST mirror found %d",
			len(overTheTool.Cycles), len(overREST.Cycles))
	}
	if len(overREST.Cycles) == 0 {
		t.Fatal("both surfaces found nothing, so this comparison is between two empty lists")
	}
	if !cycleContains(overREST.Cycles, "q-loop-a", "q-loop-b", "q-loop-c") {
		t.Errorf("the REST mirror's answer does not carry the three-cycle: %+v",
			overREST.Cycles)
	}
}
