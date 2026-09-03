package views

import (
	"context"
	"errors"
	"fmt"
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
	// The query kills one reference of *each* kind: the two miss branches
	// are separate lines of code, and a test naming only a relation type
	// leaves the entity-type one held by nothing — while "which entity
	// type died" is exactly what Task 12 reports.
	q := mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"gone_missing","to_type":"also_gone","as":"m"}]}`)
	r, err := ResolveAgainst(g.projectID, mustCatalogue(t, g), q)
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
	refs, err := ReferencesOf(g.projectID, mustCatalogue(t, g), q)
	if err != nil {
		t.Fatalf("the listing pass must not refuse its own game: %v", err)
	}
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
	found = nil
	for i := range refs {
		if refs[i].Pointer == "/traverse/0/to_type/0" {
			found = &refs[i]
		}
	}
	if found == nil {
		t.Fatalf("the unresolvable entity type must be listed too: %#v", refs)
	}
	if found.Key != "also_gone" || found.ID != nil || found.Kind != KindEntityType {
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

// TestACatalogueFromAnotherGameIsRefused is the isolation assertion the
// two above cannot make: they pair a catalogue with the game it was read
// for, and every message they check comes from a *lookup* missing a key.
// A catalogue with no identity resolves a query meant for another game
// happily, handing back type ids belonging to the game it was read for —
// and Task 12 would then compare a saved view against the wrong game's
// vocabulary and answer "not stale".
func TestACatalogueFromAnotherGameIsRefused(t *testing.T) {
	g, other := newGame(t)
	// Both games declare "quest", so the query resolves against either
	// catalogue and only their identities differ.
	q := mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}]}`)
	cat := mustCatalogue(t, g)

	// Positive control in the same test: paired with its own game it
	// resolves, so an unconditional refusal cannot pass this.
	if _, err := ResolveAgainst(g.projectID, cat, q); err != nil {
		t.Fatalf("a catalogue must resolve against the game it was read for: %v", err)
	}
	r, err := ResolveAgainst(other.projectID, cat, q)
	if !errors.Is(err, ErrWrongGame) {
		t.Fatalf("a catalogue from another game must be refused, got %#v and %v", r, err)
	}
	if r != nil {
		t.Fatalf("a refused pair must resolve to nothing, got %#v", r)
	}
	if !strings.Contains(err.Error(), g.projectID.String()) ||
		!strings.Contains(err.Error(), other.projectID.String()) {
		t.Fatalf("must name both games, got %v", err)
	}
	// The dependency list is the other door into the same catalogue, and
	// it is the one Task 12 reads.
	if _, err := ReferencesOf(other.projectID, cat, q); !errors.Is(err, ErrWrongGame) {
		t.Fatalf("the listing pass must refuse the same pair, got %v", err)
	}
	if refs, err := ReferencesOf(g.projectID, cat, q); err != nil || len(refs) != 1 {
		t.Fatalf("its own game must still be listed, got %v and %#v", err, refs)
	}
	if cat.ProjectID != g.projectID {
		t.Fatalf("a catalogue must carry the game it was read for, got %v", cat.ProjectID)
	}
}

// TestAFieldDeclaredTwoWaysNamesEachTypeWithItsOwnDeclaration pins the
// half of the multi-type rule the not-declared-everywhere test cannot
// see: which type is named beside which declaration. Pairing the first
// declaring type's name with the second type's declaration is a message
// that sends a designer to edit the type that is not the one being
// described, and reversing the destination list reverses the lie
// symmetrically — so both orders are asserted here.
func TestAFieldDeclaredTwoWaysNamesEachTypeWithItsOwnDeclaration(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	doc := func(first, second string) string {
		return `{"v":1,"from":[{"type":"quest","as":"q"}],
			"traverse":[{"from":"q","via":"takes_place_in","to_type":["` + first + `","` + second +
			`"],"as":"p","where":{"field":"min_level","op":"exists","value":true}}]}`
	}
	// quest declares min_level number, region declares it enum.
	_, err := g.views.Resolve(ctx, g.projectID, mustParse(t, doc("quest", "region")))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("a field declared two ways must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(),
		`the field "min_level" is declared number on "quest" and enum on "region"`) {
		t.Fatalf("each type must be named beside its own declaration, got %v", err)
	}
	_, err = g.views.Resolve(ctx, g.projectID, mustParse(t, doc("region", "quest")))
	if !strings.Contains(err.Error(),
		`the field "min_level" is declared enum on "region" and number on "quest"`) {
		t.Fatalf("reversing the list must reverse the message, got %v", err)
	}

	// The enum-options branch says the same thing about its own two
	// operands: quest's options belong to quest and faction's to faction.
	rank := func(first, second string) string {
		return `{"v":1,"from":[{"type":"quest","as":"q"}],
			"traverse":[{"from":"q","via":"takes_place_in","to_type":["` + first + `","` + second +
			`"],"as":"p","where":{"field":"rank","op":"eq","value":"rare"}}]}`
	}
	_, err = g.views.Resolve(ctx, g.projectID, mustParse(t, rank("quest", "faction")))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("two different option sets must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), `on "quest" ([common rare epic]) and on "faction" `+
		`([common rare])`) {
		t.Fatalf("each type must carry its own options, got %v", err)
	}
	_, err = g.views.Resolve(ctx, g.projectID, mustParse(t, rank("faction", "quest")))
	if !strings.Contains(err.Error(), `on "faction" ([common rare]) and on "quest" `+
		`([common rare epic])`) {
		t.Fatalf("reversing the list must reverse the message, got %v", err)
	}
}

