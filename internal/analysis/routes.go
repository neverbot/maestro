package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// A route is its own row, and deliberately not a saved view and not a
// kind of one.
//
// The question is fair: `views` is already a table of saved,
// parameterised, versioned artefacts addressed by (project, key), with
// events, an expected_version and a dependency index. A route is saved,
// parameterised, versioned and addressed by (project, key). Four reasons
// it is still its own table, in the order that decided it:
//
//  1. **A view stores a question; a route stores a claim and a verdict
//     about it.** internal/views caches nothing, on purpose: "nothing is
//     cached unless it can say when it went out of date", and its own
//     plan settled (O9) that it adds no design_version because it has no
//     stored result to invalidate. A route is the exception that rule
//     names -- last_check, last_checked_at and
//     last_checked_design_version are a persisted verdict on a named
//     artefact. Putting that on `views` would give that table a cached
//     answer four days after its own sub-project argued it must not have
//     one, and every views query would have to learn a column it never
//     reads.
//
//  2. **A route's payload is authored and ordered; a view's dependency
//     index is derived and unordered.** view_refs is rebuilt from the
//     stored query on every upsert -- it can always be recomputed, which
//     is why a rename is allowed to leave it stale. A route_step cannot
//     be recomputed from anything: it is content somebody wrote, in an
//     order that is the whole point, and its foreign key to `entities`
//     has to survive a deletion as a visible tombstone (ON DELETE SET
//     NULL plus the stored key). No view_refs row has, or could have,
//     that lifecycle.
//
//  3. **A view is defined by things a route has none of.** renderer,
//     renderer_params, layout_mode, background_asset_id,
//     view_positions. Making a route a view means either five columns
//     and a child table that are meaningless for half the rows, or a
//     `kind` discriminator that every existing statement, the list
//     cursor's fingerprint (which keys on the renderer filter) and every
//     renderer requirement check would have to learn. That is a schema
//     change to shipped, tested code in exchange for reusing an `id`
//     column.
//
//  4. **Running a view is a read; checking a route is a write.** views.run
//     is a GET and registerContentRoute leaves it at viewer.
//     routes.check stores its verdict, so it is a POST and takes the
//     editor gate. One table whose rows are read-only under one verb and
//     mutating under another is a permission model with an exception in
//     it.
//
// What the two **do** share is deliberately shared and not duplicated:
// the reachability closure in reach.go (which views' own O8 assigned
// here), internal/paging for the listing cursor, metamodel.RemovedError
// for a version claim against a row that is gone, and the
// (project, key) addressing every row in this product uses.
//
// And one thing a route gives back to the rest of the engine: because a
// route is an ordered list of this game's entities, it is also a **seed
// set** (spec open question 2), which is why analysis.unreachable takes
// `seed_route` and why no start_sets table exists.

// The bounds on a route's own prose. They are internal/views' and
// internal/metamodel's, deliberately: a route's name is the same kind of
// value as a view's name -- one line in a picker, ordered by a listing --
// and its description is the same kind of prose. A third number would be
// a third cap a designer meets for one shape of text in one product.
const (
	// MaxRouteNameLen bounds `name`, which is required: ListRoutes
	// orders by it, so an unnamed route sorts to the front of every list
	// a designer sees and identifies itself by nothing.
	MaxRouteNameLen = 200
	// MaxRouteDescriptionLen bounds `description`, which is optional and
	// may hold newlines: it is prose a designer writes about the
	// progression, and refusing a paragraph break would make the rule
	// less useful than no rule.
	MaxRouteDescriptionLen = 4000
	// MaxRouteStepNoteLen bounds a step's note. It is one line beside one
	// step in a list, so it takes the name's rule rather than the
	// description's.
	MaxRouteStepNoteLen = 200
)

// noRouteVersion is the expected_version an upsert passes when its
// caller has no version to expect. Versions start at 1 and only ever
// climb, so no stored row can equal it: the guarded DO UPDATE is then a
// no-op on the insert path and a guaranteed mismatch if a row turns out
// to exist after all. The same value and the same argument as
// internal/views' and internal/metamodel's.
const noRouteVersion int32 = -1

// createRouteVersion is the `expected_version` that spells "this route
// must not exist yet".
//
// It is 0 because this surface takes internal/views' and
// internal/markdown's convention rather than internal/metamodel's
// absent-means-create, and it is named rather than written as a bare `0`
// where the literal would read as a version number and is in fact the
// opposite of one: no stored route is ever at version 0, so this value
// can only mean "I am creating".
const createRouteVersion int32 = 0

