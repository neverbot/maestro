package analysis

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/graph"
)

// Gating is how many of an entity's gates must be reachable before the
// entity is.
//
// The two are a real choice and neither is stored: `any` is what a walk
// computes natively and is the default; `all` is the stricter reading a
// caller may ask for on the call. See the spec's O1 and the two tests
// that hold the pair apart,
// TestUnderAllABothGatedEntityNeedsBothGatesReachable and
// TestTheSameGameReportsFewerUnreachableUnderAnyThanUnderAll.
type Gating string

const (
	// GatingAny reaches an entity the moment one of its gates does.
	GatingAny Gating = "any"
	// GatingAll reaches an entity only when every one of its gates has
	// been reached. It is **not a walk** -- see allFixpoint.
	GatingAll Gating = "all"
)

// Gatings is every value of Gating, so a caller can enumerate them and a
// surface can validate against the list rather than repeat it.
var Gatings = []Gating{GatingAny, GatingAll}

// SeedRef is one entity named as a start point, by the pair that
// addresses it: an entity key is unique per (game, entity type) and not
// per game, so the type key is half the address.
type SeedRef struct {
	EntityType string `json:"entity_type"`
	Key        string `json:"key"`
}

// Params is one reachability question.
//
// **ExcludeInvalid defaults to false, and that is a decision this
// package took against the one internal/views took.** `invalid` on a
// relation means *this row's `fields` no longer fit its type's
// `field_schema`*. It says nothing about the row's endpoints or its
// relation type, and endpoints and relation type are the only things any
// analysis here reads. Excluding invalid edges would let a field-schema
// edit on `requires` -- adding a required `difficulty` field, say --
// make forty missions report as unreachable, with the cause being a
// change that has no relationship at all to whether they are reachable.
// **A verdict about structure must not move when a field schema moves.**
//
// internal/views/compile.go's invalidFilter excludes by default, for the
// opposite and equally correct reason: a picture *asserts* a
// relationship, a reader believes the assertion, and drawing an edge
// whose own values are known-broken is a claim the game does not
// support. A reader comparing the two files finds the disagreement
// explained here rather than assuming one of them forgot. internal/
// metamodel's traversal listing follows invalid edges too, its `Invalid`
// filter being a tri-state whose default is "both", so this package is
// with the metamodel and against views deliberately.
//
// The caller may ask for the stricter reading, and every result reports
// InvalidEdgesFollowed either way: silence about a real limit is the
// "correct and unasserted" defect with the sign flipped.
type Params struct {
	ProjectID uuid.UUID

	// Semantics is the resolved reading of the game's relation types.
	// It is passed in rather than resolved here because the refusal for
	// a game that declared nothing belongs to Resolve, which owns it,
	// and because one analysis resolves once and may reach twice.
	Semantics Semantics

	// SeedEntities, SeedEntityTypes and SeedRoute are the three ways to
	// say where a player starts. They are unioned, never intersected.
	SeedEntities    []SeedRef
	SeedEntityTypes []string
	SeedRoute       string

	// IncludeUngated adds every entity with no incoming gating edge.
	// nil means the documented default, true: it is what makes the
	// analysis usable before anybody has defined a start point.
	IncludeUngated *bool

	// PropagateContainment follows containment edges from container to
	// contained, so reaching a zone reaches what is in it. nil means the
	// documented default, true.
	PropagateContainment *bool

	// Gating is any (default) or all.
	Gating Gating

	// MaxDepth bounds the hops. Zero means DefaultMaxDepth; above
	// MaxMaxDepth is limit_exceeded naming the cap, never a clamp.
	MaxDepth int

	ExcludeInvalid bool
}

