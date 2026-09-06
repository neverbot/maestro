package analysis

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/graph"
)

// A prerequisite cycle report is **the walk internal/graph already
// emits, read for its closing hops**, and nothing about the traversal is
// new here.
//
// That is worth stating rather than implying, because the property this
// file rests on was a correction to that package and is invisible from
// the outside: graph.WalkCTE **returns the hop that closes a cycle,
// exactly once, with closed = true, and does not expand from it**. An
// earlier shape suppressed the row rather than the recursion, so an
// n-cycle came back with n-1 of its n edges and a self-loop came back
// with none -- which made the one thing this engine exists to find the
// one thing that walk could not show. This file reads `closed`, `path`
// and `rel_path` and reconstructs nothing.
//
// The reliance is asserted and not merely written down:
// TestTheCycleReportReadsTheWalksClosingHopRatherThanReconstructingIt
// runs graph.WalkCTE directly over one of these fixtures and holds the
// finding's edge ids against the closing row's own rel_path tail, so a
// future "optimisation" that rebuilt cycles from consecutive node pairs
// is red rather than subtly wrong on exactly the fixture that matters --
// two gating relation types between one pair of entities.
//
// The one thing this file needed that the walk did not have is
// CarryRelationPath (Task 4): `path` carries node ids, and a cycle's
// edges cannot be recovered from consecutive node pairs where it matters
// most.

// CyclesInput is one prerequisite-cycle report's arguments.
type CyclesInput struct {
	// EntityTypes narrows what is **seeded**; empty means every entity
	// type of the game. IgnoreEntityTypes removes types from whatever
	// that leaves. Neither bounds where the walk may travel: a cycle
	// found by starting at a quest is a cycle whichever type its other
	// members carry, and pruning the traversal by type would report
	// fewer cycles than the game has while saying it had checked.
	EntityTypes       []string
	IgnoreEntityTypes []string

	// RelationTypes is the caller's own choice of which relation types
	// this run reads, resolved here through Resolve for the reason
	// UnreachableInput.RelationTypes gives: the refusal for a game that
	// declared nothing lives in the resolver, and a caller that could
	// hand in its own Semantics would walk straight past it.
	RelationTypes []string

	// MaxDepth bounds the hops, and therefore the longest cycle this run
	// can find: a cycle of length n is closed at depth n, so a run at
	// DefaultMaxDepth finds no cycle longer than that and says so
	// through DepthLimited rather than reporting the game acyclic.
	MaxDepth int

	// MaxResults bounds the findings in each list. Above MaxMaxResults
	// it is limit_exceeded naming the cap, never clamped.
	MaxResults int

	// ExcludeInvalid asks for the stricter reading of flagged edges.
	// The default follows them; Params' own comment is the argument, and
	// it applies unchanged here -- a loop in a game's prerequisites is a
	// fact about endpoints, and `invalid` describes a row's fields.
	ExcludeInvalid bool
}

// CycleNode is one entity of a cycle, addressed the way a designer
// addresses one.
type CycleNode struct {
	EntityType string `json:"entity_type"`
	Key        string `json:"key"`
	Name       string `json:"name"`
}

// CycleEdge is one edge of a cycle.
//
// **RelationType is the field this whole widening of internal/graph was
// for.** The first thing a designer does with a reported loop is ask
// whether the type should have been declared gating at all: the clearest
// real false positive is mutual exclusion -- "choosing the Horde locks
// the Alliance" as two `requires_not` edges -- which is a legitimate
// two-cycle in a type that reads like a prerequisite, and whose fix is a
// trait declaration rather than a change to the content. A finding that
// named only the entities would send that designer to rewrite a game.
type CycleEdge struct {
	RelationID   uuid.UUID `json:"relation_id"`
	RelationType string    `json:"relation_type"`
}

// Cycle is one loop, canonicalised.
//
// Entities are in cycle order and Edges runs with them: **edge i joins
// entity i to entity (i+1) mod n**. That is the invariant rotation is
// most likely to break, so it is stated here and asserted against the
// database rather than against the walk's own row --
// TestACycleAndItsEdgesRotateTogether.
type Cycle struct {
	Entities []CycleNode `json:"entities"`
	Edges    []CycleEdge `json:"edges"`
	Length   int         `json:"length"`
}

