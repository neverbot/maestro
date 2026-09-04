package views

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/metamodel"
)

// TestEveryQueryRunsInAReadOnlyTransaction is what turns "the compiler
// only ever emits SELECT" from a property of the current code into a
// guarantee. The compiler is careful today; a transaction that refuses a
// write is careful in every task that adds a clause to it.
//
// It asserts the refusal rather than the settings, and it asserts it
// through runInTx itself with a statement of its own, because the
// question is not whether two lines were executed but whether a write
// that reached this path would be stopped.
func TestEveryQueryRunsInAReadOnlyTransaction(t *testing.T) {
	g, _ := newGame(t)
	// The positive control: the same call with a SELECT reads a row, so a
	// runInTx that refused everything — an unusable transaction, a syntax
	// error in the settings statement — cannot pass this test.
	var read int
	if err := g.views.runInTx(t.Context(), time.Second, "SELECT 1", nil,
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

	err := g.views.runInTx(t.Context(), time.Second,
		`INSERT INTO projects (slug, name) VALUES ('written-by-a-view', 'no')`, nil,
		func(pgx.Rows) error { return nil })
	if err == nil {
		t.Fatal("a write inside a view's transaction must be refused: the compiler emits " +
			"only SELECT today, and this is what keeps that true for the clauses later " +
			"tasks add")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("must be refused by the database, got %T: %v", err, err)
	}
	if pgErr.Code != "25006" {
		t.Fatalf("must be read_only_sql_transaction (25006), got %s: %v", pgErr.Code, err)
	}
}

// TestATruncatedResultIsFlaggedNotErrored is the truncation half, and the
// fixture is sized to distinguish a policy rather than to be convenient:
// twelve quests against a cap of ten separates "trimmed to the cap" from
// "returned whatever there was", which three against ten cannot.
//
// It asserts exactly ten rather than eleven, so the sentinel row the
// `LIMIT cap + 1` fetches is proved to be consumed rather than returned.
func TestATruncatedResultIsFlaggedNotErrored(t *testing.T) {
	g, _ := newGame(t)
	seedQuests(t, g, 9) // twelve in all, with the fixture's own three

	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"quests"}],
			"limits":{"max_nodes":10}}`)})
	if err != nil {
		t.Fatalf("truncation is not an error: %v", err)
	}
	if len(res.Nodes) != 10 {
		t.Fatalf("a cap of 10 over 12 quests must return exactly 10 nodes, got %d: "+
			"11 would be the sentinel row leaking into the result", len(res.Nodes))
	}
	if !res.Truncated.Nodes {
		t.Fatal("the node cap was hit and must be flagged: a designer reading a partial " +
			"picture as the whole one is the failure this flag exists to prevent")
	}
	if res.Stats.Nodes != 10 {
		t.Fatalf("stats must count what came back, got %+v", res.Stats)
	}
}

// TestAnUntruncatedResultSaysSo is the control the test above needs.
// Without it a Truncated.Nodes that was always true would pass, and the
// flag would be worth nothing.
func TestAnUntruncatedResultSaysSo(t *testing.T) {
	g, _ := newGame(t)

	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest","as":"quests"}],
			"limits":{"max_nodes":10}}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Nodes) != 3 {
		t.Fatalf("the fixture seeds three quests, got %d", len(res.Nodes))
	}
	if res.Truncated.Nodes {
		t.Fatal("three nodes under a cap of ten is not truncated")
	}
	if res.Truncated.Edges || res.Truncated.Depth {
		t.Fatalf("nothing else was capped either: %+v", res.Truncated)
	}
}

// TestTheNodeCapCountsNodesNotRows is the assertion the two tests above
// cannot make: both draw one set, where a row and a node are trivially
// the same thing.
//
// `max_nodes` is documented as a cap on nodes. It was enforced as a cap
// on *rows*, and the same entity drawn by two `nodes` entries is two rows
// — so three quests declared as two overlapping sets came back as the
// identical three nodes with Truncated.Nodes set at a cap of three, and
// clear at a cap of six. Nobody was ever told a truncated result was
// complete, which is why this is the direction it is; being told a whole
// picture is partial is still a designer chasing a cap that was never
// reached, and the trim in Go could hand back fewer nodes than the cap
// allowed.
//
// The control is the other direction in the same shape, so a cap that
// stopped flagging anything at all cannot pass.
func TestTheNodeCapCountsNodesNotRows(t *testing.T) {
	g, _ := newGame(t)
	doc := `{"v":1,"from":[{"type":"quest","as":"a"},{"type":"quest","as":"b"}],
		"limits":{"max_nodes":%d}}`

	// The fixture's three quests, drawn twice: six rows, three nodes.
	for _, cap := range []int{3, 4, 6} {
		res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
			Query: mustParse(t, fmt.Sprintf(doc, cap))})
		if err != nil {
			t.Fatalf("cap %d: %v", cap, err)
		}
		if len(res.Nodes) != 3 {
			t.Fatalf("cap %d: two overlapping sets over three quests are three nodes, "+
				"got %d", cap, len(res.Nodes))
		}
		if res.Truncated.Nodes {
			t.Errorf("cap %d: three nodes under a cap of %d is not truncated — the flag "+
				"is counting the rows the sets overlap in, not the nodes the caller got",
				cap, cap)
		}
	}

	// The control: six quests over the same two sets really do exceed a
	// cap of five, and are trimmed to exactly it.
	seedQuests(t, g, 3)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, fmt.Sprintf(doc, 5))})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Nodes) != 5 || !res.Truncated.Nodes {
		t.Fatalf("six nodes under a cap of five is five nodes and a flag, got %d and %+v",
			len(res.Nodes), res.Truncated)
	}
}

// TestAnEdgeResultIsTruncatedToo is the same mechanism at the other
// collection point, which is a separate arm with a separate cap and would
// otherwise be held by nothing. Its control is in the same test: the same
// query at a cap of ten draws all three edges and is not flagged.
func TestAnEdgeResultIsTruncatedToo(t *testing.T) {
	g, _ := newGame(t)
	doc := `{"v":1,"from":[{"type":"class","as":"cls"}],
		"traverse":[{"from":"cls","via":"available_to","direction":"in",
		             "to_type":"quest","as":"reachable"}],
		"edges":[{"from_step":"reachable"}],"limits":{"max_edges":%d}}`

	all, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, fmt.Sprintf(doc, 10))})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(all.Edges) != 3 || all.Truncated.Edges {
		t.Fatalf("the control must draw three edges untruncated, got %d and %+v",
			len(all.Edges), all.Truncated)
	}

	capped, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, fmt.Sprintf(doc, 2))})
	if err != nil {
		t.Fatalf("truncation is not an error: %v", err)
	}
	if len(capped.Edges) != 2 {
		t.Fatalf("a cap of 2 over 3 edges must return exactly 2, got %d", len(capped.Edges))
	}
	if !capped.Truncated.Edges {
		t.Fatal("the edge cap was hit and must be flagged")
	}
	// The nodes are untouched by an edge cap, which is what makes the two
	// caps two caps rather than one.
	if capped.Truncated.Nodes {
		t.Fatalf("only the edges were capped: %+v", capped.Truncated)
	}
}

// TestStatsCountWhatCameBack pins the three numbers the first question
// about a slow view is answered with. The counts are asserted against a
// result whose contents the test also names, so a stats block computed
// from the wrong thing cannot agree with it by accident.
func TestStatsCountWhatCameBack(t *testing.T) {
	g, _ := newGame(t)
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"class","as":"cls"}],
			"traverse":[{"from":"cls","via":"available_to","direction":"in",
			             "to_type":"quest","as":"reachable"}],
			"edges":[{"from_step":"reachable"}]}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stats.Nodes != len(res.Nodes) || res.Stats.Edges != len(res.Edges) {
		t.Fatalf("stats must count the envelope it ships with: %+v against %d/%d",
			res.Stats, len(res.Nodes), len(res.Edges))
	}
	// The mage and the three quests reachable from it, drawn by the three
	// available_to relations between them: a document with no `nodes`
	// entry draws every set it declared.
	if res.Stats.Nodes != 4 || res.Stats.Edges != 3 {
		t.Fatalf("the mage and its three quests, joined by three edges: %+v", res.Stats)
	}
	if res.Stats.DurationMS <= 0 {
		t.Fatalf("a run takes measurable time and the measurement is what open question O2 "+
			"is to be re-argued with: %+v", res.Stats)
	}
}

// TestATimedOutQueryIsRetryableAndSaysWhichBoundToLower settles the
// spec's query_timeout proposal: the code is `retryable`, because that is
// already what a cancelled statement maps to and a ninth code meaning the
// same thing helps nobody, and the *advice* is what gets completed.
//
// "Send the same call again" is right for contention and wrong for a
// query that is simply too expensive, and 57014 cannot tell those apart —
// so the message names the budget that elapsed and the bounds to lower.
func TestATimedOutQueryIsRetryableAndSaysWhichBoundToLower(t *testing.T) {
	g, _ := newGame(t)
	// Enough rows that the statement cannot finish inside a millisecond
	// on any machine this suite runs on. The control below is what proves
	// the same query is otherwise fine, so a fixture that grew slow would
	// not turn this into a test that passes for the wrong reason.
	seedQuests(t, g, 300)
	doc := `{"v":1,"from":[{"type":"quest","as":"quests"}],
		"traverse":[{"from":"quests","via":"available_to","direction":"out",
		             "to_type":"class","as":"classes"}],
		"edges":[{"from_step":"classes"}]}`

	if _, err := g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, doc)}); err != nil {
		t.Fatalf("the control: the same query inside the default budget must succeed: %v", err)
	}

	g.views.statementTimeout = time.Millisecond
	_, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
	if err == nil {
		t.Fatal("a query given a millisecond must run out of it")
	}
	if !metamodel.IsRetryable(err) {
		t.Fatalf("a cancelled statement must stay retryable — the wire code is the one that "+
			"already exists — got %T: %v", err, err)
	}
	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("must be a *TimeoutError so Task 15 can map it before the generic "+
			"retryable arm, got %T", err)
	}
	message := err.Error()
	for _, want := range []string{"max_depth", "max_nodes", "max_edges", "1ms"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message must name %q — the elapsed budget and the bounds to "+
				"lower are the whole difference between this and a bare retryable — got: %s",
				want, message)
		}
	}
	// The bounds are named with the values this run actually used, not the
	// package defaults, or the advice would be wrong for any query that
	// declared its own limits.
	if !strings.Contains(message, fmt.Sprint(DefaultMaxNodes)) {
		t.Errorf("the message must carry the run's own max_nodes: %s", message)
	}
}

// TestTheStatementBudgetIsClampedToItsHardCap holds the other half of
// "the value that reaches statement_timeout is one this package computed
// from its own two constants": the knob exists, so the ceiling over it
// has to be asserted rather than assumed.
func TestTheStatementBudgetIsClampedToItsHardCap(t *testing.T) {
	// The two numbers of the bounds table, asserted rather than left to
	// the constants to agree with themselves. Everything else in this file
	// is written in terms of these two names, so a default quietly raised
	// to a minute — a minute of database time for one picture — would
	// change what every view costs and fail nothing.
	if DefaultStatementTimeout != 5*time.Second || HardStatementTimeout != 15*time.Second {
		t.Fatalf("the budget is 5s with a 15s ceiling, got %s and %s",
			DefaultStatementTimeout, HardStatementTimeout)
	}
	s := &Service{}
	if got := s.statementBudget(); got != DefaultStatementTimeout {
		t.Errorf("an unset knob must give the default, got %s", got)
	}
	s.statementTimeout = time.Hour
	if got := s.statementBudget(); got != HardStatementTimeout {
		t.Errorf("a budget above the hard cap must be clamped to it, got %s", got)
	}
	s.statementTimeout = 250 * time.Millisecond
	if got := s.statementBudget(); got != 250*time.Millisecond {
		t.Errorf("a budget under the cap is used as it is, got %s", got)
	}
	// `budget <= 0` does two jobs — an unset knob and a nonsensical one —
	// and only the first was pinned. A negative budget formats as a
	// negative number of milliseconds, which Postgres refuses outright, so
	// the arm that turns it into the default is load-bearing.
	s.statementTimeout = -time.Second
	if got := s.statementBudget(); got != DefaultStatementTimeout {
		t.Errorf("a negative budget must fall back to the default, got %s", got)
	}
	// The floor. The budget is bound as whole milliseconds and
	// `statement_timeout = 0` is Postgres's spelling of *no timeout*, so a
	// sub-millisecond budget must not truncate into an unbounded run.
	s.statementTimeout = 500 * time.Microsecond
	if got := s.statementBudget(); got != time.Millisecond {
		t.Errorf("a sub-millisecond budget must floor at 1ms rather than truncate to an "+
			"unbounded statement, got %s", got)
	}
}

// TestTheBudgetPostgresHoldsIsTheOneThisPackageComputed is the assertion
// the clamp above cannot make: statementBudget is a pure function, and a
// pure function nobody calls is worth nothing.
//
// Run passes `s.statementBudget()` to runInTx. Change that one identifier
// to `s.statementTimeout` and every other test in this package stays
// green — on the default path the knob is zero, `0ms` binds, and *zero
// means no timeout in Postgres*, so every production view would run
// unbounded while the bounds table advertises 5s and 15s. The three tests
// that touch the timeout all set the knob, so the default path was
// asserted by nobody.
//
// So this one asserts the value the *database* came back holding, through
// Run's own path, on both ends of the clamp:
//
//   - knob unset — the production path — must be 5s, which is what fails
//     the moment Run passes the raw knob;
//   - knob above the ceiling must be 15s, which is the only assertion
//     proving the clamp travels through Run rather than sitting unused.
//
// The observation goes through Service.observeBounds, which reports what
// set_config returned rather than what Go computed: the readback is also
// what makes runInTx refuse a transaction that came back unbounded.
func TestTheBudgetPostgresHoldsIsTheOneThisPackageComputed(t *testing.T) {
	g, _ := newGame(t)
	doc := mustParse(t, `{"v":1,"from":[{"type":"quest"}]}`)

	observe := func(t *testing.T) *[]string {
		t.Helper()
		var seen []string
		g.views.observeBounds = func(timeout, readOnly string) {
			seen = append(seen, timeout)
			if readOnly != "on" {
				t.Errorf("the transaction must be holding transaction_read_only on, got %q",
					readOnly)
			}
		}
		return &seen
	}

	seen := observe(t)
	if _, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: doc}); err != nil {
		t.Fatalf("the default path must run: %v", err)
	}
	if len(*seen) != 1 || (*seen)[0] != "5s" {
		t.Fatalf("with no knob set, the statement must run under the 5s default this "+
			"package's table advertises; the database is holding %v. A %q here is "+
			"Postgres's spelling of *no timeout*, which is the whole defect this test "+
			"exists for", *seen, "0")
	}

	g.views.statementTimeout = time.Hour
	seen = observe(t)
	if _, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: doc}); err != nil {
		t.Fatalf("an over-cap knob is clamped, not refused: %v", err)
	}
	if len(*seen) != 1 || (*seen)[0] != "15s" {
		t.Fatalf("an hour must reach the database as the 15s ceiling, got %v", *seen)
	}

	// And the floor at the other end, measured the same way: 500µs is
	// still a bound, not the absence of one.
	g.views.statementTimeout = 500 * time.Microsecond
	seen = observe(t)
	_, _ = g.views.Run(t.Context(), g.projectID, RunRequest{Query: doc})
	if len(*seen) != 1 || (*seen)[0] != "1ms" {
		t.Fatalf("a sub-millisecond budget must reach the database as 1ms rather than as "+
			"an unbounded statement, got %v", *seen)
	}
}

// TestTheBoundsDoNotLeakOntoTheNextCaller is the pooled-connection half:
// the connection a run borrowed goes back to the pool carrying neither
// setting, and if it did not, every later write in this process would be
// refused with 25006 and every later query would inherit a millisecond.
//
// **Two things make that true and the test holds the pair, not either
// half.** The settings are made with is_local, and runInTx always rolls
// back — and a plain SET is transactional too, so a rollback undoes a
// session-level one as well. Measured: flipping is_local to false alone
// leaves this test green. Only losing both (is_local off *and* the
// rollback turned into a commit) leaks, and that is the mutation this
// test was proved red against.
//
// It checks **every connection in the pool**, not one, and that is the
// difference between an assertion and a coincidence: the pool holds four,
// the run borrowed whichever was free, and a single SHOW would three
// times out of four ask a connection the run never touched and pass
// whatever the answer was.
func TestTheBoundsDoNotLeakOntoTheNextCaller(t *testing.T) {
	g, _ := newGame(t)
	g.views.statementTimeout = time.Millisecond
	// The result is ignored: the point is what the connection carries
	// afterwards, and whether the run itself timed out does not change it.
	_, _ = g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest"}]}`)})

	// Held all at once, so that every connection the pool can hand out —
	// including the one the run used — is inspected rather than sampled.
	var held []*pgxpool.Conn
	for i := int32(0); i < g.pool.Config().MaxConns; i++ {
		conn, err := g.pool.Acquire(t.Context())
		if err != nil {
			t.Fatalf("acquire connection %d of %d: %v", i, g.pool.Config().MaxConns, err)
		}
		held = append(held, conn)
	}
	for i, conn := range held {
		var timeout, readOnly string
		if err := conn.QueryRow(t.Context(),
			"SELECT current_setting('statement_timeout'), current_setting('transaction_read_only')").
			Scan(&timeout, &readOnly); err != nil {
			t.Fatalf("read the settings back from connection %d: %v", i, err)
		}
		if timeout != "0" {
			t.Errorf("connection %d kept a statement timeout of %q past the transaction "+
				"that set it", i, timeout)
		}
		if readOnly != "off" {
			t.Errorf("connection %d is still read-only (%q): every later write in this "+
				"process would be refused", i, readOnly)
		}
		conn.Release()
	}

	// And the same question behaviourally: a write on the same pool still
	// works.
	g.entity(t, "quest", "after-the-bounds", "After the bounds", nil)
}

