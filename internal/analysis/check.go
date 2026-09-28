package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// Checking a route is the reachability closure of reach.go asked k
// questions instead of one: for each consecutive pair (n, n+1), is step
// n+1 reachable given the seed set **plus every step up to n**?
type Verdict string

const (
	// VerdictOK: the step is reachable given the seeds and every step
	// before it.
	VerdictOK Verdict = "ok"
	// VerdictMissingEntity: the step's entity was deleted.
	// 0013_analysis.sql's ON DELETE SET NULL kept the step with its keys,
	// and those keys are what this verdict names -- a step that
	// disappeared with its entity would leave a route silently shorter
	// than the walk somebody authored.
	VerdictMissingEntity Verdict = "missing_entity"
	// VerdictOutOfOrder: an `ordering` edge says this step must come
	// before a step the route places earlier.
	VerdictOutOfOrder Verdict = "out_of_order"
	// VerdictUnmetPrerequisite: the closure does not reach this step.
	// Blockers names the entities standing in the way, capped at
	// MaxBlockers.
	VerdictUnmetPrerequisite Verdict = "unmet_prerequisite"
)

// Verdicts is every verdict **in the order a step that qualifies for
// more than one is reported under**, and the order is a decision.
var Verdicts = []Verdict{
	VerdictMissingEntity, VerdictOutOfOrder, VerdictUnmetPrerequisite, VerdictOK,
}

// MarshalJSON refuses the zero verdict rather than encoding it.
func (v Verdict) MarshalJSON() ([]byte, error) {
	for _, known := range Verdicts {
		if v == known {
			return json.Marshal(string(v))
		}
	}
	return nil, fmt.Errorf("analysis: %q is not a verdict; the four are %v, and the zero "+
		"value is deliberately none of them so an unfilled step cannot encode as a proof",
		string(v), Verdicts)
}

// StepCheck is one step's answer.
type StepCheck struct {
	Position int32   `json:"position"`
	Verdict  Verdict `json:"verdict"`

	// EntityType and Key are the **stored** spelling -- the tombstone
	// pair route_steps carries, which neither a deletion nor a rename
	// rewrites. It is what a caller sent and what it will recognise.
	EntityType string `json:"entity_type"`
	Key        string `json:"key"`

	// Blockers names entities standing between the closure and this
	// step, and is set on `unmet_prerequisite` alone. Capped at
	// MaxBlockers, which is a readability bound: a reason naming forty
	// entities is a reason nobody reads.
	Blockers []SeedRef `json:"blockers,omitempty"`

	// MustPrecede names the earlier steps an `ordering` edge says this
	// one should come before, and is set on `out_of_order` alone.
	MustPrecede []SeedRef `json:"must_precede,omitempty"`

	// TypeRenamed is a **diagnostic beside a verdict and never a verdict
	// of its own.**
	TypeRenamed *RenamedKey `json:"type_renamed,omitempty"`
}

// RenamedKey is the was/now pair of a rename a step's stored spelling
// has not followed.
type RenamedKey struct {
	Was string `json:"was"`
	Now string `json:"now"`
}

// RouteVerdict is what routes.check decided, and it is exactly what is
// written to routes.last_check.
type RouteVerdict struct {
	// Holds is true when every step came back ok. It is derived from
	// Steps and reported anyway, because the one question a caller has
	// should not require it to scan a list.
	Holds bool `json:"holds"`

	Steps        []StepCheck `json:"steps"`
	StepsChecked int         `json:"steps_checked"`
	StepsOK      int         `json:"steps_ok"`
	StepsBroken  int         `json:"steps_broken"`

	// Seeds is the start set as the engine understood it, and it is the
	// field that turns "your route is entirely unreachable" from a
	// verdict into a diagnosis.
	Seeds           SeedReport      `json:"seeds"`
	SemanticsSource []TypeSemantics `json:"semantics_source"`
	Gating          Gating          `json:"gating"`

	// Walks is how many times the closure was resumed, which is the
	// number the incremental argument at the top of this file rests on.
	// One for a route that holds, however long it is.
	Walks int `json:"walks"`

	EdgesWalked          int  `json:"edges_walked"`
	InvalidEdgesFollowed int  `json:"invalid_edges_followed"`
	Truncated            bool `json:"truncated"`
	DepthLimited         bool `json:"depth_limited"`
}

