package web

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/analysis"
	"github.com/neverbot/maestro/internal/metamodel"
)

// This file is the analysis surface: the four answers a game cannot get
// by reading, and the stored artefact that carries one of them.
//
// **Every exported MCPAnalysis*/MCPRoutes* function starts with
// requireScope and delegates to an unexported core of the same name**,
// which is the split mcp_docs.go's header argues and mcp_views.go
// inherits: requireScope asks "is this token bound to this game", which
// a session caller cannot answer, and requireProject asks the equivalent
// question of a session. One implementation of every tool, two admission
// checks.
//
// **The tool descriptions carry analysis.TraitDescription() rather than
// restating the vocabulary.** That table is generated from
// metamodel.AnalysisTraits, metamodel.AnalysisTraitConflicts and the
// resolver's own derivation mapping, each guarded in both directions by
// internal/analysis's own tests. A hand-written paragraph naming a trait
// would be a paragraph that goes false the first time the vocabulary
// moves, and an agent would find out by being refused.
// TestEveryAnalysisToolDescriptionCarriesTheGeneratedTraitTable and
// TestNoAnalysisToolDescriptionNamesATraitOutsideTheTable are the two
// directions.
//
// **The output schemas name what is knowable.** internal/views shipped
// `positions` as an array of open objects and a wire-spelling defect
// lived inside that shrug for a whole sub-project, found by a reader
// rather than by a test. Nothing this surface answers with depends on a
// caller's document: a finding, a verdict, a semantics_source entry and
// a route step all have fixed members, so every one of them is spelled
// out below.

// --- The prose no structure generates ---

// analysisOrphanDoc, analysisGateDoc, analysisFalsePositiveDoc and
// analysisNoCursorDoc are the four things an agent cannot infer from any
// table, each of which was a real misreading before it was a paragraph.
const (
	// The mid-seed false positive. It is stated rather than
	// heuristically suppressed, because a heuristic that hid orphans
	// during a seed would hide the ones that are still orphans when the
	// seed finishes.
	analysisOrphanDoc = "**Bulk seeding routinely passes through a state where half the " +
		"content is orphaned**: entities land before the edges that join them. An orphan " +
		"count taken during a seed is not a finding. Run this when the seed is finished, " +
		"or expect the number to fall to nothing as the edges arrive."

	// The `any` default. An agent that assumed conjunction would read
	// every alternative route as a broken one.
	analysisGateDoc = "**An entity with two gates is reachable when *one* of them is.** " +
		"Maestro cannot express a requirement group — \"A, and either B or C\" — so `any` " +
		"is the default and is what a walk computes natively. Pass `gate: \"all\"` if your " +
		"game means conjunction everywhere; it will over-report, because it reads two " +
		"alternative routes into a place as two requirements for it."

	// What a false positive looks like, said plainly, because the spec
	// says this is the finding a designer will read as wrong.
	analysisFalsePositiveDoc = "**Content reachable by a mechanism the design never wrote " +
		"down is reported.** A vendor sells it, an NPC offers it, the player just walks " +
		"there — none of that is an edge, so the engine cannot see it. Strictly the " +
		"finding is \"the design does not say how a player gets here\", which is often the " +
		"more useful sentence. `ignore_entity_types` and the `annotation` trait are how " +
		"you silence a category you know is handled elsewhere; do not silence one you have " +
		"not checked."

	// No cursor on analysis.cycles, with the reason, so a caller does
	// not go looking for next_cursor.
	analysisNoCursorDoc = "**There is no cursor on this tool and there will not be one.** " +
		"The answer is capped hard and reports `truncated` when the cap bites: a design " +
		"with more than a thousand distinct prerequisite cycles has one problem, not a " +
		"thousand, and paging through them helps nobody. Findings come shortest first, so " +
		"a truncated answer holds the most fixable loops rather than an arbitrary scatter."

	// The staleness contract, which is the one thing about a route a
	// client gets wrong by default.
	analysisStaleDoc = "**A route has three states, not two.** `never_checked` is not " +
		"`stale`, and `stale` is neither green nor red: it says the verdict is about a " +
		"game that has since changed and says nothing about whether the route holds now. " +
		"Any write to this game's types, relation types, entities or relations moves its " +
		"`design_version`, which marks every route in the game stale — coarse on purpose, " +
		"because a per-route dependency set would be subtly wrong the moment a new " +
		"relation made a previously irrelevant entity relevant, and re-checking is cheap " +
		"while believing a stale green is not. Editing a route's own steps makes its own " +
		"verdict stale too."
)

// --- Inputs ---

// AnalysisSeedInput is one entity named as a start point, by the pair
// that addresses one: an entity key is unique per (game, entity type)
// and not per game.
type AnalysisSeedInput struct {
	EntityType string `json:"entity_type"`
	Key        string `json:"key"`
}