// RouteParams is the route's own definition of what "holding together"
// means: the reachability question a check asks about it.
//
// **A route carries its own parameters so that two people checking one
// route get one answer.** A verdict computed under the caller's defaults
// would mean something different depending on who ran it, which is
// precisely the "two people analysing one game getting different answers
// for reasons neither can see" that stored routes exist to end.
//
// It is validated at write time against the same bounds
// analysis.unreachable applies, and
// TestRouteParamsAreValidatedAtWriteTimeAndNotOnlyAtCheckTime is what
// makes that a decision rather than a paragraph: a route whose `gate` is
// nonsense is refused by routes.upsert, at `/params/gate`, rather than
// being stored and failing every check from then on.
type RouteParams struct {
	// Gate is `any` (the default) or `all`. It is spelled `gate` on the
	// wire and resolves to reach.Params.Gating, which is the same value
	// under the name that file gave it.
	Gate Gating `json:"gate,omitempty"`

	// IncludeUngated and PropagateContainment are tri-state: absent is
	// the documented default (both true) rather than false, which is why
	// they are pointers here and in reach.Params both.
	IncludeUngated       *bool `json:"include_ungated,omitempty"`
	PropagateContainment *bool `json:"propagate_containment,omitempty"`

	// SeedEntities and SeedEntityTypes are start points the route's own
	// steps do not supply -- a character creation screen, say, that
	// nobody would list as a step of a levelling route.
	//
	// They are bounded here and **resolved at check time**, not at write
	// time. A step is resolved on write because a step is the claim
	// itself and a route authored out of typos must not be told it
	// holds; a seed is a parameter of the question, and refusing to save
	// a route because a seed entity has since been deleted would make
	// the parameters harder to fix than to abandon.
	SeedEntities    []SeedRef `json:"seed_entities,omitempty"`
	SeedEntityTypes []string  `json:"seed_entity_types,omitempty"`

	// MaxDepth and ExcludeInvalid are the last two arguments of the
	// closure, stored for the same reason as the rest.
	MaxDepth       int  `json:"max_depth,omitempty"`
	ExcludeInvalid bool `json:"exclude_invalid,omitempty"`
}

// Reach turns a route's stored parameters into the closure's own
// arguments, which is the one place the two spellings meet.
//
// It exists so routes.check cannot quietly compute under the caller's
// defaults: there is exactly one conversion and it reads every stored
// field. A field added to RouteParams and not added here would be the
// write-only-column defect in its route-shaped form, which is what
// TestARouteReadsBackEverythingItWasWrittenWith and Task 10's
// TestARouteChecksUnderItsOwnStoredParamsAndNotTheCallersDefaults exist
// to catch from their two ends.
func (p RouteParams) Reach(projectID uuid.UUID) Params {
	return Params{
		ProjectID:            projectID,
		SeedEntities:         p.SeedEntities,
		SeedEntityTypes:      p.SeedEntityTypes,
		IncludeUngated:       p.IncludeUngated,
		PropagateContainment: p.PropagateContainment,
		Gating:               p.Gate,
		MaxDepth:             p.MaxDepth,
		ExcludeInvalid:       p.ExcludeInvalid,
	}
}

// RouteStepInput is one step as a caller writes it: an entity, addressed
// by the pair that addresses one, and a note.
type RouteStepInput struct {
	EntityType string
	Key        string
	Note       string
}

// RouteInput is one route upsert, addressed by its key.
//
// ExpectedVersion is **required**: 0 to create a route that must not
// exist yet, or the version read. Omitting it is invalid_input and not a
// guess, because the guess a caller would want depends on whether the
// route exists and that is the very thing it is asserting.
//
// A non-nil, non-zero ExpectedVersion against a route this game does not
// have is a metamodel.RemovedError -- not_found saying it was removed --
// rather than a quiet creation. The reason is stronger here than where
// that error was written: a route's last_check, its
// last_checked_design_version and every one of its step rows hang off
// the route's id, so a re-creation under a new id would discard a
// designer's proved progression *and* its proof, and answer success.
type RouteInput struct {
	Key             string
	Name            string
	Description     string
	Params          RouteParams
	Steps           []RouteStepInput
	ExpectedVersion *int32
	Actor           Actor
}

// RouteStep is one step as it is read back: the stored address, the
// resolved id, and the note.
//
// **EntityID is nil for a tombstone** -- the entity was deleted and
// 0013_analysis.sql's ON DELETE SET NULL kept the step, visibly, with
// its keys. The keys are never rewritten by a rename either
// (internal/metamodel/rename.go is the authority), so the stored
// spelling is what a check reports as moved.
type RouteStep struct {
	Position   int32      `json:"position"`
	EntityID   *uuid.UUID `json:"entity_id"`
	EntityType string     `json:"entity_type"`
	Key        string     `json:"key"`
	Note       string     `json:"note"`
}