// CyclesResult is the whole report.
//
// **The two lists are separate and that is the finding, not a
// formatting choice.** A containment loop -- a zone inside a zone inside
// the first -- is a hierarchy that is not one; a prerequisite loop is a
// gate nobody can open. Different sentence, different fix, different
// list. TestAContainmentLoopIsReportedSeparatelyFromAPrerequisiteLoop
// builds one of each.
//
// **EdgesWalked is what makes the negative half real.** An empty Cycles
// list means one of two entirely different things -- the game is acyclic,
// or the walk followed no edge at all -- and a report carrying only the
// findings is the same JSON in both cases. Every count here exists for
// that reason and for no other.
type CyclesResult struct {
	Cycles            []Cycle `json:"cycles"`
	ContainmentCycles []Cycle `json:"containment_cycles"`

	SemanticsSource []TypeSemantics `json:"semantics_source"`

	// SeedTotal is how many entities the run started from, which is the
	// number `entity_types` cannot be read off in keys.
	SeedTotal int `json:"seed_total"`

	EdgesWalked          int `json:"edges_walked"`
	InvalidEdgesFollowed int `json:"invalid_edges_followed"`

	// Truncated says this answer does not name every cycle, whether the
	// cause was max_results or the walk's own row cap.
	Truncated bool `json:"truncated"`
	// DepthLimited says the walk stopped expanding at its depth bound,
	// so a longer cycle may exist and this run did not look. It is not a
	// claim that one does.
	DepthLimited bool `json:"depth_limited"`

	Stats Stats `json:"stats"`
}

// Cycles answers "where do this game's prerequisites loop".
//
// **Capped hard and not paginated**, which is a decision rather than an
// omission: a design with more than a thousand distinct prerequisite
// cycles has one problem, not a thousand, and paging through them helps
// nobody. `max_results` defaults to DefaultMaxResults, refuses above
// MaxMaxResults, and sets Truncated when it bites.
//
// Findings are ordered **shortest first**, and that is not cosmetic
// either. graph.WalkCTE truncates with ORDER BY depth, so a truncated
// walk is a connected prefix of the nearest hops; reporting the shortest
// cycles first means a truncated answer holds the *most fixable* loops
// rather than an arbitrary scatter of them.
// TestATruncatedCycleReportKeepsTheShortestCycles pins it.
func (s *Service) Cycles(ctx context.Context, projectID uuid.UUID, in CyclesInput) (
	CyclesResult, error,
) {
	started := time.Now()
	if in.MaxDepth > MaxMaxDepth {
		return CyclesResult{}, limitExceeded("max_depth", in.MaxDepth, MaxMaxDepth)
	}
	if in.MaxResults > MaxMaxResults {
		return CyclesResult{}, limitExceeded("max_results", in.MaxResults, MaxMaxResults)
	}
	if len(in.EntityTypes) > MaxTypeKeys {
		return CyclesResult{}, limitExceeded("entity_types", len(in.EntityTypes), MaxTypeKeys)
	}
	if len(in.IgnoreEntityTypes) > MaxTypeKeys {
		return CyclesResult{}, limitExceeded(
			"ignore_entity_types", len(in.IgnoreEntityTypes), MaxTypeKeys)
	}
	maxResults := in.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}

	considered, _, err := s.consideredTypes(ctx, projectID, in.EntityTypes, in.IgnoreEntityTypes)
	if err != nil {
		return CyclesResult{}, err
	}
	semantics, err := s.Resolve(ctx, projectID, ResolveInput{RelationTypeKeys: in.RelationTypes})
	if err != nil {
		return CyclesResult{}, err
	}

	out := CyclesResult{SemanticsSource: semantics.Types()}
	gating, containment := cycleSemantics(semantics)

	invalid := map[uuid.UUID]bool{}
	prereqLoops, err := s.cycleWalk(ctx, projectID, "cycles", considered, gating, in, &out, invalid)
	if err != nil {
		return CyclesResult{}, err
	}
	containmentLoops, err := s.cycleWalk(
		ctx, projectID, "containment", considered, containment, in, &out, invalid)
	if err != nil {
		return CyclesResult{}, err
	}
	out.InvalidEdgesFollowed = len(invalid)

	if len(prereqLoops) > maxResults {
		prereqLoops, out.Truncated = prereqLoops[:maxResults], true
	}
	if len(containmentLoops) > maxResults {
		containmentLoops, out.Truncated = containmentLoops[:maxResults], true
	}
	if out.Cycles, err = s.describe(ctx, projectID, semantics, prereqLoops); err != nil {
		return CyclesResult{}, err
	}
	if out.ContainmentCycles, err = s.describe(
		ctx, projectID, semantics, containmentLoops); err != nil {
		return CyclesResult{}, err
	}
	out.Stats.DurationMS = time.Since(started).Milliseconds()
	return out, nil
}

