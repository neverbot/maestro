package web_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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

// analysisWorld is one server with an analysis service, one game seeded
// so that **every one of the four analyses has something to find**, and
// the credentials to drive it over either surface.
//
// The "something to find" is the point rather than convenience. Views'
// own transport test ran a view before anything had been written and an
// array of nothing satisfies any item schema at all, so two broken
// schemas passed it. Nothing here is called against an empty game.
type analysisWorld struct {
	srv    *web.Server
	deps   web.MCPDeps
	pool   *pgxpool.Pool
	game   uuid.UUID
	other  uuid.UUID
	agent  web.Caller
	guest  web.Caller
	secret string
	// viewerSecret is a token minted for a member whose role in this game
	// is viewer, which is what TestAViewerCanRunAnAnalysisAndCannotUpsertARoute
	// drives both surfaces with.
	viewerSecret string
	hub          *realtime.Hub
}

func newAnalysisWorld(t *testing.T) *analysisWorld {
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
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create game: %v", err)
	}
	other, err := projSvc.Create(ctx, "le-mans", "Le Mans", owner.ID)
	if err != nil {
		t.Fatalf("Create the second game: %v", err)
	}
	viewer, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "viewer@studio.com", DisplayName: "Viewer", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser viewer: %v", err)
	}
	if _, err := projSvc.SetRole(ctx, viewer.ID, game.ID, "viewer"); err != nil {
		t.Fatalf("add the viewer to the game: %v", err)
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
	agent, agentSecret := mint(game.ID, owner.ID, "content agent")
	guest, _ := mint(other.ID, owner.ID, "le mans agent")
	_, viewerSecret := mint(game.ID, viewer.ID, "viewer agent")

	w := &analysisWorld{
		srv: srv, pool: pool,
		deps: web.MCPDeps{Identity: ids, Projects: projSvc, Metamodel: mm, Analysis: an},
		game: game.ID, other: other.ID,
		agent: agent, guest: guest, secret: agentSecret,
		viewerSecret: viewerSecret, hub: hub,
	}
	w.seed(t)
	return w
}

// seed writes the game through the same tools an agent would call.
//
// The shape is deliberate and every part of it is read by some assertion
// below: a three-cycle in `requires`, a quest gated behind it that no
// player can reach, an entity whose only edge is an annotation, and an
// ordering pair a route then walks backwards.
func (w *analysisWorld) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	for _, spec := range []web.TypesUpsertInput{
		{Key: "quest", Label: "Quest", LabelPlural: "Quests"},
	} {
		if _, err := web.MCPTypesUpsert(ctx, w.deps, w.agent, w.game, spec); err != nil {
			t.Fatalf("types.upsert %s: %v", spec.Key, err)
		}
	}
	for _, spec := range []web.RelationTypesUpsertInput{
		{Key: "requires", Label: "Requires", AnalysisTraits: []string{"prerequisite_of"},
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"quest"}},
		{Key: "unlocks", Label: "Unlocks", AnalysisTraits: []string{"unlocks"},
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"quest"}},
		{Key: "follows", Label: "Follows", AnalysisTraits: []string{"ordering"},
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"quest"}},
		{Key: "illustrates", Label: "Illustrates", AnalysisTraits: []string{"annotation"},
			SourceTypeKeys: []string{"quest"}, TargetTypeKeys: []string{"quest"}},
	} {
		if _, err := web.MCPRelationTypesUpsert(ctx, w.deps, w.agent, w.game, spec); err != nil {
			t.Fatalf("relation_types.upsert %s: %v", spec.Key, err)
		}
	}

	keys := []string{"start", "mid", "blocked", "loop-a", "loop-b", "loop-c",
		"orphaned", "prologue", "epilogue"}
	items := make([]web.EntityItemInput, 0, len(keys))
	for _, key := range keys {
		items = append(items, web.EntityItemInput{TypeKey: "quest", Key: key, Name: key})
	}
	if _, err := web.MCPEntitiesUpsert(ctx, w.deps, w.agent, w.game,
		web.EntitiesUpsertInput{Items: items}); err != nil {
		t.Fatalf("entities.upsert: %v", err)
	}

	edges := []web.RelationItemInput{
		w.edge("unlocks", "start", "mid"),
		// The three-cycle: each requires the next, and the last requires
		// the first, so no player opens any of them.
		w.edge("requires", "loop-a", "loop-b"),
		w.edge("requires", "loop-b", "loop-c"),
		w.edge("requires", "loop-c", "loop-a"),
		// Gated behind the cycle, so it is unreachable with a blocker to
		// name.
		w.edge("requires", "blocked", "loop-a"),
		// The orphan: its only edge is an annotation, which no analysis
		// follows and which the orphan count deliberately does not count.
		w.edge("illustrates", "orphaned", "start"),
		// The ordering pair a route below walks backwards.
		w.edge("follows", "prologue", "epilogue"),
	}
	if _, err := web.MCPRelationsUpsert(ctx, w.deps, w.agent, w.game,
		web.RelationsUpsertInput{Items: edges}); err != nil {
		t.Fatalf("relations.upsert: %v", err)
	}
}

