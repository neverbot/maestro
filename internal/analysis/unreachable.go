package analysis

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/paging"
)

// Reason is why one entity is unreachable, and the four are ordered
// **most specific first**.
//
// The order is a slice and not a map, and the reason is a finding views
// recorded: a refusal-order guard there "worked" only because a map
// literal happened to begin with the alphabetically-last key, which is
// not an order at all. An entity that qualifies for more than one reason
// gets the first of these it matches, and
// TestTheReasonsAreOrderedMostSpecificFirst holds it with an entity that
// qualifies for two.
type Reason string

const (
	// ReasonDepthLimited: the walk stopped at its depth bound next to
	// this entity. **It does not claim the entity is unreachable in the
	// design** -- only that this run stopped looking -- and it is first
	// because every other reason would be a claim about the game that
	// this run has not earned.
	ReasonDepthLimited Reason = "depth_limited"
	// ReasonContainerUnreachable: the only ways in are containment
	// edges, and no container is reachable. The fix is to the container,
	// which is why it outranks no_path.
	ReasonContainerUnreachable Reason = "container_unreachable"
	// ReasonIsolatedFromStart: no incoming gating edge at all, and not a
	// seed.
	//
	// **It can only appear with include_ungated false**, because such an
	// entity is a start point under the default settings. That is said
	// here rather than discovered, because a reason that can never
	// appear under the defaults is one a reader will otherwise think is
	// broken.
	ReasonIsolatedFromStart Reason = "isolated_from_start"
	// ReasonNoPath: it has gates, and every one of them is unreachable
	// itself.
	ReasonNoPath Reason = "no_path"
)

// Reasons is the order, and every reader takes it from here.
var Reasons = []Reason{
	ReasonDepthLimited, ReasonContainerUnreachable, ReasonIsolatedFromStart, ReasonNoPath,
}

// UnreachableInput is one unreachable report's arguments.
type UnreachableInput struct {
	// Reach carries every argument the closure takes: seeds, gating,
	// containment, depth and the invalid-edge reading. It is embedded
	// rather than restated so a seed argument cannot mean one thing here
	// and another in routes.check.
	Reach Params

	// RelationTypes is the caller's own choice of which relation types
	// this run reads, and it is resolved **here** rather than handed in
	// already resolved. That is not a convenience: the whole
	// sub-project's most important refusal -- a game that declared
	// nothing is refused rather than reported healthy -- lives in
	// Resolve, and a caller that could supply its own Semantics could
	// walk straight past it.
	RelationTypes []string

	// EntityTypes narrows what is considered; empty means every type of
	// the game. IgnoreEntityTypes removes types from whatever that
	// leaves -- **from the findings and from the totals alike**, because
	// a total that counts what the report excludes is a total that
	// disagrees with itself.
	EntityTypes       []string
	IgnoreEntityTypes []string

	// MaxResults bounds how many findings the report names. Above
	// MaxMaxResults it is limit_exceeded naming the cap, never clamped.
	MaxResults int
	// Limit and Cursor are the page, and Limit is **clamped** rather
	// than refused: a declared analysis bound is a statement about the
	// answer's meaning, a page size is a transport detail. That split is
	// the existing one and not a new decision;
	// TestADeclaredBoundAboveItsCapIsRefusedAndAPageSizeIsClamped holds
	// the two halves in one test so a later tidy cannot collapse them.
	Limit  int32
	Cursor string
}

// Finding is one unreachable entity and the evidence a designer acts on.
type Finding struct {
	EntityType string    `json:"entity_type"`
	Key        string    `json:"key"`
	Name       string    `json:"name"`
	Reason     Reason    `json:"reason"`
	Blockers   []SeedRef `json:"blockers,omitempty"`
}

// TypeCount is one entity type's two totals.
type TypeCount struct {
	EntityType  string `json:"entity_type"`
	Reachable   int    `json:"reachable"`
	Unreachable int    `json:"unreachable"`
}

