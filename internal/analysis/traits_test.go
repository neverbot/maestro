package analysis

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
)

// TestADeclaredTraitIsReadAsDeclared is the resolver's first source, and
// the control every test below leans on: a type that says what it does
// is read as saying it.
func TestADeclaredTraitIsReadAsDeclared(t *testing.T) {
	g := newGame(t)
	id := g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})

	resolved, err := g.analysis.Resolve(t.Context(), g.projectID, ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	entry, ok := resolved.ByType[id]
	if !ok {
		t.Fatalf("the declared type is not in the reading: %+v", resolved.Types())
	}
	if entry.Source != SourceDeclared {
		t.Fatalf("source = %q, want %q", entry.Source, SourceDeclared)
	}
	if !slices.Equal(entry.Traits, []string{"prerequisite_of"}) {
		t.Fatalf("traits = %v, want [prerequisite_of]", entry.Traits)
	}
	if got := resolved.WithTrait("prerequisite_of"); len(got) != 1 || got[0] != id {
		t.Fatalf("WithTrait = %v, want the one declared type", got)
	}
}

// TestARoleDerivedGateIsReportedAsDerivedAndNotAsDeclared is the second
// source, and the distinction is the point: a designer must be able to
// see that this engine treated `available_to` as a gate because of a
// role they set months ago and not because they declared a behaviour.
// Reporting it as `declared` would hide the one thing they would want to
// change.
func TestARoleDerivedGateIsReportedAsDerivedAndNotAsDeclared(t *testing.T) {
	g := newGame(t)
	declared := g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})
	derived := g.declareRelationType(t, "available_to", "availability", nil)

	resolved, err := g.analysis.Resolve(t.Context(), g.projectID, ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The control, in the same test: the declared type is still declared,
	// so a resolver that labelled everything `derived_from_role` cannot
	// pass this.
	if resolved.ByType[declared].Source != SourceDeclared {
		t.Fatalf("the declared type = %q, want %q",
			resolved.ByType[declared].Source, SourceDeclared)
	}
	entry, ok := resolved.ByType[derived]
	if !ok {
		t.Fatalf("a type with a role and no traits must still be read: %+v", resolved.Types())
	}
	if entry.Source != SourceDerivedFromRole {
		t.Fatalf("source = %q, want %q: a translation of a role is not a declaration",
			entry.Source, SourceDerivedFromRole)
	}
	if !slices.Equal(entry.Traits, []string{"unlocks"}) {
		t.Fatalf("traits = %v, want the availability translation [unlocks]", entry.Traits)
	}
}

// TestADeclarationBeatsTheRoleItContradicts. A type carrying both is not
// ambiguous: traits are the stronger statement, and the role is what a
// game says the edge *means*, which is a different question.
func TestADeclarationBeatsTheRoleItContradicts(t *testing.T) {
	g := newGame(t)
	id := g.declareRelationType(t, "connects_to", "spatial", []string{"containment"})

	resolved, err := g.analysis.Resolve(t.Context(), g.projectID, ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	entry := resolved.ByType[id]
	if entry.Source != SourceDeclared || !slices.Equal(entry.Traits, []string{"containment"}) {
		t.Fatalf("entry = %+v, want the declared traits and not spatial's [symmetric]", entry)
	}
}

// TestATypeWithNeitherTraitsNorARoleIsNotWalked. It is an edge this
// engine has been told nothing about, and walking it as though it gated
// progression is exactly the guess the metamodel exists to avoid.
func TestATypeWithNeitherTraitsNorARoleIsNotWalked(t *testing.T) {
	g := newGame(t)
	gate := g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})
	silent := g.declareRelationType(t, "mentions", "", nil)

	resolved, err := g.analysis.Resolve(t.Context(), g.projectID, ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, ok := resolved.ByType[silent]; ok {
		t.Fatal("a type that declares nothing must not be read as anything")
	}
	// The positive control: the reading is not simply empty.
	if _, ok := resolved.ByType[gate]; !ok {
		t.Fatal("the declared gate must be in the reading, or this test proves nothing")
	}
}

