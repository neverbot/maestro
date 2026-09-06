package metamodel_test

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// traitsFixture is one game and one service, which is every one of these
// tests' whole setup.
func traitsFixture(t *testing.T) (*pgxpool.Pool, *metamodel.Service, uuid.UUID) {
	t.Helper()
	pool := testutil.NewPool(t)
	return pool, metamodel.New(pool, nil), newProject(t, pool)
}

// upsertRelationTypeWithTraits declares one relation type carrying the
// given traits, at whatever version it currently has, so each test states
// only the traits it is about.
func upsertRelationTypeWithTraits(t *testing.T, svc *metamodel.Service, project uuid.UUID,
	key string, traits []string,
) (dbqRelationTypeTraits, error) {
	t.Helper()
	ctx := context.Background()
	in := metamodel.RelationTypeInput{
		Key: key, Label: strings.ToUpper(key[:1]) + key[1:], AnalysisTraits: traits,
	}
	if existing, err := svc.RelationTypeByKey(ctx, project, key); err == nil {
		version := existing.Version
		in.ExpectedVersion = &version
	}
	row, err := svc.UpsertRelationType(ctx, project, in)
	return dbqRelationTypeTraits{ID: row.ID, Traits: row.AnalysisTraits}, err
}

// dbqRelationTypeTraits is the sliver of the row these tests read, named
// so a test body says what it is asserting rather than carrying a
// fourteen-field struct through.
type dbqRelationTypeTraits struct {
	ID     uuid.UUID
	Traits []string
}

// requireSchemaProblem asserts a refusal is invalid_schema, at the trait
// column's own path, and returns the message so the caller can assert
// what it *names* — which is the substance of every one of these
// refusals. A refusal that says something is wrong without saying what
// leaves a designer guessing which of two traits they meant.
func requireSchemaProblem(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("an incoherent trait declaration must be refused, and it was accepted")
	}
	if !errors.Is(err, metamodel.ErrInvalidSchema) {
		t.Fatalf("must be invalid_schema (a type declaration that cannot stand), got %T: %v",
			err, err)
	}
	var schemaErr *metamodel.SchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("must carry field paths, got %T: %v", err, err)
	}
	if len(schemaErr.Fields) == 0 {
		t.Fatal("a refusal with no field path is a refusal a caller cannot act on")
	}
	var messages []string
	for _, field := range schemaErr.Fields {
		if field.Path != "analysis_traits" {
			t.Fatalf("path = %q, want %q: the fault is in the trait list the caller sent",
				field.Path, "analysis_traits")
		}
		messages = append(messages, field.Message)
	}
	return strings.Join(messages, "; ")
}

// TestATraitOutsideTheVocabularyIsInvalidSchemaAndNotInternalError is
// review finding H3 asked of the second constrained column on
// relation_types.
//
// Without the Go-side check the value travels to Postgres, comes back as
// an untyped check-constraint violation (SQLSTATE 23514) that errors.Is
// matches nothing, and reaches an agent as internal_error: a server
// fault, with no path and no list of what would have been accepted, for
// something the agent typed. The assertion that the message names **all
// seven** traits is what makes the refusal actionable rather than merely
// correct.
func TestATraitOutsideTheVocabularyIsInvalidSchemaAndNotInternalError(t *testing.T) {
	_, svc, project := traitsFixture(t)

	// The positive control, in the same test: a coherent declaration is
	// accepted and read back, so a check that refused everything — a
	// vocabulary with a typo in it, a rule inverted — cannot pass this.
	row, err := upsertRelationTypeWithTraits(t, svc, project, "contains", []string{"containment"})
	if err != nil {
		t.Fatalf("a trait in the vocabulary must be accepted: %v", err)
	}
	if !slices.Equal(row.Traits, []string{"containment"}) {
		t.Fatalf("stored traits = %v, want [containment]", row.Traits)
	}

	_, err = upsertRelationTypeWithTraits(t, svc, project, "teleports", []string{"teleports"})
	message := requireSchemaProblem(t, err)
	if !strings.Contains(message, `"teleports"`) {
		t.Fatalf("the refusal must name the word that was refused, got %q", message)
	}
	for _, trait := range metamodel.AnalysisTraits {
		if !strings.Contains(message, `"`+trait+`"`) {
			t.Fatalf("the refusal must list every trait that would have been accepted; "+
				"%q is missing from %q", trait, message)
		}
	}
	// And it is not the database's untyped refusal wearing a Go type: a
	// 23514 reaching here would mean the Go check never ran.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Fatalf("the refusal must be Go's and not the constraint's backstop, got %s: %v",
			pgErr.Code, err)
	}
}