// RouteStatus is the three-state health of a route, and the three are
// **three**, not a boolean with a null.
//
// "Never checked" is a different fact from "checked, and the design has
// moved since", which is a different fact from "checked against this
// design". Collapsing any pair of them either alarms a designer wrongly
// or reassures them wrongly, and an empty answer satisfies any assertion
// -- which is why TestANeverCheckedRouteIsNeitherStaleNorHealthy pins
// all three in one test rather than two of them in two.
type RouteStatus string

const (
	// RouteNeverChecked: last_checked_design_version is NULL. Nothing is
	// known about this route, which is not the same as it being out of
	// date.
	RouteNeverChecked RouteStatus = "never_checked"
	// RouteStale: it was checked against a design version below the
	// game's current one. **Stale is never green and never red**: it
	// says the verdict is about a game that has since changed, and
	// nothing about whether the route holds now.
	RouteStale RouteStatus = "stale"
	// RouteChecked: checked against the current design version. Whether
	// the verdict was `ok` or `broken` is in last_check, which routes.get
	// returns and the listing does not.
	RouteChecked RouteStatus = "checked"
)

// RouteStatuses is every value, so a surface enumerates rather than
// repeats them.
var RouteStatuses = []RouteStatus{RouteNeverChecked, RouteStale, RouteChecked}

// Route is one route as routes.get returns it.
type Route struct {
	ID          uuid.UUID   `json:"-"`
	Key         string      `json:"key"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Params      RouteParams `json:"params"`
	Version     int32       `json:"version"`
	Steps       []RouteStep `json:"steps"`

	// Status is the three-state health, decided against DesignVersion.
	//
	// **There is no separate `stale` boolean beside it.** Stale is one
	// of the three states, and shipping a status *and* a boolean derived
	// from it is one fact spelled twice, which a later edit drifts apart
	// -- the defect this repository keeps producing, in its smallest
	// form. A caller that wants the boolean compares against RouteStale.
	Status RouteStatus `json:"status"`

	// LastCheck is the stored verdict as routes.check wrote it, and it
	// is **the only thing this sub-project caches**. It is allowed to be
	// cached for exactly one reason: it can say when it went out of
	// date, which is what the two fields below are.
	LastCheck                json.RawMessage `json:"last_check,omitempty"`
	LastCheckedAt            *time.Time      `json:"last_checked_at,omitempty"`
	LastCheckedDesignVersion *int64          `json:"last_checked_design_version,omitempty"`

	// DesignVersion is the game's current counter, returned beside the
	// verdict's so a reader can see the comparison rather than trust the
	// status.
	DesignVersion int64 `json:"design_version"`
}

// RouteSummary is one row of a listing: enough to pick a route and to
// see its health, and **no step list and no stored verdict**.
//
// That mirrors ListViewsPage's discipline about not shipping query
// documents in a listing, and it has the same justification: a listing
// answers "what is here and what needs attention", a route's steps and
// its proof are what routes.get is for, and a listing that carried them
// would grow without bound in the length of the routes rather than in
// their number.
type RouteSummary struct {
	Key         string      `json:"key"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Version     int32       `json:"version"`
	StepCount   int64       `json:"step_count"`
	Status      RouteStatus `json:"status"`
}

