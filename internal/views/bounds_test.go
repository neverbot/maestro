package views

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
}

// TestTheBoundsDoNotLeakOntoTheNextCaller is the pooled-connection half.
// Both settings are made with is_local, so the connection this run
// borrowed goes back to the pool with neither of them — and if it did
// not, every later write in the process would fail with 25006 and every
// later query would inherit a millisecond.
func TestTheBoundsDoNotLeakOntoTheNextCaller(t *testing.T) {
	g, _ := newGame(t)
	g.views.statementTimeout = time.Millisecond
	// Ignored: the point is what the connection carries afterwards.
	_, _ = g.views.Run(t.Context(), g.projectID, RunRequest{
		Query: mustParse(t, `{"v":1,"from":[{"type":"quest"}]}`)})

	var timeout string
	if err := g.pool.QueryRow(t.Context(), "SHOW statement_timeout").Scan(&timeout); err != nil {
		t.Fatalf("read the setting back: %v", err)
	}
	if timeout != "0" {
		t.Errorf("the statement timeout must end with the transaction, got %q", timeout)
	}
	// A write on the same pool still works, which is the read-only half of
	// the same question.
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
