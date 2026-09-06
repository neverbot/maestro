package analysis

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/views"
)

// TestExactlyOneNewWireCodeShips is the count as an assertion.
//
// The plan's whole argument about this package's error vocabulary is
// that it adds **one** code, having refused three candidates because
// each named a recovery an existing code already names. A second
// sentinel appearing here without that argument being made again is the
// standing defect — a rule established correctly and not carried one
// step along — and this is what makes adding one cost a deliberate edit
// to a test that says why.
func TestExactlyOneNewWireCodeShips(t *testing.T) {
	if len(Sentinels()) != 1 {
		t.Fatalf("Sentinels() = %v, want exactly one. This sub-project adds one wire "+
			"code, semantics_undeclared, because its recovery — declare something about "+
			"your game's relation types — is named by no existing code. A second one "+
			"needs the same argument made in errors.go before this number moves",
			Sentinels())
	}
	if !errors.Is(Sentinels()[0], ErrSemanticsUndeclared) {
		t.Fatalf("Sentinels()[0] = %v, want ErrSemanticsUndeclared", Sentinels()[0])
	}
	if ErrSemanticsUndeclared.Error() != CodeSemanticsUndeclared {
		t.Fatalf("the sentinel spells %q and the code is %q: a sentinel whose name is "+
			"not its wire code is two strings to keep in step",
			ErrSemanticsUndeclared.Error(), CodeSemanticsUndeclared)
	}
}

// TestNoTimeoutCodeShips pins the refusal by name.
//
// internal/views faced this exact choice and refused `query_timeout`:
// SQLSTATE 57014 is already in metamodel's retryableSQLStates and
// already maps to `retryable`, whose recovery is "change nothing and
// resend". Adding an `analysis_timeout` here would be the recurring
// defect in its purest form, and the argument is in errors.go rather
// than only in a plan nobody reads at edit time — so the assertion is
// that the argument is *there*, as well as that the code is not.
func TestNoTimeoutCodeShips(t *testing.T) {
	for _, sentinel := range Sentinels() {
		if strings.Contains(sentinel.Error(), "timeout") {
			t.Fatalf("this package answers with %q: a timed-out analysis is `retryable`, "+
				"whose message names the budget and the bounds to lower", sentinel)
		}
	}
	source := readSourceFile(t, "errors.go")
	for _, refused := range []string{"analysis_timeout", "invalid_argument", "query_invalid"} {
		if !strings.Contains(source, refused) {
			t.Fatalf("errors.go does not record why %q was refused. The count of codes "+
				"is the decision this package made; a decision with no argument beside "+
				"it is one the next task will make differently", refused)
		}
	}
}

// TestLimitExceededIsOneValueAcrossBothDomains.
//
// It asserts the **identity** and not the spelling, because spelling is
// exactly what a second errors.New("limit_exceeded") would get right: it
// would print the same, satisfy no errors.Is against the other package's
// name, miss internal/web's single arm and reach an agent as
// internal_error. That is the trap metamodel.ValidationError.Is's own
// comment describes, and one value in the package both domains already
// depend on is what closes it.
func TestLimitExceededIsOneValueAcrossBothDomains(t *testing.T) {
	if !errors.Is(ErrLimitExceeded, views.ErrLimitExceeded) {
		t.Fatal("analysis.ErrLimitExceeded and views.ErrLimitExceeded are two values; " +
			"internal/web has one arm for the code and it would miss one of them")
	}
	if !errors.Is(views.ErrLimitExceeded, metamodel.ErrLimitExceeded) {
		t.Fatal("views.ErrLimitExceeded is no longer the metamodel's")
	}
	// Through variables, because the three are constants a compiler folds
	// and `go vet` rightly calls a comparison of them suspect. The
	// question is not whether they are equal today — an alias makes that
	// a tautology — but that all three remain aliases of one declaration,
	// which is what a later redeclaration would break here and nowhere
	// else.
	spellings := []string{CodeLimitExceeded, views.CodeLimitExceeded, metamodel.CodeLimitExceeded}
	for _, spelling := range spellings {
		if spelling != spellings[0] {
			t.Fatalf("the code is spelled more than one way across the three packages: %v",
				spellings)
		}
	}

	// And the error this package actually builds matches, which is the
	// half that would have been dead: a ValidationError carrying the code
	// only matches the sentinel because ValidationError.Is answers for
	// it, and it did not until this task.
	err := limitExceeded("relation_types", 65, MaxTypeKeys)
	if !errors.Is(err, ErrLimitExceeded) || !errors.Is(err, views.ErrLimitExceeded) {
		t.Fatalf("the refusal this package builds does not match the sentinel: %#v", err)
	}
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
		if code == "" {
			t.Fatalf("errors.go builds a sentinel from %q, which this test cannot "+
				"resolve; add it to codeValues so the scan keeps seeing every one",
				constant)
		}
		found := false
		for _, sentinel := range Sentinels() {
			if sentinel.Error() == code {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s (%q) is declared in errors.go and is not in Sentinels(), so "+
				"internal/web's mapping test will not see it and it will reach an agent "+
				"as internal_error", constant, code)
		}
	}
	if declared != len(Sentinels()) {
		t.Fatalf("errors.go declares %d sentinels and Sentinels() lists %d: the list is "+
			"what internal/web iterates, so the two are one decision",
			declared, len(Sentinels()))
	}
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
//
// The prose is what a human reads in a log; the details payload is what
// a client renders. Both are built in the domain so the MCP surface and
// the REST mirror cannot disagree about what this refusal carries — the
// shape markdown.ConflictError.Details established.
func TestTheUndeclaredRefusalCarriesTheCatalogueAsDataAndAsProse(t *testing.T) {
	err := &UndeclaredError{
		Types: []TypeReport{
			{Key: "requires"},
			{Key: "available_to", SemanticRole: "availability"},
			{Key: "mentions", Traits: []string{"annotation"}},
		},
		Advice: undeclaredAdvice(),
	}
	if !errors.Is(err, ErrSemanticsUndeclared) {
		t.Fatal("the refusal does not match its own sentinel")
	}
	for _, key := range []string{"requires", "available_to", "mentions"} {
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("the message does not name %q: %v", key, err)
		}
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
	if details["advice"] == "" {
		t.Fatal("the payload carries no advice")
	}
}

// TestTheUndeclaredAdviceOffersOnlyWordsTheColumnAccepts, in both
// directions: every trait is offered, and every role the advice names is
// one the metamodel has. It is generated for exactly this reason — advice
// written by hand can promise a word that fails on the next call.
func TestTheUndeclaredAdviceOffersOnlyWordsTheColumnAccepts(t *testing.T) {
	advice := undeclaredAdvice()
	for _, trait := range Traits {
		if !strings.Contains(advice, trait) {
			t.Fatalf("the advice does not offer %q", trait)
		}
	}
	for _, role := range metamodel.SemanticRoles {
		if _, derivable := derivedTraits[role]; !derivable {
			continue
		}
		if !strings.Contains(advice, role) {
			t.Fatalf("the advice does not name the derivable role %q", role)
		}
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