// Reach is what one closure found, and it is deliberately more than a
// set of ids.
//
// **An empty report and a report over an empty walk are different facts**
// and this struct is what keeps them apart: SeedCount, EdgesWalked and
// ReachedTotal say how much work was done, so "nothing is unreachable"
// can be told from "the walk started nowhere and found nothing". Every
// analysis built on this carries those numbers into its own answer for
// the same reason.
type Reach struct {
	// Reached is every entity the closure admitted, seeds included.
	Reached map[uuid.UUID]bool
	// Depth is the shallowest depth each reached entity was found at. It
	// is what tells a later analysis that the walk stopped at its bound
	// next to a particular entity rather than that nothing lay beyond.
	Depth map[uuid.UUID]int

	// SeedIDs and Seeds are the start set as the engine understood it:
	// the ids the anchor matched, and the keyed refs for the ones a
	// caller named by key. Reporting them is not a courtesy -- it is the
	// only field that distinguishes a real "your whole game is
	// unreachable" from a mistyped seed key that somehow got through.
	SeedIDs []uuid.UUID
	Seeds   []SeedRef
	// SeedEntityTypes and IncludeUngated are the other two halves of the
	// same statement, for the seed sources that name no individual key.
	SeedEntityTypes []string
	IncludeUngated  bool
	// SeedCount is how many entities the anchor actually matched, which
	// is the number seed_entity_types and include_ungated cannot be read
	// off in keys.
	SeedCount int

	Gating Gating

	// EdgesWalked is one per edge traversal the walk handed back, and
	// InvalidEdgesFollowed is how many distinct flagged relations were
	// among them. The second is reported even when it is zero.
	EdgesWalked          int
	InvalidEdgesFollowed int

	// Truncated is set when the walk came back at MaxWalkRows + 1 rows,
	// which is the one thing that tells a full answer from a capped one
	// -- see graph.Walk.MaxRows.
	Truncated bool
	// DepthLimited is set when a row came back at the depth bound, so
	// the walk stopped expanding there. It does **not** assert that
	// anything lay beyond; the unreachable analysis is what turns it
	// into a per-entity reason, by asking whether an unreached entity's
	// gate sits exactly on that frontier.
	DepthLimited bool

	// Passes and Note are the all-gating fixpoint's own report. Note is
	// empty under `any` and under an `all` run that admitted everything
	// the walk reached.
	Passes int
	Note   string
}