func (w *analysisWorld) edge(typeKey, source, target string) web.RelationItemInput {
	return web.RelationItemInput{
		TypeKey: typeKey,
		Source:  web.RefInput{TypeKey: "quest", Key: source},
		Target:  web.RefInput{TypeKey: "quest", Key: target},
	}
}

// analysisRouteSteps is the route every transport call below works on:
// one step that holds, one that does not, and one on the wrong side of
// an ordering edge, so `blockers`, `must_precede` and `ok` are all
// non-empty in the one answer the output schema judges.
func analysisRouteSteps() []web.RoutesStepInput {
	return []web.RoutesStepInput{
		{EntityType: "quest", Key: "start"},
		{EntityType: "quest", Key: "mid"},
		{EntityType: "quest", Key: "blocked"},
		{EntityType: "quest", Key: "epilogue"},
		{EntityType: "quest", Key: "prologue", Note: "out of order on purpose"},
	}
}

// TestEveryAnalysisToolIsCallableOverTheRealTransport drives all eight
// tools through the mounted MCP endpoint.
//
// **It is a schema test wearing a smoke test's clothes.** The SDK
// validates a call's arguments against the registered *input* schema
// before any handler runs, and the handler's answer against the
// registered *output* schema before it reaches the wire; neither is read
// by a test that calls the MCP* functions directly. Views shipped three
// tools uncallable by any real client with its whole package green,
// twice over, because every one of its tests crossed no schema.
//
// The table is checked against the **registry**, so a ninth tool added
// without a call here fails rather than going unexercised. And every
// call is made against a game seeded to answer non-emptily: an array of
// nothing satisfies any item schema at all, which is how a misspelled
// member goes on passing a transport test that ran too early.
func TestEveryAnalysisToolIsCallableOverTheRealTransport(t *testing.T) {
	t.Parallel()
	w := newAnalysisWorld(t)
	ctx := context.Background()
	httpSrv := httptest.NewServer(w.srv)
	defer httpSrv.Close()
	session := connectMCP(t, httpSrv.URL, w.secret)

	calls := map[string]map[string]any{
		"routes.upsert": {
			"key": "levelling", "name": "The levelling route",
			"steps": analysisRouteSteps(), "expected_version": 0,
			"params": map[string]any{"gate": "any"},
		},
		"routes.list":          {},
		"routes.get":           {"key": "levelling"},
		"routes.check":         {"key": "levelling"},
		"analysis.cycles":      {},
		"analysis.unreachable": {},
		"analysis.orphans":     {"mode": "isolated"},
		// Last, deliberately: it takes the route every other call needs.
		"routes.remove": {"key": "levelling", "expected_version": 1},
	}
	order := []string{
		"routes.upsert", "routes.list", "routes.get", "routes.check",
		"analysis.cycles", "analysis.unreachable", "analysis.orphans", "routes.remove",
	}
	if len(order) != len(calls) {
		t.Fatalf("the ordering names %d tools and the table has %d", len(order), len(calls))
	}
	registered := 0
	for _, name := range w.srv.ScopedToolNamesForTest() {
		if !strings.HasPrefix(name, "analysis.") && !strings.HasPrefix(name, "routes.") {
			continue
		}
		registered++
		if _, ok := calls[name]; !ok {
			t.Fatalf("%s is registered and this test does not call it: add it, or its "+
				"input and output schemas are asserted by nothing at all", name)
		}
	}
	if registered != len(calls) {
		t.Fatalf("%d analysis tools are registered and the table has %d", registered, len(calls))
	}
	if registered == 0 {
		t.Fatal("no analysis tool is registered; this test would pass vacuously")
	}

	// What each call must have actually found, so a schema that validated
	// an empty answer cannot pass. Each entry reads one member of the
	// answer that is non-zero only because the game was seeded for it.
	nonEmpty := map[string]func(*testing.T, map[string]any){
		"routes.list": func(t *testing.T, out map[string]any) {
			requireNonEmptyList(t, out, "routes")
		},
		"routes.get": func(t *testing.T, out map[string]any) {
			requireNonEmptyList(t, out, "steps")
		},
		"routes.check": func(t *testing.T, out map[string]any) {
			requireNonEmptyList(t, out, "steps")
			verdicts := map[string]bool{}
			for _, raw := range out["steps"].([]any) {
				step := raw.(map[string]any)
				verdicts[step["verdict"].(string)] = true
				if step["verdict"] == "unmet_prerequisite" {
					requireNonEmptyList(t, step, "blockers")
				}
				if step["verdict"] == "out_of_order" {
					requireNonEmptyList(t, step, "must_precede")
				}
			}
			// Three different verdicts in one answer, so the output
			// schema is judged against a step that carries `blockers`,
			// one that carries `must_precede` and one that carries
			// neither.
			for _, want := range []string{"ok", "unmet_prerequisite", "out_of_order"} {
				if !verdicts[want] {
					t.Errorf("no step came back %q, so the schema was never judged "+
						"against one: got %v", want, verdicts)
				}
			}
		},
		"analysis.cycles": func(t *testing.T, out map[string]any) {
			requireNonEmptyList(t, out, "cycles")
			requireNonEmptyList(t, out, "semantics_source")
			first := out["cycles"].([]any)[0].(map[string]any)
			requireNonEmptyList(t, first, "entities")
			requireNonEmptyList(t, first, "edges")
		},
		"analysis.unreachable": func(t *testing.T, out map[string]any) {
			requireNonEmptyList(t, out, "unreachable")
			requireNonEmptyList(t, out, "per_type")
		},
		"analysis.orphans": func(t *testing.T, out map[string]any) {
			requireNonEmptyList(t, out, "orphans")
		},
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
			if check, ok := nonEmpty[name]; ok {
				var out map[string]any
				decodeStructured(t, result, &out)
				check(t, out)
			}
		})
	}
}

