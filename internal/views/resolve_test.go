package views

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
)

func TestAQueryResolvesEveryKeyToAnID(t *testing.T) {
	g, _ := newGame(t)
	q := mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"available_to","direction":"out","to_type":"class","as":"c"}]}`)

	r, err := g.views.Resolve(context.Background(), g.projectID, q)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.Sets[0].EntityTypeID == nil {
		t.Fatal("the seed selector's type must resolve to an id")
	}
	if len(r.Steps[0].RelationTypeIDs) != 1 {
		t.Fatalf("the step's via must resolve to one relation type id, got %v",
			r.Steps[0].RelationTypeIDs)
	}
	if len(r.Steps[0].ToTypeIDs) != 1 {
		t.Fatalf("the step's to_type must resolve to one entity type id, got %v",
			r.Steps[0].ToTypeIDs)
	}
}

// TestAKeyFromAnotherGameDoesNotResolve is the isolation test, with its
// positive control in the same test so an empty answer cannot pass it.
func TestAKeyFromAnotherGameDoesNotResolve(t *testing.T) {
	g, other := newGame(t)
	ctx := context.Background()
	// Give the second game a type the first does not have.
	if _, err := other.meta.UpsertEntityType(ctx, other.projectID, metamodel.EntityTypeInput{
		Key: "vehicle", Label: "Vehicle", LabelPlural: "Vehicles",
	}); err != nil {
		t.Fatalf("seed the other game: %v", err)
	}
	// Positive control: it resolves in its own game.
	if _, err := other.views.Resolve(ctx, other.projectID,
		mustParse(t, `{"v":1,"from":[{"type":"vehicle"}]}`)); err != nil {
		t.Fatalf("vehicle must resolve in the game that declares it: %v", err)
	}
	_, err := g.views.Resolve(ctx, g.projectID,
		mustParse(t, `{"v":1,"from":[{"type":"vehicle"}]}`))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("a type from another game must not resolve, got %v", err)
	}
	if !strings.Contains(err.Error(), `no entity type "vehicle" in this game`) {
		t.Fatalf("must name the key back, got %v", err)
	}
}

// TestARelationTypeFromAnotherGameDoesNotResolve is the same isolation
// assertion one step along: relation types are loaded by a second
// statement, and a project filter missing from that one alone would let
// every test above pass.
func TestARelationTypeFromAnotherGameDoesNotResolve(t *testing.T) {
	g, other := newGame(t)
	ctx := context.Background()
	if _, err := other.meta.UpsertRelationType(ctx, other.projectID,
		metamodel.RelationTypeInput{Key: "drives", Label: "Drives"}); err != nil {
		t.Fatalf("seed the other game: %v", err)
	}
	doc := `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"drives","as":"d"}]}`
	// Positive control: the second game has quests too, so the only thing
	// that differs between the two calls is which game declares "drives".
	if _, err := other.views.Resolve(ctx, other.projectID, mustParse(t, doc)); err != nil {
		t.Fatalf("drives must resolve in the game that declares it: %v", err)
	}
	_, err := g.views.Resolve(ctx, g.projectID, mustParse(t, doc))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("a relation type from another game must not resolve, got %v", err)
	}
	if !strings.Contains(err.Error(), `no relation type "drives" in this game`) {
		t.Fatalf("must name the key back, got %v", err)
	}
}

func TestAnOperatorTheFieldTypeCannotAnswerIsRefusedAtSaveTime(t *testing.T) {
	g, _ := newGame(t)
	_, err := g.views.Resolve(context.Background(), g.projectID,
		mustParse(t, `{"v":1,"from":[{"type":"quest","where":{"field":"rank","op":"gt","value":"rare"}}]}`))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("gt on an enum must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(),
		`the field "rank" is declared enum, which answers eq, neq, in, exists`) {
		t.Fatalf("must name the type and the operators it does answer, got %v", err)
	}
	if !strings.Contains(err.Error(), "/from/0/where/op") {
		t.Fatalf("must point at the operator, got %v", err)
	}
}

// TestAnEnumValueOutsideItsOptionsIsRefusedRatherThanEmpty is the failure
// mode the spec calls the most expensive one in a query language whose
// author is not in the room.
func TestAnEnumValueOutsideItsOptionsIsRefusedRatherThanEmpty(t *testing.T) {
	g, _ := newGame(t)
	_, err := g.views.Resolve(context.Background(), g.projectID,
		mustParse(t, `{"v":1,"from":[{"type":"quest","where":{"field":"rank","op":"eq","value":"legendary"}}]}`))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("an option outside the declaration must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), `"legendary" is not one of [common rare epic]`) {
		t.Fatalf("must list the options, got %v", err)
	}
}

func TestAParameterIsTypeCheckedAgainstTheOperatorItFeeds(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	q := mustParse(t, `{"v":1,"params":[{"key":"floor","type":"number","default":20}],
		"from":[{"type":"quest","where":{"field":"min_level","op":"gte","value":{"param":"floor"}}}]}`)
	r, err := g.views.Resolve(ctx, g.projectID, q)
	if err != nil {
		t.Fatalf("a number param feeding gte on a number field must resolve: %v", err)
	}
	if got := r.Params["floor"]; got != float64(20) {
		t.Fatalf("the default must be coerced to the declared type, got %#v", got)
	}
	if ref, ok := r.Sets[0].Where.Leaf.Value.(ParamRef); !ok || ref.Key != "floor" {
		t.Fatalf("the leaf must carry the parameter reference, got %#v", r.Sets[0].Where.Leaf.Value)
	}

	bad := mustParse(t, `{"v":1,"params":[{"key":"who","type":"text","default":"mage"}],
		"from":[{"type":"quest","where":{"field":"min_level","op":"gte","value":{"param":"who"}}}]}`)
	_, err = g.views.Resolve(ctx, g.projectID, bad)
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("a text param feeding a number field must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), `the parameter "who" is declared text and the field `+
		`"min_level" is declared number`) {
		t.Fatalf("must name both sides, got %v", err)
	}

	unbound := mustParse(t, `{"v":1,"from":[{"type":"quest",
		"where":{"field":"min_level","op":"gte","value":{"param":"nowhere"}}}]}`)
	_, err = g.views.Resolve(ctx, g.projectID, unbound)
	if !strings.Contains(err.Error(), `no parameter named "nowhere" is declared`) {
		t.Fatalf("an unbound param must be named back, got %v", err)
	}
}

// TestAParameterDefaultIsCoercedToItsDeclaredType pins the other half of
// the parameter pass: a default is a value like any other, and one that
// does not fit its declaration is refused at its own pointer rather than
// stored and coerced by whatever reads it later.
func TestAParameterDefaultIsCoercedToItsDeclaredType(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	// Positive control, in the same test: a default that fits arrives
	// coerced, so an unconditional refusal cannot pass this.
	ok := mustParse(t, `{"v":1,"params":[{"key":"who","type":"text","default":"mage"}],
		"from":[{"type":"quest"}]}`)
	r, err := g.views.Resolve(ctx, g.projectID, ok)
	if err != nil {
		t.Fatalf("a text default on a text parameter must resolve: %v", err)
	}
	if r.Params["who"] != "mage" {
		t.Fatalf("the default must be carried through, got %#v", r.Params["who"])
	}

	bad := mustParse(t, `{"v":1,"params":[{"key":"floor","type":"number","default":"twenty"}],
		"from":[{"type":"quest"}]}`)
	_, err = g.views.Resolve(ctx, g.projectID, bad)
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("a text default on a number parameter must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "/params/0/default") ||
		!strings.Contains(err.Error(), "expected number, got string") {
		t.Fatalf("must report the metamodel's own wording at the default's pointer, got %v", err)
	}
}

// TestResolutionListsEveryTypeReferenceWithItsPointer is what Task 11
// writes into view_refs and what Task 12's staleness report reads back.
func TestResolutionListsEveryTypeReferenceWithItsPointer(t *testing.T) {
	g, _ := newGame(t)
	q := mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":["available_to","requires"],"to_type":"class","as":"c"}],
		"project":{"color_by":{"related":{"via":"takes_place_in","type":"zone","attr":"@name"}}}}`)
	r, err := g.views.Resolve(context.Background(), g.projectID, q)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := map[string]string{
		"/from/0/type":                   "quest",
		"/traverse/0/via/0":              "available_to",
		"/traverse/0/via/1":              "requires",
		"/traverse/0/to_type/0":          "class",
		"/project/color_by/related/via":  "takes_place_in",
		"/project/color_by/related/type": "zone",
	}
	got := map[string]string{}
	for _, ref := range r.Refs {
		got[ref.Pointer] = ref.Key
		if ref.ID == nil {
			t.Errorf("%s resolved to no id, and every key here exists", ref.Pointer)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("listed %d references, want %d: %v", len(got), len(want), got)
	}
	for ptr, key := range want {
		if got[ptr] != key {
			t.Errorf("%s: got %q want %q", ptr, got[ptr], key)
		}
	}
}