// Reach computes one reachability closure.
//
// **This is the component internal/views' O8 promised the analysis
// engine would own**, and both analysis.unreachable and routes.check
// call it. It compiles into graph.WalkCTE and emits no recursion of its
// own.
func (s *Service) Reach(ctx context.Context, p Params) (Reach, error) {
	if p.MaxDepth > MaxMaxDepth {
		return Reach{}, limitExceeded("max_depth", p.MaxDepth, MaxMaxDepth)
	}
	if len(p.SeedEntities) > MaxSeedKeys {
		return Reach{}, limitExceeded("seed_entities", len(p.SeedEntities), MaxSeedKeys)
	}
	if len(p.SeedEntityTypes) > MaxTypeKeys {
		return Reach{}, limitExceeded("seed_entity_types", len(p.SeedEntityTypes), MaxTypeKeys)
	}
	switch p.Gating {
	case "", GatingAny:
		p.Gating = GatingAny
	case GatingAll:
	default:
		return Reach{}, invalidInput("gating", fmt.Sprintf(
			"is %q; the two readings are %q and %q", p.Gating, GatingAny, GatingAll))
	}

	seed, err := s.resolveSeeds(ctx, p)
	if err != nil {
		return Reach{}, err
	}
	walk := p.normalisedWalk("reach", seed.sql, seed.args)

	body, args := graph.WalkCTE(walk)
	// The outer statement is this package's own, and it is where the
	// invalid flag is read: the walk hands back via_relation and has no
	// opinion about a relation's own values, which is the seam between
	// the SQL primitive and this package's policy.
	statement := "WITH RECURSIVE " + body + `
SELECT w.id, w.depth, w.via_relation, COALESCE(r.invalid, false)
FROM ` + graph.ReadFrom(walk) + ` w
LEFT JOIN relations r ON r.id = w.via_relation AND r.project_id = $1`

	out := Reach{
		Reached:         map[uuid.UUID]bool{},
		Depth:           map[uuid.UUID]int{},
		Seeds:           seed.refs,
		SeedEntityTypes: seed.entityTypes,
		IncludeUngated:  seed.includeUngated,
		Gating:          p.Gating,
	}
	invalid := map[uuid.UUID]bool{}
	rows := 0
	err = s.runInTx(ctx, s.statementBudget(), statement, args, func(r pgx.Rows) error {
		for r.Next() {
			var id uuid.UUID
			var depth int
			var via *uuid.UUID
			var edgeInvalid bool
			if err := r.Scan(&id, &depth, &via, &edgeInvalid); err != nil {
				return fmt.Errorf("scan a walk row: %w", err)
			}
			rows++
			if rows > MaxWalkRows {
				// The row past the cap, which is the only reason
				// graph.WalkCTE emits LIMIT MaxRows + 1. It is counted
				// as truncation and not kept.
				out.Truncated = true
				continue
			}
			if via != nil {
				out.EdgesWalked++
				if edgeInvalid {
					invalid[*via] = true
				}
			} else {
				out.SeedCount++
				out.SeedIDs = append(out.SeedIDs, id)
			}
			if known, seen := out.Depth[id]; !seen || depth < known {
				out.Depth[id] = depth
			}
			out.Reached[id] = true
			if depth == walk.MaxDepth {
				out.DepthLimited = true
			}
		}
		return r.Err()
	})
	if err != nil {
		return Reach{}, err
	}
	out.InvalidEdgesFollowed = len(invalid)

	// A seed set that resolved to nothing, with nothing to fall back on,
	// is refused here as well as before the walk. The pre-flight
	// refusal catches a caller who named no source at all; this one
	// catches a caller whose sources named real things that hold no
	// entities, and both answer the same way rather than reporting that
	// an entire game is unreachable.
	if out.SeedCount == 0 && !seed.includeUngated {
		return Reach{}, emptySeedSet("resolved to no entity of this game")
	}

	if p.Gating == GatingAll {
		if err := s.allFixpoint(ctx, p, &out); err != nil {
			return Reach{}, err
		}
	}
	return out, nil
}

// normalisedWalk builds the one walk every gating analysis runs.
//
// **The engine normalises every gating edge into one internal form,
// needed → dependent, reversing prerequisite_of edges as it reads them,
// so every gating analysis runs over one direction and never thinks
// about direction again.** A `prerequisite_of` edge reads "A requires B"
// -- the *target* must be satisfied before the source, which is the
// spec's own wording -- so it is followed target→source; an `unlocks`
// edge reads "A unlocks B" and is followed source→target. Both are
// gates and after this function nothing downstream knows which was
// which.
//
// Direction is Any and the per-type direction lives in EdgePredicate.
// That is not a workaround, it is the only shape that is correct here.
// graph.Walk carries one Direction for the whole walk, and a game's
// gating edges point both ways. Two walks unioned would double-count a
// node reachable through both and would need two truncation flags. So:
// Any, whose near/far are deliberately a single arm over a scalar CASE
// (see graph.WalkCTE's own note on why two UNION arms are both illegal
// in Postgres and wrong when written legally), and a predicate that says
// which types may be followed which way. The predicate is spliced
// **inside** the recursion's JOIN, where both `r` and `w` are in scope,
// so an edge excluded by direction is an edge the walk does not traverse
// rather than a row it filters afterwards --
// TestANormalisedWalkWillNotFollowAGatingEdgeAgainstItsDirection is what
// makes that a decision rather than a coincidence.
//
// RelationTypeIDs still carries the union, because graph.Walk documents
// that an **empty list follows no edge at all** -- not every edge -- and
// because the type filter is the clause the index serves; the predicate
// narrows within it.
//
// `containment` is in the forward set here **and is also walked
// separately by the cycle analysis**, for two different questions.
// Reachability propagates from container to contained, which is a
// forward gate. A containment *loop* is a different report with a
// different fix, and it is a separate graph. Both statements are true,
// and both are written down because a reader who finds `containment` in
// two places will otherwise assume one of them is a mistake.
//
// CarryRelationPath is deliberately not set: reachability names entities
// and never edges, and the column exists for the cycle analysis, which
// has to name every edge of a loop.
func (p Params) normalisedWalk(name, seedSQL string, seedArgs []any) graph.Walk {
	forward, reverse, symmetric := p.edgeSets()

	pred := "((r.relation_type_id = ANY($1::uuid[]) AND r.source_id = w.id)" +
		" OR (r.relation_type_id = ANY($2::uuid[]) AND r.target_id = w.id)" +
		" OR (r.relation_type_id = ANY($3::uuid[])))"
	args := []any{forward, reverse, symmetric}
	if p.ExcludeInvalid {
		pred += " AND r.invalid = false"
	}

	maxDepth := p.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDepth
	}
	return graph.Walk{
		Name:            name,
		ProjectID:       p.ProjectID,
		SeedSQL:         seedSQL,
		SeedArgs:        seedArgs,
		RelationTypeIDs: concatIDs(forward, reverse, symmetric),
		Direction:       graph.Any,
		EdgePredicate:   pred,
		EdgeArgs:        args,
		MinDepth:        0,
		MaxDepth:        maxDepth,
		MaxRows:         MaxWalkRows,
	}
}

