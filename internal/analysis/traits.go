package analysis

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
)

// Traits and TraitConflicts are the metamodel's, aliased rather than
// redeclared.
var (
	Traits         = metamodel.AnalysisTraits
	TraitConflicts = metamodel.AnalysisTraitConflicts
)

// Source says where one relation type's traits came from, and every
// result embeds it per type.
type Source string

const (
	// SourceDeclared: the relation type's own analysis_traits column.
	// The strongest statement a game can make, and the only one that is
	// unambiguous.
	SourceDeclared Source = "declared"
	// SourceDerivedFromRole: the type declares no traits, but carries a
	// semantic_role this engine translates. It is a **translation of
	// something the game already declared**, not inference from usage,
	// and it exists so a game seeded before traits existed analyses
	// without an edit.
	SourceDerivedFromRole Source = "derived_from_role"
	// SourceCallerSupplied: the caller named this relation type on the
	// call, which shadows the selection rules entirely — see Resolve.
	SourceCallerSupplied Source = "caller_supplied"
)

// Sources is every value of Source, so a reader can enumerate them
// rather than repeat them. TestEverySourceIsReachable holds the resolver
// against it.
var Sources = []Source{SourceDeclared, SourceDerivedFromRole, SourceCallerSupplied}

// derivedTraits is the fixed mapping from a semantic_role to the traits
// an analysis reads it as, and it is a translation rather than a guess:
// every entry restates in behavioural terms something the game already
// said in descriptive ones.
var derivedTraits = map[string][]string{
	"prerequisite": {"prerequisite_of"},
	"unlock":       {"unlocks"},
	"containment":  {"containment"},
	"spatial":      {"symmetric"},
	"availability": {"unlocks"},
	"reward":       {"annotation"},
}

// TypeSemantics is one relation type as this engine reads it.
type TypeSemantics struct {
	ID     uuid.UUID `json:"-"`
	Key    string    `json:"key"`
	Traits []string  `json:"analysis_traits"`
	Source Source    `json:"source"`
}

// Semantics is the resolved reading of a whole game's relation types:
// which ones this run walks, how each behaves, and where that came from.
type Semantics struct {
	// ByType is keyed by relation type id, which is what a walk's rows
	// carry. Nothing outside this package addresses a relation type by
	// id, so the map is converted to a keyed list on the way out.
	ByType map[uuid.UUID]TypeSemantics
}

// Types lists the resolved semantics in key order, for an answer a
// caller reads. Sorted rather than map-ordered so two runs over one
// unchanged game produce the same document.
func (s Semantics) Types() []TypeSemantics {
	out := make([]TypeSemantics, 0, len(s.ByType))
	for _, entry := range s.ByType {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// WithTrait lists the ids of every resolved relation type carrying one
// trait. It is how reach.go and cycles.go turn a reading into the three
// id arrays their edge predicate is spliced with.
func (s Semantics) WithTrait(trait string) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(s.ByType))
	for id, entry := range s.ByType {
		for _, declared := range entry.Traits {
			if declared == trait {
				ids = append(ids, id)
				break
			}
		}
	}
	return ids
}

// ResolveInput is what a caller may say about which relation types an
// analysis reads.
type ResolveInput struct {
	// RelationTypeKeys, when non-empty, is the caller's own choice of
	// which relation types this run walks. It shadows the selection
	// rules entirely; see Resolve.
	RelationTypeKeys []string
}

