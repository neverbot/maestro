package analysis

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/views"
)

// TestLimitExceededIsOneValueAcrossBothDomains.
func TestLimitExceededIsOneValueAcrossBothDomains(t *testing.T) {
	if !errors.Is(ErrLimitExceeded, views.ErrLimitExceeded) {
		t.Fatal("analysis.ErrLimitExceeded and views.ErrLimitExceeded are two values; " +
			"internal/web has one arm for the code and it would miss one of them")
	}
	assert.Must(t, errors.Is(views.ErrLimitExceeded, metamodel.ErrLimitExceeded), "views.ErrLimitExceeded is no longer the metamodel's")
	// Through variables, because the three are constants a compiler folds
	// and `go vet` rightly calls a comparison of them suspect. The
	// question is not whether they are equal today — an alias makes that
	// a tautology — but that all three remain aliases of one declaration,
	// which is what a later redeclaration would break here and nowhere
	// else.
	spellings := []string{CodeLimitExceeded, views.CodeLimitExceeded, metamodel.CodeLimitExceeded}
	for _, spelling := range spellings {
		assert.Must(t, spelling == spellings[0], "the code is spelled more than one way across the three packages: %v",
			spellings)
	}

	// And the error this package actually builds matches, which is the
	// half that would have been dead: a ValidationError carrying the code
	// only matches the sentinel because ValidationError.Is answers for
	// it, and it did not until this task.
	err := limitExceeded("relation_types", 65, MaxTypeKeys)
	assert.Must(t, errors.Is(err, ErrLimitExceeded) && errors.Is(err, views.ErrLimitExceeded), "the refusal this package builds does not match the sentinel: %#v", err)
	if errors.Is(err, ErrInvalidInput) {
		t.Fatal("a limit refusal must not also read as invalid_input: they are two " +
			"recoveries — lower the number, and change the argument")
	}
}

// TestTheUndeclaredRefusalCarriesTheCatalogueAsDataAndAsProse.
func TestTheUndeclaredRefusalCarriesTheCatalogueAsDataAndAsProse(t *testing.T) {
	err := &UndeclaredError{
		Types: []TypeReport{
			{Key: "requires"},
			{Key: "available_to", SemanticRole: "availability"},
			{Key: "mentions", Traits: []string{"annotation"}},
		},
		Advice: undeclaredAdvice(),
	}
	assert.Must(t, errors.Is(err, ErrSemanticsUndeclared), "the refusal does not match its own sentinel")
	for _, key := range []string{"requires", "available_to", "mentions"} {
		assert.Must(t, strings.Contains(err.Error(), key), "the message does not name %q: %v", key, err)
	}
	details := err.Details()
	types, ok := details["relation_types"].([]map[string]any)
	if !ok || len(types) != 3 {
		t.Fatalf("details[relation_types] = %#v, want three entries", details["relation_types"])
	}
	if types[1]["semantic_role"] != "availability" {
		t.Fatalf("entry = %#v, want the role a caller would edit", types[1])
	}
	if _, present := types[0]["semantic_role"]; present {
		t.Fatalf("entry = %#v, want no role key for a type that has none: an empty "+
			"string reads as a role nobody declared", types[0])
	}
	assert.Must(t, details["advice"] != "", "the payload carries no advice")
}

// TestTheUndeclaredAdviceOffersOnlyWordsTheColumnAccepts, in both
// directions: every trait is offered, and every role the advice names is
// one the metamodel has. It is generated for exactly this reason — advice
// written by hand can promise a word that fails on the next call.
func TestTheUndeclaredAdviceOffersOnlyWordsTheColumnAccepts(t *testing.T) {
	advice := undeclaredAdvice()
	for _, trait := range Traits {
		assert.Must(t, strings.Contains(advice, trait), "the advice does not offer %q", trait)
	}
	for _, role := range metamodel.SemanticRoles {
		if _, derivable := derivedTraits[role]; !derivable {
			continue
		}
		assert.Must(t, strings.Contains(advice, role), "the advice does not name the derivable role %q", role)
	}
	// Backwards: nothing quoted in the advice is outside the two
	// vocabularies it is generated from.
	for _, quoted := range quotedWords(advice) {
		if slices.Contains(Traits, quoted) || slices.Contains(metamodel.SemanticRoles, quoted) {
			continue
		}
		t.Fatalf("the advice offers %q, which is neither a trait nor a semantic role: "+
			"a caller following it would be refused", quoted)
	}
}

// quotedWords pulls the double-quoted words out of a generated sentence.
func quotedWords(text string) []string {
	var words []string
	parts := strings.Split(text, `"`)
	for i := 1; i < len(parts); i += 2 {
		words = append(words, parts[i])
	}
	return words
}
