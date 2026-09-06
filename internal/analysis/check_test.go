package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
)

// check runs routes.check with the error surfaced.
func (g game) check(t *testing.T, key string) RouteCheck {
	t.Helper()
	got, err := g.analysis.CheckRoute(context.Background(), g.projectID, key)
	if err != nil {
		t.Fatalf("check route %q: %v", key, err)
	}
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

// TestARouteThatHoldsReportsOkForEveryStepAndNamesItsSeedSet is **the
// negative half of this whole task**, and it is the one most likely to
// pass for the wrong reason.
//
// Five `ok`s is what a check that did nothing at all would answer if
// `ok` were the zero value, so this test asserts the counts beside the
// verdicts: how many steps were checked, how many edges were walked, and
// what the engine understood the start set to be. An empty walk cannot
// satisfy all four. TestTheZeroVerdictIsNotOk closes the other end.
func TestARouteThatHoldsReportsOkForEveryStepAndNamesItsSeedSet(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
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
	if !got.Holds {
		t.Fatalf("a route whose every step is reachable does not hold: %v", verdicts(got))
	}
	for _, step := range got.Steps {
		if step.Verdict != VerdictOK {
			t.Errorf("step %d (%s) = %q, want ok", step.Position, step.Key, step.Verdict)
		}
	}
	if got.StepsChecked != len(chain) || got.StepsOK != len(chain) || got.StepsBroken != 0 {
		t.Errorf("counts = checked %d / ok %d / broken %d, want %d / %d / 0",
			got.StepsChecked, got.StepsOK, got.StepsBroken, len(chain), len(chain))
	}
	// The seed set as the engine understood it: `tutorial` is the one
	// entity with no incoming gate, so include_ungated made it the start
	// and the total is one. A run that started nowhere would report zero
	// here and five `ok`s above.
	if !got.Seeds.IncludeUngated || got.Seeds.Total != 1 {
		t.Errorf("seeds = %+v, want include_ungated with exactly one entity", got.Seeds)
	}
	if got.EdgesWalked != len(chain)-1 {
		t.Errorf("edges_walked = %d, want %d: a check that walked nothing answers ok too",
			got.EdgesWalked, len(chain)-1)
	}
	// One walk for a route that holds, which is the incremental claim
	// check.go's header makes, asserted rather than believed.
	if got.Walks != 1 {
		t.Errorf("walks = %d, want 1: a route that holds resumes the closure never", got.Walks)
	}
	if len(got.SemanticsSource) == 0 {
		t.Error("the verdict carries no semantics_source, so it does not say what reading it rests on")
	}
}

// TestTheZeroVerdictIsNotOk pins that the verdict type has no meaningful
// zero.
//
// It is the cheapest guard in this task and it is not decoration:
// declaring "the zero value is invalid" buys nothing unless something
// refuses it, so this drives the encoder, which is the last place that
// can. A step left unfilled must not reach a caller as a proof.
func TestTheZeroVerdictIsNotOk(t *testing.T) {
	t.Parallel()
	var zero Verdict
	if zero == VerdictOK {
		t.Fatal("the zero verdict is ok, so a step nothing filled in reads as proved")
	}
	if _, err := json.Marshal(StepCheck{Position: 0, Key: "hogger"}); err == nil {
		t.Fatal("a step with the zero verdict encoded without complaint")
	}
	// The control: a real verdict encodes, so this test cannot pass over
	// an encoder that refuses everything.
	raw, err := json.Marshal(StepCheck{Position: 0, Key: "hogger", Verdict: VerdictOK})
	if err != nil {
		t.Fatalf("a filled-in step did not encode: %v", err)
	}
	if !strings.Contains(string(raw), `"verdict":"ok"`) {
		t.Fatalf("an ok step encoded as %s", raw)
	}
	// Every verdict this package declares encodes, in both directions:
	// a word added to the enum and not to Verdicts would be refused by
	// its own encoder.
	for _, v := range Verdicts {
		if _, err := json.Marshal(v); err != nil {
			t.Errorf("%q is a declared verdict and does not encode: %v", v, err)
		}
	}
}

// TestADeletedStepEntityIsMissingEntityAndKeepsItsKey.
//
// Three assertions, because three separate things have to hold: the
// verdict, the position, and the tombstone key the step kept when its
// entity went.
func TestADeletedStepEntityIsMissingEntityAndKeepsItsKey(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
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
	if got.Holds {
		t.Fatal("a route whose second step's entity was deleted still holds")
	}
	if len(got.Steps) != 2 {
		t.Fatalf("the route came back with %d steps, want 2: the step went with its entity",
			len(got.Steps))
	}
	step := got.Steps[1]
	if step.Verdict != VerdictMissingEntity {
		t.Errorf("verdict = %q, want missing_entity", step.Verdict)
	}
	if step.Position != 1 {
		t.Errorf("position = %d, want 1", step.Position)
	}
	if step.EntityType != "quest" || step.Key != "hogger" {
		t.Errorf("the tombstone reads %s/%s, want quest/hogger", step.EntityType, step.Key)
	}
}

// gatedRouteGame is the fixture the prerequisite verdicts need: a route
// whose last step is gated by something the seeds do not reach.
//
// include_ungated is off and the seed is named, deliberately: with the
// documented default on, every entity with no incoming gate is a start
// point and nothing in a small fixture is ever unreachable, so a test
// built on the defaults would assert a policy rather than a walk.
func gatedRouteGame(t *testing.T) game {
	t.Helper()
	g := gatedGame(t)
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

// TestAStepThatIsNotReachableYetIsUnmetPrerequisiteAndNamesItsBlockers.
//
// **The control is the step before it.** A check that failed everything
// would report the same verdict on the last step, so the assertion that
// step 1 is `ok` is what makes the assertion about step 2 mean anything.
func TestAStepThatIsNotReachableYetIsUnmetPrerequisiteAndNamesItsBlockers(t *testing.T) {
	t.Parallel()
	g := gatedRouteGame(t)
	g.upsert(t, blockedRoute(steps("tutorial", "hogger", "deadmines")))

	got := g.check(t, "levelling")
	if got.Holds {
		t.Fatal("a route whose last step is gated by an unreachable quest still holds")
	}
	if verdict := got.Steps[0].Verdict; verdict != VerdictOK {
		t.Errorf("step 0 = %q, want ok: the seed itself must hold", verdict)
	}
	if verdict := got.Steps[1].Verdict; verdict != VerdictOK {
		t.Errorf("step 1 = %q, want ok; a check that failed everything answers the same "+
			"thing on step 2 and this control is what tells them apart", verdict)
	}
	last := got.Steps[2]
	if last.Verdict != VerdictUnmetPrerequisite {
		t.Fatalf("step 2 = %q, want unmet_prerequisite", last.Verdict)
	}
	if len(last.Blockers) != 1 || last.Blockers[0].Key != "defias" {
		t.Errorf("blockers = %v, want the one gate nobody reached, `defias`", last.Blockers)
	}
	if got.StepsOK != 2 || got.StepsBroken != 1 {
		t.Errorf("counts = ok %d / broken %d, want 2 / 1", got.StepsOK, got.StepsBroken)
	}
}

// TestABrokenStepIsStillASeedForTheStepsAfterIt.
//
// This is the half of the incremental closure a naive implementation
// gets wrong in the other direction: one gap early in a long route must
// not cascade into "every step after it is broken too", because the
// question each step answers is "given the seeds and every step before
// me", not "given the seeds and every step before me that held".
func TestABrokenStepIsStillASeedForTheStepsAfterIt(t *testing.T) {
	t.Parallel()
	g := gatedRouteGame(t)
	g.upsert(t, blockedRoute(steps("tutorial", "deadmines", "hogger")))

	got := g.check(t, "levelling")
	if want := []Verdict{VerdictOK, VerdictUnmetPrerequisite, VerdictOK}; !sameVerdicts(verdicts(got), want) {
		t.Fatalf("verdicts = %v, want %v: one broken step cascaded", verdicts(got), want)
	}
	// The closure was resumed exactly once, for the one step it did not
	// already reach. A step that came back ok costs no walk.
	if got.Walks != 2 {
		t.Errorf("walks = %d, want 2: one initial closure plus one resume for the one gap",
			got.Walks)
	}
}

// TestAStepThatViolatesAnOrderingRelationIsOutOfOrder, with the control
// that the same two steps swapped are entirely ok.
//
// The control is what makes this about the *order* rather than about the
// existence of the edge: without it, a check that reported out_of_order
// whenever an ordering edge touched a route would pass.
func TestAStepThatViolatesAnOrderingRelationIsOutOfOrder(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
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
	if got.Holds {
		t.Fatal("a route that walks an ordering relation backwards still holds")
	}
	step := got.Steps[1]
	if step.Verdict != VerdictOutOfOrder {
		t.Fatalf("step 1 = %q, want out_of_order", step.Verdict)
	}
	if len(step.MustPrecede) != 1 || step.MustPrecede[0].Key != "epilogue" {
		t.Errorf("must_precede = %v, want the step it should have come before", step.MustPrecede)
	}

	// The control: the same game, the same edge, the two steps swapped.
	g.upsert(t, RouteInput{
		Key: "forwards", Name: "Forwards", ExpectedVersion: version(0),
		Steps: steps("prologue", "epilogue"),
	})
	if forwards := g.check(t, "forwards"); !forwards.Holds {
		t.Fatalf("the same two steps in the right order do not hold: %v", verdicts(forwards))
	}
}

// TestTheVerdictsAreOrderedMostActionableFirst drives a step that
// qualifies for out_of_order *and* for unmet_prerequisite, and asserts
// the first of the two Verdicts declares.
//
// It exists because an order that is only a comment is an order the next
// edit reverses. A fault in the route the caller just wrote outranks a
// claim about the game's content.
func TestTheVerdictsAreOrderedMostActionableFirst(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
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
	if step.Verdict != VerdictOutOfOrder {
		t.Fatalf("a step that is both unreachable and out of order = %q; Verdicts puts "+
			"out_of_order first, because it is the fault in the artefact the caller owns",
			step.Verdict)
	}
	if len(step.Blockers) != 0 {
		t.Errorf("blockers = %v on an out_of_order step: the field belongs to "+
			"unmet_prerequisite alone", step.Blockers)
	}
}

// TestARouteWhoseTypeWasRenamedIsStillOkAndSaysTheKeyMoved is the other
// half of the rename rule, and it is the assertion that carries
// internal/metamodel/rename.go's lesson one step along.
//
// A rename moves the catalogue row and nothing else: the stored step
// still spells the old key, resolution falls to the by-id path, and the
// route is **healthy**. Reporting it as broken would fail a game for a
// cosmetic change, which is the failure views' staleness design exists
// to avoid. So both halves are asserted here: the verdict is `ok`, and
// the diagnostic beside it names both spellings.
func TestARouteWhoseTypeWasRenamedIsStillOkAndSaysTheKeyMoved(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
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
	if err != nil {
		t.Fatalf("read the entity type back: %v", err)
	}
	expected := current.Version
	if _, err := g.meta.RenameEntityType(context.Background(), g.projectID, metamodel.RenameInput{
		From: "quest", To: "mission", ExpectedVersion: &expected,
	}); err != nil {
		t.Fatalf("rename the entity type: %v", err)
	}

	got := g.check(t, "levelling")
	if !got.Holds {
		t.Fatalf("a renamed type broke a healthy route: %v", verdicts(got))
	}
	for _, step := range got.Steps {
		if step.Verdict != VerdictOK {
			t.Errorf("step %d = %q, want ok", step.Position, step.Verdict)
		}
		if step.EntityType != "quest" {
			t.Errorf("step %d spells its type %q; a rename does not rewrite a stored step",
				step.Position, step.EntityType)
		}
		if step.TypeRenamed == nil {
			t.Errorf("step %d says nothing about the key having moved", step.Position)
			continue
		}
		if step.TypeRenamed.Was != "quest" || step.TypeRenamed.Now != "mission" {
			t.Errorf("step %d reports %+v, want quest → mission", step.Position, step.TypeRenamed)
		}
	}
}

// TestARouteChecksUnderItsOwnStoredParamsAndNotTheCallersDefaults.
//
// `params` existing and being ignored at check time is the
// write-only-field defect in its route-shaped form, and this is the test
// for it: a route stored with `gate: "all"` is checked from a caller
// that passed nothing at all, and must get the stricter answer.
func TestARouteChecksUnderItsOwnStoredParamsAndNotTheCallersDefaults(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
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
	if strict.Holds {
		t.Fatal("a route stored with gate `all` was checked under the caller's default `any`")
	}
	if strict.Gating != GatingAll {
		t.Errorf("the answer reports gating %q, want %q: a verdict arrives with the "+
			"reading it rests on", strict.Gating, GatingAll)
	}
}

// TestCheckingARouteBumpsNothingAndLeavesTheDesignVersionAlone.
//
// `routes` is deliberately not one of the four tables 0013's triggers
// watch, and it must not be: a check that marked every route in the game
// stale -- including the one it had just checked -- would be a mechanism
// that invalidates its own output.
//
// **Mutation:** add the design-version triggers to `routes`. This is the
// only test that would notice.
func TestCheckingARouteBumpsNothingAndLeavesTheDesignVersionAlone(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
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
	if got.Status != RouteChecked {
		t.Fatalf("status = %q immediately after a check, want %q", got.Status, RouteChecked)
	}
	// The control: a write to a table the triggers *do* watch moves it,
	// so this test cannot pass over a counter that never moves at all.
	g.entity(t, "quest", "hogger")
	if after := g.designVersion(t); after <= before {
		t.Fatalf("design_version is %d after an entity write, want above %d", after, before)
	}
	if reread := g.route_(t, "levelling"); reread.Status != RouteStale {
		t.Fatalf("status = %q after a metamodel write, want %q", reread.Status, RouteStale)
	}
}

// TestAWriteDuringACheckLeavesTheRouteStaleRatherThanFreshlyGreen.
//
// The design version a verdict records is read **before** the walk. Read
// after it, a metamodel write that committed while the walk was running
// would be stored as a design this verdict had seen, and the route would
// read `checked` against content it never looked at -- a stale green,
// which is the one failure the two clocks on this row exist to prevent.
//
// The write is landed through Service.beforeWalk, which is the only way
// to observe *when* a value was read: from outside, a check that read
// the counter late is indistinguishable from a correct check on a quiet
// game.
func TestAWriteDuringACheckLeavesTheRouteStaleRatherThanFreshlyGreen(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
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
	if !landed {
		t.Fatal("the mid-check write never ran, so this test asserted nothing")
	}
	if got.CheckedDesignVersion >= got.DesignVersion {
		t.Fatalf("the verdict was stored against design version %d and the game is at %d: "+
			"a write that landed mid-check was counted as a design this check had seen",
			got.CheckedDesignVersion, got.DesignVersion)
	}
	if got.Status != RouteStale {
		t.Errorf("status = %q, want %q", got.Status, RouteStale)
	}
	if reread := g.route_(t, "levelling"); reread.Status != RouteStale {
		t.Errorf("the route reads back as %q, want %q", reread.Status, RouteStale)
	}
}

// TestCheckingAFiveHundredStepRouteStaysInsideOneBudget.
//
// MaxRouteSteps is 500, so this is the longest route the product admits,
// and the claim check.go makes about it is that a route that holds costs
// exactly one walk however long it is. Measuring instead of arguing is
// the third thing the views sub-project named among what actually caught
// defects, so the wall time is recorded here rather than asserted:
//
//	measured 2026-09-06, one throwaway database on a laptop:
//	~0.4s for the whole test including seeding 500 entities, of which
//	the check itself was under 30ms.
//
// What is asserted is what a slower machine cannot change: it completes
// inside the statement budget, and it does it in one walk.
func TestCheckingAFiveHundredStepRouteStaysInsideOneBudget(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
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

	if !got.Holds || got.StepsChecked != MaxRouteSteps {
		t.Fatalf("a %d-step route of ungated entities did not hold: checked %d, broken %d",
			MaxRouteSteps, got.StepsChecked, got.StepsBroken)
	}
	if got.Walks != 1 {
		t.Errorf("walks = %d, want 1: %d steps that all hold must resume the closure never",
			got.Walks, MaxRouteSteps)
	}
	if elapsed > HardStatementTimeout {
		t.Errorf("the check took %s, past the %s ceiling one analysis is allowed",
			elapsed, HardStatementTimeout)
	}
}

// TestCheckingARouteInAGameThatDeclaredNothingRefusesRatherThanProvingIt.
//
// routes.check is one of the three analyses that walk edges, and all
// three refuse an undeclared game: an engine with no edge it is allowed
// to follow must not answer that a progression holds. The orphan
// aggregate is the deliberate exception and says so in its own file.
func TestCheckingARouteInAGameThatDeclaredNothingRefusesRatherThanProvingIt(t *testing.T) {
	t.Parallel()
	g := newGame(t)
	g.declareEntityType(t, "quest")
	g.declareRelationType(t, "mentions", "", nil)
	g.entity(t, "quest", "tutorial")
	g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial"),
	})

	_, err := g.analysis.CheckRoute(context.Background(), g.projectID, "levelling")
	if !errors.Is(err, ErrSemanticsUndeclared) {
		t.Fatalf("a check over an undeclared game answered %v, want semantics_undeclared", err)
	}
	// The control: declaring one trait makes the same route checkable,
	// so this test cannot pass over a check that refuses everything.
	g.declareRelationType(t, "mentions", "", []string{"unlocks"})
	if got := g.check(t, "levelling"); !got.Holds {
		t.Fatalf("with a trait declared the same route does not hold: %v", verdicts(got))
	}
}