// RoutePage is one page of routes plus the cursor for the next.
//
// **NextCursor is set when the page came back full**, and empty
// otherwise, so a caller looping until it is empty is correct and must
// expect a final empty page rather than treating one as an error. The
// rest of the contract is paging.Cursor's. Cursor.Sort, for this
// listing, is the route's name.
type RoutePage struct {
	Routes     []RouteSummary `json:"routes"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// The bounds on one route listing: asking for nothing is no opinion and
// gets the default, asking for too much is an opinion and gets the cap.
// A game holds tens of routes rather than thousands.
const (
	defaultRoutePage = int32(50)
	maxRoutePage     = int32(200)
)

// UpsertRoute creates or replaces a route, addressed by its key, and
// **replaces its whole step list**.
//
// Steps are positional, and rewriting them wholesale is simpler for an
// agent than diffing a list whose every index moves when one step is
// inserted. The rewrite is DELETE-then-INSERT inside the same
// transaction as the route row's compare-and-set, so a step list never
// lands beside a route row that rolled back.
//
// **A step's keys are resolved to an entity id before anything is
// stored, and a key that does not resolve is not_found naming the step's
// position and its key.** It is not stored as a tombstone: a tombstone
// is what a *deletion* leaves behind, and accepting one on write would
// let an agent author a route out of typos and be told it holds.
func (s *Service) UpsertRoute(ctx context.Context, projectID uuid.UUID, in RouteInput) (
	Route, error,
) {
	problems := metamodel.RowKeyProblems("/key", in.Key)
	problems = append(problems, routeNameProblems(in.Name)...)
	if problem := storableText(in.Description, MaxRouteDescriptionLen, true); problem != "" {
		problems = append(problems, metamodel.FieldError{Path: "/description", Message: problem})
	}
	if in.ExpectedVersion == nil {
		problems = append(problems, metamodel.FieldError{
			Path: "/expected_version",
			Message: "is required: send 0 to create a route that must not exist yet, or " +
				"the version you read. It is not defaulted, because the default a caller " +
				"would want depends on whether the route already exists, which is the " +
				"very thing this argument asserts",
		})
	}
	problems = append(problems, in.Params.problems()...)
	problems = append(problems, stepProblems(in.Steps)...)
	if len(problems) > 0 {
		return Route{}, &metamodel.ValidationError{
			Code: metamodel.CodeInvalidInput, Fields: problems,
		}
	}

	// Resolved before the transaction opens, because a step that names
	// nothing is a refusal and not a write, and holding a row lock while
	// deciding it buys nothing.
	steps, err := s.resolveSteps(ctx, projectID, in.Steps)
	if err != nil {
		return Route{}, err
	}
	params, err := json.Marshal(in.Params)
	if err != nil {
		// Unreachable: RouteParams is this package's own struct of
		// scalars, slices and pointers to bool. Reported rather than
		// swallowed, because a params column silently written as `{}`
		// would be a stored question nobody asked.
		return Route{}, fmt.Errorf("encode the route parameters: %w", err)
	}

	expected := noRouteVersion
	if in.ExpectedVersion != nil {
		expected = *in.ExpectedVersion
	}

	var row dbq.Route
	err = s.withTx(ctx, func(q *dbq.Queries) error {
		existing, err := q.GetRouteByKeyForUpdate(ctx, dbq.GetRouteByKeyForUpdateParams{
			ProjectID: projectID, Key: in.Key,
		})
		switch {
		case err == nil:
			// Spelling before version, the order internal/metamodel,
			// internal/markdown and internal/views all settled: a caller
			// failing for two reasons at once hears the one it can act
			// on, rather than "current version is N" over a key that
			// would be refused again at that same version.
			if existing.Key != in.Key {
				return routeKeyRespellingError(in.Key, existing.Key)
			}
			if in.ExpectedVersion == nil || *in.ExpectedVersion != existing.Version {
				return &metamodel.VersionConflictError{Current: existing.Version}
			}
		case errors.Is(err, pgx.ErrNoRows):
			// **No row, and a version claimed: the route was removed.**
			// Zero is exempt, because on this surface 0 is how a caller
			// spells "this must not exist yet" and a 0 reaching here is a
			// creation claim that has just been proved right.
			if in.ExpectedVersion != nil && *in.ExpectedVersion != createRouteVersion {
				return &metamodel.RemovedError{
					Subject: "route",
					Address: fmt.Sprintf("%q", in.Key),
					Claimed: *in.ExpectedVersion,
				}
			}
		default:
			return fmt.Errorf("lock route: %w", err)
		}

		row, err = q.UpsertRoute(ctx, dbq.UpsertRouteParams{
			ProjectID:        projectID,
			Key:              in.Key,
			Name:             in.Name,
			Description:      in.Description,
			Params:           params,
			ExpectedVersion:  expected,
			UpdatedByUserID:  in.Actor.UserID,
			UpdatedByTokenID: in.Actor.TokenID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// The guarded DO UPDATE matched nothing: between the locked
			// read above and this statement another writer created or
			// advanced the row.
			return conflictOnRouteKey(ctx, q, projectID, in.Key)
		}
		if err != nil {
			// 0013_analysis.sql gives routes the same composite
			// (updated_by_token_id, project_id) key every table in
			// 0004_metamodel.sql carries, so a token scoped to another
			// game cannot be recorded as the editor of this one's route.
			// Without this arm that refusal reaches a log as a raw
			// SQLSTATE 23503 over a constraint name.
			if mapped := metamodel.ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
				return mapped
			}
			return fmt.Errorf("upsert route: %w", err)
		}
		return writeSteps(ctx, q, projectID, row.ID, steps)
	})
	if err != nil {
		return Route{}, err
	}

	// **After the commit, never inside it.** An event published inside
	// the transaction announces a change that may still roll back, and a
	// subscriber that re-reads on hearing it would read the state before
	// the change and cache it as the state after.
	s.publish(projectID, eventRouteUpserted, routeEventMinRole, routeEventHumanOnly,
		routeEvent{ID: row.ID, Key: row.Key, Version: row.Version})
	return s.routeFromRow(ctx, projectID, row)
}

// resolvedStep is one step with its entity id, between resolution and
// the write.
type resolvedStep struct {
	entityID   uuid.UUID
	entityType string
	key        string
	note       string
}

// resolveSteps turns each (entity type key, entity key) pair into an
// entity id of **this game**, through the metamodel's own scoped lookup.
//
// A key that resolves to nothing is not_found naming the step's position
// and its key -- never dropped, never stored as a tombstone. A route
// silently missing the step an agent typed wrongly is a proof of a
// progression the agent never described.
func (s *Service) resolveSteps(ctx context.Context, projectID uuid.UUID, steps []RouteStepInput) (
	[]resolvedStep, error,
) {
	out := make([]resolvedStep, 0, len(steps))
	for i, step := range steps {
		row, err := s.meta.EntityByKey(ctx, projectID, step.EntityType, step.Key)
		if err != nil {
			return nil, fmt.Errorf("steps[%d] (%s %q): %w", i, step.EntityType, step.Key, err)
		}
		out = append(out, resolvedStep{
			entityID: row.ID, entityType: step.EntityType, key: row.Key, note: step.Note,
		})
	}
	return out, nil
}

// writeSteps replaces one route's whole step list.
//
// Delete-then-insert rather than a diff, for writeRefs' reason and one
// of its own: positions are dense and every one of them moves when a
// step is inserted, so a diff would rewrite most rows anyway and would
// be a second place that decides what a step is.
func writeSteps(ctx context.Context, q *dbq.Queries, projectID, routeID uuid.UUID,
	steps []resolvedStep,
) error {
	if err := q.DeleteRouteSteps(ctx, dbq.DeleteRouteStepsParams{
		ProjectID: projectID, RouteID: routeID,
	}); err != nil {
		return fmt.Errorf("clear the route's steps: %w", err)
	}
	for i, step := range steps {
		if err := q.InsertRouteStep(ctx, dbq.InsertRouteStepParams{
			RouteID:       routeID,
			ProjectID:     projectID,
			Position:      int32(i),
			EntityID:      step.entityID,
			EntityTypeKey: step.entityType,
			EntityKey:     step.key,
			Note:          step.note,
		}); err != nil {
			return fmt.Errorf("write route step %d: %w", i, err)
		}
	}
	return nil
}

// RouteByKey loads one route by its key, matched without regard to case,
// with its steps and its three-state status.
//
// A missing key is named rather than reported as a bare sentinel,
// because the caller supplied this key and it is the one thing it can
// act on -- the same split internal/views draws between ViewByKey and
// ViewByID.
func (s *Service) RouteByKey(ctx context.Context, projectID uuid.UUID, key string) (Route, error) {
	row, err := s.q.GetRouteByKey(ctx, dbq.GetRouteByKeyParams{ProjectID: projectID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, fmt.Errorf("%w: no route %q in this game", ErrNotFound, key)
	}
	if err != nil {
		return Route{}, fmt.Errorf("read route: %w", err)
	}
	return s.routeFromRow(ctx, projectID, row)
}

// routeFromRow fills in what the row does not carry: the steps, the
// game's current design version, and the status the two decide.
func (s *Service) routeFromRow(ctx context.Context, projectID uuid.UUID, row dbq.Route) (
	Route, error,
) {
	out := Route{
		ID: row.ID, Key: row.Key, Name: row.Name, Description: row.Description,
		Version: row.Version, LastCheck: row.LastCheck,
		LastCheckedDesignVersion: row.LastCheckedDesignVersion,
	}
	// pgtype.Timestamptz rather than a *time.Time on the generated row,
	// so the NULL is unwrapped here: a never-checked route has no time,
	// and a zero time presented as one would read as 1 January year 1
	// wherever a client formats it.
	if row.LastCheckedAt.Valid {
		at := row.LastCheckedAt.Time
		out.LastCheckedAt = &at
	}
	if len(row.Params) > 0 {
		if err := json.Unmarshal(row.Params, &out.Params); err != nil {
			// The column is written by this package alone, so this is a
			// stored value that no longer decodes -- reported rather
			// than defaulted, because a route silently checked under
			// `any` when it was authored as `all` is the wrong answer in
			// the right shape.
			return Route{}, fmt.Errorf("decode the stored route parameters: %w", err)
		}
	}
	steps, err := s.q.ListRouteStepsByRouteID(ctx, dbq.ListRouteStepsByRouteIDParams{
		ProjectID: projectID, RouteID: row.ID,
	})
	if err != nil {
		return Route{}, fmt.Errorf("read the route's steps: %w", err)
	}
	out.Steps = make([]RouteStep, 0, len(steps))
	for _, step := range steps {
		out.Steps = append(out.Steps, RouteStep{
			Position: step.Position, EntityID: step.EntityID,
			EntityType: step.EntityTypeKey, Key: step.EntityKey, Note: step.Note,
		})
	}
	current, err := s.q.GetDesignVersion(ctx, projectID)
	if err != nil {
		return Route{}, fmt.Errorf("read the game's design version: %w", err)
	}
	out.DesignVersion = current
	out.Status = routeStatus(out.LastCheckedAt, row.UpdatedAt.Time,
		row.LastCheckedDesignVersion, current)
	return out, nil
}

// routeStatus is the three-state decision, written once because two
// readers ask it -- routes.get and the listing -- and a status decided
// twice is a status two callers can disagree about.
//
// **The counter is coarse and that is accepted.** Any write anywhere in
// the game moves design_version, so a typo fixed in one entity's
// description marks every route in the game stale. The alternative is a
// per-route dependency set maintained on every write, which would be
// wrong the moment a *new* relation makes a previously irrelevant entity
// relevant -- and a staleness signal that is subtly wrong is worse than
// one that is bluntly right, because re-checking is cheap and believing
// a stale green is not.
//
// **Two clocks, not one, and the second is not decoration.**
// design_version answers "has the game changed under this verdict"; it
// cannot answer "has the *route* changed under it", because `routes` is
// deliberately not one of the four tables the triggers watch -- a check
// that marked every route in the game stale, including the one it had
// just checked, would be a mechanism that invalidates its own output
// (0013_analysis.sql argues it). So a route whose steps were rewritten
// after its last check would otherwise read as `checked`: a verdict
// about a claim nobody makes any more, presented as green. That is the
// stale-green this whole design exists to prevent, arriving through the
// one door design_version does not watch. updated_at against
// last_checked_at closes it, with no new column and no trigger:
// routes_set_updated_at already moves the first on every write to the
// row, and every upsert writes the row because it advances the version.
// TestEditingARouteMakesItsOwnVerdictStale is that half; the design half
// is TestANeverCheckedRouteIsNeitherStaleNorHealthy.
func routeStatus(checkedAt *time.Time, updatedAt time.Time,
	checkedVersion *int64, current int64,
) RouteStatus {
	switch {
	case checkedVersion == nil:
		return RouteNeverChecked
	case *checkedVersion < current:
		return RouteStale
	case checkedAt == nil || checkedAt.Before(updatedAt):
		return RouteStale
	default:
		return RouteChecked
	}
}

// ListRoutes returns one page of a game's routes.
//
// **A page is a position, not a snapshot**; paging.Cursor records what
// that means while the game is being edited underneath the caller.
func (s *Service) ListRoutes(ctx context.Context, projectID uuid.UUID, cursor string, limit int32) (
	RoutePage, error,
) {
	size := paging.Size(limit, defaultRoutePage, maxRoutePage)
	fingerprint := routeListingFingerprint(projectID)
	after, err := paging.Decode(cursor, fingerprint, refuseRouteCursor)
	if err != nil {
		return RoutePage{}, err
	}
	params := dbq.ListRoutesPageParams{ProjectID: projectID, Limit: size}
	if after.ID != uuid.Nil {
		params.AfterID = &after.ID
		params.AfterName = &after.Sort
	}
	rows, err := s.q.ListRoutesPage(ctx, params)
	if err != nil {
		return RoutePage{}, fmt.Errorf("list routes: %w", err)
	}
	current, err := s.q.GetDesignVersion(ctx, projectID)
	if err != nil {
		return RoutePage{}, fmt.Errorf("read the game's design version: %w", err)
	}
	page := RoutePage{Routes: make([]RouteSummary, 0, len(rows))}
	for _, row := range rows {
		page.Routes = append(page.Routes, RouteSummary{
			Key: row.Key, Name: row.Name, Description: row.Description,
			Version: row.Version, StepCount: row.StepCount,
			Status: routeStatus(checkedAt(row.LastCheckedAt), row.UpdatedAt.Time,
				row.LastCheckedDesignVersion, current),
		})
	}
	// paging.Size never returns a limit below one, so a full page is
	// never an empty one.
	if len(rows) == int(size) {
		last := rows[len(rows)-1]
		page.NextCursor = paging.Encode(paging.Cursor{
			Sort: last.Name, ID: last.ID, Fingerprint: fingerprint,
		})
	}
	return page, nil
}

// routeListingFingerprint digests the listing a cursor was issued under.
//
// **The project id is first and the domain discriminator is second.**
// Without the project id two games' listings share a fingerprint and one
// game's cursor pages the other's rows from a position that means
// nothing there -- the defect internal/paging's package comment records.
// Without "routes", a cursor from this listing and one from another
// domain's listing over the same game would be interchangeable, and each
// would page the other perfectly and answer a different question.
//
// This listing takes no filter, so the digest has only two parts, and
// that is exactly why it is asserted compositionally as well as
// behaviourally: with no filter to discriminate, the behavioural test
// rests on the project id alone, and
// TestTheRouteListingFingerprintIsProjectIdFirstAndCarriesItsDomain is
// what pins the order rather than the mere fact of a difference.
func routeListingFingerprint(projectID uuid.UUID) string {
	return paging.Fingerprint(projectID.String(), "routes")
}

// checkedAt unwraps the generated row's nullable timestamp, so the
// listing and routes.get hand routeStatus the same shape and cannot
// disagree about what "never" looks like.
func checkedAt(at pgtype.Timestamptz) *time.Time {
	if !at.Valid {
		return nil
	}
	moment := at.Time
	return &moment
}

// refuseRouteCursor turns paging's message into this package's own
// refusal, at the caller's own argument path.
func refuseRouteCursor(message string) error { return invalidInput("cursor", message) }

// RemoveRoute deletes a route and, through ON DELETE CASCADE, its steps.
//
// **It takes an expected_version, and that is the opposite of what
// views.remove does.** internal/views argues that a view is derived
// content, cheap to re-upsert from a document the caller already holds,
// so requiring a version there would make every removal a read-then-write
// against churn the caller does not care about. A route inverts both
// halves of that argument: its steps are *authored*, in an order that is
// the whole point, and its stored verdict is not reconstructible at all
// -- somebody proved a progression and the proof goes with the row. So
// the inversion is stated here rather than left to be read as an
// omission by someone who has just read views.remove.
//
// TestRemovingARouteRequiresTheVersionAndSaysWhy asserts the refusal and
// that the message names what would be lost.
func (s *Service) RemoveRoute(ctx context.Context, projectID uuid.UUID, key string,
	expectedVersion *int32,
) error {
	if expectedVersion == nil {
		return &metamodel.ValidationError{
			Code: metamodel.CodeInvalidInput,
			Fields: []metamodel.FieldError{{
				Path: "/expected_version",
				Message: "is required to remove a route, unlike views.remove, and the " +
					"difference is the point: a view is derived content that can be " +
					"re-upserted from the document you hold, while a route's ordered " +
					"steps are authored and its stored verdict -- last_check, and the " +
					"design version it was proved against -- cannot be reconstructed " +
					"from anything. Read the route and send the version you saw",
			}},
		}
	}
	route, err := s.RouteByKey(ctx, projectID, key)
	if err != nil {
		return err
	}
	if route.Version != *expectedVersion {
		return &metamodel.VersionConflictError{Current: route.Version}
	}
	// Deleted by id rather than by key, so the row removed is the row
	// that was read: a key is folded and a second writer could have
	// replaced it between the two statements. Both are filtered on the
	// project regardless.
	removed, err := s.q.DeleteRoute(ctx, dbq.DeleteRouteParams{ProjectID: projectID, ID: route.ID})
	if err != nil {
		return fmt.Errorf("remove route: %w", err)
	}
	if removed == 0 {
		// Zero rows is the answer to "was it there": this caller
		// resolved the route a moment ago and raced another remover.
		return fmt.Errorf("%w: no route %q in this game", ErrNotFound, key)
	}
	s.publish(projectID, eventRouteRemoved, routeEventMinRole, routeEventHumanOnly,
		routeEvent{ID: route.ID, Key: route.Key, Version: route.Version})
	return nil
}

// conflictOnRouteKey re-reads a key whose guarded upsert matched no row
// and names what actually stands in the way. Both outcomes are real: the
// winning writer may have created the key under a different spelling, or
// advanced a version this caller was holding.
func conflictOnRouteKey(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, key string) error {
	row, err := q.GetRouteByKey(ctx, dbq.GetRouteByKeyParams{ProjectID: projectID, Key: key})
	if err != nil {
		return fmt.Errorf("re-read route after a failed upsert: %w", err)
	}
	if row.Key != key {
		return routeKeyRespellingError(key, row.Key)
	}
	return &metamodel.VersionConflictError{Current: row.Version}
}

// routeKeyRespellingError refuses a second spelling of a stored key.
// Keys are matched without regard to case, so a respelling is a rewrite
// of a handle rather than a new route, and it is refused rather than
// silently ignored.
func routeKeyRespellingError(requested, stored string) error {
	return &metamodel.ValidationError{
		Code: metamodel.CodeInvalidInput,
		Fields: []metamodel.FieldError{{
			Path: "/key",
			Message: fmt.Sprintf(
				"%q already exists here spelled %q, and keys are matched without regard "+
					"to case: use %q to update it, or pick a different key",
				requested, stored, stored),
		}},
	}
}

// problems judges a route's stored parameters against the same bounds
// analysis.unreachable applies to the same arguments.
//
// **They are judged on write and not only on check**, which is the whole
// of Step 2's `params` decision: a route whose gate is nonsense is a
// route every check from now on will refuse, and the caller who can fix
// it is the one writing it. The paths are the pointer into the stored
// document, so a caller is told which member of which object to change.
func (p RouteParams) problems() []metamodel.FieldError {
	var problems []metamodel.FieldError
	switch p.Gate {
	case "", GatingAny, GatingAll:
	default:
		names := make([]string, 0, len(Gatings))
		for _, gate := range Gatings {
			names = append(names, string(gate))
		}
		problems = append(problems, metamodel.FieldError{
			Path: "/params/gate",
			Message: fmt.Sprintf("is %q; the two readings are %s, or leave it out for %q",
				p.Gate, metamodel.QuotedList(names), GatingAny),
		})
	}
	if p.MaxDepth > MaxMaxDepth {
		problems = append(problems, metamodel.FieldError{
			Path: "/params/max_depth",
			Message: fmt.Sprintf("is %d, above the cap of %d; lower it. It is refused "+
				"rather than trimmed because a route checked under a bound nobody asked "+
				"for reads exactly like one that holds", p.MaxDepth, MaxMaxDepth),
		})
	}
	if len(p.SeedEntities) > MaxSeedKeys {
		problems = append(problems, metamodel.FieldError{
			Path: "/params/seed_entities",
			Message: fmt.Sprintf("names %d entities, above the cap of %d",
				len(p.SeedEntities), MaxSeedKeys),
		})
	}
	if len(p.SeedEntityTypes) > MaxTypeKeys {
		problems = append(problems, metamodel.FieldError{
			Path: "/params/seed_entity_types",
			Message: fmt.Sprintf("names %d entity types, above the cap of %d",
				len(p.SeedEntityTypes), MaxTypeKeys),
		})
	}
	return problems
}

// stepProblems bounds the step list itself.
//
// **At most MaxRouteSteps, refused and not clamped.** A route silently
// truncated to five hundred steps is a proof of a progression that stops
// halfway and says it holds, which is the worst thing a stored verdict
// can be. The refusal names the count and the cap so a caller can split
// the route rather than guess.
func stepProblems(steps []RouteStepInput) []metamodel.FieldError {
	var problems []metamodel.FieldError
	if len(steps) > MaxRouteSteps {
		problems = append(problems, metamodel.FieldError{
			Path: "/steps",
			Message: fmt.Sprintf("names %d steps, above the cap of %d; split the route. "+
				"It is refused rather than truncated to the cap because a route cut off "+
				"halfway would be checked, and could pass, as a claim about a "+
				"progression nobody made", len(steps), MaxRouteSteps),
		})
		// The per-step checks are skipped once the list is over the cap:
		// five hundred and one FieldErrors is not a message.
		return problems
	}
	for i, step := range steps {
		if problem := storableText(step.Note, MaxRouteStepNoteLen, false); problem != "" {
			problems = append(problems, metamodel.FieldError{
				Path:    fmt.Sprintf("/steps/%d/note", i),
				Message: problem,
			})
		}
	}
	return problems
}

// routeNameProblems judges the name, which is required for the reason
// every named row in this product requires one: the listing is ordered by
// it, so an unnamed route sorts to the front of every list a designer
// sees and identifies itself by nothing.
func routeNameProblems(name string) []metamodel.FieldError {
	if name == "" {
		return []metamodel.FieldError{{
			Path: "/name",
			Message: "is required: it is what a designer reads in the route list, and " +
				"the listing is ordered by it",
		}}
	}
	if problem := storableText(name, MaxRouteNameLen, false); problem != "" {
		return []metamodel.FieldError{{Path: "/name", Message: problem}}
	}
	return nil
}

// storableText is internal/views' checkStorableText, over the metamodel's
// two exported primitives rather than over a copy of its rules.
//
// It is written here rather than imported because internal/views and
// internal/analysis may not import each other -- the seam views' own O9
// settled -- and the shared half is already as low as it goes:
// metamodel.LengthProblem counts characters rather than bytes and
// metamodel.CheckText finds the control character. What is local is the
// one asymmetry: a description is prose and a newline in it is the
// author's paragraph break, while a name is one line in a picker and a
// newline there is refused like any other control character.
func storableText(value string, max int, allowParagraphs bool) string {
	if problem := metamodel.LengthProblem(value, max); problem != "" {
		return problem
	}
	allowed := ""
	if allowParagraphs {
		allowed = "\n\t"
	}
	fault, bad := metamodel.CheckText(value, allowed)
	switch {
	case !bad:
		return ""
	case fault.InvalidUTF8:
		return "is not valid UTF-8: a byte in it does not decode as any character, " +
			"and Postgres refuses that outright"
	default:
		line := "this is one line of text"
		if allowParagraphs {
			line = "only a newline or a tab is allowed here"
		}
		return fmt.Sprintf("holds a control character (%U at byte %d): %s",
			fault.Rune, fault.Offset, line)
	}
}