// SeedReport is the start set as the engine understood it.
//
// **It is not a courtesy.** It is the field that turns "your whole game
// is unreachable" from a verdict into a diagnosis, and it is the only
// thing that distinguishes a real finding from a mistyped seed key that
// somehow got through.
type SeedReport struct {
	Entities       []SeedRef `json:"entities"`
	EntityTypes    []string  `json:"entity_types,omitempty"`
	IncludeUngated bool      `json:"include_ungated"`
	Total          int       `json:"total"`
}

// UnreachableResult is the whole report.
//
// **Every count here is what makes the negative half real.** An empty
// Findings list means one of two entirely different things -- everything
// is reachable, or the walk found nothing -- and ReachableTotal,
// PerType, Seed.Total and EdgesWalked are what tell them apart. A report
// that carried only the findings would be the same JSON in both cases.
type UnreachableResult struct {
	Findings   []Finding `json:"unreachable"`
	NextCursor string    `json:"next_cursor,omitempty"`

	ReachableTotal   int         `json:"reachable_total"`
	UnreachableTotal int         `json:"unreachable_total"`
	PerType          []TypeCount `json:"per_type"`

	Seeds           SeedReport      `json:"seeds"`
	SemanticsSource []TypeSemantics `json:"semantics_source"`
	Gating          Gating          `json:"gating"`

	InvalidEdgesFollowed int    `json:"invalid_edges_followed"`
	EdgesWalked          int    `json:"edges_walked"`
	Truncated            bool   `json:"truncated"`
	DepthLimited         bool   `json:"depth_limited"`
	Note                 string `json:"note,omitempty"`

	Stats Stats `json:"stats"`
}

// Stats is what one run cost.
type Stats struct {
	DurationMS int64 `json:"duration_ms"`
}

const (
	defaultUnreachablePage = int32(50)
	maxUnreachablePage     = int32(200)
)

// Unreachable answers "what can no player reach", over the reachability
// closure reach.go computes.
//
// **The complement is computed in SQL and not in Go**: the reached ids
// go down as one array parameter and the entities table is subtracted
// against them there, so a game of ten thousand entities is never
// shipped across a wire to be subtracted. The reached ids are bounded by
// MaxWalkRows and are already in this process, because the closure needs
// them for its own truncation and invalid-edge counts.
func (s *Service) Unreachable(ctx context.Context, projectID uuid.UUID, in UnreachableInput) (
	UnreachableResult, error,
) {
	started := time.Now()
	if in.MaxResults > MaxMaxResults {
		return UnreachableResult{}, limitExceeded("max_results", in.MaxResults, MaxMaxResults)
	}
	if len(in.EntityTypes) > MaxTypeKeys {
		return UnreachableResult{}, limitExceeded("entity_types", len(in.EntityTypes), MaxTypeKeys)
	}
	if len(in.IgnoreEntityTypes) > MaxTypeKeys {
		return UnreachableResult{}, limitExceeded(
			"ignore_entity_types", len(in.IgnoreEntityTypes), MaxTypeKeys)
	}
	maxResults := in.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}

	considered, consideredKeys, err := s.consideredTypes(
		ctx, projectID, in.EntityTypes, in.IgnoreEntityTypes)
	if err != nil {
		return UnreachableResult{}, err
	}

	// Resolved here, so an undeclared game is refused on the path the
	// product uses and not only in the resolver's own tests.
	semantics, err := s.Resolve(ctx, projectID, ResolveInput{RelationTypeKeys: in.RelationTypes})
	if err != nil {
		return UnreachableResult{}, err
	}
	in.Reach.Semantics = semantics
	in.Reach.ProjectID = projectID
	reach, err := s.Reach(ctx, in.Reach)
	if err != nil {
		return UnreachableResult{}, err
	}
	reached := make([]uuid.UUID, 0, len(reach.Reached))
	for id := range reach.Reached {
		reached = append(reached, id)
	}

	out := UnreachableResult{
		Seeds: SeedReport{
			Entities:       reach.Seeds,
			EntityTypes:    reach.SeedEntityTypes,
			IncludeUngated: reach.IncludeUngated,
			Total:          reach.SeedCount,
		},
		SemanticsSource:      semantics.Types(),
		Gating:               reach.Gating,
		InvalidEdgesFollowed: reach.InvalidEdgesFollowed,
		EdgesWalked:          reach.EdgesWalked,
		DepthLimited:         reach.DepthLimited,
		Note:                 reach.Note,
	}
	if err := s.countTotals(ctx, projectID, considered, reached, &out); err != nil {
		return UnreachableResult{}, err
	}

	limit := paging.Size(in.Limit, defaultUnreachablePage, maxUnreachablePage)
	if int(limit) > maxResults {
		limit = int32(maxResults)
	}
	fingerprint := unreachableFingerprint(projectID, in, consideredKeys)
	after, err := paging.Decode(in.Cursor, fingerprint, refuseUnreachableCursor)
	if err != nil {
		return UnreachableResult{}, err
	}

	page, more, err := s.unreachablePage(ctx, projectID, considered, reached, after, limit)
	if err != nil {
		return UnreachableResult{}, err
	}
	if err := s.explain(ctx, in.Reach, reach, page); err != nil {
		return UnreachableResult{}, err
	}
	out.Findings = make([]Finding, 0, len(page))
	for _, row := range page {
		out.Findings = append(out.Findings, row.finding)
	}
	if more && len(page) > 0 {
		last := page[len(page)-1]
		out.NextCursor = paging.Encode(paging.Cursor{
			Sort: last.finding.EntityType + "\x00" + last.finding.Key,
			ID:   last.id, Fingerprint: fingerprint,
		})
	}
	// Truncated says **this answer does not name every unreachable
	// entity**, whether the cause was max_results or the page. Without
	// it, a report capped at exactly its bound and one capped below it
	// are the same JSON -- which is the same defect graph.WalkCTE emits
	// LIMIT MaxRows + 1 to avoid, one level up.
	out.Truncated = out.UnreachableTotal > len(out.Findings)
	if reach.Truncated {
		out.Truncated = true
	}
	out.Stats.DurationMS = time.Since(started).Milliseconds()
	return out, nil
}