// AnalysisReachArgs is every argument the reachability closure takes,
// embedded by the two tools that run one and mirrored by a route's
// stored parameters.
//
// It is one struct rather than two lists of fields for the reason
// analysis.UnreachableInput embeds analysis.Params: a seed argument must
// not mean one thing on analysis.unreachable and another on
// routes.upsert.
type AnalysisReachArgs struct {
	SeedEntities    []AnalysisSeedInput `json:"seed_entities,omitempty"`
	SeedEntityTypes []string            `json:"seed_entity_types,omitempty"`
	SeedRoute       string              `json:"seed_route,omitempty"`

	// IncludeUngated and PropagateContainment are pointers because both
	// default to **true** and false is a value a caller means. A plain
	// bool would store the opposite of the documented default for every
	// caller that said nothing.
	IncludeUngated       *bool `json:"include_ungated,omitempty"`
	PropagateContainment *bool `json:"propagate_containment,omitempty"`

	Gate           string `json:"gate,omitempty"`
	MaxDepth       int    `json:"max_depth,omitempty"`
	ExcludeInvalid bool   `json:"exclude_invalid,omitempty"`
}

func (a AnalysisReachArgs) params() analysis.Params {
	return analysis.Params{
		SeedEntities:         seedRefs(a.SeedEntities),
		SeedEntityTypes:      a.SeedEntityTypes,
		SeedRoute:            a.SeedRoute,
		IncludeUngated:       a.IncludeUngated,
		PropagateContainment: a.PropagateContainment,
		Gating:               analysis.Gating(a.Gate),
		MaxDepth:             a.MaxDepth,
		ExcludeInvalid:       a.ExcludeInvalid,
	}
}

func seedRefs(in []AnalysisSeedInput) []analysis.SeedRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]analysis.SeedRef, 0, len(in))
	for _, ref := range in {
		out = append(out, analysis.SeedRef{EntityType: ref.EntityType, Key: ref.Key})
	}
	return out
}

// AnalysisCyclesInput is analysis.cycles' arguments.
type AnalysisCyclesInput struct {
	ScopedArgs
	EntityTypes       []string `json:"entity_types,omitempty"`
	IgnoreEntityTypes []string `json:"ignore_entity_types,omitempty"`
	RelationTypes     []string `json:"relation_types,omitempty"`
	MaxDepth          int      `json:"max_depth,omitempty"`
	MaxResults        int      `json:"max_results,omitempty"`
	ExcludeInvalid    bool     `json:"exclude_invalid,omitempty"`
}

// AnalysisUnreachableInput is analysis.unreachable's arguments.
type AnalysisUnreachableInput struct {
	ScopedArgs
	AnalysisReachArgs
	RelationTypes     []string `json:"relation_types,omitempty"`
	EntityTypes       []string `json:"entity_types,omitempty"`
	IgnoreEntityTypes []string `json:"ignore_entity_types,omitempty"`
	MaxResults        int      `json:"max_results,omitempty"`
	Limit             int32    `json:"limit,omitempty"`
	Cursor            string   `json:"cursor,omitempty"`
}

// AnalysisOrphansInput is analysis.orphans' arguments.
type AnalysisOrphansInput struct {
	ScopedArgs
	Mode              string   `json:"mode,omitempty"`
	EntityTypes       []string `json:"entity_types,omitempty"`
	IgnoreEntityTypes []string `json:"ignore_entity_types,omitempty"`
	Limit             int32    `json:"limit,omitempty"`
	Cursor            string   `json:"cursor,omitempty"`
}

// RoutesListInput pages this game's routes.
type RoutesListInput struct {
	ScopedArgs
	Cursor string `json:"cursor,omitempty"`
	Limit  int32  `json:"limit,omitempty"`
}

// RoutesGetInput reads one route by key.
type RoutesGetInput struct {
	ScopedArgs
	Key string `json:"key"`
}

// RoutesStepInput is one step as a caller writes it.
type RoutesStepInput struct {
	EntityType string `json:"entity_type"`
	Key        string `json:"key"`
	Note       string `json:"note,omitempty"`
}

// RoutesUpsertInput creates or replaces one route and its whole step
// list.
type RoutesUpsertInput struct {
	ScopedArgs
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Params is the route's own definition of what holding together
	// means, stored and read back at check time. It is a pointer so that
	// "said nothing" and "said the defaults explicitly" are one thing
	// here rather than two, which is what they are in the domain: an
	// omitted object stores an empty one.
	Params          *AnalysisReachArgs `json:"params,omitempty"`
	Steps           []RoutesStepInput  `json:"steps"`
	ExpectedVersion *int32             `json:"expected_version"`
}

// RoutesRemoveInput deletes one route.
//
// **expected_version is required here and is not on views.remove**, and
// the difference is the point: a view is derived content that can be
// re-upserted from a document the caller holds, while a route's ordered
// steps are authored and its stored verdict cannot be reconstructed from
// anything.
type RoutesRemoveInput struct {
	ScopedArgs
	Key             string `json:"key"`
	ExpectedVersion *int32 `json:"expected_version"`
}

// RoutesCheckInput checks one route by key.
type RoutesCheckInput struct {
	ScopedArgs
	Key string `json:"key"`
}