// TestSymmetricCannotGate pins the first coherence rule: an edge that
// means the same in both directions cannot also gate in one.
func TestSymmetricCannotGate(t *testing.T) {
	_, svc, project := traitsFixture(t)

	// Control: the coherent half alone is accepted, so the rule refuses a
	// combination rather than a word.
	if _, err := upsertRelationTypeWithTraits(t, svc, project,
		"connects_to", []string{"symmetric"}); err != nil {
		t.Fatalf("symmetric alone must be accepted: %v", err)
	}

	for _, gate := range []string{"prerequisite_of", "unlocks", "ordering"} {
		_, err := upsertRelationTypeWithTraits(t, svc, project,
			"borders_"+gate, []string{"symmetric", gate})
		message := requireSchemaProblem(t, err)
		if !strings.Contains(message, `"symmetric"`) || !strings.Contains(message, `"`+gate+`"`) {
			t.Fatalf("the refusal must name both traits it refuses together, got %q", message)
		}
		if !strings.Contains(message, "both directions") {
			t.Fatalf("the refusal must say why, got %q", message)
		}
	}
}

// TestAnnotationCannotShareATypeWithAnything pins the second rule, and
// pins it against **every** other word in the vocabulary rather than
// against one: `annotation`'s conflict list is derived from
// AnalysisTraits, and a derivation that quietly stopped covering a
// seventh trait is the shape this repository keeps producing.
func TestAnnotationCannotShareATypeWithAnything(t *testing.T) {
	_, svc, project := traitsFixture(t)

	if _, err := upsertRelationTypeWithTraits(t, svc, project,
		"mentions", []string{"annotation"}); err != nil {
		t.Fatalf("annotation alone must be accepted — it is how a type says it is "+
			"deliberately inert: %v", err)
	}

	for _, other := range metamodel.AnalysisTraits {
		if other == "annotation" {
			continue
		}
		_, err := upsertRelationTypeWithTraits(t, svc, project,
			"notes_"+other, []string{"annotation", other})
		message := requireSchemaProblem(t, err)
		if !strings.Contains(message, `"annotation"`) || !strings.Contains(message, `"`+other+`"`) {
			t.Fatalf("the refusal must name both, got %q", message)
		}
		if !strings.Contains(message, "inert") {
			t.Fatalf("the refusal must say why, got %q", message)
		}
	}
}

// TestATypeCannotGateInBothDirections pins the third rule.
func TestATypeCannotGateInBothDirections(t *testing.T) {
	_, svc, project := traitsFixture(t)

	if _, err := upsertRelationTypeWithTraits(t, svc, project,
		"requires", []string{"prerequisite_of"}); err != nil {
		t.Fatalf("prerequisite_of alone must be accepted: %v", err)
	}
	if _, err := upsertRelationTypeWithTraits(t, svc, project,
		"opens", []string{"unlocks"}); err != nil {
		t.Fatalf("unlocks alone must be accepted: %v", err)
	}

	_, err := upsertRelationTypeWithTraits(t, svc, project,
		"gates", []string{"prerequisite_of", "unlocks"})
	message := requireSchemaProblem(t, err)
	if !strings.Contains(message, `"prerequisite_of"`) || !strings.Contains(message, `"unlocks"`) {
		t.Fatalf("the refusal must name both, got %q", message)
	}
	if !strings.Contains(message, "two relation types") {
		t.Fatalf("the refusal must name the recovery, got %q", message)
	}
}