// TestAGameThatDeclaredNothingIsRefusedRatherThanReportedHealthy is the
// fourth arm, and the most important behaviour in this file.
//
// A clean bill of health from an engine that had nothing to read is the
// worst output this package could produce: a designer would believe
// their prerequisites do not loop when the truth is that no relation
// type declares itself a prerequisite, so no walk had an edge to follow.
// The refusal carries the whole catalogue because the recovery is
// "declare something", and a caller cannot do that without the list.
func TestAGameThatDeclaredNothingIsRefusedRatherThanReportedHealthy(t *testing.T) {
	g := newGame(t)
	g.declareRelationType(t, "mentions", "", nil)
	g.declareRelationType(t, "see_also", "", nil)

	_, err := g.analysis.Resolve(t.Context(), g.projectID, ResolveInput{})
	if err == nil {
		t.Fatal("a game that declared nothing must be refused, not reported healthy")
	}
	if !errors.Is(err, ErrSemanticsUndeclared) {
		t.Fatalf("err = %T %v, want ErrSemanticsUndeclared", err, err)
	}
	var undeclared *UndeclaredError
	if !errors.As(err, &undeclared) {
		t.Fatalf("err = %T, want it to carry the catalogue", err)
	}
	if len(undeclared.Types) != 2 {
		t.Fatalf("catalogue = %+v, want both relation types of the game", undeclared.Types)
	}
	for _, want := range []string{"mentions", "see_also"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}
	if undeclared.Advice == "" {
		t.Fatal("a refusal whose recovery is 'declare something' must say what to declare")
	}
	// The advice is generated, so it can only ever offer words the column
	// accepts.
	for _, trait := range Traits {
		if !strings.Contains(undeclared.Advice, trait) {
			t.Fatalf("the advice does not offer %q: %s", trait, undeclared.Advice)
		}
	}
	details := undeclared.Details()
	if _, ok := details["relation_types"]; !ok {
		t.Fatalf("details = %+v, want the catalogue as data and not only as prose", details)
	}
}

// TestAGameWithNoRelationTypesAtAllIsRefusedToo, and says so differently,
// because "declare traits on your types" is unhelpful advice to a game
// that has no types.
func TestAGameWithNoRelationTypesAtAllIsRefusedToo(t *testing.T) {
	g := newGame(t)
	_, err := g.analysis.Resolve(t.Context(), g.projectID, ResolveInput{})
	if !errors.Is(err, ErrSemanticsUndeclared) {
		t.Fatalf("err = %v, want ErrSemanticsUndeclared", err)
	}
	if !strings.Contains(err.Error(), "no relation types at all") {
		t.Fatalf("err = %v, want it to say the game has no relation types", err)
	}
}

// TestACallerSuppliedTypeShadowsTheSelectionAndSaysSo is the first
// source: the caller has said which edges its question is about, which
// is a stronger statement than anything stored.
func TestACallerSuppliedTypeShadowsTheSelectionAndSaysSo(t *testing.T) {
	g := newGame(t)
	declared := g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})
	other := g.declareRelationType(t, "contains", "", []string{"containment"})

	resolved, err := g.analysis.Resolve(t.Context(), g.projectID,
		ResolveInput{RelationTypeKeys: []string{"CONTAINS"}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved.ByType) != 1 {
		t.Fatalf("reading = %+v, want only the type the caller named", resolved.Types())
	}
	if _, ok := resolved.ByType[declared]; ok {
		t.Fatal("a caller's list must shadow the declared selection, not extend it")
	}
	entry := resolved.ByType[other]
	if entry.Source != SourceCallerSupplied {
		t.Fatalf("source = %q, want %q", entry.Source, SourceCallerSupplied)
	}
	// Matched without regard to case, as every key in this domain is, and
	// the answer quotes the stored spelling.
	if entry.Key != "contains" {
		t.Fatalf("key = %q, want the stored spelling", entry.Key)
	}
	if !slices.Equal(entry.Traits, []string{"containment"}) {
		t.Fatalf("traits = %v: naming a type says which edges the question is about, "+
			"not how they behave", entry.Traits)
	}
}