// requireNonEmptyList fails unless the named member is a list with
// something in it. It exists because "the schema validated" and "the
// schema was validated against anything" are different statements.
func requireNonEmptyList(t *testing.T, out map[string]any, key string) {
	t.Helper()
	raw, ok := out[key]
	if !ok {
		t.Fatalf("the answer carries no %q member: %v", key, analysisKeysOf(out))
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("%q is %T and not a list", key, raw)
	}
	if len(list) == 0 {
		t.Fatalf("%q came back empty, so the item schema below it was judged against "+
			"nothing at all", key)
	}
}

func analysisKeysOf(out map[string]any) []string {
	keys := make([]string, 0, len(out))
	for key := range out {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestEveryAnalysisSentinelHasAWireCode drives analysis.Sentinels()
// through the two mapping functions rather than repeating the list.
//
// A sentinel added there and not mapped here surfaces as internal_error,
// which tells an agent to give up on something it could fix in one call.
// That is the pattern TestEveryViewsSentinelHasAWireCode established and
// the reason it exists.
func TestEveryAnalysisSentinelHasAWireCode(t *testing.T) {
	t.Parallel()
	sentinels := analysis.Sentinels()
	if len(sentinels) == 0 {
		t.Fatal("analysis.Sentinels() is empty, so this guard checks nothing")
	}
	for _, sentinel := range sentinels {
		t.Run(sentinel.Error(), func(t *testing.T) {
			// The sentinel itself, not a typed error built around it:
			// this asserts the arm matches with errors.Is, which is what
			// every richer error in the domain unwraps to.
			result := web.MCPErrorForTest(sentinel)
			var body map[string]any
			decodeToolText(t, result, &body)
			if body["error"] == "internal_error" {
				t.Fatalf("%v reaches an agent as internal_error: it matches no arm of "+
					"mcpErrorFor, so an agent is told to give up on a refusal it could "+
					"act on", sentinel)
			}
			rec := httptest.NewRecorder()
			web.WriteDomainErrorForTest(rec, httptest.NewRequest(http.MethodPost, "/x", nil),
				sentinel)
			var rest map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &rest); err != nil {
				t.Fatalf("decode the REST body %q: %v", rec.Body.String(), err)
			}
			if rest["error"] != body["error"] {
				t.Errorf("the two surfaces answer %v with %q and %q: writeDomainError "+
					"claims to be arm for arm with mcpErrorFor", sentinel,
					body["error"], rest["error"])
			}
			if rec.Code == http.StatusInternalServerError {
				t.Errorf("%v is a 500 over REST", sentinel)
			}
		})
	}
}

// TestSemanticsUndeclaredIs422OnRESTAndCarriesTheCatalogue.
//
// **422 and not 400 or 409.** The request is well formed and the game is
// in a state the caller can fix by declaring something, which is exactly
// what invalid_schema and schema_violation are 422 for. And the payload
// is the whole reason the code exists: the recovery is "declare
// something about your relation types", and no caller can perform it
// without the list.
func TestSemanticsUndeclaredIs422OnRESTAndCarriesTheCatalogue(t *testing.T) {
	t.Parallel()
	err := &analysis.UndeclaredError{
		Types:  []analysis.TypeReport{{Key: "mentions"}},
		Advice: "declare something",
	}
	rec := httptest.NewRecorder()
	web.WriteDomainErrorForTest(rec, httptest.NewRequest(http.MethodPost, "/x", nil), err)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	var body map[string]any
	if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), e)
	}
	if body["error"] != "semantics_undeclared" {
		t.Fatalf("code = %v, want semantics_undeclared", body["error"])
	}
	details, ok := body["details"].(map[string]any)
	if !ok {
		t.Fatalf("the refusal carries no details: %v", body)
	}
	types, ok := details["relation_types"].([]any)
	if !ok || len(types) != 1 {
		t.Fatalf("details.relation_types = %v, want the game's one relation type",
			details["relation_types"])
	}
	if details["advice"] == nil {
		t.Error("the refusal carries no advice, so it names the problem and not the recovery")
	}
}

