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
//
// **The dependency runs metamodel → nothing and analysis → metamodel,
// and the vocabulary sits at the bottom of it.** The obvious placement
// is here, in the package that reads traits — but this package depends
// on internal/metamodel for FieldError, Actor and every reused sentinel,
// so a vocabulary here would need the reverse edge and there is no
// acyclic way to have both. The vocabulary therefore lives beside the
// column it constrains and beside the upsert that writes it
// (relation_types.upsert is where a trait combination arrives), and this
// package aliases it. Two copies would be two lists that drift, which is
// exactly what metamodel.SemanticRoles' own comment says about the CHECK
// beside it.
//
// This inversion is stated in both files because it is the sort of thing
// a later reader reverses "for tidiness". Reversing it does not compile.
var (
	Traits         = metamodel.AnalysisTraits
	TraitConflicts = metamodel.AnalysisTraitConflicts
)

// Source says where one relation type's traits came from, and every
// result embeds it per type.
//
// It exists so a designer can see that the engine treated `available_to`
// as a gate because of a role they set months ago, rather than
// discovering it from a finding they cannot explain. A mechanism nothing
// reads is a lie; this is the mechanism that makes the resolver's three
// sources readable.
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
//
// It is checked **in both directions** against metamodel.SemanticRoles —
// TestEverySemanticRoleHasADerivedTraitSet and
// TestEveryDerivedTraitSetNamesARealRole — so a seventh role added over
// there fails the first and a typo'd trait in here fails the second.
// That bidirectional guard is the pattern the views sub-project ranked
// fifth among the things that actually caught defects, and this is the
// cheapest place in the whole engine to apply it.
//
// `reward` maps to {annotation} and not to a gate, deliberately: a
// reward edge says what a player gets, which is a fact about content and
// not a constraint on order, and reading it as a gate would make every
// rewarding quest a prerequisite for its own reward.
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
//
// Every analysis result embeds it — as `semantics_source` on the wire —
// so a verdict always arrives with the reading it rests on. An engine
// whose interpretation of a game is invisible produces findings a
// designer can only accept or reject wholesale.
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
//
//  1. If the caller passed relation_types keys, **those are the set**,
//     and the declared/derived selection below is not consulted for
//     which types participate: the caller has said which edges this
//     question is about, and that is a stronger statement than anything
//     stored. Each such type is reported as caller_supplied. A key that
//     names no relation type of **this game** is not_found naming the
//     key — never a silently empty filter, which would answer "your
//     whole game is unreachable" to a caller who did narrow the run.
//  2. Otherwise, every relation type with non-null analysis_traits →
//     declared.
//  3. For a type whose traits are null but whose semantic_role is set,
//     the fixed mapping → derived_from_role.
//  4. If nothing remains, semantics_undeclared, carrying the whole
//     catalogue. **Not "no problems found".**
//
// A type that is neither declared nor derivable is simply not in the
// result: it is an edge this engine has been told nothing about, and
// walking it as though it gated progression would be exactly the guess
// the metamodel exists to avoid.
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
//
// It is separated from Resolve because one analysis reads a game's
// traits without needing them: the orphan aggregate asks the vocabulary
// for exactly one thing -- which relation types are `annotation` -- and a
// game with no traits and no roles has no annotation type, which is a
// perfectly meaningful input rather than an engine with nothing to read.
// Resolve's refusal is right for the three analyses that walk edges and
// wrong for the one that counts them, so the refusal sits in Resolve and
// the reading sits here. orphans.go's own comment names the asymmetry
// from the other side.
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
//
// It has exactly one caller, analysis.orphans, and the argument for the
// asymmetry is that analysis's own: see catalogueSemantics above and
// Orphans' doc comment, which names the three analyses that do refuse so
// the difference reads as a decision rather than as an oversight.
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
//
// Its traits still come from the row — declared first, then derived —
// because "which types this question is about" and "how those types
// behave" are two different statements and the caller only made the
// first. A named type that carries neither is kept in the set with no
// traits and reported as caller_supplied, so it appears in
// semantics_source; if *none* of the named types carries any traits, the
// run has a set and no behaviour and falls to the same
// semantics_undeclared refusal as an undeclared game.
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
//
// **It is generated from the structures it describes** — the vocabulary,
// metamodel.AnalysisTraitConflicts and derivedTraits — and parsed back
// in three tests, in both directions each. Generating agent-facing text
// from the structure it describes is the first thing the views
// sub-project named as worth copying: it is what caught views.run
// shipping without its operator table at all. Prose written by hand
// beside a table is prose that stops being true on the first edit to the
// table, and an agent has no way to notice.
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
	"ordering": "the edges of this type impose a sequence on what they join",
	"symmetric": "the edge means the same read from either end, so a walk may follow " +
		"it in both directions",
	"acyclic": "edges of this type must not form a loop; a loop in them is a finding. " +
		"prerequisite_of, unlocks, ordering and containment each already imply it, so " +
		"declaring it beside one of them is redundant and accepted unchanged",
	"annotation": "this type is deliberately inert: no analysis follows its edges. It " +
		"is how a game says 'nothing to see here', which is a different statement from " +
		"declaring nothing at all",
}