// edgeSets is the one place the trait vocabulary becomes three id
// arrays, and every statement in this package that has an opinion about
// direction takes them from here.
//
// forward is followed source→target, reverse target→source, symmetric
// either way. `containment` joins forward only when the caller left
// propagation on. `annotation` and a bare `acyclic` appear in none of
// the three, which is how a type declared inert is followed by nothing.
func (p Params) edgeSets() (forward, reverse, symmetric []uuid.UUID) {
	sem := p.Semantics
	forward = concatIDs(sem.WithTrait("unlocks"), sem.WithTrait("ordering"))
	if p.propagateContainment() {
		forward = concatIDs(forward, sem.WithTrait("containment"))
	}
	reverse = sem.WithTrait("prerequisite_of")
	symmetric = sem.WithTrait("symmetric")
	return dedupeIDs(forward), dedupeIDs(reverse), dedupeIDs(symmetric)
}

// inGateSQL is the normalised in-edge test, written once because five
// things ask it: which entities are ungated (and therefore seeds), which
// gates an entity has under `all`, whether an unreachable entity is
// isolated or merely blocked, which entities block it, and whether a
// route step's prerequisites hold.
//
// **A symmetric edge is not an in-gate.** It is an adjacency: it says
// two places are joined, not that one must be reached before the other,
// and counting it would make every zone in a connected map gated by
// every neighbour -- so with no explicit seeds a map would have no
// ungated entity at all and the whole game would report unreachable.
//
// node is the SQL expression naming the entity whose in-edges are
// wanted; forward and reverse are the placeholders holding the two id
// arrays.
func inGateSQL(node, forward, reverse string, excludeInvalid bool) string {
	pred := fmt.Sprintf(
		"r.project_id = $1 AND ((r.relation_type_id = ANY(%s::uuid[]) AND r.target_id = %s)"+
			" OR (r.relation_type_id = ANY(%s::uuid[]) AND r.source_id = %s))",
		forward, node, reverse, node)
	if excludeInvalid {
		pred += " AND r.invalid = false"
	}
	return pred
}

// seedPlan is the resolved start set: the SQL the walk anchors on, its
// arguments, and the readable account of what it means.
type seedPlan struct {
	sql            string
	args           []any
	refs           []SeedRef
	entityTypes    []string
	includeUngated bool
}