// TestAReferenceThatDoesNotResolveIsStillListedWithItsKey is the half of
// the dependency list Task 12 depends on and the test above cannot see:
// a ref survives with its key and its pointer and a nil id, which is what
// lets a stale view say which part of itself broke rather than only that
// something did.
func TestAReferenceThatDoesNotResolveIsStillListedWithItsKey(t *testing.T) {
	g, _ := newGame(t)
	q := mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"gone_missing","as":"m"}]}`)
	r, err := ResolveAgainst(mustCatalogue(t, g), q)
	if r != nil {
		t.Fatalf("an unresolvable key must refuse the query, got %#v", r)
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("want a *QueryError, got %v", err)
	}
	// The refusal is what a caller sees; the listing is what Task 11
	// stores. Resolve refuses, so the listing is read from a second pass
	// over the same catalogue, which is exactly what stale.go will do.
	refs := ReferencesOf(mustCatalogue(t, g), q)
	var found *TypeRef
	for i := range refs {
		if refs[i].Pointer == "/traverse/0/via/0" {
			found = &refs[i]
		}
	}
	if found == nil {
		t.Fatalf("the unresolvable reference must still be listed: %#v", refs)
	}
	if found.Key != "gone_missing" || found.ID != nil || found.Kind != KindRelationType {
		t.Fatalf("want the key kept and the id nil, got %#v", *found)
	}
	// Positive control in the same test: a reference that does resolve is
	// listed with an id, so a listing that nils everything cannot pass.
	for _, ref := range refs {
		if ref.Pointer == "/from/0/type" && ref.ID == nil {
			t.Fatalf("quest resolves and must carry its id: %#v", ref)
		}
	}
}

// TestAStepWithoutAToTypeAdmitsOnlyBuiltins pins the claim resolve.go's
// own comment makes about an open step: it reaches entities of any type,
// so no declared field key can be compared there, and the refusal says
// that rather than blaming a type the caller never named.
func TestAStepWithoutAToTypeAdmitsOnlyBuiltins(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	// Positive control: a built-in is fine in exactly the same position.
	if _, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"available_to","as":"c",
		               "where":{"field":"@name","op":"eq","value":"Mage"}}]}`)); err != nil {
		t.Fatalf("a built-in must be comparable on an open step: %v", err)
	}
	_, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"available_to","as":"c",
		               "where":{"field":"min_level","op":"gte","value":10}}]}`))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("a declared field on an open step must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "reaches entities of any type") {
		t.Fatalf("must say why rather than blaming a type, got %v", err)
	}
}

// TestAFieldMustBeDeclaredTheSameWayOnEveryTypeAStepReaches is the
// multi-type case the single-schema shortcut gets wrong in both
// directions: a field declared on one of two reached types would compile
// to a comparison that silently matches nothing on the other, and a key
// declared with two different types would be coerced against whichever
// type happened to be listed first.
func TestAFieldMustBeDeclaredTheSameWayOnEveryTypeAStepReaches(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	// Positive control: min_level is declared on quest alone, and a step
	// reaching only quest compares it happily.
	if _, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"requires","to_type":"quest","as":"p",
		               "where":{"field":"min_level","op":"gte","value":10}}]}`)); err != nil {
		t.Fatalf("min_level must be comparable where it is declared: %v", err)
	}
	_, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":["requires","takes_place_in"],
		               "to_type":["quest","zone"],"as":"p",
		               "where":{"field":"min_level","op":"gte","value":10}}]}`))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("a field declared on one of two reached types must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), `"zone" does not declare it`) {
		t.Fatalf("must name the type that lacks it, got %v", err)
	}
}

// TestADeclaredRangeDoesNotRefuseAComparisonOutsideIt is the one place
// this pass deliberately does not take the metamodel's whole judgement.
// A stored value may sit outside its field's declared range — the
// metamodel flags such an entity invalid and leaves the value in place —
// so a bound is a fact about writes and never about what a query may ask.
func TestADeclaredRangeDoesNotRefuseAComparisonOutsideIt(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	if _, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","where":{"field":"difficulty","op":"gte","value":0}}]}`)); err != nil {
		t.Fatalf("0 is below difficulty's declared minimum of 1 and must still be askable: %v", err)
	}
	// Positive control in the same test: the type itself is still judged,
	// so this is not a pass that stopped checking.
	_, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","where":{"field":"difficulty","op":"gte","value":"hard"}}]}`))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("text against a number field must still be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "expected number, got string") {
		t.Fatalf("must keep the metamodel's own wording, got %v", err)
	}
}