// TestATraitDeclaredTwiceIsRefusedAsATypo. A repeated trait cannot mean
// anything a caller intended, and folding it silently would make what
// comes back differ from what was sent.
func TestATraitDeclaredTwiceIsRefusedAsATypo(t *testing.T) {
	_, svc, project := traitsFixture(t)

	if _, err := upsertRelationTypeWithTraits(t, svc, project,
		"leads_to", []string{"ordering"}); err != nil {
		t.Fatalf("the control must be accepted: %v", err)
	}

	_, err := upsertRelationTypeWithTraits(t, svc, project,
		"follows", []string{"ordering", "ordering"})
	message := requireSchemaProblem(t, err)
	if !strings.Contains(message, `"ordering"`) || !strings.Contains(message, "twice") {
		t.Fatalf("the refusal must name the repeated word, got %q", message)
	}
}

// TestARedundantAcyclicIsAcceptedAndReadBackUnchanged.
//
// prerequisite_of implies acyclic, so declaring both says nothing new —
// and stripping the redundant word would make what a caller reads back
// differ from what it sent, which is the round-trip rule
// relation_types.upsert's own description states.
func TestARedundantAcyclicIsAcceptedAndReadBackUnchanged(t *testing.T) {
	_, svc, project := traitsFixture(t)

	row, err := upsertRelationTypeWithTraits(t, svc, project,
		"requires", []string{"prerequisite_of", "acyclic"})
	if err != nil {
		t.Fatalf("a redundant acyclic must be accepted: %v", err)
	}
	if !slices.Equal(row.Traits, []string{"prerequisite_of", "acyclic"}) {
		t.Fatalf("stored traits = %v, want both words in the order they were sent", row.Traits)
	}
	read, err := svc.RelationTypeByKey(context.Background(), project, "requires")
	if err != nil {
		t.Fatalf("read the type back: %v", err)
	}
	if !slices.Equal(read.AnalysisTraits, []string{"prerequisite_of", "acyclic"}) {
		t.Fatalf("read back = %v, want [prerequisite_of acyclic]", read.AnalysisTraits)
	}
}