// RouteCheck is one check as a caller reads it: the stored verdict, plus
// the three values that say how old it is and what it cost.
type RouteCheck struct {
	RouteVerdict
	Key string `json:"key"`

	CheckedAt            time.Time `json:"checked_at"`
	CheckedDesignVersion int64     `json:"checked_design_version"`
	// DesignVersion is the game's counter as this call read it, **before
	// the walk**. It equals CheckedDesignVersion on a quiet game and is
	// below the project's current value the moment a write lands
	// mid-check, which is what leaves the route stale rather than
	// freshly green.
	DesignVersion int64       `json:"design_version"`
	Status        RouteStatus `json:"status"`

	Stats Stats `json:"stats"`
}

// CheckRoute walks a route and stores the verdict.
func (s *Service) CheckRoute(ctx context.Context, projectID uuid.UUID, key string) (
	RouteCheck, error,
) {
	started := time.Now()
	route, err := s.RouteByKey(ctx, projectID, key)
	if err != nil {
		return RouteCheck{}, err
	}

	// **Read before the walk, never after it.** This is the whole of
	// step 3's promise: a metamodel write that commits while the closure
	// is running must not be counted as a design this verdict saw. Read
	// afterwards, such a write would be stored as the version checked
	// and the route would read `checked` against content it never looked
	// at -- a stale green, which is the one failure the two clocks on
	// this row exist to prevent.
	designVersion, err := s.q.GetDesignVersion(ctx, projectID)
	if err != nil {
		return RouteCheck{}, fmt.Errorf("read the game's design version: %w", err)
	}

	// Resolved here, on the path the product uses, so a game that
	// declared nothing about its relation types is refused rather than
	// told its route holds. An engine with no edge it is allowed to walk
	// would otherwise prove every route in an undeclared game.
	semantics, err := s.Resolve(ctx, projectID, ResolveInput{})
	if err != nil {
		return RouteCheck{}, err
	}

	if s.beforeWalk != nil {
		s.beforeWalk()
	}

	live, err := s.liveSteps(ctx, projectID, route.Steps)
	if err != nil {
		return RouteCheck{}, err
	}

	params := route.Params.Reach(projectID)
	params.Semantics = semantics
	verdict, err := s.walkSteps(ctx, params, route, live)
	if err != nil {
		return RouteCheck{}, err
	}
	if err := s.orderingViolations(ctx, projectID, semantics, route, live, &verdict); err != nil {
		return RouteCheck{}, err
	}
	tallyVerdict(&verdict)

	stored, err := json.Marshal(verdict)
	if err != nil {
		return RouteCheck{}, fmt.Errorf("encode the route verdict: %w", err)
	}
	row, err := s.q.WriteRouteCheck(ctx, dbq.WriteRouteCheckParams{
		ProjectID: projectID, ID: route.ID,
		LastCheck: stored, DesignVersion: designVersion,
	})
	if err != nil {
		return RouteCheck{}, fmt.Errorf("store the route verdict: %w", err)
	}

	// **Read again, after the write.** CheckedDesignVersion is what this
	// verdict was proved against; DesignVersion is what the game says
	// now. On a quiet game they are equal and the route reads `checked`.
	// When a write landed while the walk ran, the second is higher and
	// the answer this call returns already says `stale` -- so a caller
	// learns immediately that its own verdict is about a game that has
	// since moved, rather than reading `checked` here and `stale` from
	// the next routes.get.
	current, err := s.q.GetDesignVersion(ctx, projectID)
	if err != nil {
		return RouteCheck{}, fmt.Errorf("read the game's design version: %w", err)
	}
	out := RouteCheck{
		RouteVerdict:         verdict,
		Key:                  route.Key,
		CheckedAt:            row.LastCheckedAt.Time,
		CheckedDesignVersion: designVersion,
		DesignVersion:        current,
	}
	// The status is decided by routeStatus and not by this function, for
	// the reason that function's own comment gives: a status decided
	// twice is a status two callers can disagree about. The route's
	// updated_at moved to this same now() -- routes_set_updated_at fires
	// on the write above -- so a route checked and not since edited
	// reads as `checked`.
	checkedAt := row.LastCheckedAt.Time
	out.Status = routeStatus(&checkedAt, checkedAt, row.LastCheckedDesignVersion, current)
	out.Stats.DurationMS = time.Since(started).Milliseconds()

	// **After the write, never inside a transaction.** The gating is
	// routeEventMinRole and routeEventHumanOnly, taken rather than
	// re-decided: a caller who may learn that a route changed may learn
	// that it was checked. events.go records that decision beside the
	// other two kinds; the constant is declared here because a constant
	// with no reader is a mechanism nothing reads, which this
	// repository's linter refuses outright.
	s.publish(projectID, eventRouteChecked, routeEventMinRole, routeEventHumanOnly,
		routeCheckedEvent{
			ID: route.ID, Key: route.Key, Version: route.Version,
			Holds: verdict.Holds, StepsChecked: verdict.StepsChecked,
			StepsBroken: verdict.StepsBroken,
		})
	return out, nil
}