// consideredTypes resolves which entity types a report is about.
//
// An ignored type is removed from the findings **and** from the totals,
// which is why it is applied here, once, to the id list every statement
// below takes.
//
// It takes the two key lists rather than one analysis's own input
// struct, because three analyses now narrow themselves the same way and
// a second copy of "resolve, then subtract the ignored" is a second
// place the totals could stop agreeing with the findings.
func (s *Service) consideredTypes(ctx context.Context, projectID uuid.UUID,
	entityTypes, ignoreEntityTypes []string,
) (
	[]uuid.UUID, []string, error,
) {
	ignored := make(map[string]bool, len(ignoreEntityTypes))
	for i, key := range ignoreEntityTypes {
		typ, err := s.meta.EntityTypeByKey(ctx, projectID, key)
		if err != nil {
			return nil, nil, fmt.Errorf("ignore_entity_types[%d] (%q): %w", i, key, err)
		}
		ignored[typ.Key] = true
	}

	var rows []struct {
		id  uuid.UUID
		key string
	}
	if len(entityTypes) > 0 {
		for i, key := range entityTypes {
			typ, err := s.meta.EntityTypeByKey(ctx, projectID, key)
			if err != nil {
				return nil, nil, fmt.Errorf("entity_types[%d] (%q): %w", i, key, err)
			}
			rows = append(rows, struct {
				id  uuid.UUID
				key string
			}{typ.ID, typ.Key})
		}
	} else {
		all, err := s.meta.ListEntityTypes(ctx, projectID)
		if err != nil {
			return nil, nil, fmt.Errorf("read the entity type catalogue: %w", err)
		}
		for _, typ := range all {
			rows = append(rows, struct {
				id  uuid.UUID
				key string
			}{typ.ID, typ.Key})
		}
	}

	ids := make([]uuid.UUID, 0, len(rows))
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		if ignored[row.key] {
			continue
		}
		ids = append(ids, row.id)
		keys = append(keys, row.key)
	}
	sort.Strings(keys)
	return ids, keys, nil
}