// TestTraitsSurviveARenameOfTheirType.
//
// Traits hang off the relation type's row and a rename moves the key on
// that same row, so this ought to hold — and asserting it is cheap while
// the alternative is discovering it from an analysis that silently
// stopped reading a gate.
func TestTraitsSurviveARenameOfTheirType(t *testing.T) {
	_, svc, project := traitsFixture(t)
	ctx := context.Background()

	before, err := upsertRelationTypeWithTraits(t, svc, project,
		"available_to", []string{"unlocks"})
	if err != nil {
		t.Fatalf("declare the type: %v", err)
	}
	current, err := svc.RelationTypeByKey(ctx, project, "available_to")
	if err != nil {
		t.Fatalf("read before renaming: %v", err)
	}
	version := current.Version
	renamed, err := svc.RenameRelationType(ctx, project, metamodel.RenameInput{
		From: "available_to", To: "usable_by", ExpectedVersion: &version,
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.ID != before.ID {
		t.Fatalf("a rename must move the key on the same row, got %s want %s",
			renamed.ID, before.ID)
	}
	if !slices.Equal(renamed.AnalysisTraits, []string{"unlocks"}) {
		t.Fatalf("the rename's answer = %v, want [unlocks]", renamed.AnalysisTraits)
	}
	read, err := svc.RelationTypeByKey(ctx, project, "usable_by")
	if err != nil {
		t.Fatalf("read by the new key: %v", err)
	}
	if !slices.Equal(read.AnalysisTraits, []string{"unlocks"}) {
		t.Fatalf("read back under the new key = %v, want [unlocks]", read.AnalysisTraits)
	}
}

// TestClearingTraitsMakesATypeUndeclaredAndNotInert separates the two
// states the whole column exists to carry: NULL is *undeclared* — this
// type has never been given an opinion — and `{annotation}` is
// *deliberately inert*. An empty array would be a third spelling of one
// of them, and the constraint refuses it outright.
//
// This is the only test that separates them on the write path.
func TestClearingTraitsMakesATypeUndeclaredAndNotInert(t *testing.T) {
	pool, svc, project := traitsFixture(t)
	ctx := context.Background()

	if _, err := upsertRelationTypeWithTraits(t, svc, project,
		"opens", []string{"unlocks"}); err != nil {
		t.Fatalf("declare the type: %v", err)
	}
	// The control: it really was declared a moment ago, so the NULL
	// below is a clearing and not a write that never landed.
	if got := storedTraits(t, pool, project, "opens"); got == nil {
		t.Fatal("the control must have stored the declaration it made")
	}

	if _, err := upsertRelationTypeWithTraits(t, svc, project,
		"opens", []string{}); err != nil {
		t.Fatalf("an explicit empty list must be accepted as a clearing: %v", err)
	}
	if got := storedTraits(t, pool, project, "opens"); got != nil {
		t.Fatalf("stored = %v, want SQL NULL: clearing a type's traits makes it "+
			"undeclared, and an empty array is refused by the column outright", got)
	}
	read, err := svc.RelationTypeByKey(ctx, project, "opens")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if read.AnalysisTraits != nil {
		t.Fatalf("read back = %v, want nil (undeclared)", read.AnalysisTraits)
	}
	// And it is emphatically not the inert declaration, which is the
	// confusion the column's shape exists to make impossible.
	if slices.Contains(read.AnalysisTraits, "annotation") {
		t.Fatal("clearing traits must not be stored as {annotation}: undeclared and " +
			"deliberately inert are two different statements about a game")
	}
}

// storedTraits reads the column straight out of the table, past every Go
// reader, because "what the database is holding" is the question and a
// read path that mapped NULL onto `{}` would make the test agree with
// the bug.
func storedTraits(t *testing.T, pool *pgxpool.Pool, project uuid.UUID, key string) []string {
	t.Helper()
	var traits []string
	err := pool.QueryRow(context.Background(),
		`SELECT analysis_traits FROM relation_types WHERE project_id = $1 AND lower(key) = lower($2)`,
		project, key).Scan(&traits)
	if err != nil {
		t.Fatalf("read stored traits for %q: %v", key, err)
	}
	return traits
}

// traitLiteral picks the quoted words out of a CHECK constraint's own
// text.
var traitLiteral = regexp.MustCompile(`'([a-z_]+)'::text`)

// TestTheTraitVocabularyIsOneSetInGoAndInTheDatabase is the bidirectional
// guard the whole pairing rests on, and it is bidirectional on purpose:
// a one-way test passes against a constraint that has grown an eighth
// word no Go reader knows about, and passes against a Go slice that has
// grown one the column will refuse at write time with an untyped 23514.
//
//   - Go → database, behaviourally: every word in metamodel.AnalysisTraits
//     is written through the service and lands. A word Go offers that the
//     column refuses fails here.
//   - database → Go, textually: every literal in
//     relation_types_traits_vocab's own definition, read back from
//     pg_get_constraintdef, is in metamodel.AnalysisTraits. A word the
//     column admits that no Go reader knows about fails here — and it is
//     the direction that matters most, because the six places a trait has
//     to reach (this slice, the coherence rules, the resolver, the tool
//     description, the analysis package's alias and its semantics_source)
//     all hang off the Go list.
func TestTheTraitVocabularyIsOneSetInGoAndInTheDatabase(t *testing.T) {
	pool, svc, project := traitsFixture(t)

	// Go → database. Each trait alone, because the coherence rules refuse
	// several combinations and the question here is the vocabulary and
	// not the combinations.
	for _, trait := range metamodel.AnalysisTraits {
		row, err := upsertRelationTypeWithTraits(t, svc, project, "edge_"+trait, []string{trait})
		if err != nil {
			t.Fatalf("the database refused %q, which metamodel.AnalysisTraits offers: %v",
				trait, err)
		}
		if !slices.Equal(row.Traits, []string{trait}) {
			t.Fatalf("stored %v for %q", row.Traits, trait)
		}
	}

	// database → Go. The constraint's own text, not a second literal.
	var definition string
	err := pool.QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		  WHERE conname = 'relation_types_traits_vocab'`).Scan(&definition)
	if err != nil {
		t.Fatalf("read the constraint definition: %v", err)
	}
	matches := traitLiteral.FindAllStringSubmatch(definition, -1)
	if len(matches) == 0 {
		t.Fatalf("no trait literals found in %q: this test's own reading of the "+
			"constraint is broken, which would make it pass against anything", definition)
	}
	inDatabase := make([]string, 0, len(matches))
	for _, match := range matches {
		inDatabase = append(inDatabase, match[1])
	}
	for _, trait := range inDatabase {
		if !slices.Contains(metamodel.AnalysisTraits, trait) {
			t.Fatalf("the column admits %q and metamodel.AnalysisTraits does not offer it: "+
				"a trait the database accepts that no Go reader knows about is a gate no "+
				"analysis will ever read. Constraint: %s", trait, definition)
		}
	}
	if len(inDatabase) != len(metamodel.AnalysisTraits) {
		t.Fatalf("the constraint lists %d traits and metamodel.AnalysisTraits carries %d: "+
			"%v against %v", len(inDatabase), len(metamodel.AnalysisTraits),
			inDatabase, metamodel.AnalysisTraits)
	}
}

// TestEveryRefusedCombinationIsRefusedAndEveryRefusalIsInTheTable is the
// other bidirectional guard: the coherence check and
// AnalysisTraitConflicts are one rule set, not two that agree today.
//
// Forwards, every row of the table is actually refused. Backwards, every
// *pair* of traits the check refuses is named by some row of the table —
// checked by trying all of them, which is 21 pairs and therefore cheap.
// A rule hard-coded into the check but missing from the table would ship
// a refusal no generated description mentions, which is precisely a
// mechanism nothing reads.
func TestEveryRefusedCombinationIsRefusedAndEveryRefusalIsInTheTable(t *testing.T) {
	_, svc, project := traitsFixture(t)
	if len(metamodel.AnalysisTraitConflicts) == 0 {
		t.Fatal("the conflict table is empty, so every assertion below is vacuous")
	}

	tabled := func(a, b string) bool {
		for _, conflict := range metamodel.AnalysisTraitConflicts {
			if conflict.Trait == a && slices.Contains(conflict.With, b) {
				return true
			}
			if conflict.Trait == b && slices.Contains(conflict.With, a) {
				return true
			}
		}
		return false
	}

	n := 0
	for i, a := range metamodel.AnalysisTraits {
		for _, b := range metamodel.AnalysisTraits[i+1:] {
			n++
			_, err := upsertRelationTypeWithTraits(t, svc, project,
				"pair_"+a+"_"+b, []string{a, b})
			refused := err != nil
			if refused && !errors.Is(err, metamodel.ErrInvalidSchema) {
				t.Fatalf("{%s,%s} failed for a reason that is not a coherence refusal: %v",
					a, b, err)
			}
			if refused != tabled(a, b) {
				t.Fatalf("{%s,%s}: the check %s it and AnalysisTraitConflicts %s it",
					a, b,
					map[bool]string{true: "refuses", false: "accepts"}[refused],
					map[bool]string{true: "names", false: "does not name"}[tabled(a, b)])
			}
		}
	}
	if n != len(metamodel.AnalysisTraits)*(len(metamodel.AnalysisTraits)-1)/2 {
		t.Fatalf("checked %d pairs, which is not every pair of %d traits",
			n, len(metamodel.AnalysisTraits))
	}
}