// TestACallerSuppliedTypeFromAnotherGameIsNotFoundAndNamesTheKey.
//
// The alternative — dropping an unmatched key — turns a narrowed run
// into a differently narrowed run with no way for the caller to tell,
// and in the limit into an empty filter that answers "your whole game is
// unreachable".
func TestACallerSuppliedTypeFromAnotherGameIsNotFoundAndNamesTheKey(t *testing.T) {
	g := newGame(t)
	g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})

	// The control: the same call with the key that does exist works.
	if _, err := g.analysis.Resolve(t.Context(), g.projectID,
		ResolveInput{RelationTypeKeys: []string{"requires"}}); err != nil {
		t.Fatalf("the control must resolve: %v", err)
	}

	_, err := g.analysis.Resolve(t.Context(), g.projectID,
		ResolveInput{RelationTypeKeys: []string{"requires", "leads_to"}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %T %v, want not_found", err, err)
	}
	if !strings.Contains(err.Error(), "leads_to") ||
		!strings.Contains(err.Error(), "relation_types[1]") {
		t.Fatalf("err = %v, want it to name the key and the element at fault", err)
	}
}

// TestACallerSuppliedListIsASetAndARepeatIsRefused, for the reason an
// endpoint list is: naming one type twice cannot mean anything a caller
// intended, and folding it silently would make the answer disagree with
// what was sent.
func TestACallerSuppliedListIsASetAndARepeatIsRefused(t *testing.T) {
	g := newGame(t)
	g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})

	_, err := g.analysis.Resolve(t.Context(), g.projectID,
		ResolveInput{RelationTypeKeys: []string{"requires", "REQUIRES"}})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %T %v, want invalid_input", err, err)
	}
	if !strings.Contains(err.Error(), "relation_types[1]") {
		t.Fatalf("err = %v, want the repeated element named", err)
	}
}

// TestACallerSuppliedSetWithNoBehaviourIsStillRefused. A run with a set
// of types and no idea how any of them behaves has nothing to walk, and
// answering "nothing is wrong" is the failure this whole arm exists to
// prevent.
func TestACallerSuppliedSetWithNoBehaviourIsStillRefused(t *testing.T) {
	g := newGame(t)
	g.declareRelationType(t, "mentions", "", nil)
	g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})

	// The control: naming the type that does carry behaviour resolves.
	if _, err := g.analysis.Resolve(t.Context(), g.projectID,
		ResolveInput{RelationTypeKeys: []string{"requires"}}); err != nil {
		t.Fatalf("the control must resolve: %v", err)
	}

	_, err := g.analysis.Resolve(t.Context(), g.projectID,
		ResolveInput{RelationTypeKeys: []string{"mentions"}})
	if !errors.Is(err, ErrSemanticsUndeclared) {
		t.Fatalf("err = %T %v, want ErrSemanticsUndeclared", err, err)
	}
}