// resolveSeeds turns the three seed sources into one anchor.
//
// **A key that does not resolve is not_found naming the key, never
// dropped silently.** A silently empty seed set turns "you gave me a bad
// key" into "your entire game is unreachable", and that sentence is the
// single most damaging wrong answer this engine can produce.
//
// The seed SQL filters on the project in every arm even though
// graph.WalkCTE's anchor join filters again. That is **redundant today**
// and it is written anyway: the redundancy is one line, and the shape it
// defends is a later seed source that does not go through the anchor.
// Saying so here is the phrasing the views sub-project's own lesson asks
// for -- a silence around a real property is the documentation defect
// with the sign flipped.
func (s *Service) resolveSeeds(ctx context.Context, p Params) (seedPlan, error) {
	plan := seedPlan{includeUngated: p.includeUngated()}

	ids := make([]uuid.UUID, 0, len(p.SeedEntities))
	for i, ref := range p.SeedEntities {
		row, err := s.meta.EntityByKey(ctx, p.ProjectID, ref.EntityType, ref.Key)
		if err != nil {
			return seedPlan{}, fmt.Errorf("seed_entities[%d] (%s %q): %w",
				i, ref.EntityType, ref.Key, err)
		}
		ids = append(ids, row.ID)
		plan.refs = append(plan.refs, SeedRef{EntityType: ref.EntityType, Key: row.Key})
	}
	if p.SeedRoute != "" {
		steps, err := s.routeSeeds(ctx, p.ProjectID, p.SeedRoute)
		if err != nil {
			return seedPlan{}, err
		}
		for _, step := range steps {
			if step.EntityID == nil {
				// A tombstoned step: its entity was deleted and the row
				// kept its keys. It contributes no seed and is not an
				// error -- routes.check is where a missing step is a
				// verdict -- but it is named in the seed report so the
				// count and the list agree.
				plan.refs = append(plan.refs,
					SeedRef{EntityType: step.EntityTypeKey, Key: step.EntityKey})
				continue
			}
			ids = append(ids, *step.EntityID)
			plan.refs = append(plan.refs,
				SeedRef{EntityType: step.EntityTypeKey, Key: step.EntityKey})
		}
	}

	typeIDs := make([]uuid.UUID, 0, len(p.SeedEntityTypes))
	for i, key := range p.SeedEntityTypes {
		typ, err := s.meta.EntityTypeByKey(ctx, p.ProjectID, key)
		if err != nil {
			return seedPlan{}, fmt.Errorf("seed_entity_types[%d] (%q): %w", i, key, err)
		}
		typeIDs = append(typeIDs, typ.ID)
		plan.entityTypes = append(plan.entityTypes, typ.Key)
	}

	if len(ids) == 0 && len(typeIDs) == 0 && !plan.includeUngated {
		return seedPlan{}, emptySeedSet("names no start point at all")
	}

	forward, reverse, _ := p.edgeSets()
	b := &binder{}
	project := b.bind(p.ProjectID)
	var arms []string
	if len(ids) > 0 {
		arms = append(arms, fmt.Sprintf(
			"SELECT e.id FROM entities e WHERE e.project_id = %s AND e.id = ANY(%s::uuid[])",
			project, b.bind(ids)))
	}
	if len(typeIDs) > 0 {
		arms = append(arms, fmt.Sprintf(
			"SELECT e.id FROM entities e WHERE e.project_id = %s "+
				"AND e.entity_type_id = ANY(%s::uuid[])", project, b.bind(typeIDs)))
	}
	if plan.includeUngated {
		// $1 inside inGateSQL is the *walk's* project placeholder, which
		// graph.Renumber leaves alone and which every statement in this
		// repository agrees holds the project id. The seed's own arms
		// bind their own copy because the seed is renumbered from $1 up.
		arms = append(arms, fmt.Sprintf(
			"SELECT e.id FROM entities e WHERE e.project_id = %s AND NOT EXISTS ("+
				"SELECT 1 FROM relations r WHERE %s)",
			project, inGateSQL("e.id", b.bind(forward), b.bind(reverse), p.ExcludeInvalid)))
	}
	plan.sql = strings.Join(arms, "\n  UNION\n")
	plan.args = b.args
	// The seed's own `$1` is its first bound argument and not the walk's:
	// graph renumbers a seed from $1 upwards. inGateSQL writes `$1` for
	// the project id, so the ungated arm is rewritten to use the seed's
	// own placeholder for it.
	plan.sql = strings.ReplaceAll(plan.sql, "r.project_id = $1", "r.project_id = "+project)
	return plan, nil
}