// loop is one canonicalised cycle before its ids become keys.
type loop struct {
	nodes []uuid.UUID
	edges []uuid.UUID
}

// cycleWalk runs one walk and returns the cycles its closing hops
// carried, canonicalised and deduplicated, shortest first.
//
// It adds this walk's counts into the shared result rather than
// returning them, because the two walks are one report: a designer told
// "seven edges were walked" wants the number for the run and not for the
// half of it that happened to find nothing.
func (s *Service) cycleWalk(ctx context.Context, projectID uuid.UUID, name string,
	considered []uuid.UUID, sem Semantics, in CyclesInput, out *CyclesResult,
	invalid map[uuid.UUID]bool,
) ([]loop, error) {
	if len(sem.ByType) == 0 {
		// Nothing of this kind is declared, so there is no walk to run.
		// It is not a refusal: a game with prerequisites and no
		// containment is an ordinary game, and Resolve has already
		// refused the game that declared nothing at all.
		return nil, nil
	}

	b := &binder{}
	// The seed filters on the project even though graph.WalkCTE's anchor
	// join filters again -- redundant today, written anyway, for the
	// reason resolveSeeds records: the redundancy is one line and the
	// shape it defends is a seed source that does not go through the
	// anchor.
	seed := fmt.Sprintf(
		"SELECT e.id FROM entities e WHERE e.project_id = %s AND e.entity_type_id = ANY(%s::uuid[])",
		b.bind(projectID), b.bind(considered))

	p := Params{
		ProjectID:      projectID,
		Semantics:      sem,
		MaxDepth:       in.MaxDepth,
		ExcludeInvalid: in.ExcludeInvalid,
	}
	walk := p.normalisedWalk(name, seed, b.args)
	// The one thing this analysis needs that reachability does not: the
	// relation ids along each path, so a closing hop can name every edge
	// of the loop it closed and not only the edge that closed it.
	walk.CarryRelationPath = true

	body, args := graph.WalkCTE(walk)
	statement := "WITH RECURSIVE " + body + `
SELECT w.depth, w.path, w.rel_path, w.via_relation, w.closed, COALESCE(r.invalid, false)
FROM ` + graph.ReadFrom(walk) + ` w
LEFT JOIN relations r ON r.id = w.via_relation AND r.project_id = $1`

	seen := map[string]bool{}
	var loops []loop
	rows := 0
	err := s.runInTx(ctx, s.statementBudget(), statement, args, func(r pgx.Rows) error {
		for r.Next() {
			var depth int
			var path, relPath []uuid.UUID
			var via *uuid.UUID
			var closed, edgeInvalid bool
			if err := r.Scan(&depth, &path, &relPath, &via, &closed, &edgeInvalid); err != nil {
				return fmt.Errorf("scan a walk row: %w", err)
			}
			rows++
			if rows > graphRowCap(walk) {
				// The row past the cap, which is the only reason
				// graph.WalkCTE emits LIMIT MaxRows + 1. Counted as
				// truncation and not read.
				out.Truncated = true
				continue
			}
			if via == nil {
				out.SeedTotal++
				continue
			}
			out.EdgesWalked++
			if edgeInvalid {
				invalid[*via] = true
			}
			if depth == walk.MaxDepth {
				out.DepthLimited = true
			}
			if !closed {
				continue
			}
			found, ok := cycleFromClosedRow(path, relPath)
			if !ok {
				continue
			}
			key := loopKey(found.nodes)
			if seen[key] {
				continue
			}
			seen[key] = true
			loops = append(loops, found)
		}
		return r.Err()
	})
	if err != nil {
		return nil, err
	}
	// Shortest first, then by the canonical id sequence so two runs over
	// one unchanged game produce the same document.
	sort.SliceStable(loops, func(i, j int) bool {
		if len(loops[i].nodes) != len(loops[j].nodes) {
			return len(loops[i].nodes) < len(loops[j].nodes)
		}
		return loopKey(loops[i].nodes) < loopKey(loops[j].nodes)
	})
	return loops, nil
}