// TestARelationTypeListAboveItsCapIsRefusedAndNotTrimmed. Refused, not
// clamped, naming the cap — bounds.go argues the split and this asserts
// it on the one caller-supplied list this task ships.
func TestARelationTypeListAboveItsCapIsRefusedAndNotTrimmed(t *testing.T) {
	g := newGame(t)
	g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})

	// The control: a list at the cap is accepted, so the refusal is
	// about the bound rather than about the list existing.
	atCap := make([]string, 0, MaxTypeKeys)
	atCap = append(atCap, "requires")
	for len(atCap) < MaxTypeKeys {
		key := fmt.Sprintf("filler_%d", len(atCap))
		g.declareRelationType(t, key, "", []string{"annotation"})
		atCap = append(atCap, key)
	}
	if _, err := g.analysis.Resolve(t.Context(), g.projectID,
		ResolveInput{RelationTypeKeys: atCap}); err != nil {
		t.Fatalf("a list exactly at the cap must be accepted: %v", err)
	}

	overCap := append(slices.Clone(atCap), "one_too_many")
	_, err := g.analysis.Resolve(t.Context(), g.projectID,
		ResolveInput{RelationTypeKeys: overCap})
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("err = %T %v, want limit_exceeded", err, err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(MaxTypeKeys)) {
		t.Fatalf("err = %v, want it to name the cap so the caller can lower to it", err)
	}
	if !strings.Contains(err.Error(), "relation_types") {
		t.Fatalf("err = %v, want it to name the caller's own argument", err)
	}
}

// TestEverySemanticRoleHasADerivedTraitSet is the forward half of the
// bidirectional guard over the derivation mapping: a seventh role added
// to metamodel.SemanticRoles fails here, rather than silently becoming a
// role this engine skips.
func TestEverySemanticRoleHasADerivedTraitSet(t *testing.T) {
	for _, role := range metamodel.SemanticRoles {
		traits, ok := derivedTraits[role]
		if !ok {
			t.Fatalf("semantic_role %q has no derived trait set, so a game that declares "+
				"it and nothing else analyses as though it had declared nothing", role)
		}
		if len(traits) == 0 {
			t.Fatalf("semantic_role %q derives an empty trait set, which is the same "+
				"silence with more steps", role)
		}
	}
}

// TestEveryDerivedTraitSetNamesARealRole is the backward half, and it
// checks both ends of each entry: the key is a role the metamodel offers,
// and every word in the value is a trait the column accepts. A typo in
// either would otherwise sit in this map producing a reading no game can
// ever match.
func TestEveryDerivedTraitSetNamesARealRole(t *testing.T) {
	if len(derivedTraits) == 0 {
		t.Fatal("the derivation mapping is empty, so every assertion here is vacuous")
	}
	for role, traits := range derivedTraits {
		if !slices.Contains(metamodel.SemanticRoles, role) {
			t.Fatalf("%q is not a semantic_role the metamodel offers, so nothing can ever "+
				"be read through this entry", role)
		}
		for _, trait := range traits {
			if !slices.Contains(Traits, trait) {
				t.Fatalf("role %q derives %q, which is not in the trait vocabulary: the "+
					"reading it produces could never have been declared by hand", role, trait)
			}
		}
	}
	if len(derivedTraits) != len(metamodel.SemanticRoles) {
		t.Fatalf("the mapping carries %d roles and the metamodel offers %d",
			len(derivedTraits), len(metamodel.SemanticRoles))
	}
}

// TestTheTraitVocabularyIsTheMetamodelsAndNotACopy. The alias is the
// whole mechanism that keeps six places in step; a redeclaration here
// would compile and drift.
func TestTheTraitVocabularyIsTheMetamodelsAndNotACopy(t *testing.T) {
	if !slices.Equal(Traits, metamodel.AnalysisTraits) {
		t.Fatalf("analysis.Traits = %v, metamodel.AnalysisTraits = %v: two lists",
			Traits, metamodel.AnalysisTraits)
	}
	if len(TraitConflicts) != len(metamodel.AnalysisTraitConflicts) {
		t.Fatal("analysis.TraitConflicts is not the metamodel's table")
	}
}

