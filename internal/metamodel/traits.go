package metamodel

import (
	"fmt"
	"strings"
)

// AnalysisTraits is the closed vocabulary of analytical behaviours a
// relation type may declare, in the order 0013_analysis.sql's
// relation_types_traits_vocab CHECK lists them.
//
// **This slice and that CHECK are one list written twice, and this one is
// the copy a caller ever sees.** SemanticRoles carries the same pairing
// for the same reason, and it exists because of review finding H3: a
// value outside the constraint travelled to Postgres, came back as an
// untyped check-constraint violation and reached an agent as
// `internal_error` — a server fault, with no path and no list of what
// would have been accepted, for something the agent typed. The
// constraint stays as the backstop for a writer that does not come
// through this package;
// TestTheTraitVocabularyIsOneSetInGoAndInTheDatabase reads the CHECK's
// own text back out of pg_get_constraintdef and asserts set equality in
// **both** directions, so neither list can grow a word the other lacks.
//
// **Why the vocabulary lives here and not in internal/analysis, which is
// the package that reads it.** internal/analysis depends on this package
// for FieldError, Actor and every reused sentinel, so the reverse edge
// would be an import cycle. The vocabulary therefore belongs beside the
// column it constrains and beside the upsert that writes it —
// relation_types.upsert is where a trait combination arrives — and
// internal/analysis aliases it rather than redeclaring it. That
// inversion is stated in both files because it is exactly the sort of
// thing a later reader reverses "for tidiness", and reversing it does
// not compile.
//
// Exported for the rule correction 24 established for MaxSearchQuery: a
// bound a caller cannot read is a bound a caller trips over. The MCP
// tool description for `relation_types.upsert` is built from it, so a
// trait added here is offered to agents without a second edit.
var AnalysisTraits = []string{
	"prerequisite_of", "unlocks", "containment",
	"ordering", "symmetric", "acyclic", "annotation",
}

// TraitConflict is one combination of traits that contradicts itself:
// Trait may not be declared beside any of With, and Why is the sentence
// a designer is given instead of "invalid".
//
// It is a table rather than three hand-written `if`s because the same
// structure has three readers — the coherence check, the generated tool
// description, and internal/analysis's own description — and three
// hand-written copies of one rule is this repository's most repeated
// defect. AnalysisTraitConflicts is what all three read.
type TraitConflict struct {
	Trait string
	With  []string
	Why   string
}

// AnalysisTraitConflicts is every combination of admissible traits that
// cannot stand together.
//
// A **duplicate** and an **unknown word** are refused too and are not in
// this table: they are faults in the list itself rather than in the
// combination, they are caught before these rules run, and a rule of the
// form "X may not be declared beside X" would read as nonsense in the
// generated description.
var AnalysisTraitConflicts = []TraitConflict{
	{
		Trait: "annotation",
		// Every other word in the vocabulary, derived rather than
		// written out, so a trait added to AnalysisTraits is refused
		// beside `annotation` without a second edit. That derivation is
		// the whole reason `annotation` is the first row: it is the rule
		// most likely to be left one word short by hand.
		With: othersInVocabulary("annotation"),
		Why: "annotation means deliberately inert, and a type is inert or it is not. " +
			"Declare annotation alone, or drop it and keep the traits that say what " +
			"these edges do",
	},
	{
		Trait: "symmetric",
		With:  []string{"prerequisite_of", "unlocks", "ordering"},
		Why: "an edge that means the same in both directions cannot also gate in one. " +
			"Drop symmetric, or declare a second relation type for the direction " +
			"that gates",
	},
	{
		Trait: "prerequisite_of",
		With:  []string{"unlocks"},
		Why: "they are the same gate read from its two ends, so a type carrying both " +
			"gates in both directions at once. Declare two relation types, one for " +
			"each direction",
	},
}

// TraitConflictLines renders AnalysisTraitConflicts as one sentence per
// rule, for a tool description an agent reads before it types anything.
//
// Generated from the table the check runs on, so a rule added to the
// table is offered to agents and a rule removed from it stops being
// promised. internal/web's
// TestTheRelationTypesUpsertDescriptionNamesEveryRefusedCombination asserts that
// in both directions.
func TraitConflictLines() []string {
	lines := make([]string, 0, len(AnalysisTraitConflicts))
	for _, conflict := range AnalysisTraitConflicts {
		lines = append(lines, fmt.Sprintf("%q with %s (%s)",
			conflict.Trait, QuotedList(conflict.With), conflict.Why))
	}
	return lines
}