// graphRowCap is the cap the walk was built with, read back from the
// walk rather than from the constant, so a walk built with a different
// cap cannot be counted against this one.
func graphRowCap(w graph.Walk) int { return w.MaxRows }

// cycleFromClosedRow turns one closing hop into a cycle.
//
// A closed row's `path` ends with a node it already contains -- that is
// what `closed` means -- and `rel_path` runs one shorter and in step:
// rel_path[i] is the edge from path[i] to path[i+1]. So the cycle is the
// segment of path from the first occurrence of the repeated node to the
// end, and its edges are the corresponding tail of rel_path.
//
// A self-loop arrives as path [a a] and comes back as one node and one
// edge, which is a length-1 cycle and is **reported rather than
// filtered**: it is always a bug and always cheap to fix.
func cycleFromClosedRow(path, relPath []uuid.UUID) (loop, bool) {
	if len(path) < 2 || len(relPath) != len(path)-1 {
		// A row shaped like nothing this walk emits. Skipped rather than
		// guessed at: a cycle assembled from a row whose two arrays
		// disagreed would name edges that join nothing.
		return loop{}, false
	}
	last := path[len(path)-1]
	first := -1
	for i, id := range path[:len(path)-1] {
		if id == last {
			first = i
			break
		}
	}
	if first < 0 {
		return loop{}, false
	}
	nodes := append([]uuid.UUID(nil), path[first:len(path)-1]...)
	edges := append([]uuid.UUID(nil), relPath[first:]...)
	if len(nodes) != len(edges) {
		return loop{}, false
	}
	return rotate(loop{nodes: nodes, edges: edges}), true
}

// rotate is canonicalisation: the smallest entity id comes first, and
// **the edge sequence rotates with it**.
//
// A four-node cycle is discovered from four starting points and would
// otherwise be reported four times; rotating and then deduplicating on
// the node sequence is what makes it one finding. The edge rotation is
// the step that gets skipped, because a report that rotated only the
// nodes still looks right -- the entities are all there, in a legal
// order -- and every edge then names the wrong pair.
// TestACycleAndItsEdgesRotateTogether is red under exactly that
// mutation for every cycle longer than one, and stays green on
// self-loops, which is the discrimination that proves it tests rotation
// rather than existence.
func rotate(l loop) loop {
	if len(l.nodes) < 2 {
		return l
	}
	at := 0
	for i, id := range l.nodes {
		if id.String() < l.nodes[at].String() {
			at = i
		}
	}
	nodes := make([]uuid.UUID, 0, len(l.nodes))
	edges := make([]uuid.UUID, 0, len(l.edges))
	for i := range l.nodes {
		nodes = append(nodes, l.nodes[(at+i)%len(l.nodes)])
		edges = append(edges, l.edges[(at+i)%len(l.edges)])
	}
	return loop{nodes: nodes, edges: edges}
}

// loopKey is the deduplication key: the canonical node sequence.
func loopKey(nodes []uuid.UUID) string {
	key := ""
	for _, id := range nodes {
		key += id.String() + "/"
	}
	return key
}