// routeSeeds reads a route's steps as start points. It is O2's answer:
// **no start_sets table**, because a route already is an ordered list of
// this game's entities, which is a seed set with an order nobody has to
// use. A route of another game is not_found for the reason every lookup
// in this repository is -- the statement filters on the resolved project
// id, in SQL.
func (s *Service) routeSeeds(ctx context.Context, projectID uuid.UUID, key string) (
	[]dbq.ListRouteStepsByRouteKeyRow, error,
) {
	steps, err := s.q.ListRouteStepsByRouteKey(ctx, dbq.ListRouteStepsByRouteKeyParams{
		ProjectID: projectID, Key: key,
	})
	if err != nil {
		return nil, fmt.Errorf("read the seed route: %w", err)
	}
	if len(steps) == 0 {
		// Empty and absent are one answer here on purpose: a route with
		// no steps contributes no seed, and answering "found, but it
		// gave you nothing" would leave a caller with an empty start set
		// and no error -- the shape this whole file exists to refuse.
		exists, err := s.q.RouteExists(ctx, dbq.RouteExistsParams{ProjectID: projectID, Key: key})
		if err != nil {
			return nil, fmt.Errorf("look up the seed route: %w", err)
		}
		if !exists {
			return nil, fmt.Errorf("%w: seed_route names no route of this game: %q",
				ErrNotFound, key)
		}
		return nil, fmt.Errorf("%w: seed_route %q has no steps, so it names no start point",
			ErrInvalidInput, key)
	}
	return steps, nil
}

// allFixpoint computes GatingAll over the rows the walk already
// returned.
//
// **This is a separate code path and it is not a walk**, which is stated
// rather than implied because the two modes look like variants of one
// thing and are not. "Every gate of X is reachable" is a property of X's
// whole in-neighbourhood, not of any one path, and a recursion that
// carries paths cannot answer it. So: start from the seeds, repeatedly
// admit an entity whose every normalised in-gate is already admitted,
// stop when a pass admits nothing.
//
// The candidate set is the `any` closure, because all-reachable is a
// subset of any-reachable by construction -- an entity none of whose
// gates the walk ever reached cannot have all of them reached. That is
// what the plan means by "one walk's rows plus one in-degree query".
//
// A symmetric neighbour admits on `any` even here: gating is a statement
// about gates, and an adjacency does not become a dependency because the
// mode changed.
//
// It is bounded by MaxMaxDepth passes. A fixpoint that has not converged
// in that many passes over a graph whose walk was depth-bounded at most
// that deep has a cycle in it, and a cycle admits nothing under `all` --
// each member waits for another. Whichever way it ends, entities the
// walk reached and this pass did not admit are reported in Note, which
// names analysis.cycles as where to look, because that is the analysis
// that says which loop it is.
func (s *Service) allFixpoint(ctx context.Context, p Params, out *Reach) error {
	candidates := make([]uuid.UUID, 0, len(out.Reached))
	for id := range out.Reached {
		candidates = append(candidates, id)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].String() < candidates[j].String() })

	edges, err := s.inEdges(ctx, p, candidates)
	if err != nil {
		return err
	}

	admitted := make(map[uuid.UUID]bool, len(out.SeedIDs))
	for _, id := range out.SeedIDs {
		admitted[id] = true
	}
	for out.Passes = 0; out.Passes < MaxMaxDepth; {
		out.Passes++
		grew := false
		for _, id := range candidates {
			if admitted[id] {
				continue
			}
			in := edges[id]
			if len(in.gates) > 0 {
				every := true
				for _, needed := range in.gates {
					if !admitted[needed] {
						every = false
						break
					}
				}
				if every {
					admitted[id] = true
					grew = true
				}
				continue
			}
			for _, near := range in.adjacent {
				if admitted[near] {
					admitted[id] = true
					grew = true
					break
				}
			}
		}
		if !grew {
			break
		}
	}

	held := 0
	for _, id := range candidates {
		if !admitted[id] {
			held++
		}
	}
	out.Reached = admitted
	depth := make(map[uuid.UUID]int, len(admitted))
	for id := range admitted {
		depth[id] = out.Depth[id]
	}
	out.Depth = depth
	if held > 0 {
		out.Note = fmt.Sprintf(
			"gating=all: %d entities the walk reached were not admitted, because at least "+
				"one gate of each was never admitted itself. Mutually gating entities can "+
				"never be admitted under `all` -- each waits for the other -- so run "+
				"analysis.cycles to see whether a prerequisite loop is the cause.", held)
	}
	return nil
}