// TestEnumOptionsAreComparedAsASetNotASequence is the same defect class
// as the declared range: a rule that refuses a query with no ambiguity in
// it. Two types declaring the same options in a different order admit
// exactly the same values, and the refusal's own justification — a value
// legal for one is not legal for the other — is false in precisely that
// case.
func TestEnumOptionsAreComparedAsASetNotASequence(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	// quest declares rank [common rare epic]; region declares the same
	// three in another order.
	if _, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"takes_place_in","to_type":["quest","region"],"as":"p",
		               "where":{"field":"rank","op":"eq","value":"rare"}}]}`)); err != nil {
		t.Fatalf("the same options in another order admit the same values: %v", err)
	}
	// Positive control in the same test: a genuinely different set is
	// still refused, so this is not a check that stopped checking.
	_, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"takes_place_in","to_type":["quest","faction"],"as":"p",
		               "where":{"field":"rank","op":"eq","value":"rare"}}]}`))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("a different option set must still be refused, got %v", err)
	}
}

// TestAParameterCannotFeedAnEnumOrAListField records a limit of the
// language rather than a mistake in a document: a parameter is one of
// three scalars, the check is strict equality on the declared type, and
// so no parameter can ever feed a field declared enum or list<text>. The
// refusal has to say that, because "the parameter is declared text and
// the field is declared enum" reads like something the author could fix
// by redeclaring one of them, and neither redeclaration exists.
func TestAParameterCannotFeedAnEnumOrAListField(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	// Each field is asked with an operator its own type answers, so the
	// refusal under test is the parameter's and not the operator table's.
	for field, op := range map[string]string{"rank": "eq", "tags": "contains"} {
		_, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
			`{"v":1,"params":[{"key":"which","type":"text"}],
			  "from":[{"type":"quest","where":{"field":"`+field+`","op":"`+op+
				`","value":{"param":"which"}}}]}`))
		if !errors.Is(err, ErrQueryInvalid) {
			t.Fatalf("%s: a parameter must not feed this field, got %v", field, err)
		}
		if !strings.Contains(err.Error(), "a parameter cannot stand in for one") {
			t.Fatalf("%s: must say the language cannot express it, got %v", field, err)
		}
	}
	// Positive control in the same test: a text parameter still feeds a
	// text-typed built-in, so this is not a blanket refusal.
	if _, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"params":[{"key":"which","type":"text"}],
		  "from":[{"type":"quest","where":{"field":"@name","op":"eq","value":{"param":"which"}}}]}`,
	)); err != nil {
		t.Fatalf("a text parameter must still feed a text field: %v", err)
	}
}

// TestAHandBuiltOperandOfTheWrongShapeIsRefused covers the third arm of
// the value switch. Its two neighbours guard, and for a reason that
// applies to it word for word: ResolveAgainst is exported and a Query a
// Go caller built by hand has been through no parse pass, so the shape
// checkValueShape would have made was never made.
func TestAHandBuiltOperandOfTheWrongShapeIsRefused(t *testing.T) {
	g, _ := newGame(t)
	cat := mustCatalogue(t, g)
	hand := func(field, op string, value any) *Query {
		return &Query{V: 1, From: []Selector{{Type: "quest", As: "q", Where: &Predicate{
			Field: field, Op: op, Value: value,
			FieldRef: FieldRef{Key: field},
		}}}}
	}
	// Positive control first: the well-shaped operands resolve and land
	// where Task 6 binds them, typed.
	r, err := ResolveAgainst(g.projectID, cat, hand("min_level", "exists", true))
	if err != nil {
		t.Fatalf("a bool beside exists must resolve: %v", err)
	}
	if v, ok := r.Sets[0].Where.Leaf.Value.(bool); !ok || !v {
		t.Fatalf("exists must carry a bool, got %#v", r.Sets[0].Where.Leaf.Value)
	}
	r, err = ResolveAgainst(g.projectID, cat, hand("tags", "length_gte", float64(2)))
	if err != nil {
		t.Fatalf("a number beside length_gte must resolve: %v", err)
	}
	if v, ok := r.Sets[0].Where.Leaf.Value.(float64); !ok || v != 2 {
		t.Fatalf("length_gte must carry a float64, got %#v", r.Sets[0].Where.Leaf.Value)
	}

	if _, err := ResolveAgainst(g.projectID, cat,
		hand("min_level", "exists", "yes")); !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("a string beside exists must be refused, got %v", err)
	}
	_, err = ResolveAgainst(g.projectID, cat,
		hand("tags", "length_gte", map[string]any{"deep": true}))
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("a map beside length_gte must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), `must be a number, because the operator is "length_gte"`) {
		t.Fatalf("must say what the operator takes, got %v", err)
	}
}

// TestACatalogueFoldsCaseOnBothSides pins the claim Catalogue's comment
// makes: the database folds these keys, so the catalogue folds them on
// the way in and the lookup folds them on the way out. Folding one side
// and not the other refuses a spelling the write path accepted.
func TestACatalogueFoldsCaseOnBothSides(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	r, err := g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"QUEST","as":"q"}],
		  "traverse":[{"from":"q","via":"Available_To","to_type":"Class","as":"c"}]}`))
	if err != nil {
		t.Fatalf("a key spelled in another case must resolve: %v", err)
	}
	if r.Sets[0].EntityTypeID == nil || len(r.Steps[0].RelationTypeIDs) != 1 ||
		len(r.Steps[0].ToTypeIDs) != 1 {
		t.Fatalf("every key must have resolved, got %#v", r)
	}
	// The reference keeps the query's own spelling, which is what a rename
	// diagnostic compares against the stored one.
	if r.Refs[0].Key != "QUEST" {
		t.Fatalf("a reference must keep the spelling the query used, got %q", r.Refs[0].Key)
	}
	// The load side needs a type whose *stored* spelling is not already
	// folded, or a catalogue that folds neither side passes: "Boss" is
	// seeded with its capital and asked for without one.
	r, err = g.views.Resolve(ctx, g.projectID, mustParse(t,
		`{"v":1,"from":[{"type":"boss","as":"b"}],
		  "traverse":[{"from":"b","via":"guards","as":"g"}]}`))
	if err != nil {
		t.Fatalf("a type stored with a capital must resolve in lower case: %v", err)
	}
	if r.Sets[0].EntityTypeID == nil || len(r.Steps[0].RelationTypeIDs) != 1 {
		t.Fatalf("boss and guards must both have resolved, got %#v", r)
	}
	cat := mustCatalogue(t, g)
	if _, ok := cat.EntityTypes["boss"]; !ok {
		t.Fatalf("the catalogue must be keyed by the folded entity-type key")
	}
	if _, ok := cat.RelationTypes["guards"]; !ok {
		t.Fatalf("the catalogue must be keyed by the folded relation-type key")
	}
}

// TestProblemsAreReportedInDocumentOrderNotPointerOrder pins the order
// this pass reports in. Sorting by pointer string reads /from/10 before
// /from/2, and a designer reading a list that jumps about cannot tell
// where in their document to start.
func TestProblemsAreReportedInDocumentOrderNotPointerOrder(t *testing.T) {
	g, _ := newGame(t)
	var b strings.Builder
	b.WriteString(`{"v":1,"from":[`)
	for i := 0; i < 12; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		// Every selector but the second and the eleventh is fine, so the
		// two problems are at /from/2 and /from/10 and nothing else.
		switch i {
		case 2, 10:
			fmt.Fprintf(&b, `{"type":"missing_%d","as":"s%d"}`, i, i)
		default:
			fmt.Fprintf(&b, `{"type":"quest","as":"s%d"}`, i)
		}
	}
	b.WriteString(`]}`)
	_, err := g.views.Resolve(context.Background(), g.projectID, mustParse(t, b.String()))
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("want a *QueryError, got %v", err)
	}
	got := []string{qe.Fields[0].Path, qe.Fields[1].Path}
	want := []string{"/from/2/type", "/from/10/type"}
	if len(qe.Fields) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("want %v in document order, got %v", want, qe.Fields)
	}
}