// TestEveryAnalysisToolDescriptionCarriesTheGeneratedTraitTable.
//
// Nothing in these descriptions restates the vocabulary, the coherence
// rules or the derivation mapping in prose: they splice
// analysis.TraitDescription(), which is generated from the tables the
// engine enforces against. A hand-written paragraph beside a table is a
// paragraph that goes false on the first edit to the table, and an agent
// has no way to notice.
//
// The set of tools that must carry it is derived rather than listed: any
// tool that can answer `semantics_undeclared` needs to tell a caller
// what to declare. `analysis.orphans` carries it too, because it reads
// the `annotation` word off the same vocabulary even though it does not
// refuse.
func TestEveryAnalysisToolDescriptionCarriesTheGeneratedTraitTable(t *testing.T) {
	t.Parallel()
	table := analysis.TraitDescription()
	if len(table) < 200 {
		t.Fatalf("the generated trait table is %d characters: too short to be the real "+
			"one, so the assertions below would be vacuous", len(table))
	}
	descriptions := web.NewToolReferenceServer().ToolDescriptionsForTest()
	want := []string{"analysis.cycles", "analysis.unreachable", "analysis.orphans",
		"routes.check"}
	for _, name := range want {
		description, ok := descriptions[name]
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if !strings.Contains(description, table) {
			t.Errorf("%s's description does not carry analysis.TraitDescription(): a "+
				"trait added to the vocabulary would never reach an agent through it",
				name)
		}
	}
}