// TestCheckingARouteFromAnotherGameIsNotFound -- the isolation half,
// over the call routes.check actually makes. RouteByKey is where the
// filter lives and routes_test.go pins it there; this asserts the check
// reaches it rather than resolving a route by key alone.
func TestCheckingARouteFromAnotherGameIsNotFound(t *testing.T) {
	t.Parallel()
	mine := gatedGame(t)
	mine.entity(t, "quest", "tutorial")
	mine.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial"),
	})
	theirs := mine.sibling(t)
	_, err := theirs.analysis.CheckRoute(
		context.Background(), theirs.projectID, "levelling")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("checking another game's route key answered %v, want not_found", err)
	}
	// The control: the owning game still checks it.
	if got := mine.check(t, "levelling"); !got.Holds {
		t.Fatalf("the owning game can no longer check its own route: %v", verdicts(got))
	}
}

// TestTheStoredVerdictIsTheOneTheCheckReturned.
//
// last_check is the only thing this sub-project caches, and a cache
// nothing reads back is a mechanism nothing reads. This drives the whole
// round trip: check, read the route, decode the blob, and find the same
// per-step answer.
func TestTheStoredVerdictIsTheOneTheCheckReturned(t *testing.T) {
	t.Parallel()
	g := gatedRouteGame(t)
	g.upsert(t, blockedRoute(steps("tutorial", "hogger", "deadmines")))
	got := g.check(t, "levelling")

	route := g.route_(t, "levelling")
	if len(route.LastCheck) == 0 {
		t.Fatal("the route has no stored verdict after a check")
	}
	var stored RouteVerdict
	if err := json.Unmarshal(route.LastCheck, &stored); err != nil {
		t.Fatalf("decode the stored verdict: %v", err)
	}
	if stored.Holds != got.Holds || stored.StepsChecked != got.StepsChecked ||
		stored.StepsBroken != got.StepsBroken {
		t.Fatalf("stored %+v does not match the answer %+v", stored, got.RouteVerdict)
	}
	if !sameVerdicts(verdictsOf(stored.Steps), verdicts(got)) {
		t.Errorf("stored verdicts %v, answered %v",
			verdictsOf(stored.Steps), verdicts(got))
	}
	if route.LastCheckedDesignVersion == nil ||
		*route.LastCheckedDesignVersion != got.CheckedDesignVersion {
		t.Errorf("the row records design version %v and the answer says %d",
			route.LastCheckedDesignVersion, got.CheckedDesignVersion)
	}
}

