package analysis

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/metamodel"
)

// TestEveryBoundIsExported reads bounds.go's own AST and fails on an
// unexported const in it.
//
// It is the bidirectional guard for the rule that file's comment states,
// which is correction 24's: **a bound a caller cannot read is a bound a
// caller trips over.** Stating the rule in a comment leaves it true for
// the constants that are there today and silent about the eleventh one a
// later task adds; this is what makes it true for that one.
func TestEveryBoundIsExported(t *testing.T) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "bounds.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse bounds.go: %v", err)
	}
	found := 0
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range value.Names {
				found++
				if !name.IsExported() {
					t.Fatalf("bounds.go declares the unexported const %q at %s: every "+
						"bound in this file is part of the contract, because a caller "+
						"that cannot read a cap discovers it by tripping over it",
						name.Name, fset.Position(name.Pos()))
				}
			}
		}
	}
	// The positive control. A test that walked no declarations at all —
	// a renamed file, a parser that returned an empty tree — would pass
	// silently, and this is a file whose whole subject is a list.
	if found < 10 {
		t.Fatalf("only %d consts found in bounds.go; this test is not reading the file "+
			"it thinks it is", found)
	}
}

// TestTheCapsRefuseRatherThanClamp is the behavioural half of the rule
// bounds.go states in prose, asserted on the one caller-settable bound
// this task ships.
//
// The distinction is deliberate and is not new: **a declared limit is
// refused, a page limit is clamped.** metamodel.MaxBulkItems refuses,
// paging.Size clamps, and this package sits on the refusing side of that
// line for every bound a caller can state — because an answer computed
// under a bound the caller did not ask for is indistinguishable from a
// complete one, and this engine's whole value is that a designer can
// trust what it says.
func TestTheCapsRefuseRatherThanClamp(t *testing.T) {
	g := newGame(t)
	g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})

	keys := make([]string, 0, MaxTypeKeys+1)
	keys = append(keys, "requires")
	for len(keys) < MaxTypeKeys+1 {
		keys = append(keys, "requires")
	}
	_, err := g.analysis.Resolve(t.Context(), g.projectID, ResolveInput{RelationTypeKeys: keys})
	if err == nil {
		t.Fatal("a list above the cap must be refused; a run that silently trimmed it " +
			"would answer a narrower question in the same shape")
	}
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("err = %T %v, want limit_exceeded and not, say, the duplicate refusal: "+
			"the bound is judged before the list's contents are", err, err)
	}
}

// TestTheStatementBudgetIsClampedToItsHardCap is the *other* side of the
// same rule, and the two are in one file on purpose. The statement
// budget is the package's one clamp, and it is allowed to be one because
// no caller can set it: there is nobody to mislead about the bound their
// answer was computed under.
func TestTheStatementBudgetIsClampedToItsHardCap(t *testing.T) {
	g := newGame(t)
	if got := g.analysis.statementBudget(); got != DefaultStatementTimeout {
		t.Fatalf("default budget = %s, want %s", got, DefaultStatementTimeout)
	}
	g.analysis.statementTimeout = HardStatementTimeout + time.Hour
	if got := g.analysis.statementBudget(); got != HardStatementTimeout {
		t.Fatalf("budget = %s, want it clamped to %s", got, HardStatementTimeout)
	}
	g.analysis.statementTimeout = 250 * time.Millisecond
	if got := g.analysis.statementBudget(); got != 250*time.Millisecond {
		t.Fatalf("budget = %s, want the knob's own value below the cap", got)
	}
}