// --- Outputs ---
//
// The three analyses, routes.get and routes.check answer with the domain
// package's own result types rather than a projection.
//
// That is the opposite of what this file does for a metamodel row, and
// the reason is that these types are already wire shapes: every member
// is tagged, every one was designed as the answer to a tool call, and a
// projection would be a second place that decides what a finding is.
// wire_tags_test.go's sweep is what makes that safe — the analysis roots
// are named in wireDomainRoots, so an untagged field added to any of
// them fails a test rather than reaching an agent under a Go field name.

// RoutesRemovedOutput is what a deletion answers with. A count is not
// available — the row is gone or it was not there, and "not there" is
// not_found — so this is the acknowledgement, spelled the way
// views.remove spells it.
type RoutesRemovedOutput struct {
	Removed bool `json:"removed"`
}

// --- The tools ---

// MCPAnalysisCycles implements analysis.cycles.
func MCPAnalysisCycles(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in AnalysisCyclesInput) (analysis.CyclesResult, error) {
	if err := requireScope(caller, projectID); err != nil {
		return analysis.CyclesResult{}, err
	}
	return analysisCycles(ctx, deps, projectID, in)
}

func analysisCycles(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in AnalysisCyclesInput) (analysis.CyclesResult, error) {
	return deps.Analysis.Cycles(ctx, projectID, analysis.CyclesInput{
		EntityTypes: in.EntityTypes, IgnoreEntityTypes: in.IgnoreEntityTypes,
		RelationTypes: in.RelationTypes, MaxDepth: in.MaxDepth,
		MaxResults: in.MaxResults, ExcludeInvalid: in.ExcludeInvalid,
	})
}

// MCPAnalysisUnreachable implements analysis.unreachable.
func MCPAnalysisUnreachable(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in AnalysisUnreachableInput) (analysis.UnreachableResult, error) {
	if err := requireScope(caller, projectID); err != nil {
		return analysis.UnreachableResult{}, err
	}
	return analysisUnreachable(ctx, deps, projectID, in)
}

func analysisUnreachable(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in AnalysisUnreachableInput) (analysis.UnreachableResult, error) {
	return deps.Analysis.Unreachable(ctx, projectID, analysis.UnreachableInput{
		Reach:         in.AnalysisReachArgs.params(),
		RelationTypes: in.RelationTypes,
		EntityTypes:   in.EntityTypes, IgnoreEntityTypes: in.IgnoreEntityTypes,
		MaxResults: in.MaxResults, Limit: in.Limit, Cursor: in.Cursor,
	})
}

// MCPAnalysisOrphans implements analysis.orphans.
func MCPAnalysisOrphans(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in AnalysisOrphansInput) (analysis.OrphansResult, error) {
	if err := requireScope(caller, projectID); err != nil {
		return analysis.OrphansResult{}, err
	}
	return analysisOrphans(ctx, deps, projectID, in)
}

func analysisOrphans(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in AnalysisOrphansInput) (analysis.OrphansResult, error) {
	return deps.Analysis.Orphans(ctx, projectID, analysis.OrphansInput{
		Mode:        analysis.OrphanMode(in.Mode),
		EntityTypes: in.EntityTypes, IgnoreEntityTypes: in.IgnoreEntityTypes,
		Limit: in.Limit, Cursor: in.Cursor,
	})
}

// MCPRoutesList implements routes.list.
func MCPRoutesList(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in RoutesListInput) (analysis.RoutePage, error) {
	if err := requireScope(caller, projectID); err != nil {
		return analysis.RoutePage{}, err
	}
	return routesList(ctx, deps, projectID, in)
}

func routesList(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in RoutesListInput) (analysis.RoutePage, error) {
	page, err := deps.Analysis.ListRoutes(ctx, projectID, in.Cursor, in.Limit)
	if err != nil {
		return analysis.RoutePage{}, err
	}
	return page, nil
}

// MCPRoutesGet implements routes.get.
func MCPRoutesGet(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in RoutesGetInput) (analysis.Route, error) {
	if err := requireScope(caller, projectID); err != nil {
		return analysis.Route{}, err
	}
	return routesGet(ctx, deps, projectID, in)
}

func routesGet(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in RoutesGetInput) (analysis.Route, error) {
	return deps.Analysis.RouteByKey(ctx, projectID, in.Key)
}

// MCPRoutesUpsert implements routes.upsert.
func MCPRoutesUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in RoutesUpsertInput) (analysis.Route, error) {
	if err := requireScope(caller, projectID); err != nil {
		return analysis.Route{}, err
	}
	return routesUpsert(ctx, deps, caller, projectID, in)
}

