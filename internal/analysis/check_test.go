package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
)

// check runs routes.check with the error surfaced.
func (g game) check(t *testing.T, key string) RouteCheck {
	t.Helper()
	got, err := g.analysis.CheckRoute(context.Background(), g.projectID, key)
	assert.Must(t, err == nil, "check route %q: %v", key, err)
	return got
}

// verdicts is the per-step answer as a list, for a test that reads the
// shape of a whole route rather than one step.
func verdicts(got RouteCheck) []Verdict {
	out := make([]Verdict, 0, len(got.Steps))
	for _, step := range got.Steps {
		out = append(out, step.Verdict)
	}
	return out
}

func TestCheck(t *testing.T) {
	t.Parallel()
	a := newArea(t)

	// TestARouteThatHoldsReportsOkForEveryStepAndNamesItsSeedSet is **the
	// negative half of this whole task**, and it is the one most likely to
	// pass for the wrong reason.
	t.Run("a route that holds reports ok for every step and names its seed set", func(t *testing.T) {
		g := a.gated(t)
		chain := []string{"tutorial", "hogger", "defias", "deadmines", "vancleef"}
		for _, key := range chain {
			g.entity(t, "quest", key)
		}
		for i := 0; i+1 < len(chain); i++ {
			g.edge(t, "unlocks", "quest", chain[i], chain[i+1])
		}
		g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps(chain...),
		})

		got := g.check(t, "levelling")
		assert.Must(t, got.Holds, "a route whose every step is reachable does not hold: %v", verdicts(got))
		for _, step := range got.Steps {
			assert.Should(t, step.Verdict == VerdictOK, "step %d (%s) = %q, want ok", step.Position, step.Key, step.Verdict)
		}
		assert.Should(t, got.StepsChecked == len(chain) && got.StepsOK == len(chain) && got.StepsBroken == 0, "counts = checked %d / ok %d / broken %d, want %d / %d / 0",
			got.StepsChecked, got.StepsOK, got.StepsBroken, len(chain), len(chain))
		// The seed set as the engine understood it: `tutorial` is the one
		// entity with no incoming gate, so include_ungated made it the start
		// and the total is one. A run that started nowhere would report zero
		// here and five `ok`s above.
		assert.Should(t, got.Seeds.IncludeUngated && got.Seeds.Total == 1, "seeds = %+v, want include_ungated with exactly one entity", got.Seeds)
		assert.Should(t, got.EdgesWalked == len(chain)-1, "edges_walked = %d, want %d: a check that walked nothing answers ok too",
			got.EdgesWalked, len(chain)-1)
		// One walk for a route that holds, which is the incremental claim
		// check.go's header makes, asserted rather than believed.
		assert.Should(t, got.Walks == 1, "walks = %d, want 1: a route that holds resumes the closure never", got.Walks)
		assert.Should(t, len(got.SemanticsSource) != 0, "the verdict carries no semantics_source, so it does not say what reading it rests on")
	})

	// TestTheZeroVerdictIsNotOk pins that the verdict type has no meaningful
	// zero.
	t.Run("the zero verdict is not ok", func(t *testing.T) {
		var zero Verdict
		assert.Must(t, zero != VerdictOK, "the zero verdict is ok, so a step nothing filled in reads as proved")
		if _, err := json.Marshal(StepCheck{Position: 0, Key: "hogger"}); err == nil {
			t.Fatal("a step with the zero verdict encoded without complaint")
		}
		// The control: a real verdict encodes, so this test cannot pass over
		// an encoder that refuses everything.
		raw, err := json.Marshal(StepCheck{Position: 0, Key: "hogger", Verdict: VerdictOK})
		assert.Must(t, err == nil, "a filled-in step did not encode: %v", err)
		assert.Must(t, strings.Contains(string(raw), `"verdict":"ok"`), "an ok step encoded as %s", raw)
		// Every verdict this package declares encodes, in both directions:
		// a word added to the enum and not to Verdicts would be refused by
		// its own encoder.
		for _, v := range Verdicts {
			if _, err := json.Marshal(v); err != nil {
				t.Errorf("%q is a declared verdict and does not encode: %v", v, err)
			}
		}
	})

	// TestADeletedStepEntityIsMissingEntityAndKeepsItsKey.
	t.Run("a deleted step entity is missing entity and keeps its key", func(t *testing.T) {
		g := a.gated(t)
		for _, key := range []string{"tutorial", "hogger"} {
			g.entity(t, "quest", key)
		}
		g.edge(t, "unlocks", "quest", "tutorial", "hogger")
		g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial", "hogger"),
		})
		// The control: before the deletion the route holds, so a check that
		// reported missing_entity for everything could not pass this test.
		if before := g.check(t, "levelling"); !before.Holds {
			t.Fatalf("the route did not hold before the deletion: %v", verdicts(before))
		}

		if err := g.meta.RemoveEntity(context.Background(), g.projectID, "quest", "hogger"); err != nil {
			t.Fatalf("remove the step's entity: %v", err)
		}
		got := g.check(t, "levelling")
		assert.Must(t, !got.Holds, "a route whose second step's entity was deleted still holds")
		assert.Must(t, len(got.Steps) == 2, "the route came back with %d steps, want 2: the step went with its entity",
			len(got.Steps))
		step := got.Steps[1]
		assert.Should(t, step.Verdict == VerdictMissingEntity, "verdict = %q, want missing_entity", step.Verdict)
		assert.Should(t, step.Position == 1, "position = %d, want 1", step.Position)
		assert.Should(t, step.EntityType == "quest" && step.Key == "hogger", "the tombstone reads %s/%s, want quest/hogger", step.EntityType, step.Key)
	})

	// TestAStepThatIsNotReachableYetIsUnmetPrerequisiteAndNamesItsBlockers.
	t.Run("a step that is not reachable yet is unmet prerequisite and names its blockers", func(t *testing.T) {
		g := gatedRouteGame(t, a)
		g.upsert(t, blockedRoute(steps("tutorial", "hogger", "deadmines")))

		got := g.check(t, "levelling")
		assert.Must(t, !got.Holds, "a route whose last step is gated by an unreachable quest still holds")
		if verdict := got.Steps[0].Verdict; verdict != VerdictOK {
			t.Errorf("step 0 = %q, want ok: the seed itself must hold", verdict)
		}
		if verdict := got.Steps[1].Verdict; verdict != VerdictOK {
			t.Errorf("step 1 = %q, want ok; a check that failed everything answers the same "+
				"thing on step 2 and this control is what tells them apart", verdict)
		}
		last := got.Steps[2]
		assert.Must(t, last.Verdict == VerdictUnmetPrerequisite, "step 2 = %q, want unmet_prerequisite", last.Verdict)
		assert.Should(t, len(last.Blockers) == 1 && last.Blockers[0].Key == "defias", "blockers = %v, want the one gate nobody reached, `defias`", last.Blockers)
		assert.Should(t, got.StepsOK == 2 && got.StepsBroken == 1, "counts = ok %d / broken %d, want 2 / 1", got.StepsOK, got.StepsBroken)
	})

	// TestABrokenStepIsStillASeedForTheStepsAfterIt.
	t.Run("a broken step is still a seed for the steps after it", func(t *testing.T) {
		g := gatedRouteGame(t, a)
		g.upsert(t, blockedRoute(steps("tutorial", "deadmines", "hogger")))

		got := g.check(t, "levelling")
		if want := []Verdict{VerdictOK, VerdictUnmetPrerequisite, VerdictOK}; !sameVerdicts(verdicts(got), want) {
			t.Fatalf("verdicts = %v, want %v: one broken step cascaded", verdicts(got), want)
		}
		// The closure was resumed exactly once, for the one step it did not
		// already reach. A step that came back ok costs no walk.
		assert.Should(t, got.Walks == 2, "walks = %d, want 2: one initial closure plus one resume for the one gap",
			got.Walks)
	})

	// TestAStepThatViolatesAnOrderingRelationIsOutOfOrder, with the control
	// that the same two steps swapped are entirely ok.
	t.Run("a step that violates an ordering relation is out of order", func(t *testing.T) {
		g := a.gated(t)
		g.declareRelationType(t, "follows", "", []string{"ordering"})
		for _, key := range []string{"prologue", "epilogue"} {
			g.entity(t, "quest", key)
		}
		// Source before target, the direction traits.go states for the word:
		// the prologue comes before the epilogue.
		g.edge(t, "follows", "quest", "prologue", "epilogue")

		g.upsert(t, RouteInput{
			Key: "backwards", Name: "Backwards", ExpectedVersion: version(0),
			Steps: steps("epilogue", "prologue"),
		})
		got := g.check(t, "backwards")
		assert.Must(t, !got.Holds, "a route that walks an ordering relation backwards still holds")
		step := got.Steps[1]
		assert.Must(t, step.Verdict == VerdictOutOfOrder, "step 1 = %q, want out_of_order", step.Verdict)
		assert.Should(t, len(step.MustPrecede) == 1 && step.MustPrecede[0].Key == "epilogue", "must_precede = %v, want the step it should have come before", step.MustPrecede)

		// The control: the same game, the same edge, the two steps swapped.
		g.upsert(t, RouteInput{
			Key: "forwards", Name: "Forwards", ExpectedVersion: version(0),
			Steps: steps("prologue", "epilogue"),
		})
		if forwards := g.check(t, "forwards"); !forwards.Holds {
			t.Fatalf("the same two steps in the right order do not hold: %v", verdicts(forwards))
		}
	})

	// TestTheVerdictsAreOrderedMostActionableFirst drives a step that
	// qualifies for out_of_order *and* for unmet_prerequisite, and asserts
	// the first of the two Verdicts declares.
	t.Run("the verdicts are ordered most actionable first", func(t *testing.T) {
		g := a.gated(t)
		g.declareRelationType(t, "follows", "", []string{"ordering"})
		for _, key := range []string{"prologue", "epilogue", "elsewhere"} {
			g.entity(t, "quest", key)
		}
		g.edge(t, "follows", "quest", "prologue", "epilogue")

		// Seeded from `elsewhere` with include_ungated off, so neither of
		// the two steps is reachable at all -- and the second is also out of
		// order.
		g.upsert(t, RouteInput{
			Key: "backwards", Name: "Backwards", ExpectedVersion: version(0),
			Params: RouteParams{
				SeedEntities:   []SeedRef{{EntityType: "quest", Key: "elsewhere"}},
				IncludeUngated: boolPtr(false),
			},
			Steps: steps("epilogue", "prologue"),
		})
		got := g.check(t, "backwards")
		// The control: step 0 qualifies for one verdict only, and gets it.
		// Without this, a check that answered out_of_order for everything
		// would pass the assertion below.
		if got.Steps[0].Verdict != VerdictUnmetPrerequisite {
			t.Errorf("step 0 = %q, want unmet_prerequisite: it is unreachable and in the right place",
				got.Steps[0].Verdict)
		}
		step := got.Steps[1]
		assert.Must(t, step.Verdict == VerdictOutOfOrder, "a step that is both unreachable and out of order = %q; Verdicts puts "+
			"out_of_order first, because it is the fault in the artefact the caller owns",
			step.Verdict)
		assert.Should(t, len(step.Blockers) == 0, "blockers = %v on an out_of_order step: the field belongs to "+
			"unmet_prerequisite alone", step.Blockers)
	})

	// TestARouteWhoseTypeWasRenamedIsStillOkAndSaysTheKeyMoved is the other
	// half of the rename rule, and it is the assertion that carries
	// internal/metamodel/rename.go's lesson one step along.
	t.Run("a route whose type was renamed is still ok and says the key moved", func(t *testing.T) {
		g := a.gated(t)
		for _, key := range []string{"tutorial", "hogger"} {
			g.entity(t, "quest", key)
		}
		g.edge(t, "unlocks", "quest", "tutorial", "hogger")
		g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial", "hogger"),
		})
		// The control, before the rename: the route holds and no step
		// reports a moved key, so the assertions below cannot be satisfied
		// by a check that always reports one.
		before := g.check(t, "levelling")
		if !before.Holds || before.Steps[0].TypeRenamed != nil {
			t.Fatalf("before the rename: holds=%v, renamed=%+v", before.Holds, before.Steps[0].TypeRenamed)
		}

		current, err := g.meta.EntityTypeByKey(context.Background(), g.projectID, "quest")
		assert.Must(t, err == nil, "read the entity type back: %v", err)
		expected := current.Version
		if _, err := g.meta.RenameEntityType(context.Background(), g.projectID, metamodel.RenameInput{
			From: "quest", To: "mission", ExpectedVersion: &expected,
		}); err != nil {
			t.Fatalf("rename the entity type: %v", err)
		}

		got := g.check(t, "levelling")
		assert.Must(t, got.Holds, "a renamed type broke a healthy route: %v", verdicts(got))
		for _, step := range got.Steps {
			assert.Should(t, step.Verdict == VerdictOK, "step %d = %q, want ok", step.Position, step.Verdict)
			assert.Should(t, step.EntityType == "quest", "step %d spells its type %q; a rename does not rewrite a stored step",
				step.Position, step.EntityType)
			if step.TypeRenamed == nil {
				t.Errorf("step %d says nothing about the key having moved", step.Position)
				continue
			}
			assert.Should(t, step.TypeRenamed.Was == "quest" && step.TypeRenamed.Now == "mission", "step %d reports %+v, want quest → mission", step.Position, step.TypeRenamed)
		}
	})

	// TestARouteChecksUnderItsOwnStoredParamsAndNotTheCallersDefaults.
	t.Run("a route checks under its own stored params and not the callers defaults", func(t *testing.T) {
		g := a.gated(t)
		for _, key := range []string{"north", "south", "keep"} {
			g.entity(t, "quest", key)
		}
		// `keep` has two gates. Under `any` one reached gate admits it;
		// under `all` both must be.
		g.edge(t, "unlocks", "quest", "north", "keep")
		g.edge(t, "unlocks", "quest", "south", "keep")

		// The control, stored under the default gating: the same game, the
		// same step, and it holds. Without it this test would pass over a
		// check that reported `keep` unreachable under every reading.
		g.upsert(t, RouteInput{
			Key: "lenient", Name: "Lenient", ExpectedVersion: version(0),
			Params: RouteParams{
				SeedEntities:   []SeedRef{{EntityType: "quest", Key: "north"}},
				IncludeUngated: boolPtr(false),
			},
			Steps: steps("keep"),
		})
		if lenient := g.check(t, "lenient"); !lenient.Holds {
			t.Fatalf("under `any`, one reached gate does not admit the step: %v", verdicts(lenient))
		}

		g.upsert(t, RouteInput{
			Key: "strict", Name: "Strict", ExpectedVersion: version(0),
			Params: RouteParams{
				Gate:           GatingAll,
				SeedEntities:   []SeedRef{{EntityType: "quest", Key: "north"}},
				IncludeUngated: boolPtr(false),
			},
			Steps: steps("keep"),
		})
		strict := g.check(t, "strict")
		assert.Must(t, !strict.Holds, "a route stored with gate `all` was checked under the caller's default `any`")
		assert.Should(t, strict.Gating == GatingAll, "the answer reports gating %q, want %q: a verdict arrives with the "+
			"reading it rests on", strict.Gating, GatingAll)
	})

	// TestCheckingARouteBumpsNothingAndLeavesTheDesignVersionAlone.
	t.Run("checking a route bumps nothing and leaves the design version alone", func(t *testing.T) {
		g := a.gated(t)
		g.entity(t, "quest", "tutorial")
		g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial"),
		})
		before := g.designVersion(t)

		got := g.check(t, "levelling")
		if after := g.designVersion(t); after != before {
			t.Fatalf("design_version moved from %d to %d over a check: a check that invalidates "+
				"its own verdict is a mechanism that can never report a fresh one", before, after)
		}
		assert.Must(t, got.Status == RouteChecked, "status = %q immediately after a check, want %q", got.Status, RouteChecked)
		// The control: a write to a table the triggers *do* watch moves it,
		// so this test cannot pass over a counter that never moves at all.
		g.entity(t, "quest", "hogger")
		if after := g.designVersion(t); after <= before {
			t.Fatalf("design_version is %d after an entity write, want above %d", after, before)
		}
		if reread := g.route_(t, "levelling"); reread.Status != RouteStale {
			t.Fatalf("status = %q after a metamodel write, want %q", reread.Status, RouteStale)
		}
	})

	// TestAWriteDuringACheckLeavesTheRouteStaleRatherThanFreshlyGreen.
	t.Run("a write during a check leaves the route stale rather than freshly green", func(t *testing.T) {
		g := a.gated(t)
		g.entity(t, "quest", "tutorial")
		g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial"),
		})
		// The control: an undisturbed check comes back fresh, so the
		// assertions below cannot be satisfied by a check that is always
		// stale.
		if quiet := g.check(t, "levelling"); quiet.Status != RouteChecked {
			t.Fatalf("an undisturbed check reports %q, want %q", quiet.Status, RouteChecked)
		}

		var landed bool
		g.analysis.beforeWalk = func() {
			if landed {
				return
			}
			landed = true
			g.entity(t, "quest", "hogger")
		}
		t.Cleanup(func() { g.analysis.beforeWalk = nil })

		got := g.check(t, "levelling")
		assert.Must(t, landed, "the mid-check write never ran, so this test asserted nothing")
		assert.Must(t, got.CheckedDesignVersion < got.DesignVersion, "the verdict was stored against design version %d and the game is at %d: "+
			"a write that landed mid-check was counted as a design this check had seen",
			got.CheckedDesignVersion, got.DesignVersion)
		assert.Should(t, got.Status == RouteStale, "status = %q, want %q", got.Status, RouteStale)
		if reread := g.route_(t, "levelling"); reread.Status != RouteStale {
			t.Errorf("the route reads back as %q, want %q", reread.Status, RouteStale)
		}
	})

	// TestCheckingAFiveHundredStepRouteStaysInsideOneBudget.
	t.Run("checking a five hundred step route stays inside one budget", func(t *testing.T) {
		g := a.gated(t)
		items := make([]metamodel.EntityInput, 0, MaxRouteSteps)
		stepList := make([]RouteStepInput, 0, MaxRouteSteps)
		for i := range MaxRouteSteps {
			key := fmt.Sprintf("quest-%03d", i)
			items = append(items, metamodel.EntityInput{TypeKey: "quest", Key: key, Name: key})
			stepList = append(stepList, RouteStepInput{EntityType: "quest", Key: key})
		}
		if _, err := g.meta.UpsertEntities(
			context.Background(), g.projectID, items, metamodel.BulkAtomic); err != nil {
			t.Fatalf("seed %d entities: %v", MaxRouteSteps, err)
		}
		g.upsert(t, RouteInput{
			Key: "the-long-haul", Name: "The long haul", ExpectedVersion: version(0),
			Steps: stepList,
		})

		started := time.Now()
		got := g.check(t, "the-long-haul")
		elapsed := time.Since(started)

		assert.Must(t, got.Holds && got.StepsChecked == MaxRouteSteps, "a %d-step route of ungated entities did not hold: checked %d, broken %d",
			MaxRouteSteps, got.StepsChecked, got.StepsBroken)
		assert.Should(t, got.Walks == 1, "walks = %d, want 1: %d steps that all hold must resume the closure never",
			got.Walks, MaxRouteSteps)
		assert.Should(t, elapsed <= HardStatementTimeout, "the check took %s, past the %s ceiling one analysis is allowed",
			elapsed, HardStatementTimeout)
	})

	// TestCheckingARouteInAGameThatDeclaredNothingRefusesRatherThanProvingIt.
	t.Run("checking a route in a game that declared nothing refuses rather than proving it", func(t *testing.T) {
		g := a.game(t)
		g.declareEntityType(t, "quest")
		g.declareRelationType(t, "mentions", "", nil)
		g.entity(t, "quest", "tutorial")
		g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial"),
		})

		_, err := g.analysis.CheckRoute(context.Background(), g.projectID, "levelling")
		assert.Must(t, errors.Is(err, ErrSemanticsUndeclared), "a check over an undeclared game answered %v, want semantics_undeclared", err)
		// The control: declaring one trait makes the same route checkable,
		// so this test cannot pass over a check that refuses everything.
		g.declareRelationType(t, "mentions", "", []string{"unlocks"})
		if got := g.check(t, "levelling"); !got.Holds {
			t.Fatalf("with a trait declared the same route does not hold: %v", verdicts(got))
		}
	})

	// TestCheckingARouteFromAnotherGameIsNotFound -- the isolation half,
	// over the call routes.check actually makes. RouteByKey is where the
	// filter lives and routes_test.go pins it there; this asserts the check
	// reaches it rather than resolving a route by key alone.
	t.Run("checking a route from another game is not found", func(t *testing.T) {
		mine := a.gated(t)
		mine.entity(t, "quest", "tutorial")
		mine.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial"),
		})
		theirs := mine.sibling(t)
		_, err := theirs.analysis.CheckRoute(
			context.Background(), theirs.projectID, "levelling")
		assert.Must(t, errors.Is(err, ErrNotFound), "checking another game's route key answered %v, want not_found", err)
		// The control: the owning game still checks it.
		if got := mine.check(t, "levelling"); !got.Holds {
			t.Fatalf("the owning game can no longer check its own route: %v", verdicts(got))
		}
	})

	// TestTheStoredVerdictIsTheOneTheCheckReturned.
	t.Run("the stored verdict is the one the check returned", func(t *testing.T) {
		g := gatedRouteGame(t, a)
		g.upsert(t, blockedRoute(steps("tutorial", "hogger", "deadmines")))
		got := g.check(t, "levelling")

		route := g.route_(t, "levelling")
		assert.Must(t, len(route.LastCheck) != 0, "the route has no stored verdict after a check")
		var stored RouteVerdict
		if err := json.Unmarshal(route.LastCheck, &stored); err != nil {
			t.Fatalf("decode the stored verdict: %v", err)
		}
		assert.Must(t, stored.Holds == got.Holds && stored.StepsChecked == got.StepsChecked && stored.StepsBroken == got.StepsBroken, "stored %+v does not match the answer %+v", stored, got.RouteVerdict)
		assert.Should(t, sameVerdicts(verdictsOf(stored.Steps), verdicts(got)), "stored verdicts %v, answered %v",
			verdictsOf(stored.Steps), verdicts(got))
		assert.Should(t, route.LastCheckedDesignVersion != nil && *route.LastCheckedDesignVersion == got.CheckedDesignVersion, "the row records design version %v and the answer says %d",
			route.LastCheckedDesignVersion, got.CheckedDesignVersion)
	})

	// TestCheckingARoutePublishesItsVerdictSummary, with the gating and the
	// payload shape events.go decided for it.
	t.Run("checking a route publishes its verdict summary", func(t *testing.T) {
		g := gatedRouteGame(t, a)
		hub := realtime.NewHub()
		svc := New(g.pool, hub)
		if _, err := svc.UpsertRoute(context.Background(), g.projectID,
			blockedRoute(steps("tutorial", "hogger", "deadmines"))); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		sub := hub.Subscribe(g.projectID, "viewer", false)
		defer hub.Unsubscribe(sub)

		got, err := svc.CheckRoute(context.Background(), g.projectID, "levelling")
		assert.Must(t, err == nil, "check: %v", err)
		event := requireEvent(t, sub)
		assert.Must(t, event.Kind == eventRouteChecked, "kind = %q, want %q", event.Kind, eventRouteChecked)
		assert.Should(t, event.MinRole == string(routeEventMinRole) && event.HumanOnly == routeEventHumanOnly, "gating = min_role %q / human_only %v, want %q / %v",
			event.MinRole, event.HumanOnly, routeEventMinRole, routeEventHumanOnly)
		payload, ok := event.Payload.(routeCheckedEvent)
		assert.Must(t, ok, "payload = %#v, want a routeCheckedEvent", event.Payload)
		assert.Should(t, payload.Key == "levelling" && payload.Holds == got.Holds && payload.StepsChecked == got.StepsChecked && payload.StepsBroken == got.StepsBroken, "payload = %+v, want the verdict summary of %+v", payload, got.RouteVerdict)
		// The shape assertion that matters: whatever a routeCheckedEvent
		// grows, it must not grow the steps. A client that rendered them
		// would eventually render the older of two checks.
		raw, err := json.Marshal(payload)
		assert.Must(t, err == nil, "encode the payload: %v", err)
		assert.Should(t, !strings.Contains(string(raw), `"steps"`), "the payload carries a step list: %s", raw)
	})
}