// TestNoAnalysisToolDescriptionNamesATraitOutsideTheTable is the other
// direction, and it is the half that catches a hand-written mention of a
// trait that no longer exists.
//
// Every backticked lowercase token in the *hand-written* part of an
// analysis description — the description with the generated table
// removed — must be either an argument this tool actually takes or a
// word of the trait vocabulary. A trait dropped from the vocabulary
// leaves its mention behind, and that mention then names neither.
func TestNoAnalysisToolDescriptionNamesATraitOutsideTheTable(t *testing.T) {
	t.Parallel()
	descriptions := web.NewToolReferenceServer().ToolDescriptionsForTest()
	table := analysis.TraitDescription()

	known := map[string]bool{}
	// Every argument these tools take, read off the input types' own json
	// tags rather than typed out here: an argument renamed in the struct
	// and still named in the prose is exactly the drift this guard is
	// for, and a hand-written list of argument names would hide it.
	// Every argument these tools take **and every member they answer
	// with**, read off the types' own json tags rather than typed out
	// here. A name renamed in a struct and still named in the prose is
	// exactly the drift this guard is for, and a hand-written list would
	// hide it.
	for _, shape := range []any{
		web.AnalysisCyclesInput{}, web.AnalysisUnreachableInput{},
		web.AnalysisOrphansInput{}, web.AnalysisReachArgs{}, web.AnalysisSeedInput{},
		web.RoutesListInput{}, web.RoutesGetInput{}, web.RoutesUpsertInput{},
		web.RoutesRemoveInput{}, web.RoutesCheckInput{}, web.RoutesStepInput{},
		analysis.CyclesResult{}, analysis.Cycle{}, analysis.CycleNode{}, analysis.CycleEdge{},
		analysis.UnreachableResult{}, analysis.Finding{}, analysis.TypeCount{},
		analysis.SeedReport{}, analysis.OrphansResult{}, analysis.Orphan{},
		analysis.Route{}, analysis.RouteStep{}, analysis.RouteSummary{},
		analysis.RoutePage{}, analysis.RouteCheck{}, analysis.RouteVerdict{},
		analysis.StepCheck{}, analysis.RenamedKey{}, analysis.TypeSemantics{},
		analysis.Stats{},
	} {
		for _, name := range jsonFieldNames(reflect.TypeOf(shape)) {
			known[name] = true
		}
	}
	for _, trait := range metamodel.AnalysisTraits {
		known[trait] = true
	}
	for _, role := range metamodel.SemanticRoles {
		known[role] = true
	}
	// The words that are not traits and legitimately appear backticked:
	// wire codes, status values, verdicts and the two gating readings.
	// The values, as opposed to the member names: wire codes, statuses,
	// verdicts, orphan modes and the two gating readings. Each of these
	// is enumerated somewhere the engine enforces, so the lists are read
	// from there rather than retyped.
	for _, verdict := range analysis.Verdicts {
		known[string(verdict)] = true
	}
	for _, status := range analysis.RouteStatuses {
		known[string(status)] = true
	}
	for _, mode := range analysis.OrphanModes {
		known[string(mode)] = true
	}
	for _, gating := range analysis.Gatings {
		known[string(gating)] = true
	}
	for _, reason := range analysis.Reasons {
		known[string(reason)] = true
	}
	for _, code := range web.ErrorCodesForTest() {
		known[code] = true
	}
	// The handful of words no vocabulary holds: another tool's name, and
	// the one MCP tool this surface points at by name.
	for _, word := range []string{"views.run", "relation_types.upsert"} {
		known[word] = true
	}

	checked := 0
	for name, description := range descriptions {
		if !strings.HasPrefix(name, "analysis.") && !strings.HasPrefix(name, "routes.") {
			continue
		}
		checked++
		handWritten := strings.ReplaceAll(description, table, " ")
		for _, token := range backtickedTokens(handWritten) {
			// A dotted mention names a path into an answer --
			// `seeds.total` -- and every part of it has to be a real
			// name, which is a stricter check than the whole string
			// being one.
			if known[token] {
				continue
			}
			for _, part := range strings.Split(token, ".") {
				if known[part] {
					continue
				}
				t.Errorf("%s's hand-written prose names `%s`, which is neither an "+
					"argument it takes nor a word of any vocabulary this engine "+
					"enforces: a mention of a trait that no longer exists reads to an "+
					"agent as one it can declare", name, token)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no analysis tool description was scanned; this guard read nothing")
	}
}

// jsonFieldNames is every wire name a struct's exported fields carry,
// through embedded structs, which is how ScopedArgs' `game` and
// AnalysisReachArgs' seed arguments are reached.
func jsonFieldNames(typ reflect.Type) []string {
	var out []string
	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			out = append(out, jsonFieldNames(field.Type)...)
			continue
		}
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag != "" && tag != "-" {
			out = append(out, tag)
		}
	}
	return out
}

// backtickedTokens pulls the `word` mentions out of a description,
// keeping only the ones that could be a vocabulary word: a single
// lowercase identifier, which is what every trait and every argument
// name in this product is.
func backtickedTokens(text string) []string {
	var out []string
	parts := strings.Split(text, "`")
	for i := 1; i < len(parts); i += 2 {
		token := parts[i]
		if token == "" || strings.ContainsAny(token, " \t\n:/{}[]()\"") {
			continue
		}
		if strings.ToLower(token) != token {
			continue
		}
		out = append(out, token)
	}
	return out
}