func routesUpsert(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in RoutesUpsertInput) (analysis.Route, error) {
	// The step list is bounded here, before anything resolves a key,
	// because it is a batch and a batch is refused rather than clamped:
	// a caller that asked for 501 steps and silently got 500 reads a
	// partial claim as a whole one. The domain applies the same bound;
	// this arm exists so the refusal names the count the caller sent
	// rather than the first key that failed to resolve.
	if len(in.Steps) > analysis.MaxRouteSteps {
		return analysis.Route{}, &metamodel.ValidationError{
			Code: metamodel.CodeInvalidInput,
			Fields: []metamodel.FieldError{{
				Path: "/steps",
				Message: fmt.Sprintf("carries %d steps and at most %d are accepted. It is "+
					"refused rather than truncated: a route silently cut to its cap is a "+
					"proof of a progression nobody wrote. Split the walk into two routes",
					len(in.Steps), analysis.MaxRouteSteps),
			}},
		}
	}
	steps := make([]analysis.RouteStepInput, 0, len(in.Steps))
	for _, step := range in.Steps {
		steps = append(steps, analysis.RouteStepInput{
			EntityType: step.EntityType, Key: step.Key, Note: step.Note,
		})
	}
	var params analysis.RouteParams
	if in.Params != nil {
		reach := in.Params.params()
		params = analysis.RouteParams{
			Gate:                 reach.Gating,
			IncludeUngated:       reach.IncludeUngated,
			PropagateContainment: reach.PropagateContainment,
			SeedEntities:         reach.SeedEntities,
			SeedEntityTypes:      reach.SeedEntityTypes,
			MaxDepth:             reach.MaxDepth,
			ExcludeInvalid:       reach.ExcludeInvalid,
		}
	}
	return deps.Analysis.UpsertRoute(ctx, projectID, analysis.RouteInput{
		Key: in.Key, Name: in.Name, Description: in.Description,
		Params: params, Steps: steps,
		ExpectedVersion: in.ExpectedVersion, Actor: actorOf(caller),
	})
}

// MCPRoutesRemove implements routes.remove.
func MCPRoutesRemove(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in RoutesRemoveInput) (RoutesRemovedOutput, error) {
	if err := requireScope(caller, projectID); err != nil {
		return RoutesRemovedOutput{}, err
	}
	return routesRemove(ctx, deps, projectID, in)
}

func routesRemove(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in RoutesRemoveInput) (RoutesRemovedOutput, error) {
	if err := deps.Analysis.RemoveRoute(ctx, projectID, in.Key, in.ExpectedVersion); err != nil {
		return RoutesRemovedOutput{}, err
	}
	return RoutesRemovedOutput{Removed: true}, nil
}

// MCPRoutesCheck implements routes.check.
func MCPRoutesCheck(ctx context.Context, deps MCPDeps, caller Caller, projectID uuid.UUID,
	in RoutesCheckInput) (analysis.RouteCheck, error) {
	if err := requireScope(caller, projectID); err != nil {
		return analysis.RouteCheck{}, err
	}
	return routesCheck(ctx, deps, projectID, in)
}

func routesCheck(ctx context.Context, deps MCPDeps, projectID uuid.UUID,
	in RoutesCheckInput) (analysis.RouteCheck, error) {
	return deps.Analysis.CheckRoute(ctx, projectID, in.Key)
}

// --- Registration ---