// eventRouteChecked is the third route kind. Its gating decision, its
// name and the shape of its payload are recorded in events.go beside the
// other two -- one gating decision, made once -- and the constant is
// declared here because this is the only file that can use it.
const eventRouteChecked = "route.checked"

// routeCheckedEvent is the payload: **the verdict summary, never the
// per-step list.**
type routeCheckedEvent struct {
	ID           uuid.UUID `json:"id"`
	Key          string    `json:"key"`
	Version      int32     `json:"version"`
	Holds        bool      `json:"holds"`
	StepsChecked int       `json:"steps_checked"`
	StepsBroken  int       `json:"steps_broken"`
}

// liveStep is one step with the address its entity carries **today**,
// which is not always the address the step stores.
type liveStep struct {
	step       RouteStep
	entityID   uuid.UUID
	present    bool
	entityType string
	key        string
}

// liveSteps reads each step's entity back by id and gives it its current
// address.
func (s *Service) liveSteps(ctx context.Context, projectID uuid.UUID, steps []RouteStep) (
	[]liveStep, error,
) {
	ids := make([]uuid.UUID, 0, len(steps))
	for _, step := range steps {
		if step.EntityID != nil {
			ids = append(ids, *step.EntityID)
		}
	}
	byID, err := s.meta.EntitiesByIDs(ctx, projectID, dedupeIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("read the route's step entities: %w", err)
	}
	typeKey, err := s.entityTypeKeys(ctx, projectID)
	if err != nil {
		return nil, err
	}

	out := make([]liveStep, 0, len(steps))
	for _, step := range steps {
		live := liveStep{step: step}
		if step.EntityID != nil {
			if row, ok := byID[*step.EntityID]; ok {
				live.present = true
				live.entityID = row.ID
				live.entityType = typeKey[row.EntityTypeID]
				live.key = row.Key
			}
		}
		out = append(out, live)
	}
	return out, nil
}

// entityTypeKeys is the game's entity type catalogue as an id → key map,
// read through the metamodel service so this package's isolation rule is
// one implementation rather than a fourth copy of one WHERE clause.
func (s *Service) entityTypeKeys(ctx context.Context, projectID uuid.UUID) (
	map[uuid.UUID]string, error,
) {
	types, err := s.meta.ListEntityTypes(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("read the entity type catalogue: %w", err)
	}
	keys := make(map[uuid.UUID]string, len(types))
	for _, typ := range types {
		keys[typ.ID] = typ.Key
	}
	return keys, nil
}