// Resolve reads a game's relation types and decides how this engine will
// treat each of them. It is the spec's §2, in order, and it does not
// guess:
func (s *Service) Resolve(ctx context.Context, projectID uuid.UUID, in ResolveInput) (Semantics, error) {
	if len(in.RelationTypeKeys) > MaxTypeKeys {
		return Semantics{}, limitExceeded("relation_types", len(in.RelationTypeKeys), MaxTypeKeys)
	}
	// Read through the metamodel service, which already takes a project
	// id and already filters on it in SQL. That is this package's
	// isolation decision made once rather than reimplemented as a fourth
	// copy of the same WHERE clause — the reasoning internal/views'
	// LoadCatalogue records.
	rows, err := s.meta.ListRelationTypes(ctx, projectID)
	if err != nil {
		return Semantics{}, fmt.Errorf("read the relation type catalogue: %w", err)
	}

	if len(in.RelationTypeKeys) > 0 {
		return s.resolveCallerSupplied(rows, in.RelationTypeKeys)
	}

	resolved := catalogueSemantics(rows)
	if len(resolved.ByType) == 0 {
		return Semantics{}, undeclared(rows)
	}
	return resolved, nil
}

// catalogueSemantics is steps 2 and 3 alone: declared traits, then
// traits derived from a semantic_role, and **no refusal**.
func catalogueSemantics(rows []dbqRelationType) Semantics {
	resolved := Semantics{ByType: make(map[uuid.UUID]TypeSemantics, len(rows))}
	for _, row := range rows {
		if len(row.AnalysisTraits) > 0 {
			resolved.ByType[row.ID] = TypeSemantics{
				ID: row.ID, Key: row.Key, Traits: row.AnalysisTraits, Source: SourceDeclared,
			}
			continue
		}
		if row.SemanticRole == nil {
			continue
		}
		traits, ok := derivedTraits[*row.SemanticRole]
		if !ok {
			// A role the mapping does not carry. It cannot happen while
			// TestEverySemanticRoleHasADerivedTraitSet passes, and it is
			// skipped rather than guessed at if it ever does: a role
			// nobody translated is a role this engine has not been told
			// how to read.
			continue
		}
		resolved.ByType[row.ID] = TypeSemantics{
			ID: row.ID, Key: row.Key, Traits: traits, Source: SourceDerivedFromRole,
		}
	}
	return resolved
}

// ResolveWithoutRefusing reads a game's relation types the way Resolve
// does and **answers an empty reading rather than semantics_undeclared**.
func (s *Service) ResolveWithoutRefusing(ctx context.Context, projectID uuid.UUID) (
	Semantics, error,
) {
	rows, err := s.meta.ListRelationTypes(ctx, projectID)
	if err != nil {
		return Semantics{}, fmt.Errorf("read the relation type catalogue: %w", err)
	}
	return catalogueSemantics(rows), nil
}

// resolveCallerSupplied is step 1: the caller's list is the set.
func (s *Service) resolveCallerSupplied(rows []dbqRelationType, keys []string) (Semantics, error) {
	byKey := make(map[string]dbqRelationType, len(rows))
	for _, row := range rows {
		byKey[strings.ToLower(row.Key)] = row
	}
	resolved := Semantics{ByType: make(map[uuid.UUID]TypeSemantics, len(keys))}
	seen := make(map[string]int, len(keys))
	for i, key := range keys {
		lowered := strings.ToLower(key)
		row, ok := byKey[lowered]
		if !ok {
			// Named, and never silently dropped. A filter that quietly
			// lost one of its terms answers a different question in the
			// same shape.
			return Semantics{}, fmt.Errorf(
				"%w: relation_types[%d] names no relation type of this game: %q",
				ErrNotFound, i, key)
		}
		if first, repeated := seen[lowered]; repeated {
			return Semantics{}, invalidInput(fmt.Sprintf("relation_types[%d]", i),
				fmt.Sprintf("names the same relation type as element %d; keys are matched "+
					"without regard to case and this list is a set", first))
		}
		seen[lowered] = i
		traits := row.AnalysisTraits
		if len(traits) == 0 && row.SemanticRole != nil {
			traits = derivedTraits[*row.SemanticRole]
		}
		resolved.ByType[row.ID] = TypeSemantics{
			ID: row.ID, Key: row.Key, Traits: traits, Source: SourceCallerSupplied,
		}
	}
	withTraits := false
	for _, entry := range resolved.ByType {
		if len(entry.Traits) > 0 {
			withTraits = true
			break
		}
	}
	if !withTraits {
		return Semantics{}, undeclared(rows)
	}
	return resolved, nil
}

