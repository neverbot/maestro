package metamodel

import (
	"fmt"
	"strings"
)

// AnalysisTraits is the closed vocabulary of analytical behaviours a
// relation type may declare, in the order 0013_analysis.sql's
// relation_types_traits_vocab CHECK lists them.
var AnalysisTraits = []string{
	"prerequisite_of", "unlocks", "containment",
	"ordering", "symmetric", "acyclic", "annotation",
}

// TraitConflict is one combination of traits that contradicts itself:
// Trait may not be declared beside any of With, and Why is the sentence
// a designer is given instead of "invalid".
type TraitConflict struct {
	Trait string
	With  []string
	Why   string
}

// AnalysisTraitConflicts is every combination of admissible traits that
// cannot stand together.
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
func QuotedList(words []string) string {
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = fmt.Sprintf("%q", word)
	}
	return strings.Join(quoted, ", ")
}