// countTotals fills the two totals and the per-type breakdown, in one
// aggregate over the entities table with the reached set as an array.
func (s *Service) countTotals(ctx context.Context, projectID uuid.UUID,
	considered, reached []uuid.UUID, out *UnreachableResult,
) error {
	b := &binder{}
	statement := fmt.Sprintf(`
SELECT et.key,
       count(*) FILTER (WHERE e.id = ANY(%[3]s::uuid[])) AS reachable,
       count(*) FILTER (WHERE NOT (e.id = ANY(%[3]s::uuid[]))) AS unreachable
FROM entities e
JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = %[1]s
WHERE e.project_id = %[1]s AND e.entity_type_id = ANY(%[2]s::uuid[])
GROUP BY et.key
ORDER BY et.key`,
		b.bind(projectID), b.bind(considered), b.bind(reached))

	return s.runInTx(ctx, s.statementBudget(), statement, b.args, func(rows pgx.Rows) error {
		for rows.Next() {
			var count TypeCount
			if err := rows.Scan(&count.EntityType, &count.Reachable, &count.Unreachable); err != nil {
				return fmt.Errorf("scan a per-type count: %w", err)
			}
			out.PerType = append(out.PerType, count)
			out.ReachableTotal += count.Reachable
			out.UnreachableTotal += count.Unreachable
		}
		return rows.Err()
	})
}

// candidate is one unreachable entity before its reason is decided.
type candidate struct {
	id      uuid.UUID
	finding Finding
}