// checkAnalysisTraits refuses a trait list the column would refuse, and
// every combination of admissible traits that contradicts itself.
//
// **These are invalid_schema and not schema_violation**, and errors.go
// gives those two sentinels a split by who is at fault: invalid_schema is
// a type *declaration* that cannot stand, schema_violation is a *row of
// values* that does not fit a declaration that can. A trait combination
// arrives on relation_types.upsert and is part of the declaration, so it
// is the first — which is why this returns a *SchemaError and not a
// *ValidationError.
//
// An empty or nil list is not a refusal but the absence of a
// declaration: the column is nullable precisely because a relation type
// need not say how it behaves, and the caller stores nil as NULL.
// **NULL is undeclared and `{annotation}` is deliberately inert**, which
// is the distinction the whole column carries and the reason the
// database refuses an empty array rather than storing one.
//
// Every message names *what* is wrong — the offending word, or the pair
// that cannot stand together — rather than reporting that something is,
// because the refusal is where a designer learns which of the two traits
// they meant.
func checkAnalysisTraits(traits []string) error {
	if len(traits) == 0 {
		return nil
	}

	// Vocabulary and duplicates first, and nothing else if either fires:
	// the combination rules below ask whether two *known* traits can
	// stand together, and running them over a typo would report a
	// contradiction between a word and a word that does not exist.
	var problems []FieldError
	seen := make(map[string]int, len(traits))
	for i, trait := range traits {
		if !knownTrait(trait) {
			problems = append(problems, FieldError{
				Path: "analysis_traits",
				Message: fmt.Sprintf(
					"%q is not an analysis trait: must be one of %s. A trait is how an "+
						"edge of this type behaves in a graph walk, which is a different "+
						"question from semantic_role's what it means",
					trait, QuotedList(AnalysisTraits)),
			})
			continue
		}
		if first, repeated := seen[trait]; repeated {
			problems = append(problems, FieldError{
				Path: "analysis_traits",
				Message: fmt.Sprintf(
					"%q is declared twice, here and at element %d: a trait declared twice "+
						"is a typo, not an emphasis", trait, first),
			})
			continue
		}
		seen[trait] = i
	}
	if len(problems) > 0 {
		return &SchemaError{Fields: problems}
	}

	// Every incoherent combination in one pass, so a declaration wrong
	// twice is fixed in one round trip. The order is the table's, and
	// the offenders are listed in the vocabulary's order rather than the
	// caller's, so two callers who sent the same set in two orders get
	// the same sentence.
	for _, conflict := range AnalysisTraitConflicts {
		if _, declared := seen[conflict.Trait]; !declared {
			continue
		}
		var offenders []string
		for _, other := range conflict.With {
			if _, ok := seen[other]; ok {
				offenders = append(offenders, other)
			}
		}
		if len(offenders) == 0 {
			continue
		}
		problems = append(problems, FieldError{
			Path: "analysis_traits",
			Message: fmt.Sprintf("%q cannot be declared beside %s: %s",
				conflict.Trait, QuotedList(offenders), conflict.Why),
		})
	}
	if len(problems) > 0 {
		return &SchemaError{Fields: problems}
	}

	// **A redundant `acyclic` is neither refused nor stripped.**
	// prerequisite_of, unlocks, ordering and containment each imply it,
	// so declaring it as well says nothing new — but stripping it would
	// make what a caller reads back differ from what it sent, which is
	// the round-trip rule relation_types.upsert's own description states.
	return nil
}

// knownTrait answers whether one word is in the vocabulary.
func knownTrait(trait string) bool {
	for _, allowed := range AnalysisTraits {
		if trait == allowed {
			return true
		}
	}
	return false
}

// othersInVocabulary is every trait but one, in the vocabulary's own
// order. It is what makes `annotation`'s conflict row a derivation
// rather than a list that has to be extended by hand.
func othersInVocabulary(trait string) []string {
	others := make([]string, 0, len(AnalysisTraits)-1)
	for _, allowed := range AnalysisTraits {
		if allowed != trait {
			others = append(others, allowed)
		}
	}
	return others
}

// QuotedList renders a list of words as a quoted, comma-separated
// sentence fragment.
//
// Exported because internal/web builds tool descriptions out of this
// package's own vocabularies and had a private copy of exactly this
// function; two spellings of one rendering is how a generated
// description and the list it is generated from stop being comparable.
func QuotedList(words []string) string {
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = fmt.Sprintf("%q", word)
	}
	return strings.Join(quoted, ", ")
}