// TestTheTraitDescriptionNamesEveryTraitAndEveryTraitIsNamed is the
// bidirectional guard over the generated description's vocabulary half.
//
// Forwards: every word is offered, with a meaning. Backwards: every word
// the description explains is in the vocabulary — asserted through
// traitMeanings, which is the one hand-written table in the generator
// and therefore the one that can drift.
func TestTheTraitDescriptionNamesEveryTraitAndEveryTraitIsNamed(t *testing.T) {
	description := TraitDescription()
	for _, trait := range Traits {
		if !strings.Contains(description, `"`+trait+`"`) {
			t.Fatalf("the description does not name %q", trait)
		}
		meaning, ok := traitMeanings[trait]
		if !ok || meaning == "" {
			t.Fatalf("%q has no meaning in traitMeanings, so it reaches an agent as a "+
				"bare name with nothing to act on", trait)
		}
		if !strings.Contains(description, meaning) {
			t.Fatalf("the description does not carry %q's meaning", trait)
		}
	}
	for trait := range traitMeanings {
		if !slices.Contains(Traits, trait) {
			t.Fatalf("traitMeanings explains %q, which is not in the vocabulary: an agent "+
				"is being offered a word the column would refuse", trait)
		}
	}
}

// TestEveryRefusedCombinationIsNamedInTheTraitDescription. A refusal an
// agent is never told about is a refusal it discovers by failing a call.
func TestEveryRefusedCombinationIsNamedInTheTraitDescription(t *testing.T) {
	description := TraitDescription()
	if len(TraitConflicts) == 0 {
		t.Fatal("the conflict table is empty, so this assertion is vacuous")
	}
	for _, line := range metamodel.TraitConflictLines() {
		if !strings.Contains(description, line) {
			t.Fatalf("the description does not name the refused combination %q", line)
		}
	}
}

// TestEveryDerivedRoleIsNamedInTheTraitDescription, in both directions:
// every role the mapping translates is documented, and the documented
// translation is the one the resolver actually applies.
func TestEveryDerivedRoleIsNamedInTheTraitDescription(t *testing.T) {
	description := TraitDescription()
	for role, traits := range derivedTraits {
		line := fmt.Sprintf("semantic_role %q is read as %s", role, metamodel.QuotedList(traits))
		if !strings.Contains(description, line) {
			t.Fatalf("the description does not carry the translation %q: an agent cannot "+
				"know why this engine treats its edges as gates", line)
		}
	}
}

// TestEverySourceIsReachable holds the Source enum against the resolver:
// a value nothing can ever produce is a value a reader has to have an
// opinion about for nothing, and a value the resolver produces that is
// not in Sources is one no reader enumerating them will handle.
func TestEverySourceIsReachable(t *testing.T) {
	g := newGame(t)
	g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})
	g.declareRelationType(t, "available_to", "availability", nil)

	seen := map[Source]bool{}
	resolved, err := g.analysis.Resolve(t.Context(), g.projectID, ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, entry := range resolved.Types() {
		seen[entry.Source] = true
	}
	supplied, err := g.analysis.Resolve(t.Context(), g.projectID,
		ResolveInput{RelationTypeKeys: []string{"requires"}})
	if err != nil {
		t.Fatalf("resolve with a caller list: %v", err)
	}
	for _, entry := range supplied.Types() {
		seen[entry.Source] = true
	}
	for _, source := range Sources {
		if !seen[source] {
			t.Fatalf("no reading this package produces ever carries source %q", source)
		}
	}
	if len(seen) != len(Sources) {
		t.Fatalf("the resolver produced %d sources and Sources lists %d: %v",
			len(seen), len(Sources), seen)
	}
}

// TestTheReadingIsOrderedSoTwoRunsAgree. A result document that reorders
// itself between two runs over one unchanged game is a document nobody
// can diff.
func TestTheReadingIsOrderedSoTwoRunsAgree(t *testing.T) {
	g := newGame(t)
	for _, key := range []string{"zeta", "alpha", "mu"} {
		g.declareRelationType(t, key, "", []string{"prerequisite_of"})
	}
	resolved, err := g.analysis.Resolve(t.Context(), g.projectID, ResolveInput{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	keys := make([]string, 0, 3)
	for _, entry := range resolved.Types() {
		keys = append(keys, entry.Key)
	}
	if !slices.Equal(keys, []string{"alpha", "mu", "zeta"}) {
		t.Fatalf("Types() = %v, want key order", keys)
	}
}