// TestTheAnalysisToolsAreAbsentWithoutAnAnalysisService.
//
// A Server built with no analysis service still starts and still answers
// every other tool, which is the shape Options.Analysis documents. It is
// asserted rather than assumed because the registration is behind an
// `if` and an `if` nothing exercises is a branch that can be deleted.
func TestTheAnalysisToolsAreAbsentWithoutAnAnalysisService(t *testing.T) {
	t.Parallel()
	srv := web.NewServer(web.Options{
		Version:  "test",
		Identity: identity.New(nil, config.Config{}),
		Projects: projects.New(nil),
	})
	for _, name := range srv.ScopedToolNamesForTest() {
		if strings.HasPrefix(name, "analysis.") || strings.HasPrefix(name, "routes.") {
			t.Errorf("%s is registered on a server built with no analysis service", name)
		}
	}
	// The control: the server did register something, so this test
	// cannot pass over a server with no tools at all.
	if len(srv.ScopedToolNamesForTest()) == 0 {
		t.Fatal("the server registered no tools, so the loop above asserted nothing")
	}
}

// TestAViewerCanRunAnAnalysisAndCannotUpsertARoute pins what actually
// ships on both surfaces, including the half that is a finding.
//
// **A token is editor-equivalent by construction** (requireProject's own
// doc comment argues it), so the role gate this test is about only bites
// a *session* caller — a designer in a browser. For that caller:
//
//   - routes.upsert is a write and is refused. Correct.
//   - **analysis.cycles is a read and is refused too**, because an
//     analysis is a POST and registerContentRoute gates every non-GET
//     route on this surface. That is the finding api_analysis.go's
//     header records: it is the same shape `views.run` has carried since
//     the views sub-project, and it belongs to the change that builds the
//     browser client rather than to one domain.
//
// Over MCP, where an agent lives and where these tools are primary, a
// viewer's token runs the analysis. So a viewer *can* run one, on the
// surface a viewer with a token has, and cannot on the one it does not —
// which is the sentence this test's name makes, spelled out.
//
// It asserts what ships rather than what would be nicer, because a test
// asserting the nicer thing would have to be skipped, and a skipped test
// is a claim nobody checks.
func TestAViewerCanRunAnAnalysisAndCannotUpsertARoute(t *testing.T) {
	t.Parallel()
	w := newAnalysisWorld(t)
	ctx := context.Background()
	httpSrv := httptest.NewServer(w.srv)
	defer httpSrv.Close()

	// MCP: a viewer's token runs the analysis.
	session := connectMCP(t, httpSrv.URL, w.viewerSecret)
	for _, name := range []string{"analysis.cycles", "analysis.orphans"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name})
		if err != nil {
			t.Fatalf("CallTool(%s) as a viewer: %v", name, err)
		}
		if result.IsError {
			var msg any
			decodeToolText(t, result, &msg)
			t.Fatalf("a viewer's token was refused %s over MCP: %v", name, msg)
		}
	}

	// REST, with a session: the write is refused, and so is the read.
	cookie := loginAs(t, w.srv, "viewer@studio.com")
	// The control: a viewer reads the game, so the two refusals below are
	// about the method rather than about being shut out of the game.
	if code, _ := w.callAs(t, cookie, http.MethodGet, "/api/games/azeroth/routes", ""); code != http.StatusOK {
		t.Fatalf("a viewer listing routes got %d, want 200: the refusals below would "+
			"then be about membership rather than about the editor gate", code)
	}
	if code, body := w.callAs(t, cookie, http.MethodPost, "/api/games/azeroth/routes",
		`{"key":"x","name":"X","steps":[],"expected_version":0}`); code != http.StatusForbidden {
		t.Errorf("a viewer upserting a route got %d, want 403: %s", code, body)
	}
	code, body := w.callAs(t, cookie, http.MethodPost,
		"/api/games/azeroth/analysis/cycles", `{}`)
	if code != http.StatusForbidden {
		t.Fatalf("a viewer POSTing a read-only analysis got %d, want 403: this is the "+
			"finding api_analysis.go records. If it has been fixed, fix `views.run` in "+
			"the same change and rewrite this test", code)
	}
	if !strings.Contains(body, "viewer") {
		t.Errorf("the refusal does not name the role that cannot write: %s", body)
	}
}