// unreachablePage reads one page of the complement, ordered by entity
// type key then entity key then id -- a total order, so a cursor cannot
// skip or repeat a row.
func (s *Service) unreachablePage(ctx context.Context, projectID uuid.UUID,
	considered, reached []uuid.UUID, after paging.Cursor, limit int32,
) ([]candidate, bool, error) {
	b := &binder{}
	project := b.bind(projectID)
	where := fmt.Sprintf(
		"e.project_id = %s AND e.entity_type_id = ANY(%s::uuid[]) AND NOT (e.id = ANY(%s::uuid[]))",
		project, b.bind(considered), b.bind(reached))
	if after.ID != uuid.Nil {
		typeKey, entityKey, ok := strings.Cut(after.Sort, "\x00")
		if !ok {
			return nil, false, refuseUnreachableCursor(
				"is not a cursor this listing issued: it carries no entity type and key")
		}
		where += fmt.Sprintf(" AND (et.key, e.key, e.id) > (%s, %s, %s)",
			b.bind(typeKey), b.bind(entityKey), b.bind(after.ID))
	}
	statement := fmt.Sprintf(`
SELECT e.id, e.key, e.name, et.key
FROM entities e
JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = %s
WHERE %s
ORDER BY et.key, e.key, e.id
LIMIT %s`, project, where, b.bind(int64(limit)+1))

	var page []candidate
	err := s.runInTx(ctx, s.statementBudget(), statement, b.args, func(rows pgx.Rows) error {
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.finding.Key, &c.finding.Name,
				&c.finding.EntityType); err != nil {
				return fmt.Errorf("scan an unreachable entity: %w", err)
			}
			page = append(page, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	// The row past the page, read for the same reason graph.WalkCTE
	// reads the row past its cap: a full page and a last page are
	// otherwise the same answer.
	more := len(page) > int(limit)
	if more {
		page = page[:limit]
	}
	return page, more, nil
}

// explain gives each finding on the page its reason and its blockers.
//
// It reads the same normalised in-edges the fixpoint does, from the same
// statement, so "what gates this entity" has one definition in this
// package rather than one per question.
func (s *Service) explain(ctx context.Context, p Params, reach Reach, page []candidate) error {
	if len(page) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(page))
	for _, c := range page {
		ids = append(ids, c.id)
	}
	edges, err := s.inEdges(ctx, p, ids)
	if err != nil {
		return err
	}

	// The blockers a designer acts on are named by key, so every
	// in-neighbour of the page is read back once.
	var neighbours []uuid.UUID
	for _, in := range edges {
		neighbours = append(neighbours, in.all()...)
	}
	byID, err := s.meta.EntitiesByIDs(ctx, p.ProjectID, dedupeIDs(neighbours))
	if err != nil {
		return fmt.Errorf("read the blocking entities: %w", err)
	}
	types, err := s.meta.ListEntityTypes(ctx, p.ProjectID)
	if err != nil {
		return fmt.Errorf("read the entity type catalogue: %w", err)
	}
	typeKey := make(map[uuid.UUID]string, len(types))
	for _, typ := range types {
		typeKey[typ.ID] = typ.Key
	}

	frontier := reach.maxDepthReached()
	for i := range page {
		in := edges[page[i].id]
		page[i].finding.Reason = reasonFor(in, reach, frontier)
		for _, needed := range in.all() {
			if reach.Reached[needed] {
				continue
			}
			row, ok := byID[needed]
			if !ok {
				continue
			}
			page[i].finding.Blockers = append(page[i].finding.Blockers,
				SeedRef{EntityType: typeKey[row.EntityTypeID], Key: row.Key})
			if len(page[i].finding.Blockers) == MaxBlockers {
				break
			}
		}
	}
	return nil
}

// reasonFor picks the most specific reason, in the order Reasons
// declares.
func reasonFor(in inEdge, reach Reach, frontier int) Reason {
	// depth_limited first: every other reason is a claim about the game,
	// and a walk that stopped at its bound has not earned one.
	if reach.DepthLimited {
		for _, needed := range in.all() {
			if depth, ok := reach.Depth[needed]; ok && depth == frontier {
				return ReasonDepthLimited
			}
		}
	}
	if len(in.containers) > 0 && len(in.gates) == 0 {
		return ReasonContainerUnreachable
	}
	if len(in.all()) == 0 {
		return ReasonIsolatedFromStart
	}
	return ReasonNoPath
}

// maxDepthReached is the deepest hop the walk handed back, which is the
// frontier a depth-limited run stopped at.
func (r Reach) maxDepthReached() int {
	deepest := 0
	for _, depth := range r.Depth {
		if depth > deepest {
			deepest = depth
		}
	}
	return deepest
}

// unreachableFingerprint digests the report a cursor was issued under.
//
// **The project id is first, always** -- internal/paging's contract,
// stated in prose there and enforced by nothing, so every caller obeys
// it and every listing has a test. The parameters follow, because paging
// through "unreachable from the tutorial zone" and having page 2 answer
// "unreachable from character creation" is a wrong answer with no error.
//
// It is asserted compositionally as well as behaviourally, because the
// behavioural test can pass without the project id whenever another part
// discriminates -- here the parameter digest does -- and that is exactly
// how the original paging defect survived its first test.
func unreachableFingerprint(projectID uuid.UUID, in UnreachableInput, considered []string) string {
	parts := []string{projectID.String(), "analysis.unreachable"}
	parts = append(parts, considered...)
	seeds := make([]string, 0, len(in.Reach.SeedEntities))
	for _, seed := range in.Reach.SeedEntities {
		seeds = append(seeds, seed.EntityType+"/"+seed.Key)
	}
	sort.Strings(seeds)
	parts = append(parts, seeds...)
	types := append([]string(nil), in.Reach.SeedEntityTypes...)
	sort.Strings(types)
	parts = append(parts, types...)
	relationTypes := append([]string(nil), in.RelationTypes...)
	sort.Strings(relationTypes)
	parts = append(parts, relationTypes...)
	parts = append(parts,
		in.Reach.SeedRoute,
		string(in.Reach.Gating),
		paging.TriState(in.Reach.IncludeUngated),
		paging.TriState(in.Reach.PropagateContainment),
		fmt.Sprintf("%t", in.Reach.ExcludeInvalid),
		fmt.Sprintf("%d", in.Reach.MaxDepth),
	)
	return paging.Fingerprint(parts...)
}

// refuseUnreachableCursor turns paging's message into this package's own
// refusal, at the caller's own argument path. Left untyped a bad cursor
// would reach an agent as internal_error over a value the agent itself
// supplied.
func refuseUnreachableCursor(message string) error {
	return invalidInput("cursor", message)
}
