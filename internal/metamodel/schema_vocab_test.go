package metamodel_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// The two closed vocabularies this package owns, each pinned in both
// directions against its *other* copy.
//
// A closed vocabulary in this repository is never written once. A field
// type is a constant and a member of a list; a semantic role is a member
// of a list and a literal inside a CHECK constraint. Every one of those
// pairings has drifted somewhere in this repository's history, and the
// drift is silent in the expensive direction: the copy an agent reads
// grows a word the copy that enforces does not admit.

// TestFieldTypesListsEveryDeclaredFieldType compares metamodel.FieldTypes
// against the constants declared in schema.go, parsed out of the source
// rather than written down again here — a second hand-written list would
// be a third copy, and the test would pass by agreeing with itself.
//
// Both directions: a constant that is not in the list (a seventh type
// declared and never offered), and a member of the list that no constant
// declares (a word offered to agents that no writer produces).
func TestFieldTypesListsEveryDeclaredFieldType(t *testing.T) {
	declared := fieldTypeConstantsInSource(t)

	// The precision fixture, before either comparison: this parse must
	// have found the constants it claims to be comparing. Two empty sets
	// compare equal, which is how a guard of this shape passes against a
	// file it can no longer read.
	if len(declared) != len(metamodel.FieldTypes) || len(declared) < 6 {
		t.Fatalf("schema.go declares %d FieldType constants (%v) and metamodel.FieldTypes "+
			"carries %d (%v)", len(declared), declared, len(metamodel.FieldTypes),
			metamodel.FieldTypes)
	}

	for _, value := range declared {
		if !slices.Contains(metamodel.FieldTypes, metamodel.FieldType(value)) {
			t.Errorf("schema.go declares the field type %q and metamodel.FieldTypes does not "+
				"list it: a type nothing offers is a type no agent can ask for", value)
		}
	}
	for _, listed := range metamodel.FieldTypes {
		if !slices.Contains(declared, string(listed)) {
			t.Errorf("metamodel.FieldTypes offers %q and schema.go declares no such constant: "+
				"the list is offering a word the package does not know", listed)
		}
	}
}

// TestEveryListedFieldTypeIsAcceptedByValidate carries the list one step
// along, which is where a vocabulary usually dies: correct in its own
// declaration and unknown to the code that enforces it. Every type the
// list offers must be a type Validate accepts, or the bundle would name
// a value the server refuses as unknown.
func TestEveryListedFieldTypeIsAcceptedByValidate(t *testing.T) {
	if len(metamodel.FieldTypes) == 0 {
		t.Fatal("metamodel.FieldTypes is empty, so every assertion below is vacuous")
	}
	for _, fieldType := range metamodel.FieldTypes {
		field := metamodel.Field{Key: "f", Type: fieldType}
		if fieldType == metamodel.FieldEnum {
			field.Options = []string{"a", "b"}
		}
		if err := (metamodel.Schema{field}).Check(); err != nil {
			t.Errorf("Check refuses the declared field type %q: %v", fieldType, err)
		}
	}
	// And the other side of the same step: a type nothing declares is
	// still refused, so the check above is not passing because Check
	// accepts anything.
	if err := (metamodel.Schema{{Key: "f", Type: "colour"}}).Check(); err == nil {
		t.Error("Check accepted the undeclared field type \"colour\": the membership test " +
			"above proves nothing if everything is a member")
	}
}

// fieldTypeConstantsInSource parses schema.go and returns the value of
// every constant declared with the type FieldType.
func fieldTypeConstantsInSource(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "schema.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing schema.go: %v", err)
	}
	var out []string
	for _, decl := range file.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			name, ok := value.Type.(*ast.Ident)
			if !ok || name.Name != "FieldType" {
				continue
			}
			for _, expr := range value.Values {
				literal, ok := expr.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("unquoting %s: %v", literal.Value, err)
				}
				out = append(out, unquoted)
			}
		}
	}
	sort.Strings(out)
	return out
}

// roleLiteral picks the quoted words out of a CHECK constraint's own
// text, the way traits_test.go's traitLiteral does for
// relation_types_traits_vocab.
var roleLiteral = regexp.MustCompile(`'([a-z_]+)'::text`)

// TestTheSemanticRoleVocabularyIsOneSetInGoAndInTheDatabase is the same
// bidirectional pairing internal/analysis established for
// analysis_traits, applied to the one other vocabulary in this
// repository that lives in Go and in a CHECK constraint at once.
//
// It is deliberately asymmetric, because the two directions cannot be
// asserted the same way:
//
//   - **Go → database, behaviourally.** Every role in
//     metamodel.SemanticRoles is written through the service and lands.
//     A word Go offers that the column refuses fails here, as an untyped
//     23514 that would reach an agent as internal_error.
//     TestARelationTypeSemanticRoleIsCheckedHereAndNotOnlyByTheDatabase
//     in relations_test.go is where that half already lives; it is named
//     here so this pairing can be read as one rule.
//   - **database → Go, textually.** Postgres cannot enumerate what its
//     own CHECK admits — there is no "list the values this constraint
//     would accept" — so the only way to ask the column what it knows is
//     to read pg_get_constraintdef and parse its literals. A word the
//     column admits that no Go reader knows about is invisible to every
//     behavioural test that could be written, because nothing would ever
//     send it.
//
// That is the direction that matters most: the bundle, the tool
// descriptions and every view built on roles all hang off the Go list.
func TestTheSemanticRoleVocabularyIsOneSetInGoAndInTheDatabase(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()

	var definition string
	err := pool.QueryRow(ctx,
		`SELECT pg_get_constraintdef(c.oid)
		   FROM pg_constraint c
		   JOIN pg_class t ON t.oid = c.conrelid
		  WHERE t.relname = 'relation_types'
		    AND c.contype = 'c'
		    AND pg_get_constraintdef(c.oid) LIKE '%semantic_role%'`).Scan(&definition)
	if err != nil {
		t.Fatalf("read the semantic_role constraint definition: %v", err)
	}

	matches := roleLiteral.FindAllStringSubmatch(definition, -1)
	if len(matches) == 0 {
		t.Fatalf("no role literals found in %q: this test's own reading of the constraint "+
			"is broken, which would make it pass against anything", definition)
	}
	inDatabase := make([]string, 0, len(matches))
	for _, match := range matches {
		inDatabase = append(inDatabase, match[1])
	}

	for _, role := range inDatabase {
		if !slices.Contains(metamodel.SemanticRoles, role) {
			t.Errorf("the column admits %q and metamodel.SemanticRoles does not offer it: a "+
				"role the database accepts that no Go reader knows about is a classification "+
				"no view will ever draw. Constraint: %s", role, definition)
		}
	}
	if len(inDatabase) != len(metamodel.SemanticRoles) {
		t.Errorf("the constraint lists %d roles and metamodel.SemanticRoles carries %d: %v "+
			"against %v", len(inDatabase), len(metamodel.SemanticRoles), inDatabase,
			metamodel.SemanticRoles)
	}
}