// walkSteps is the incremental closure and the three verdicts that come
// out of it. orderingViolations adds the fourth afterwards.
func (s *Service) walkSteps(ctx context.Context, params Params, route Route, live []liveStep) (
	RouteVerdict, error,
) {
	reach, err := s.Reach(ctx, params)
	if err != nil {
		return RouteVerdict{}, err
	}
	verdict := RouteVerdict{
		Steps: make([]StepCheck, 0, len(live)),
		Walks: 1,
		Seeds: SeedReport{
			Entities:       reach.Seeds,
			EntityTypes:    reach.SeedEntityTypes,
			IncludeUngated: reach.IncludeUngated,
			Total:          reach.SeedCount,
		},
		SemanticsSource:      params.Semantics.Types(),
		Gating:               reach.Gating,
		EdgesWalked:          reach.EdgesWalked,
		InvalidEdgesFollowed: reach.InvalidEdgesFollowed,
		Truncated:            reach.Truncated,
		DepthLimited:         reach.DepthLimited,
	}

	// The accumulating seed set: the route's own stored seeds, plus
	// every step judged so far that the closure did not already reach.
	// A step it *did* reach adds nothing -- closure(S ∪ {x}) is
	// closure(S) when x is in it -- so it costs no walk.
	accumulated := append([]SeedRef(nil), params.SeedEntities...)
	for _, item := range live {
		check := StepCheck{
			Position:   item.step.Position,
			EntityType: item.step.EntityType,
			Key:        item.step.Key,
		}
		if !item.present {
			// **The tombstone keys are what this names.** The entity is
			// gone and the step is not: a route silently one step
			// shorter is a proof of a progression nobody described.
			check.Verdict = VerdictMissingEntity
			verdict.Steps = append(verdict.Steps, check)
			continue
		}
		if !isSameKey(item.step.EntityType, item.entityType) {
			check.TypeRenamed = &RenamedKey{Was: item.step.EntityType, Now: item.entityType}
		}
		if reach.Reached[item.entityID] {
			check.Verdict = VerdictOK
			verdict.Steps = append(verdict.Steps, check)
			continue
		}

		check.Verdict = VerdictUnmetPrerequisite
		blockers, err := s.blockersFor(ctx, params, reach, item.entityID)
		if err != nil {
			return RouteVerdict{}, err
		}
		check.Blockers = blockers
		verdict.Steps = append(verdict.Steps, check)

		// **A step that did not hold is still a seed for the steps after
		// it.** The question each step answers is "given the seeds and
		// every step before me", and letting one broken step cascade
		// would report a whole route broken for one gap -- a check that
		// failed everything, which is exactly the answer
		// TestAStepThatIsNotReachableYetIsUnmetPrerequisiteAndNamesItsBlockers
		// keeps a control against.
		accumulated = append(accumulated,
			SeedRef{EntityType: item.entityType, Key: item.key})
		resumed := params
		resumed.SeedEntities = accumulated
		reach, err = s.Reach(ctx, resumed)
		if err != nil {
			return RouteVerdict{}, err
		}
		verdict.Walks++
		verdict.EdgesWalked += reach.EdgesWalked
		verdict.InvalidEdgesFollowed += reach.InvalidEdgesFollowed
		verdict.Truncated = verdict.Truncated || reach.Truncated
		verdict.DepthLimited = verdict.DepthLimited || reach.DepthLimited
	}
	return verdict, nil
}