// seedQuests adds n quests beyond the fixture's own three, with distinct
// keys and a level each, so a test can ask for more rows than a cap
// allows.
func seedQuests(t *testing.T, g *game, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("filler-%03d", i)
		g.entity(t, "quest", key, fmt.Sprintf("Filler %d", i),
			map[string]any{"min_level": i})
	}
}

// TestNoStatementTextIsAssembledOutsideTheCompiler closes the route the
// frag guard cannot see.
//
// TestTheOnlyStringToFragmentConversionsAreTheOnesNamedHere watches
// conversions into the builder's fragment type, which is every statement
// the compiler emits — but this file executes SQL of its own, written as
// Go string literals that never become a frag, and a value concatenated
// or formatted into one of those would reach Postgres with no guard
// speaking. That is the shape of defect this repository keeps producing:
// a hole closed at one call site and left open one step along.
//
// So: every statement this package hands to pgx is either a literal it
// wrote or the identifier holding the compiler's output. A `+`, a
// fmt.Sprintf, a function call — anything a caller's value could be in —
// fails here, and the failure names the argument.
//
// The plan's own block would have failed this test: it formatted the
// milliseconds into `SET LOCAL statement_timeout` with fmt.Sprintf and
// called it the one deliberate exception. set_config takes its value as a
// bind parameter, so the exception is unnecessary and this guard needs no
// allowance carved into it.
//
// **It watches the assignment as well as the call**, because the call
// site alone is one step short of the hole: `statement` is allowed by
// name, so `statement = statement + " -- " + fromACaller` one line above
// the Query left both halves of the earlier guard green. An identifier
// this test lets through must therefore be one whose only value is
// compileWith's.
func TestNoStatementTextIsAssembledOutsideTheCompiler(t *testing.T) {
	// The pgx methods that take statement text, and which argument of each
	// one it is (Exec/Query/QueryRow take a ctx first; Batch.Queue does
	// not; Conn.Prepare takes ctx and a name).
	//
	// **This set is a list of the pgx entry points this package uses, and
	// it has to be revisited whenever a new one is.** The vacuity check
	// below only fails when a *watched* call disappears, so an unwatched
	// executor that is added is invisible to it: `batch.Queue("SELECT " +
	// fromACaller)` executed with every guard in this file silent until
	// Queue was named here. CopyFrom takes a table identifier rather than
	// SQL, and is watched anyway — it is a write, this package performs
	// none, and a composite literal in that position fails the default arm
	// loudly, which is the answer wanted.
	executors := map[string]int{
		"Exec": 1, "Query": 1, "QueryRow": 1,
		"Queue": 0, "Prepare": 2, "CopyFrom": 1,
	}
	// The one identifier allowed to carry a statement: the compiler's
	// output, which the frag guard already covers end to end.
	compiled := map[string]bool{"statement": true}
	// The only call an identifier in `compiled` may be assigned from.
	const builder = "compileWith"

	found, built := 0, 0
	for _, file := range packageFiles(t) {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			if assign, ok := n.(*ast.AssignStmt); ok {
				// Route: the value of an allowed identifier, rather than the
				// argument at the call site. Anything written into
				// `statement` that is not the compiler's own output makes
				// the allowance at the call site meaningless.
				names := false
				for _, lhs := range assign.Lhs {
					if ident, ok := unparen(lhs).(*ast.Ident); ok && compiled[ident.Name] {
						names = true
					}
				}
				if !names {
					return true
				}
				line := fset.Position(assign.Pos()).Line
				if len(assign.Rhs) != 1 {
					t.Errorf("%s:%d assigns a statement identifier from %d values; the "+
						"compiler's output is the only thing it may hold",
						file, line, len(assign.Rhs))
					return true
				}
				call, ok := unparen(assign.Rhs[0]).(*ast.CallExpr)
				if !ok {
					t.Errorf("%s:%d assigns a statement identifier from a %T; only %s may "+
						"produce a statement this package executes",
						file, line, assign.Rhs[0], builder)
					return true
				}
				name := ""
				switch fn := unparen(call.Fun).(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				if name != builder {
					t.Errorf("%s:%d assigns a statement identifier from %s(); only %s may "+
						"produce a statement this package executes", file, line, name, builder)
					return true
				}
				built++
				return true
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			at, watched := executors[sel.Sel.Name]
			if !watched || len(call.Args) <= at {
				return true
			}
			found++
			switch arg := unparen(call.Args[at]).(type) {
			case *ast.BasicLit:
				return true
			case *ast.Ident:
				if compiled[arg.Name] {
					return true
				}
				t.Errorf("%s:%d passes the statement %q to %s; only a literal this package "+
					"wrote or the compiler's output may be executed", file,
					fset.Position(call.Pos()).Line, arg.Name, sel.Sel.Name)
			default:
				t.Errorf("%s:%d assembles the statement it passes to %s (%T); a caller's "+
					"value has no route into a statement this package did not build",
					file, fset.Position(call.Pos()).Line, sel.Sel.Name, arg)
			}
			return true
		})
	}
	// Vacuity: this package executes SQL, and a walk that found none is a
	// walk that stopped matching rather than a package that stopped
	// running queries. The same for the assignment arm — `statement` is
	// assigned in Run, and an arm that matched nothing would let anything
	// through.
	if found < 2 {
		t.Fatalf("found %d statements executed in this package; the guard above is no "+
			"longer watching what it names", found)
	}
	if built < 1 {
		t.Fatalf("found %d assignments of a statement identifier; the assignment arm is "+
			"watching nothing", built)
	}
}