// route_ reads one route back through the product's own reader.
func (g game) route_(t *testing.T, key string) Route {
	t.Helper()
	got, err := g.analysis.RouteByKey(context.Background(), g.projectID, key)
	if err != nil {
		t.Fatalf("read route %q: %v", key, err)
	}
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

// TestCheckingARoutePublishesItsVerdictSummary, with the gating and the
// payload shape events.go decided for it.
//
// **The payload is the summary and never the per-step list**, which is
// asserted here rather than left to the comment that decided it:
// publication order is not commit order, so a client that rendered the
// steps out of an event would eventually render the older of two checks.
// The subscription is a viewer's and not an owner's, because the gating
// claim is that a viewer hears this and a subscription at owner would
// pass whatever MinRole said.
func TestCheckingARoutePublishesItsVerdictSummary(t *testing.T) {
	t.Parallel()
	g := gatedRouteGame(t)
	hub := realtime.NewHub()
	svc := New(g.pool, hub)
	if _, err := svc.UpsertRoute(context.Background(), g.projectID,
		blockedRoute(steps("tutorial", "hogger", "deadmines"))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	sub := hub.Subscribe(g.projectID, "viewer", false)
	defer hub.Unsubscribe(sub)

	got, err := svc.CheckRoute(context.Background(), g.projectID, "levelling")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	event := requireEvent(t, sub)
	if event.Kind != eventRouteChecked {
		t.Fatalf("kind = %q, want %q", event.Kind, eventRouteChecked)
	}
	if event.MinRole != string(routeEventMinRole) || event.HumanOnly != routeEventHumanOnly {
		t.Errorf("gating = min_role %q / human_only %v, want %q / %v",
			event.MinRole, event.HumanOnly, routeEventMinRole, routeEventHumanOnly)
	}
	payload, ok := event.Payload.(routeCheckedEvent)
	if !ok {
		t.Fatalf("payload = %#v, want a routeCheckedEvent", event.Payload)
	}
	if payload.Key != "levelling" || payload.Holds != got.Holds ||
		payload.StepsChecked != got.StepsChecked || payload.StepsBroken != got.StepsBroken {
		t.Errorf("payload = %+v, want the verdict summary of %+v", payload, got.RouteVerdict)
	}
	// The shape assertion that matters: whatever a routeCheckedEvent
	// grows, it must not grow the steps. A client that rendered them
	// would eventually render the older of two checks.
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode the payload: %v", err)
	}
	if strings.Contains(string(raw), `"steps"`) {
		t.Errorf("the payload carries a step list: %s", raw)
	}
}