// describe turns cycles of ids into findings a designer reads: entity
// type and key for every node, relation type key for every edge.
//
// The relation rows are read back through this package's own statement
// rather than taken from the walk, because the walk hands back ids and
// the type of an edge is what the finding is *for*.
func (s *Service) describe(ctx context.Context, projectID uuid.UUID, sem Semantics,
	loops []loop,
) ([]Cycle, error) {
	out := make([]Cycle, 0, len(loops))
	if len(loops) == 0 {
		return out, nil
	}

	var nodeIDs, edgeIDs []uuid.UUID
	for _, l := range loops {
		nodeIDs = append(nodeIDs, l.nodes...)
		edgeIDs = append(edgeIDs, l.edges...)
	}
	entities, err := s.meta.EntitiesByIDs(ctx, projectID, dedupeIDs(nodeIDs))
	if err != nil {
		return nil, fmt.Errorf("read the entities of a cycle: %w", err)
	}
	types, err := s.meta.ListEntityTypes(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("read the entity type catalogue: %w", err)
	}
	typeKey := make(map[uuid.UUID]string, len(types))
	for _, typ := range types {
		typeKey[typ.ID] = typ.Key
	}
	relations, err := s.q.ListRelationsByIDs(ctx, dbq.ListRelationsByIDsParams{
		ProjectID: projectID, Ids: dedupeIDs(edgeIDs),
	})
	if err != nil {
		return nil, fmt.Errorf("read the edges of a cycle: %w", err)
	}
	relationType := make(map[uuid.UUID]uuid.UUID, len(relations))
	for _, row := range relations {
		relationType[row.ID] = row.RelationTypeID
	}

	for _, l := range loops {
		cycle := Cycle{Length: len(l.nodes)}
		for _, id := range l.nodes {
			node := CycleNode{}
			if row, ok := entities[id]; ok {
				node = CycleNode{
					EntityType: typeKey[row.EntityTypeID], Key: row.Key, Name: row.Name,
				}
			}
			cycle.Entities = append(cycle.Entities, node)
		}
		for _, id := range l.edges {
			edge := CycleEdge{RelationID: id}
			if entry, ok := sem.ByType[relationType[id]]; ok {
				edge.RelationType = entry.Key
			}
			cycle.Edges = append(cycle.Edges, edge)
		}
		out = append(out, cycle)
	}
	return out, nil
}

// cycleSemantics splits a game's resolved reading into the two graphs
// this analysis checks, and it is the one place this file decides what a
// cycle is *of*.
//
// The prerequisite graph carries every type that orders anything --
// `prerequisite_of` (followed backwards, as everywhere in this engine),
// `unlocks`, `ordering`, and a bare `acyclic`. That last is the trait's
// whole reason for existing: a `variant_of` declared `{acyclic}` and
// nothing else gates no progression at all, and a loop in it is still a
// finding. It is also the entry most likely to be quietly dropped from
// this set, which is why TestAnAcyclicOnlyTypeWithNoGatingIsStillChecked
// exists.
//
// Two kinds are excluded, each for its own reason:
//
//   - `symmetric`. A cycle over an adjacency is meaningless -- two zones
//     that connect to each other are a map, not a loop -- and reporting
//     one would fill the report of every connected game with findings
//     nobody can act on. TestASymmetricTypeProducesNoCycleFindings pins
//     it, with a `requires` loop over the same nodes as its control, so
//     a walk that found nothing at all cannot pass.
//   - `annotation`. The type is declared inert and no analysis follows
//     its edges.
//
// `containment` gets its **own** graph, walked in its stored direction.
// A type carrying containment is not also in the prerequisite graph even
// if it carries `acyclic` beside it: it would then be reported twice for
// one loop, in two lists whose whole point is that they are different
// findings.
func cycleSemantics(sem Semantics) (gating, containment Semantics) {
	gating = Semantics{ByType: map[uuid.UUID]TypeSemantics{}}
	containment = Semantics{ByType: map[uuid.UUID]TypeSemantics{}}
	for id, entry := range sem.ByType {
		has := func(trait string) bool {
			for _, declared := range entry.Traits {
				if declared == trait {
					return true
				}
			}
			return false
		}
		if has("annotation") || has("symmetric") {
			continue
		}
		if has("containment") {
			// Rewritten to the single trait the walk reads, so the
			// containment walk cannot be handed a type that also
			// registers as a forward gate and traverse it twice.
			containment.ByType[id] = TypeSemantics{
				ID: entry.ID, Key: entry.Key, Traits: []string{"containment"},
				Source: entry.Source,
			}
			continue
		}
		switch {
		case has("prerequisite_of"):
			// Followed target→source, which is the normalisation every
			// gating analysis in this package runs under.
			gating.ByType[id] = TypeSemantics{
				ID: entry.ID, Key: entry.Key, Traits: []string{"prerequisite_of"},
				Source: entry.Source,
			}
		case has("unlocks"), has("ordering"), has("acyclic"):
			// Followed source→target. `ordering` implies `acyclic` and a
			// bare `acyclic` is followed in its stored direction for want
			// of any other statement about it; both are the same hop, so
			// both are spelled as the one trait normalisedWalk reads
			// forwards.
			gating.ByType[id] = TypeSemantics{
				ID: entry.ID, Key: entry.Key, Traits: []string{"unlocks"},
				Source: entry.Source,
			}
		}
	}
	return gating, containment
}