// inEdge is one entity's normalised in-neighbourhood, split by whether
// each neighbour gates it or is merely adjacent to it.
type inEdge struct {
	gates    []uuid.UUID
	adjacent []uuid.UUID
}

// inEdges reads the normalised in-neighbourhood of a set of entities. It
// is the "one in-degree query" the all-fixpoint runs beside the walk,
// and unreachable.go's reasons read the same rows for the same
// definition of an in-edge.
func (s *Service) inEdges(ctx context.Context, p Params, of []uuid.UUID) (
	map[uuid.UUID]inEdge, error,
) {
	forward, reverse, symmetric := p.edgeSets()
	rows, err := s.q.ListNormalisedInEdges(ctx, dbq.ListNormalisedInEdgesParams{
		ProjectID:      p.ProjectID,
		Dependents:     of,
		ForwardGates:   forward,
		ReverseGates:   reverse,
		Adjacency:      symmetric,
		ExcludeInvalid: p.ExcludeInvalid,
	})
	if err != nil {
		return nil, fmt.Errorf("read the normalised in-edges: %w", err)
	}
	out := make(map[uuid.UUID]inEdge, len(of))
	for _, row := range rows {
		entry := out[row.Dependent]
		if row.Gate {
			entry.gates = append(entry.gates, row.Needed)
		} else {
			entry.adjacent = append(entry.adjacent, row.Needed)
		}
		out[row.Dependent] = entry
	}
	return out, nil
}

// includeUngated and propagateContainment apply the two documented
// defaults in one place each, so a nil pointer cannot mean one thing in
// the walk and another in the seed.
func (p Params) includeUngated() bool {
	return p.IncludeUngated == nil || *p.IncludeUngated
}

func (p Params) propagateContainment() bool {
	return p.PropagateContainment == nil || *p.PropagateContainment
}

// emptySeedSet is the refusal for a start set that names nothing, and it
// names all three sources because a caller who supplied none of them
// cannot guess which one this call wanted.
//
// It is invalid_input and not a new code, for the reason errors.go
// argues: metamodel.CodeInvalidInput already means "a row's own
// arguments being malformed; the fix is to change that argument", and an
// empty seed set with include_ungated false is exactly that.
func emptySeedSet(what string) error {
	return invalidInput("seed_entities", fmt.Sprintf(
		"the start set %s, and include_ungated is off, so this run would report every "+
			"entity in the game as unreachable. Supply a start set with `seed_entities` "+
			"(entity type and key), `seed_entity_types` (every entity of a type) or "+
			"`seed_route` (a route's steps), or leave include_ungated on so every entity "+
			"with no incoming gating edge starts the walk", what))
}

// binder numbers a statement's own placeholders. It exists so a
// statement this package assembles from optional arms cannot get its
// numbering wrong by counting arms.
type binder struct{ args []any }

func (b *binder) bind(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

// concatIDs and dedupeIDs keep the three id arrays honest: a relation
// type carrying two traits of one set would otherwise be listed twice,
// and `= ANY` does not care but a count of "types this run walks" would.
func concatIDs(lists ...[]uuid.UUID) []uuid.UUID {
	out := []uuid.UUID{}
	for _, list := range lists {
		out = append(out, list...)
	}
	return out
}

func dedupeIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