// TestEveryProblemInOneQueryIsReportedInOnePass is the property the
// refusal shape exists for: an agent writing against an unfamiliar game
// gets every mistake at once instead of one per round trip.
func TestEveryProblemInOneQueryIsReportedInOnePass(t *testing.T) {
	g, _ := newGame(t)
	_, err := g.views.Resolve(context.Background(), g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"vehicle","as":"v"},
		                {"type":"quest","as":"q","where":{"field":"rank","op":"gt","value":"rare"}}],
		  "traverse":[{"from":"q","via":"drives","as":"d"}]}`))
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("want a *QueryError, got %v", err)
	}
	if len(qe.Fields) != 3 {
		t.Fatalf("want three problems in one pass, got %d: %v", len(qe.Fields), qe.Fields)
	}
	wantPaths := []string{"/from/0/type", "/from/1/where/op", "/traverse/0/via/0"}
	for i, path := range wantPaths {
		if qe.Fields[i].Path != path {
			t.Errorf("problem %d: got %q want %q", i, qe.Fields[i].Path, path)
		}
	}
}

// TestAStepDeeperThanTheQueryAllowsIsRefused pins the one bound this pass
// applies that ParseQuery cannot: a step's own depth is only judgeable
// against the query's max_depth, which may itself be an override.
func TestAStepDeeperThanTheQueryAllowsIsRefused(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	// Positive control: raising limits.max_depth makes the same step legal.
	if _, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"limits":{"max_depth":6},"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"requires","depth":6,"as":"p"}]}`)); err != nil {
		t.Fatalf("depth 6 under max_depth 6 must resolve: %v", err)
	}
	_, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"requires","depth":6,"as":"p"}]}`))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("depth 6 under the default max_depth of 4 must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "asks for depth 6 and this query's max_depth is 4") {
		t.Fatalf("must name both numbers, got %v", err)
	}
	if !strings.Contains(err.Error(), "/traverse/0/depth") {
		t.Fatalf("must point at the step's depth, got %v", err)
	}
}

// TestResolutionTakesTheQuerysLimitsAndDefaultsTheRest pins the constants
// Task 3 shipped unread: the compiler reads these three numbers and
// nothing else decides them.
func TestResolutionTakesTheQuerysLimitsAndDefaultsTheRest(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	r, err := g.views.Resolve(ctx, g.projectID, mustParse(t, `{"v":1,"from":[{"type":"quest"}]}`))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.Limits != (ResolvedLimits{DefaultMaxDepth, DefaultMaxNodes, DefaultMaxEdges}) {
		t.Fatalf("an unset limits block must take every default, got %#v", r.Limits)
	}
	r, err = g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"limits":{"max_nodes":7},"from":[{"type":"quest"}]}`))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.Limits != (ResolvedLimits{DefaultMaxDepth, 7, DefaultMaxEdges}) {
		t.Fatalf("one override must not disturb the other two, got %#v", r.Limits)
	}
}

// mustCatalogue reads the catalogue this package's non-refusal helpers
// take directly.
func mustCatalogue(t *testing.T, g *game) *Catalogue {
	t.Helper()
	cat, err := g.views.LoadCatalogue(context.Background(), g.projectID)
	if err != nil {
		t.Fatalf("load catalogue: %v", err)
	}
	return cat
}