// TestAnAnalysisTransactionIsActuallyReadOnly is what turns "this
// package only ever emits SELECT" from a property of today's code into a
// guarantee.
//
// It asserts the *refusal* rather than the settings, and through runInTx
// itself with a statement of its own, because the question is not
// whether two lines executed but whether a write that reached this path
// would be stopped.
//
// It also pins the measurement this package inherits rather than
// re-derives: `SET LOCAL default_transaction_read_only` does not make
// its own transaction read-only, which is why runInTx sets
// `transaction_read_only` on a transaction already begun read-only.
func TestAnAnalysisTransactionIsActuallyReadOnly(t *testing.T) {
	g := newGame(t)

	// The positive control: the same call with a SELECT reads its row, so
	// a runInTx that refused everything cannot pass this test.
	var read int
	if err := g.analysis.runInTx(t.Context(), time.Second, "SELECT 1", nil,
		func(rows pgx.Rows) error {
			for rows.Next() {
				if err := rows.Scan(&read); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		t.Fatalf("a read must go through: %v", err)
	}
	if read != 1 {
		t.Fatalf("the control must have read its row, got %d", read)
	}

	err := g.analysis.runInTx(t.Context(), time.Second,
		`INSERT INTO projects (slug, name) VALUES ('written-by-an-analysis', 'no')`, nil,
		func(pgx.Rows) error { return nil })
	if err == nil {
		t.Fatal("a write inside an analysis's transaction must be refused: the three " +
			"read-only analyses compute and cache nothing, and this is what keeps that " +
			"true for the statements later tasks add")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("must be refused by the database, got %T: %v", err, err)
	}
	if pgErr.Code != "25006" {
		t.Fatalf("must be read_only_sql_transaction (25006), got %s: %v", pgErr.Code, err)
	}
}

// TestTheBudgetPostgresHoldsIsTheOneThisPackageComputed.
//
// This is not decoration. On the default path the knob is zero, `0ms`
// means *no timeout at all* in Postgres, and a run that quietly lost its
// bound looks exactly like one that kept it — so the only honest
// assertion is over the value the database reported back, read through
// runInTx's own path.
func TestTheBudgetPostgresHoldsIsTheOneThisPackageComputed(t *testing.T) {
	g := newGame(t)
	var timeout, readOnly string
	g.analysis.observeBounds = func(statementTimeout, ro string) {
		timeout, readOnly = statementTimeout, ro
	}
	if err := g.analysis.runInTx(t.Context(), g.analysis.statementBudget(), "SELECT 1", nil,
		func(rows pgx.Rows) error {
			for rows.Next() {
			}
			return nil
		}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if readOnly != "on" {
		t.Fatalf("transaction_read_only = %q, want \"on\"", readOnly)
	}
	// Postgres renders the setting in whatever unit reads best, so the
	// assertion is on the duration and not on the spelling.
	got, err := parsePostgresInterval(timeout)
	if err != nil {
		t.Fatalf("statement_timeout came back as %q: %v", timeout, err)
	}
	if got != DefaultStatementTimeout {
		t.Fatalf("statement_timeout = %s, want %s: a budget that arrived as anything "+
			"else is a bound the documentation promises and the database is not holding",
			got, DefaultStatementTimeout)
	}
}

// parsePostgresInterval reads back the `statement_timeout` spelling
// set_config returns: a bare number of milliseconds, or one carrying a
// unit Postgres chose.
func parsePostgresInterval(value string) (time.Duration, error) {
	if ms, err := strconv.Atoi(value); err == nil {
		return time.Duration(ms) * time.Millisecond, nil
	}
	return time.ParseDuration(value)
}

// TestTheBoundsDoNotLeakOntoTheNextCaller. Both settings are installed
// with is_local = true, which is what SET LOCAL means, so a pooled
// connection handed to the next caller carries neither. Without it, one
// analysis would leave every later query on that connection read-only.
func TestTheBoundsDoNotLeakOntoTheNextCaller(t *testing.T) {
	g := newGame(t)
	if err := g.analysis.runInTx(t.Context(), 250*time.Millisecond, "SELECT 1", nil,
		func(rows pgx.Rows) error {
			for rows.Next() {
			}
			return nil
		}); err != nil {
		t.Fatalf("run: %v", err)
	}
	// A write through the same pool, after the analysis: it must land.
	if _, err := g.meta.UpsertRelationType(t.Context(), g.projectID,
		metamodel.RelationTypeInput{Key: "requires", Label: "requires"}); err != nil {
		t.Fatalf("a write after an analysis must still work; the read-only setting "+
			"leaked onto the connection: %v", err)
	}
}

// TestAnUnknownAnalysisArgumentIsRefusedRatherThanIgnored sends the typo
// an agent will actually make.
//
// Silently ignoring an unknown member would answer "your entire game is
// unreachable" to a caller who did supply seeds — a wrong answer in the
// right shape, which is the worst thing this engine can produce. It is
// also O5's reservation: refusing what is unknown now is what lets a
// `source` argument be added later as an additive change.
func TestAnUnknownAnalysisArgumentIsRefusedRatherThanIgnored(t *testing.T) {
	type seedArgs struct {
		SeedEntities []string `json:"seed_entities"`
	}

	// The control, first: the correctly spelled argument decodes.
	var good seedArgs
	if err := DecodeArgs([]byte(`{"seed_entities":["a"]}`), &good); err != nil {
		t.Fatalf("the control must decode: %v", err)
	}
	if len(good.SeedEntities) != 1 {
		t.Fatalf("decoded %+v, want the one seed", good)
	}

	var bad seedArgs
	err := DecodeArgs([]byte(`{"seed_entites":["a"]}`), &bad)
	if err == nil {
		t.Fatal("an unknown argument must be refused, not ignored")
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %T %v, want invalid_input", err, err)
	}
	if !strings.Contains(err.Error(), "seed_entites") {
		t.Fatalf("err = %v, want it to name the member it did not recognise", err)
	}
	if len(bad.SeedEntities) != 0 {
		t.Fatalf("the refused call left %+v decoded, which a caller could act on", bad)
	}
}