// callAs drives one REST request with a session cookie and hands back
// the status and the body.
func (w *analysisWorld) callAs(t *testing.T, cookie *http.Cookie, method, path, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestARouteCheckIsPublishedToASubscriber is the SSE half.
//
// **Analyses are not streamed** — they are pull, not push, and there is
// nothing about running one that another session needs to learn. A route
// is different: it holds a stored verdict that another writer can
// invalidate, so the three route kinds are published.
func TestARouteCheckIsPublishedToASubscriber(t *testing.T) {
	t.Parallel()
	w := newAnalysisWorld(t)
	ctx := context.Background()
	sub := w.hub.Subscribe(w.game, "viewer", false)
	defer w.hub.Unsubscribe(sub)

	if _, err := web.MCPRoutesUpsert(ctx, w.deps, w.agent, w.game, web.RoutesUpsertInput{
		Key: "levelling", Name: "Levelling", Steps: analysisRouteSteps(),
		ExpectedVersion: new(int32),
	}); err != nil {
		t.Fatalf("routes.upsert: %v", err)
	}
	if got := requireWorldEvent(t, sub); got.Kind != "route.upserted" {
		t.Fatalf("kind = %q, want route.upserted", got.Kind)
	}
	if _, err := web.MCPRoutesCheck(ctx, w.deps, w.agent, w.game,
		web.RoutesCheckInput{Key: "levelling"}); err != nil {
		t.Fatalf("routes.check: %v", err)
	}
	if got := requireWorldEvent(t, sub); got.Kind != "route.checked" {
		t.Fatalf("kind = %q, want route.checked", got.Kind)
	}
}

// TestStalenessIsReadFromTheRouteAndNotFromAnEvent.
//
// The plan asked for `design_version` to ride on every metamodel event,
// so a client could compare it locally against each route's
// `last_checked_design_version`. **It does not, and the reason is that
// the server already answers the question the comparison exists to
// answer.** `routes.list` computes each route's three-state status
// against the game's current counter, server-side, on every read — so a
// client that hears any metamodel event and re-reads gets the answer
// directly, and putting the counter on every event would cost a query
// per published event during exactly the burst (a bulk seed) where the
// events are already being dropped into a resync.
//
// This test is what makes that a decision rather than an omission: it
// asserts the path the client actually has.
func TestStalenessIsReadFromTheRouteAndNotFromAnEvent(t *testing.T) {
	t.Parallel()
	w := newAnalysisWorld(t)
	ctx := context.Background()
	if _, err := web.MCPRoutesUpsert(ctx, w.deps, w.agent, w.game, web.RoutesUpsertInput{
		Key: "levelling", Name: "Levelling", Steps: analysisRouteSteps(),
		ExpectedVersion: new(int32),
	}); err != nil {
		t.Fatalf("routes.upsert: %v", err)
	}
	if _, err := web.MCPRoutesCheck(ctx, w.deps, w.agent, w.game,
		web.RoutesCheckInput{Key: "levelling"}); err != nil {
		t.Fatalf("routes.check: %v", err)
	}
	// The control: freshly checked, the listing says so.
	page, err := web.MCPRoutesList(ctx, w.deps, w.agent, w.game, web.RoutesListInput{})
	if err != nil {
		t.Fatalf("routes.list: %v", err)
	}
	if len(page.Routes) != 1 || page.Routes[0].Status != analysis.RouteChecked {
		t.Fatalf("the listing reports %+v immediately after a check, want %q",
			page.Routes, analysis.RouteChecked)
	}

	out, err := web.MCPEntitiesUpsert(ctx, w.deps, w.agent, w.game, web.EntitiesUpsertInput{
		Items: []web.EntityItemInput{{TypeKey: "quest", Key: "epilogue-notes", Name: "Notes"}},
	})
	if err != nil {
		t.Fatalf("entities.upsert: %v", err)
	}
	t.Logf("bulk result: %+v", out)
	page, err = web.MCPRoutesList(ctx, w.deps, w.agent, w.game, web.RoutesListInput{})
	if err != nil {
		t.Fatalf("routes.list: %v", err)
	}
	if len(page.Routes) != 1 || page.Routes[0].Status != analysis.RouteStale {
		t.Fatalf("after one entity write the listing reports %+v, want %q: a client "+
			"learns staleness by re-reading, which is the only mechanism this design "+
			"actually has", page.Routes, analysis.RouteStale)
	}
}

func requireWorldEvent(t *testing.T, sub *realtime.Subscription) realtime.Event {
	t.Helper()
	select {
	case got := <-sub.C:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("no event was published")
		return realtime.Event{}
	}
}
