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

// TestExactlyOneNewWireCodeShips is the count as an assertion.
func TestExactlyOneNewWireCodeShips(t *testing.T) {
	assert.Must(t, len(Sentinels()) == 1, "Sentinels() = %v, want exactly one. This sub-project adds one wire "+
		"code, semantics_undeclared, because its recovery — declare something about "+
		"your game's relation types — is named by no existing code. A second one "+
		"needs the same argument made in errors.go before this number moves",
		Sentinels())
	if !errors.Is(Sentinels()[0], ErrSemanticsUndeclared) {
		t.Fatalf("Sentinels()[0] = %v, want ErrSemanticsUndeclared", Sentinels()[0])
	}
	assert.Must(t, ErrSemanticsUndeclared.Error() == CodeSemanticsUndeclared, "the sentinel spells %q and the code is %q: a sentinel whose name is "+
		"not its wire code is two strings to keep in step",
		ErrSemanticsUndeclared.Error(), CodeSemanticsUndeclared)
}

// TestNoTimeoutCodeShips pins the refusal by name.
func TestNoTimeoutCodeShips(t *testing.T) {
	for _, sentinel := range Sentinels() {
		assert.Must(t, !strings.Contains(sentinel.Error(), "timeout"), "this package answers with %q: a timed-out analysis is `retryable`, "+
			"whose message names the budget and the bounds to lower", sentinel)
	}
	source := readSourceFile(t, "errors.go")
	for _, refused := range []string{"analysis_timeout", "invalid_argument", "query_invalid"} {
		assert.Must(t, strings.Contains(source, refused), "errors.go does not record why %q was refused. The count of codes "+
			"is the decision this package made; a decision with no argument beside "+
			"it is one the next task will make differently", refused)
	}
}

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

// TestEverySentinelThisPackageAnswersWithIsInSentinels is the
// bidirectional half: Sentinels() is what internal/web will iterate, so a
// sentinel declared and left out of it is a code that reaches an agent as
// internal_error.
func TestEverySentinelThisPackageAnswersWithIsInSentinels(t *testing.T) {
	source := readSourceFile(t, "errors.go")
	// Every `errors.New(Code…)` in this file is a sentinel this package
	// declares. The aliases beside them are assignments from another
	// package and are not this package's to publish, which is why the
	// scan looks for the constructor and not for the `Err` prefix.
	declared := 0
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		// Comments in this file quote `errors.New("limit_exceeded")` while
		// arguing why there is no second one, so a scan that read comments
		// would fail on the argument for the rule it is checking.
		if strings.HasPrefix(trimmed, "//") || !strings.Contains(trimmed, "= errors.New(") {
			continue
		}
		declared++
		constant := strings.TrimSuffix(
			strings.SplitN(trimmed, "= errors.New(", 2)[1], ")")
		// The constant's own value, resolved through the one place it is
		// declared rather than re-spelled here.
		code := codeValues[constant]
		assert.Must(t, code != "", "errors.go builds a sentinel from %q, which this test cannot "+
			"resolve; add it to codeValues so the scan keeps seeing every one",
			constant)
		found := false
		for _, sentinel := range Sentinels() {
			if sentinel.Error() == code {
				found = true
			}
		}
		assert.Must(t, found, "%s (%q) is declared in errors.go and is not in Sentinels(), so "+
			"internal/web's mapping test will not see it and it will reach an agent "+
			"as internal_error", constant, code)
	}
	assert.Must(t, declared == len(Sentinels()), "errors.go declares %d sentinels and Sentinels() lists %d: the list is "+
		"what internal/web iterates, so the two are one decision",
		declared, len(Sentinels()))
}

// codeValues resolves the code constants errors.go builds its sentinels
// from. It is a map rather than a reflection trick because Go offers no
// way to read a package's own constants by name, and a test that
// silently failed to resolve one would stop scanning without saying so —
// which is why an unresolved constant is a failure above and not a skip.
var codeValues = map[string]string{
	"CodeSemanticsUndeclared": CodeSemanticsUndeclared,
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