// addAnalysisTools registers the eight analysis and route tools.
//
// Every one goes through addScopedTool, so game isolation is enforced in
// one place regardless of how many tools this file grows —
// TestEveryMCPToolGoesThroughAddScopedTool walks the *served* tool list
// and fails on any tool that reached the server another way.
func (s *Server) addAnalysisTools(srv *mcp.Server, deps MCPDeps) {
	traits := analysis.TraitDescription()

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "analysis.cycles",
		Description: fmt.Sprintf(
			"Find where this game's prerequisites loop: a set of entities each of which "+
				"gates the next and the last of which gates the first, so no player can "+
				"open any of them. Every cycle comes back with its entities **and the "+
				"relation type of every edge that closes it**, because the first question a "+
				"designer asks of a reported loop is whether that type should have been "+
				"declared gating at all.\n\n"+
				"**Containment loops are a second list, not more of the first.** A zone "+
				"inside a zone inside the first is a hierarchy that is not one; a "+
				"prerequisite loop is a gate nobody can open. Different sentence, different "+
				"fix, different list.\n\n"+
				"**The clearest real false positive is mutual exclusion.** \"Choosing the "+
				"Horde locks the Alliance\", written as two edges of one type, is a "+
				"legitimate two-cycle in a type that reads like a prerequisite. The fix is a "+
				"trait declaration, not a change to the content — which is why every edge "+
				"here names its type.\n\n"+
				"`max_depth` bounds the hops and therefore the longest cycle this run can "+
				"find; a run reports `depth_limited` rather than calling a game acyclic it "+
				"stopped looking at. %s\n\n%s\n\n%s",
			analysisNoCursorDoc, traits, retryAdvice),
		OutputSchema: analysisCyclesOutputSchema(),
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in AnalysisCyclesInput) (analysis.CyclesResult, error) {
		caller, _ := CallerFrom(ctx)
		return MCPAnalysisCycles(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "analysis.unreachable",
		Description: fmt.Sprintf(
			"Find the content no player can reach: entities the game's own gating edges "+
				"never admit, starting from wherever a player starts. Every finding carries "+
				"a **reason** — the walk stopped at its depth bound beside it, its only way "+
				"in is a container nobody can reach, it has no incoming gate at all, or "+
				"every route to it is blocked — and the entities that block it.\n\n"+
				"**Say where a player starts, or the default does.** `seed_entities` names "+
				"them by (entity type, key); `seed_entity_types` starts from every entity of "+
				"a type; `seed_route` uses a saved route's steps, which is what routes are "+
				"for when you want two people analysing one game to get one answer. The "+
				"three are unioned, never intersected. With none of them, "+
				"`include_ungated` (on by default) starts from every entity with no incoming "+
				"gating edge, which is what makes this usable before anybody has defined a "+
				"start point. Turning it off with no seeds is refused rather than answered "+
				"with \"your whole game is unreachable\".\n\n"+
				"%s\n\n%s\n\n"+
				"**Every count in the answer is there because an empty findings list is not "+
				"a verdict.** `reachable_total`, `per_type`, `seeds.total` and `edges_walked` "+
				"are what tell \"nothing is unreachable\" from \"the walk started nowhere\". "+
				"Read them before you believe the empty list.\n\n"+
				"Pass the previous answer's next_cursor for the next page; a cursor belongs "+
				"to the game and the question it was issued for and is refused against any "+
				"other. %s\n\n%s",
			analysisGateDoc, analysisFalsePositiveDoc, traits, retryAdvice),
		OutputSchema: analysisUnreachableOutputSchema(),
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in AnalysisUnreachableInput) (analysis.UnreachableResult, error) {
		caller, _ := CallerFrom(ctx)
		return MCPAnalysisUnreachable(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "analysis.orphans",
		Description: fmt.Sprintf(
			"Find the content nothing points at. `mode` is one of %s: `isolated` (the "+
				"default) has no edges at all, `sink` has incoming edges and none outgoing, "+
				"`source` has outgoing and none incoming. Every finding carries both "+
				"degrees, so the three modes are checkable against each other rather than "+
				"believed.\n\n"+
				"**Edges of every relation type are counted except those declared "+
				"`annotation`**, and that exception is the entire reason that word exists in "+
				"the vocabulary: a designer saying \"I looked, and this type is decoration\" "+
				"is what stops an illustration edge from keeping a half-finished idea off "+
				"this list. The types excluded are named in the answer.\n\n"+
				"**This is the one analysis that does not refuse a game which declared "+
				"nothing.** It asks the vocabulary for exactly one thing — which types are "+
				"`annotation` — and a game with none is a perfectly meaningful input. The "+
				"other three refuse, because each would otherwise report a clean bill of "+
				"health from an engine with no edge it was allowed to walk.\n\n"+
				"%s\n\n"+
				"`considered_total` is how many entities this run looked at, and it is what "+
				"makes an empty findings list mean something. %s\n\n%s",
			metamodel.QuotedList(orphanModeNames()), analysisOrphanDoc, traits, retryAdvice),
		OutputSchema: analysisOrphansOutputSchema(),
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in AnalysisOrphansInput) (analysis.OrphansResult, error) {
		caller, _ := CallerFrom(ctx)
		return MCPAnalysisOrphans(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "routes.list",
		Description: fmt.Sprintf(
			"List this game's routes: key, name, description, version, how many steps each "+
				"has and its three-state health. **No step lists and no stored verdicts** — "+
				"routes.get is where a walk and its proof come from, and a listing that "+
				"carried them would grow in the length of the routes rather than in their "+
				"number.\n\n%s\n\n"+
				"Pass the previous answer's next_cursor for the next page; a cursor belongs "+
				"to the game it was issued for and is refused against any other. %s",
			analysisStaleDoc, retryAdvice),
		OutputSchema: routesListOutputSchema(),
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RoutesListInput) (analysis.RoutePage, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRoutesList(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "routes.get",
		Description: fmt.Sprintf(
			"Read one route by key: its ordered steps, its stored parameters, its version, "+
				"its three-state status and the verdict its last check left behind. Keys are "+
				"matched without regard to case. The version is what routes.upsert's and "+
				"routes.remove's expected_version take.\n\n"+
				"**A step whose entity was deleted survives as a tombstone**: `entity_id` is "+
				"null and the entity type key and key it was written with are still there, "+
				"so a route visibly keeps a step it can no longer resolve rather than "+
				"quietly becoming shorter. A step whose *type* was renamed keeps spelling "+
				"the old key too, and still resolves — routes.check reports the move beside "+
				"an `ok`.\n\n%s\n\n%s",
			analysisStaleDoc, retryAdvice),
		OutputSchema: routeOutputSchema(),
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RoutesGetInput) (analysis.Route, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRoutesGet(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "routes.upsert",
		Description: fmt.Sprintf(
			"Create or replace one route, addressed by key. A route is an **ordered claim "+
				"about this game's content**: these entities, in this order, are a walk a "+
				"player can actually make. It is not a saved query and not a picture — "+
				"routes.check is what turns it into a verdict.\n\n"+
				"**The whole step list is replaced.** Steps are positional and every index "+
				"moves when one is inserted, so sending the list you want is simpler than "+
				"diffing one. At most %d steps; a longer list is refused rather than "+
				"truncated, because a route silently cut to its cap is a proof of a "+
				"progression nobody wrote.\n\n"+
				"**Every step must resolve.** A step naming an entity this game does not "+
				"have is not_found, at the step's own position, and nothing is stored — a "+
				"tombstone is what a *deletion* leaves behind, and accepting one on write "+
				"would let you author a route out of typos and be told it holds.\n\n"+
				"**expected_version is required. Pass 0 to create a route that must not "+
				"exist yet, or the version you read to replace the one that is there.** "+
				"Omitting it is invalid_input, not a guess. Any other version, against a "+
				"route this game does not have, is not_found saying it was removed — not a "+
				"quiet re-creation: a route's stored verdict and every one of its step rows "+
				"hang off its id, so a re-creation would discard a proved progression *and* "+
				"its proof and answer success.\n\n"+
				"**`params` is the route's own definition of what holding together means** — "+
				"its start set, its gating, its depth bound, its reading of flagged edges — "+
				"and routes.check reads these and not the caller's defaults, so two people "+
				"checking one route get one answer. They are validated here: a route whose "+
				"`gate` is nonsense is refused now rather than failing every check from now "+
				"on. %s\n\n%s",
			analysis.MaxRouteSteps, analysisGateDoc, retryAdvice),
		OutputSchema: routeOutputSchema(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RoutesUpsertInput) (analysis.Route, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRoutesUpsert(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "routes.remove",
		Description: fmt.Sprintf(
			"Delete one route by key, with its steps and its stored verdict.\n\n"+
				"**It takes an expected_version, and views.remove does not.** The inversion "+
				"is deliberate: a view is derived content, cheap to re-upsert from a document "+
				"you already hold, so a version there would make every removal a "+
				"read-then-write against churn nobody cares about. A route's ordered steps "+
				"are authored and its verdict is not reconstructible from anything — "+
				"somebody proved a progression and the proof goes with the row. Read the "+
				"route and send the version you saw. A key that names no route is "+
				"not_found. %s",
			retryAdvice),
		OutputSchema: routesRemovedOutputSchema(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RoutesRemoveInput) (RoutesRemovedOutput, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRoutesRemove(ctx, deps, caller, projectID, in)
	})

	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "routes.check",
		Description: fmt.Sprintf(
			"Check whether a route still holds, and store the verdict. For each step in "+
				"turn: is it reachable given the route's start set **plus every step before "+
				"it**? Each step gets exactly one of four answers.\n\n"+
				"  - `ok` — reachable.\n"+
				"  - `missing_entity` — its entity was deleted; the step and the keys it was "+
				"written with are still there, and those keys are what the verdict names.\n"+
				"  - `unmet_prerequisite` — the walk does not reach it; `blockers` names what "+
				"stands in the way.\n"+
				"  - `out_of_order` — an `ordering` relation says this step must come before "+
				"a step the route places earlier. This is **not** a reachability answer: it "+
				"is a comparison of edge direction against step position, so do not go "+
				"looking for a missing prerequisite when you see it.\n\n"+
				"**A step whose entity type was renamed is still `ok`**, with `type_renamed` "+
				"beside it naming both spellings. A rename moves the catalogue row and "+
				"nothing else; the stored step keeps the old spelling and resolves by id. "+
				"Reporting a healthy route as broken over a cosmetic change would be the "+
				"worse answer.\n\n"+
				"**The check runs under the route's own stored `params`**, never the "+
				"caller's defaults, which is what makes one route mean one thing however "+
				"many people check it.\n\n"+
				"**This is the only thing this engine caches**, and it is allowed to be "+
				"cached for one reason: it can say when it went out of date. "+
				"`checked_design_version` is the design this verdict was proved against and "+
				"`design_version` is where the game is now.\n\n%s\n\n%s\n\n%s",
			analysisStaleDoc, traits, retryAdvice),
		OutputSchema: routeCheckOutputSchema(),
	}, func(ctx context.Context, deps MCPDeps, projectID uuid.UUID, in RoutesCheckInput) (analysis.RouteCheck, error) {
		caller, _ := CallerFrom(ctx)
		return MCPRoutesCheck(ctx, deps, caller, projectID, in)
	})
}

// orphanModeNames renders the mode vocabulary for a description, from
// the domain's own list rather than from three words typed here.
func orphanModeNames() []string {
	out := make([]string, 0, len(analysis.OrphanModes))
	for _, mode := range analysis.OrphanModes {
		out = append(out, string(mode))
	}
	return out
}

// --- Hand-written output schemas ---
//
// Written by hand for the reason mcp.go's own schema block gives: the
// SDK validates a tool's output against its *marshalled JSON*, and its
// reflection-based inference gets that shape wrong for any type whose
// marshalling comes from a method rather than from its literal Go
// structure — analysis.Verdict is exactly such a type, and a mismatch
// there surfaces only when the tool is called.
//
// **Nothing here is an open object.** Every shape this surface answers
// with is fixed: it does not depend on a caller's document the way a
// view's nodes depend on its query, so there is nothing to shrug at.
//
// **The shared pieces are functions and not variables**, which is not a
// style choice: `mcp.AddTool` refuses a schema whose sub-schemas do not
// form a *tree*, and a `*jsonschema.Schema` reused at two positions —
// `cycles` and `containment_cycles` hold the same shape — is one node
// appearing twice. The refusal is a panic at registration, so a server
// built that way does not start; a function hands each position its own
// value. TestEveryAnalysisToolIsCallableOverTheRealTransport is what
// exercises the schemas afterwards.

func analysisStatsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:       "object",
		Required:   []string{"duration_ms"},
		Properties: map[string]*jsonschema.Schema{"duration_ms": integerSchema()},
	}
}

// analysisSemanticsSchema is one relation type as the engine read it,
// and it is on every answer this surface gives: a verdict always arrives
// with the reading it rests on.
func analysisSemanticsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"key", "analysis_traits", "source"},
		Properties: map[string]*jsonschema.Schema{
			"key":             stringSchema(),
			"analysis_traits": arrayOf(stringSchema()),
			"source":          stringSchema(),
		},
	}
}

func analysisSeedRefSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"entity_type", "key"},
		Properties: map[string]*jsonschema.Schema{
			"entity_type": stringSchema(), "key": stringSchema(),
		},
	}
}

func analysisCycleSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"entities", "edges", "length"},
		Properties: map[string]*jsonschema.Schema{
			"entities": arrayOf(&jsonschema.Schema{
				Type:     "object",
				Required: []string{"entity_type", "key", "name"},
				Properties: map[string]*jsonschema.Schema{
					"entity_type": stringSchema(), "key": stringSchema(), "name": stringSchema(),
				},
			}),
			"edges": arrayOf(&jsonschema.Schema{
				Type:     "object",
				Required: []string{"relation_id", "relation_type"},
				Properties: map[string]*jsonschema.Schema{
					"relation_id": stringSchema(), "relation_type": stringSchema(),
				},
			}),
			"length": integerSchema(),
		},
	}
}

func analysisCyclesOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Required: []string{"cycles", "containment_cycles", "semantics_source", "seed_total",
			"edges_walked", "invalid_edges_followed", "truncated", "depth_limited", "stats"},
		Properties: map[string]*jsonschema.Schema{
			"cycles":                 arrayOf(analysisCycleSchema()),
			"containment_cycles":     arrayOf(analysisCycleSchema()),
			"semantics_source":       arrayOf(analysisSemanticsSchema()),
			"seed_total":             integerSchema(),
			"edges_walked":           integerSchema(),
			"invalid_edges_followed": integerSchema(),
			"truncated":              boolSchema(),
			"depth_limited":          boolSchema(),
			"stats":                  analysisStatsSchema(),
		},
	}
}

func analysisSeedReportSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"entities", "include_ungated", "total"},
		Properties: map[string]*jsonschema.Schema{
			"entities":        arrayOf(analysisSeedRefSchema()),
			"entity_types":    arrayOf(stringSchema()),
			"include_ungated": boolSchema(),
			"total":           integerSchema(),
		},
	}
}

func analysisUnreachableOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Required: []string{"unreachable", "reachable_total", "unreachable_total", "per_type",
			"seeds", "semantics_source", "gating", "invalid_edges_followed", "edges_walked",
			"truncated", "depth_limited", "stats"},
		Properties: map[string]*jsonschema.Schema{
			"unreachable": arrayOf(&jsonschema.Schema{
				Type:     "object",
				Required: []string{"entity_type", "key", "name", "reason"},
				Properties: map[string]*jsonschema.Schema{
					"entity_type": stringSchema(), "key": stringSchema(), "name": stringSchema(),
					"reason": stringSchema(), "blockers": arrayOf(analysisSeedRefSchema()),
				},
			}),
			"next_cursor":       stringSchema(),
			"reachable_total":   integerSchema(),
			"unreachable_total": integerSchema(),
			"per_type": arrayOf(&jsonschema.Schema{
				Type:     "object",
				Required: []string{"entity_type", "reachable", "unreachable"},
				Properties: map[string]*jsonschema.Schema{
					"entity_type": stringSchema(),
					"reachable":   integerSchema(), "unreachable": integerSchema(),
				},
			}),
			"seeds":                  analysisSeedReportSchema(),
			"semantics_source":       arrayOf(analysisSemanticsSchema()),
			"gating":                 stringSchema(),
			"invalid_edges_followed": integerSchema(),
			"edges_walked":           integerSchema(),
			"truncated":              boolSchema(),
			"depth_limited":          boolSchema(),
			"note":                   stringSchema(),
			"stats":                  analysisStatsSchema(),
		},
	}
}

func analysisOrphansOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Required: []string{"orphans", "mode", "considered_total", "excluded_relation_types",
			"semantics_source", "stats"},
		Properties: map[string]*jsonschema.Schema{
			"orphans": arrayOf(&jsonschema.Schema{
				Type:     "object",
				Required: []string{"entity_type", "key", "name", "in_degree", "out_degree"},
				Properties: map[string]*jsonschema.Schema{
					"entity_type": stringSchema(), "key": stringSchema(), "name": stringSchema(),
					"in_degree": integerSchema(), "out_degree": integerSchema(),
				},
			}),
			"next_cursor":             stringSchema(),
			"mode":                    stringSchema(),
			"considered_total":        integerSchema(),
			"excluded_relation_types": arrayOf(stringSchema()),
			"semantics_source":        arrayOf(analysisSemanticsSchema()),
			"stats":                   analysisStatsSchema(),
		},
	}
}

// analysisRouteParamsSchema is a route's stored parameters, on the way
// out. It is the same vocabulary the upsert takes, spelled once.
func analysisRouteParamsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"gate":                  stringSchema(),
			"include_ungated":       boolSchema(),
			"propagate_containment": boolSchema(),
			"seed_entities":         arrayOf(analysisSeedRefSchema()),
			"seed_entity_types":     arrayOf(stringSchema()),
			"max_depth":             integerSchema(),
			"exclude_invalid":       boolSchema(),
		},
	}
}

// analysisRouteStepSchema is one stored step. entity_id is nullable and
// the null is the tombstone, so the schema says so rather than promising
// a string that is not always there.
func analysisRouteStepSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"position", "entity_id", "entity_type", "key", "note"},
		Properties: map[string]*jsonschema.Schema{
			"position":    integerSchema(),
			"entity_id":   {Types: []string{"string", "null"}},
			"entity_type": stringSchema(), "key": stringSchema(), "note": stringSchema(),
		},
	}
}

func routeOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Required: []string{"key", "name", "description", "params", "version", "steps",
			"status", "design_version"},
		Properties: map[string]*jsonschema.Schema{
			"key": stringSchema(), "name": stringSchema(), "description": stringSchema(),
			"params": analysisRouteParamsSchema(), "version": integerSchema(),
			"steps": arrayOf(analysisRouteStepSchema()), "status": stringSchema(),
			"last_check":                  objectSchema(),
			"last_checked_at":             stringSchema(),
			"last_checked_design_version": integerSchema(),
			"design_version":              integerSchema(),
		},
	}
}

func routesListOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"routes"},
		Properties: map[string]*jsonschema.Schema{
			"routes": arrayOf(&jsonschema.Schema{
				Type: "object",
				Required: []string{"key", "name", "description", "version", "step_count",
					"status"},
				Properties: map[string]*jsonschema.Schema{
					"key": stringSchema(), "name": stringSchema(), "description": stringSchema(),
					"version": integerSchema(), "step_count": integerSchema(),
					"status": stringSchema(),
				},
			}),
			"next_cursor": stringSchema(),
		},
	}
}

func routesRemovedOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:       "object",
		Required:   []string{"removed"},
		Properties: map[string]*jsonschema.Schema{"removed": boolSchema()},
	}
}

// analysisStepCheckSchema is one step's verdict.
//
// `verdict` is a string and the four values are named in the tool's own
// description rather than as an enum here: an enum would be a second
// copy of analysis.Verdicts, and that list is already enforced by the
// domain's own encoder, which refuses anything outside it — including
// the zero value, which is deliberately not `ok`.
func analysisStepCheckSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"position", "verdict", "entity_type", "key"},
		Properties: map[string]*jsonschema.Schema{
			"position": integerSchema(), "verdict": stringSchema(),
			"entity_type": stringSchema(), "key": stringSchema(),
			"blockers":     arrayOf(analysisSeedRefSchema()),
			"must_precede": arrayOf(analysisSeedRefSchema()),
			"type_renamed": {
				Type:     "object",
				Required: []string{"was", "now"},
				Properties: map[string]*jsonschema.Schema{
					"was": stringSchema(), "now": stringSchema(),
				},
			},
		},
	}
}

func routeCheckOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Required: []string{"key", "holds", "steps", "steps_checked", "steps_ok", "steps_broken",
			"seeds", "semantics_source", "gating", "walks", "edges_walked",
			"invalid_edges_followed", "truncated", "depth_limited",
			"checked_at", "checked_design_version", "design_version", "status", "stats"},
		Properties: map[string]*jsonschema.Schema{
			"key": stringSchema(), "holds": boolSchema(),
			"steps":         arrayOf(analysisStepCheckSchema()),
			"steps_checked": integerSchema(), "steps_ok": integerSchema(),
			"steps_broken":     integerSchema(),
			"seeds":            analysisSeedReportSchema(),
			"semantics_source": arrayOf(analysisSemanticsSchema()),
			"gating":           stringSchema(),
			"walks":            integerSchema(),
			"edges_walked":     integerSchema(), "invalid_edges_followed": integerSchema(),
			"truncated": boolSchema(), "depth_limited": boolSchema(),
			"checked_at": stringSchema(), "checked_design_version": integerSchema(),
			"design_version": integerSchema(), "status": stringSchema(),
			"stats": analysisStatsSchema(),
		},
	}
}