// undeclared builds the refusal, carrying the whole catalogue.
func undeclared(rows []dbqRelationType) error {
	report := make([]TypeReport, 0, len(rows))
	for _, row := range rows {
		entry := TypeReport{Key: row.Key, Traits: row.AnalysisTraits}
		if row.SemanticRole != nil {
			entry.SemanticRole = *row.SemanticRole
		}
		report = append(report, entry)
	}
	return &UndeclaredError{Types: report, Advice: undeclaredAdvice()}
}

// TraitDescription is the agent-facing account of the whole trait
// vocabulary: what each word is for, which combinations are refused, and
// which semantic roles this engine translates when a type declares no
// traits.
func TraitDescription() string {
	var b strings.Builder
	b.WriteString("An analysis reads a game through its relation types' `analysis_traits`. " +
		"The vocabulary is closed and every word is one of: ")
	b.WriteString(metamodel.QuotedList(Traits))
	b.WriteString(".\n\nWhat each word says about an edge of that type:\n")
	for _, trait := range Traits {
		fmt.Fprintf(&b, "  - %q: %s\n", trait, traitMeanings[trait])
	}
	b.WriteString("\nCombinations that contradict themselves are refused as invalid_schema " +
		"at path `analysis_traits`, naming what is wrong rather than that something is:\n")
	for _, line := range metamodel.TraitConflictLines() {
		b.WriteString("  - " + line + "\n")
	}
	b.WriteString("\nA type that declares no traits but carries a `semantic_role` is read " +
		"through this fixed translation, so a game seeded before traits existed analyses " +
		"without an edit:\n")
	for _, role := range metamodel.SemanticRoles {
		traits, ok := derivedTraits[role]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "  - semantic_role %q is read as %s\n",
			role, metamodel.QuotedList(traits))
	}
	b.WriteString("\nA type with neither is undeclared and no analysis walks it. " +
		"A game where nothing is declared is refused with `" + CodeSemanticsUndeclared +
		"` rather than reported healthy: an engine that had nothing to read must not " +
		"answer that nothing is wrong.")
	return b.String()
}

// traitMeanings is one line per word, and it is the one hand-written
// half of TraitDescription — a sentence cannot be derived from a string.
// It is held against the vocabulary in both directions by
// TestTheTraitDescriptionNamesEveryTraitAndEveryTraitIsNamed, so a word
// added to the vocabulary with no meaning here fails a test rather than
// reaching an agent as a bare name.
var traitMeanings = map[string]string{
	"prerequisite_of": "the **target** must be satisfied before the source, which is " +
		"the edge read as \"A requires B\". The engine follows it target\u2192source when it " +
		"normalises, so a gate declared this way points backwards along the edge",
	"unlocks": "the **source** must be satisfied before the target, which is the edge " +
		"read as \"A unlocks B\". It is followed source\u2192target, which is already the " +
		"normalised direction, so nothing about it is reversed",
	"containment": "the target is inside the source, so reaching the source reaches " +
		"everything it holds",
	"ordering": "the **source** comes before the target, which is the same direction " +
		"`unlocks` is read in and is deliberately not a second convention. A route whose " +
		"steps put the target first is `out_of_order` at the step that arrived late. It " +
		"gates as well as orders: reaching the source is what makes the target reachable",
	"symmetric": "the edge means the same read from either end, so a walk may follow " +
		"it in both directions",
	"acyclic": "edges of this type must not form a loop; a loop in them is a finding. " +
		"prerequisite_of, unlocks, ordering and containment each already imply it, so " +
		"declaring it beside one of them is redundant and accepted unchanged",
	"annotation": "this type is deliberately inert: no analysis follows its edges. It " +
		"is how a game says 'nothing to see here', which is a different statement from " +
		"declaring nothing at all",
}
