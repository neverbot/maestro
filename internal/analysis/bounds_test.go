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

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/metamodel"
)

func TestBounds(t *testing.T) {
	t.Parallel()
	a := newArea(t)

	// TestEveryBoundIsExported reads bounds.go's own AST and fails on an
	// unexported const in it.
	t.Run("every bound is exported", func(t *testing.T) {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, "bounds.go", nil, parser.ParseComments)
		assert.Must(t, err == nil, "parse bounds.go: %v", err)
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
					assert.Must(t, name.IsExported(), "bounds.go declares the unexported const %q at %s: every "+
						"bound in this file is part of the contract, because a caller "+
						"that cannot read a cap discovers it by tripping over it",
						name.Name, fset.Position(name.Pos()))
				}
			}
		}
		// The positive control. A test that walked no declarations at all —
		// a renamed file, a parser that returned an empty tree — would pass
		// silently, and this is a file whose whole subject is a list.
		assert.Must(t, found >= 10, "only %d consts found in bounds.go; this test is not reading the file "+
			"it thinks it is", found)
	})

	// TestTheCapsRefuseRatherThanClamp is the behavioural half of the rule
	// bounds.go states in prose, asserted on the one caller-settable bound
	// this task ships.
	t.Run("the caps refuse rather than clamp", func(t *testing.T) {
		g := a.game(t)
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
		assert.Must(t, errors.Is(err, ErrLimitExceeded), "err = %T %v, want limit_exceeded and not, say, the duplicate refusal: "+
			"the bound is judged before the list's contents are", err, err)
	})

	// TestBoundsArea's "the statement budget is clamped to its hard cap" case
	// is the *other* side of the same rule, and the two are in one file on
	// purpose. The statement budget is the package's one clamp, and it is
	// allowed to be one because no caller can set it: there is nobody to
	// mislead about the bound their answer was computed under.
	t.Run("the statement budget is clamped to its hard cap", func(t *testing.T) {
		g := a.game(t)
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
	})

	// TestAnAnalysisTransactionIsActuallyReadOnly is what turns "this
	// package only ever emits SELECT" from a property of today's code into a
	// guarantee.
	t.Run("an analysis transaction is actually read only", func(t *testing.T) {
		g := a.game(t)

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
		assert.Must(t, read == 1, "the control must have read its row, got %d", read)

		err := g.analysis.runInTx(t.Context(), time.Second,
			`INSERT INTO projects (slug, name) VALUES ('written-by-an-analysis', 'no')`, nil,
			func(pgx.Rows) error { return nil })
		if err == nil {
			t.Fatal("a write inside an analysis's transaction must be refused: the three " +
				"read-only analyses compute and cache nothing, and this is what keeps that " +
				"true for the statements later tasks add")
		}
		var pgErr *pgconn.PgError
		assert.Must(t, errors.As(err, &pgErr), "must be refused by the database, got %T: %v", err, err)
		assert.Must(t, pgErr.Code == "25006", "must be read_only_sql_transaction (25006), got %s: %v", pgErr.Code, err)
	})

	// TestBoundsArea's "the budget postgres holds is the one this package
	// computed" case.
	t.Run("the budget postgres holds is the one this package computed", func(t *testing.T) {
		g := a.game(t)
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
		assert.Must(t, readOnly == "on", "transaction_read_only = %q, want \"on\"", readOnly)
		// Postgres renders the setting in whatever unit reads best, so the
		// assertion is on the duration and not on the spelling.
		got, err := parsePostgresInterval(timeout)
		assert.Must(t, err == nil, "statement_timeout came back as %q: %v", timeout, err)
		assert.Must(t, got == DefaultStatementTimeout, "statement_timeout = %s, want %s: a budget that arrived as anything "+
			"else is a bound the documentation promises and the database is not holding",
			got, DefaultStatementTimeout)
	})

	// TestBoundsArea's "the bounds do not leak onto the next caller" case.
	// Both settings are installed with is_local = true, which is what SET
	// LOCAL means, so a pooled connection handed to the next caller carries
	// neither. Without it, one analysis would leave every later query on that
	// connection read-only.
	t.Run("the bounds do not leak onto the next caller", func(t *testing.T) {
		g := a.game(t)
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
	})

	// TestAnUnknownAnalysisArgumentIsRefusedRatherThanIgnored sends the typo
	// an agent will actually make.
	t.Run("an unknown analysis argument is refused rather than ignored", func(t *testing.T) {
		type seedArgs struct {
			SeedEntities []string `json:"seed_entities"`
		}

		// The control, first: the correctly spelled argument decodes.
		var good seedArgs
		if err := DecodeArgs([]byte(`{"seed_entities":["a"]}`), &good); err != nil {
			t.Fatalf("the control must decode: %v", err)
		}
		assert.Must(t, len(good.SeedEntities) == 1, "decoded %+v, want the one seed", good)

		var bad seedArgs
		err := DecodeArgs([]byte(`{"seed_entites":["a"]}`), &bad)
		assert.Must(t, err != nil, "an unknown argument must be refused, not ignored")
		assert.Must(t, errors.Is(err, ErrInvalidInput), "err = %T %v, want invalid_input", err, err)
		assert.Must(t, strings.Contains(err.Error(), "seed_entites"), "err = %v, want it to name the member it did not recognise", err)
		assert.Must(t, len(bad.SeedEntities) == 0, "the refused call left %+v decoded, which a caller could act on", bad)
	})
}

func parsePostgresInterval(value string) (time.Duration, error) {
	if ms, err := strconv.Atoi(value); err == nil {
		return time.Duration(ms) * time.Millisecond, nil
	}
	return time.ParseDuration(value)
}