func gatedRouteGame(t *testing.T, a area) game {
	t.Helper()
	g := a.gated(t)
	for _, key := range []string{"tutorial", "hogger", "defias", "deadmines"} {
		g.entity(t, "quest", key)
	}
	g.edge(t, "unlocks", "quest", "tutorial", "hogger")
	g.edge(t, "unlocks", "quest", "defias", "deadmines")
	return g
}

func blockedRoute(steps []RouteStepInput) RouteInput {
	return RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Params: RouteParams{
			SeedEntities:   []SeedRef{{EntityType: "quest", Key: "tutorial"}},
			IncludeUngated: boolPtr(false),
		},
		Steps: steps,
	}
}
func (g game) route_(t *testing.T, key string) Route {
	t.Helper()
	got, err := g.analysis.RouteByKey(context.Background(), g.projectID, key)
	assert.Must(t, err == nil, "read route %q: %v", key, err)
	return got
}

// designVersion reads the game's counter the way routeStatus does.
func (g game) designVersion(t *testing.T) int64 {
	t.Helper()
	var current int64
	if err := g.pool.QueryRow(context.Background(),
		`SELECT design_version FROM projects WHERE id = $1`, g.projectID).Scan(&current); err != nil {
		t.Fatalf("read the design version: %v", err)
	}
	return current
}

func verdictsOf(steps []StepCheck) []Verdict {
	out := make([]Verdict, 0, len(steps))
	for _, step := range steps {
		out = append(out, step.Verdict)
	}
	return out
}

func sameVerdicts(got, want []Verdict) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