// blockersFor names the entities standing between the closure and one
// step, through the same normalised in-edge statement the unreachable
// analysis reads. One definition of "what gates this", not two.
func (s *Service) blockersFor(ctx context.Context, params Params, reach Reach, of uuid.UUID) (
	[]SeedRef, error,
) {
	edges, err := s.inEdges(ctx, params, []uuid.UUID{of})
	if err != nil {
		return nil, err
	}
	needed := edges[of].all()
	if len(needed) == 0 {
		return nil, nil
	}
	byID, err := s.meta.EntitiesByIDs(ctx, params.ProjectID, dedupeIDs(needed))
	if err != nil {
		return nil, fmt.Errorf("read the blocking entities: %w", err)
	}
	typeKey, err := s.entityTypeKeys(ctx, params.ProjectID)
	if err != nil {
		return nil, err
	}
	out := make([]SeedRef, 0, MaxBlockers)
	for _, id := range needed {
		if reach.Reached[id] {
			continue
		}
		row, ok := byID[id]
		if !ok {
			continue
		}
		out = append(out, SeedRef{EntityType: typeKey[row.EntityTypeID], Key: row.Key})
		if len(out) == MaxBlockers {
			break
		}
	}
	return out, nil
}

// orderingViolations is the fourth verdict, and it is **a different
// mechanism from the other three**.
func (s *Service) orderingViolations(ctx context.Context, projectID uuid.UUID,
	semantics Semantics, route Route, live []liveStep, verdict *RouteVerdict,
) error {
	ordering := semantics.WithTrait("ordering")
	if len(ordering) == 0 {
		return nil
	}
	positions := make(map[uuid.UUID]int, len(live))
	ids := make([]uuid.UUID, 0, len(live))
	for i, item := range live {
		if !item.present {
			continue
		}
		// First occurrence wins for a route that names one entity twice:
		// the earliest position is where the route claims it happens.
		if _, seen := positions[item.entityID]; !seen {
			positions[item.entityID] = i
		}
		ids = append(ids, item.entityID)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.q.ListOrderingEdgesAmong(ctx, dbq.ListOrderingEdgesAmongParams{
		ProjectID: projectID, RelationTypes: dedupeIDs(ordering),
		Entities: dedupeIDs(ids), ExcludeInvalid: route.Params.ExcludeInvalid,
	})
	if err != nil {
		return fmt.Errorf("read the route's ordering edges: %w", err)
	}
	typeKey, err := s.entityTypeKeys(ctx, projectID)
	if err != nil {
		return err
	}
	byID, err := s.meta.EntitiesByIDs(ctx, projectID, dedupeIDs(ids))
	if err != nil {
		return fmt.Errorf("read the route's step entities: %w", err)
	}

	for _, row := range rows {
		if row.SourceID == row.TargetID {
			// An ordering edge from an entity to itself is a loop in the
			// game's own vocabulary, not a fault in this route's order:
			// no arrangement of steps satisfies it. analysis.cycles is
			// the report that names it, and reporting it here as well
			// would send a designer to reorder a route that is not the
			// problem.
			continue
		}
		before, okBefore := positions[row.SourceID]
		after, okAfter := positions[row.TargetID]
		if !okBefore || !okAfter || before < after {
			continue
		}
		step := &verdict.Steps[before]
		step.Verdict = VerdictOutOfOrder
		step.Blockers = nil
		target, ok := byID[row.TargetID]
		if !ok {
			continue
		}
		step.MustPrecede = append(step.MustPrecede,
			SeedRef{EntityType: typeKey[target.EntityTypeID], Key: target.Key})
	}
	return nil
}

// tallyVerdict fills in the counts and the one boolean a caller reads
// first. It is separate from walkSteps so the ordering pass can move a
// verdict before anything is counted, rather than the counts being right
// for three of the four.
func tallyVerdict(verdict *RouteVerdict) {
	verdict.StepsChecked = len(verdict.Steps)
	for _, step := range verdict.Steps {
		if step.Verdict == VerdictOK {
			verdict.StepsOK++
			continue
		}
		verdict.StepsBroken++
	}
	verdict.Holds = verdict.StepsBroken == 0
}

// isSameKey folds case, the way every key comparison in this product
// does: entity_types_key_key is UNIQUE on lower(key), so a step storing
// "Quest" against a type called "quest" is the same key and not a
// rename. internal/metamodel/rename.go records why EqualFold means
// exactly what SQL's lower() means for a key: the key pattern admits no
// character whose case folding differs between the two.
func isSameKey(a, b string) bool { return strings.EqualFold(a, b) }
