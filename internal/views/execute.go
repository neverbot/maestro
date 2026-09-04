package views

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/metamodel"
)

// Node is one row of a result. It carries identity and the attributes the
// projection asked for, and never a full fields payload unless
// include_fields was set: a thousand-node result with every jsonb field
// inlined is a five-figure token bill for a picture the agent is not
// going to look at.
//
// Attrs carries the projection: one entry per slot the document asked
// for, keyed by the member it was written under — attrs["color_by"] is
// what `project.color_by` resolved to for this node. `label` is always
// there, because it defaults to the entity's name.
//
// **A slot that found nothing is absent, not empty.** The attributes are
// built as jsonb and stripped of their nulls, so a renderer can tell "this
// quest has no zone" from "this quest's zone is named the empty string",
// and a value keeps its JSON type: a projected number is a number, not
// the text of one.
type Node struct {
	ID     uuid.UUID      `json:"id"`
	Key    string         `json:"key"`
	Type   string         `json:"type"`
	Name   string         `json:"name"`
	Set    string         `json:"set"`
	Role   string         `json:"role,omitempty"`
	Attrs  map[string]any `json:"attrs,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
	// Ambiguous is set when a one-hop related attribute found more than
	// one entity and the first by name was used. Silently picking one and
	// saying nothing would produce a map that is wrong in a way nobody
	// can see.
	//
	// It is a property of the *node*, not of one slot: a node with two
	// related slots, one of them ambiguous, is flagged, and which of the
	// two it was is not said. Saying it would mean an attrs-shaped second
	// map that every renderer would have to read to draw anything, for a
	// distinction a designer resolves by looking at the query.
	Ambiguous bool `json:"ambiguous,omitempty"`
}

// Edge is one relation in a result.
//
// **ID is a relations.id and is not stable across a re-seed.** relations
// carries no key and no version (0004_metamodel.sql), so an edge is
// addressable only as (relation_type_id, source_id, target_id) and a
// re-seed that deleted and recreated it produces a new id for the same
// edge. Nothing in this sub-project stores an edge id; a client that does
// is storing something that will change under it.
//
// Label is what the edge entry's label_from asked to be drawn on this
// relation: a field it declares, or its relation type's key for @type.
// Empty when the entry asked for none, which is the default — an
// unasked-for label on every edge is text a renderer has to hide again.
//
// **An edge drawn by two entries takes the first entry's label**, where
// "first" is entry order: capOf's DISTINCT ON keeps the lowest rank per
// id, and the Go-side dedupe applies the same rule, so the choice is
// deterministic rather than arbitrary. It is still a choice, and it is
// one the document controls without saying so: two documents that differ
// only in the order of their edges[] entries label the same edge
// differently. Recorded rather than changed — the alternative is either
// a list of labels, which no renderer wants, or refusing the overlap,
// which refuses a legitimate document.
type Edge struct {
	ID     uuid.UUID      `json:"id"`
	Type   string         `json:"type"`
	Source uuid.UUID      `json:"source"`
	Target uuid.UUID      `json:"target"`
	Label  string         `json:"label,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// Stats is always populated. The first question about a slow view is
// whether it is the query or the size of the answer, and without both
// numbers it cannot be answered. DurationMS is also the measurement this
// plan's open question O2 (indexed jsonb fields) is to be re-argued with.
//
// MaxDepthReached is **measured, not declared**: every row the statement
// returns carries the hops from a seed selector to the node it drew, a
// selector's own rows being zero, and this is the largest of them. A step
// that asked for four hops and found two reports two — the declared depth
// cannot say that, which is why the arithmetic this used to be is gone.
// A set that drew no node contributes no depth, because a depth is only
// ever read off a row that came back.
type Stats struct {
	Nodes           int   `json:"nodes"`
	Edges           int   `json:"edges"`
	MaxDepthReached int   `json:"max_depth_reached"`
	DurationMS      int64 `json:"duration_ms"`
}

// Truncated says which caps were hit. Truncation is not an error: a
// designer asking about a huge graph should see a thousand nodes and a
// warning, not a stack trace.
//
// Nodes and Edges are **detected, not inferred**: each collection point
// is emitted with `LIMIT cap + 1` (compiler.capOf), so a result that came
// back one row over its cap is a result the graph had more of. Run trims
// the extra rows and sets the flag. The rows are deduplicated by id
// before the LIMIT, so a row is a node and the flag is exact in both
// directions — it is neither set for a graph that fits nor left unset for
// one that did not.
//
// **A multi-hop step sets both of them together**, without either
// collection point overflowing, when its walk hit the row cap
// internal/graph applies for it (compiler.walk). A walk row is one edge
// traversal, so a cap of four times max_nodes rows can collapse to a
// handful of nodes: the picture is then short of content that neither
// element cap can see. Which of the two it is short of — a node, an
// edge, or an edge whose node another traversal also reached — the
// statement cannot say, so both are set rather than a guess made.
//
// This is the one position where Nodes and Edges can over-report, and
// the over-report cannot be removed: the walk hands back one row past
// its cap and no more, so "a traversal was cut" is knowable and "the
// picture is poorer for it" is not — the cut traversals may all have
// reached nodes and edges the picture already holds. The alternative is
// the silent loss this replaced, where content vanished with all three
// flags false.
// TestAWalkRowCapIsReportedRatherThanLosingContentSilently pins it, with
// the same fixture under a larger cap as the control.
//
// **An edge's endpoints are not guaranteed to be in Nodes.** The two caps
// are independent and the edge arms are collected from the sets, not from
// the trimmed node list, so a node truncation leaves the edges that
// pointed at the trimmed nodes in the result. Dropping them is not the
// obvious repair it looks like: an `edges: [{between: …}]` entry
// legitimately draws relations between sets the document chose *not* to
// draw as nodes, so "endpoint missing" is a normal, untruncated answer
// too, and a renderer has to tolerate it regardless of this flag. A
// renderer that wants a closed graph filters on Nodes itself.
//
// **Depth is detected the same way**, and by the same cap + 1 idea: each
// multi-hop step is walked one hop *past* what it asked for, the extra
// hop is dropped before the picture is built, and the flag is whether
// that hop found anything. So it is exact about the traversal — a chain
// that ends exactly at the bound is not flagged, where "the deepest node
// sits at max_depth" would report a whole picture partial.
//
// **"Found anything" means a node or an edge the walk does not already
// hold within the bound**, not merely a row past it. On a dense or a
// cyclic graph the rows past the bound never stop: a clique of six drawn
// whole, every node and all fifteen edges, produced deeper simple paths
// that reached nothing new, and the designer was told their complete
// picture had been cut short. compiler.walk's probe is the predicate.
//
// Two things it deliberately does not say, because a flag that means two
// things means neither:
//
//   - It is about the *walk*, not about the picture. A hop the bound
//     refused whose far entity the step's to_type or where would have
//     filtered out still sets it: the traversal was cut short, and
//     whether the next node would have been drawn is a different
//     question from whether there was one.
//   - A **one-hop step is not probed**, and cannot set it. A step that
//     asks for exactly one hop is a neighbour query, not a bounded
//     traversal, and probing it would cost a second scan of relations on
//     the common case to tell a designer something they already know.
//     So a false Depth on a query whose every step is one hop means "not
//     measured", exactly as an all-false Truncated did before Task 8, and
//     nothing but this paragraph says which — which is why
//     TestTruncatedDepthIsFlagged observes the one-hop case rather than
//     leaving it to the prose.
//
// The walk's own row cap cannot hide the evidence: the probe reads the
// recursion, not the wrapper the cap is applied to (compiler.walk says
// so, and that is its stated reason). A run that hit the row cap reports
// a node and an edge truncation as well, and the two flags are
// independent — neither substitutes for the other.
type Truncated struct {
	Nodes bool `json:"nodes"`
	Edges bool `json:"edges"`
	Depth bool `json:"depth"`
}

// Result is the one envelope every renderer consumes. One shape for every
// renderer is the point: swapping `graph` for `table` on a saved view
// must never require rewriting the query.
//
// The spec's envelope also carries a staleness report; it is not here,
// because Diagnostic is Task 12's type and a field declared against a
// type that does not exist yet is a field nothing can fill.
type Result struct {
	Nodes     []Node    `json:"nodes"`
	Edges     []Edge    `json:"edges"`
	Stats     Stats     `json:"stats"`
	Truncated Truncated `json:"truncated"`
}

// RunRequest is one execution: a parsed query and the parameter values
// that override its declared defaults.
//
// The spec's on_stale switch is not here for the same reason Result has
// no staleness report: Task 12 owns both, and a knob that is read by
// nothing is a knob that lies.
type RunRequest struct {
	Query  *Query
	Params map[string]any
	// IncludeFields selects each node's and each edge's declared fields
	// into the envelope. Off by default; see Node.
	IncludeFields bool
}

// Run compiles and executes one query against one game.
//
// It resolves against a catalogue read for that game, binds the run's
// parameters over the query's declared defaults, compiles, and reads the
// single statement's rows into one envelope. Every value the caller
// controls — a key, a field name, a comparison operand, a set name —
// travels as a bind parameter; see compile.go's builder.
func (s *Service) Run(ctx context.Context, projectID uuid.UUID, req RunRequest) (Result, error) {
	if req.Query == nil {
		return Result{}, invalidQuery("", "no query document was given")
	}
	cat, err := s.LoadCatalogue(ctx, projectID)
	if err != nil {
		return Result{}, err
	}
	resolved, err := ResolveAgainst(projectID, cat, req.Query)
	if err != nil {
		return Result{}, err
	}
	params, err := bindParams(resolved, req.Params)
	if err != nil {
		return Result{}, err
	}
	// A copy, so one run's bindings cannot leak into a *Resolved another
	// run — or Task 12's staleness report — is still holding.
	forRun := *resolved
	forRun.Params = params

	statement, args, err := compileWith(&forRun, projectID, compileOptions{
		IncludeFields: req.IncludeFields,
	})
	if err != nil {
		return Result{}, err
	}

	started := time.Now()
	budget := s.statementBudget()

	// Empty rather than nil, because a nil slice serialises as JSON null
	// and an empty result is `{"nodes": [], "edges": []}` — a picture
	// with nothing in it, not an absent picture. Task 15's REST mirror
	// hands this envelope to clients that would otherwise have to handle
	// both spellings of "no nodes".
	result := Result{Nodes: []Node{}, Edges: []Edge{}}
	seenNode := map[uuid.UUID]bool{}
	seenEdge := map[uuid.UUID]bool{}
	// How many rows each collection point actually returned, which is what
	// *detects* truncation: capOf asked each of them for one row more than
	// its cap, so a count over the cap is a graph that had more.
	//
	// A row is a node. capOf deduplicates by id *before* the LIMIT, so the
	// two numbers cannot drift: counting rows while the LIMIT counted
	// duplicates is what used to report a graph of three quests declared
	// as two overlapping sets truncated at a cap of three. The maps below
	// stay as the second half of that rule — the one that decides which
	// set a node belongs to — and no longer collapse anything.
	nodeRowsRead, edgeRowsRead := 0, 0
	// The deepest node that came back, in hops from a seed selector. It is
	// read off the statement rather than derived from the query's shape:
	// a walk that asked for four hops and found two reached two, and only
	// the rows know which.
	deepest := 0
	collect := func(rows pgx.Rows) error {
		for rows.Next() {
			var (
				kind                        string
				id                          *uuid.UUID
				key, name, typeKey, setName *string
				role                        *string
				source, target              *uuid.UUID
				fields                      []byte
				rank, depth                 *int32
				attrs                       []byte
				ambiguous                   *bool
			)
			// id is a pointer because the depth row below carries no graph
			// element at all: it is one row of typed nothings whose only
			// content is that it exists.
			if err := rows.Scan(&kind, &id, &key, &name, &typeKey, &setName, &role,
				&source, &target, &fields, &rank, &depth, &attrs, &ambiguous); err != nil {
				return fmt.Errorf("scan a view row: %w", err)
			}
			if kind == string(depthTruncatedKind) {
				// A walk had a node or an edge one hop past its bound
				// that the picture does not already hold. See
				// Truncated.Depth.
				result.Truncated.Depth = true
				continue
			}
			if kind == string(walkTruncatedKind) {
				// A walk hit the row cap it carries internally, so it
				// handed the statement a prefix of the traversals the
				// graph holds. Both element flags, because the row it
				// dropped carried a node and an edge and nothing can say
				// which of the two the picture came up short of. See
				// Truncated.Nodes.
				result.Truncated.Nodes = true
				result.Truncated.Edges = true
				continue
			}
			if id == nil {
				return fmt.Errorf("views: a %s row came back without an id", kind)
			}
			payload, err := decodeFields(fields)
			if err != nil {
				return err
			}
			projected, err := decodeFields(attrs)
			if err != nil {
				return err
			}
			switch kind {
			case "node":
				// Counted before the deduplication, deliberately: this is
				// the row the collection point returned, and the row count
				// is the only thing that knows whether the LIMIT was
				// reached.
				nodeRowsRead++
				// One node per id, keeping the first `nodes` entry that
				// claimed it: a node in two sets is one thing on the picture,
				// and the ORDER BY on the entry's rank is what makes "first"
				// mean the order the document declared rather than whatever
				// Postgres happened to return.
				// TestANodeInTwoSetsComesBackOnceUnderTheFirstSetThatClaimedIt
				// pins both halves, and says which of the two the ordering is.
				if seenNode[*id] {
					continue
				}
				seenNode[*id] = true
				if depth != nil && int(*depth) > deepest {
					deepest = int(*depth)
				}
				result.Nodes = append(result.Nodes, Node{
					ID:        *id,
					Key:       text(key),
					Type:      text(typeKey),
					Name:      text(name),
					Set:       text(setName),
					Role:      text(role),
					Attrs:     projected,
					Fields:    payload,
					Ambiguous: ambiguous != nil && *ambiguous,
				})
			case "edge":
				edgeRowsRead++
				if seenEdge[*id] {
					continue
				}
				seenEdge[*id] = true
				edge := Edge{ID: *id, Type: text(typeKey), Fields: payload}
				// An edge's label rides in the same attrs column the nodes
				// use, under one key. It is a string on the wire because a
				// renderer draws one string on an edge; a value of another
				// JSON type is rendered the way it was stored rather than
				// refused, since a number is a perfectly good edge label.
				if label, ok := projected[edgeLabelKey]; ok {
					if text, ok := label.(string); ok {
						edge.Label = text
					} else {
						edge.Label = fmt.Sprint(label)
					}
				}
				if source != nil {
					edge.Source = *source
				}
				if target != nil {
					edge.Target = *target
				}
				result.Edges = append(result.Edges, edge)
			default:
				return fmt.Errorf("views: a result row is a node, an edge or a "+
					"truncation report, got %q", kind)
			}
		}
		return nil
	}
	if err := s.runInTx(ctx, budget, statement, args, collect); err != nil {
		return Result{}, runFailure(err, budget, forRun.Limits)
	}

	// Trim what the cap + 1 brought back, and say so. A truncated result
	// is not an error: a designer asking about a huge graph should get a
	// thousand nodes and a flag, not a stack trace, and the flag is what
	// stops them reading a partial picture as the whole one.
	if nodeRowsRead > forRun.Limits.MaxNodes {
		result.Truncated.Nodes = true
		if len(result.Nodes) > forRun.Limits.MaxNodes {
			result.Nodes = result.Nodes[:forRun.Limits.MaxNodes]
		}
	}
	if edgeRowsRead > forRun.Limits.MaxEdges {
		result.Truncated.Edges = true
		if len(result.Edges) > forRun.Limits.MaxEdges {
			result.Edges = result.Edges[:forRun.Limits.MaxEdges]
		}
	}

	result.Stats = Stats{
		Nodes:           len(result.Nodes),
		Edges:           len(result.Edges),
		MaxDepthReached: deepest,
		DurationMS:      time.Since(started).Milliseconds(),
	}
	return result, nil
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func decodeFields(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode a row's fields: %w", err)
	}
	return out, nil
}

// bindParams overlays the values one run supplies onto the defaults the
// document declared, checking each against the parameter's declared type.
//
// Every problem is reported in one pass, like every other refusal in this
// package: an agent that mistyped two parameter names should learn both
// at once. A supplied value is addressed at the *declaration* it belongs
// to, /params/N, because that is the position in the document the caller
// can go and read; a name nothing declares is addressed at /params,
// because it has no position of its own.
func bindParams(r *Resolved, supplied map[string]any) (map[string]any, error) {
	bound := make(map[string]any, len(r.Params)+len(supplied))
	for key, value := range r.Params {
		bound[key] = value
	}
	declared := make(map[string]int, len(r.Query.Params))
	for i, decl := range r.Query.Params {
		declared[decl.Key] = i
	}
	var problems []metamodel.FieldError
	for _, key := range sortedKeys(supplied) {
		i, ok := declared[key]
		if !ok {
			problems = append(problems, metamodel.FieldError{
				Path: pointer("params"),
				Message: fmt.Sprintf("no parameter named %q is declared by this query: "+
					"declare it in params, or drop it from the run", key),
			})
			continue
		}
		value, err := coerceParam(metamodel.FieldType(r.Query.Params[i].Type), supplied[key])
		if err != nil {
			problems = append(problems, metamodel.FieldError{
				Path:    pointer("params", i),
				Message: err.Error(),
			})
			continue
		}
		bound[key] = value
	}
	if len(problems) > 0 {
		return nil, invalidQueryProblems(problems)
	}
	return bound, nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	// Sorted so that two runs of the same wrong call report their
	// problems in the same order; a map's iteration order is not one an
	// agent can learn from.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// The statement budget, in the two numbers Task 7's table gives. They are
// this package's own constants, and the only value that ever reaches
// `statement_timeout` is derived from them: see statementBudget.
const (
	// DefaultStatementTimeout is what a run gets when nothing overrides it.
	DefaultStatementTimeout = 5 * time.Second
	// HardStatementTimeout is the ceiling. Nothing in the query document
	// sets a timeout — limits are about the size of the answer, and a
	// designer who could ask for a minute of database time would — so this
	// is a ceiling over this package's own knob rather than over a caller's
	// value.
	HardStatementTimeout = 15 * time.Second
)

// statementBudget is the timeout one run gets.
//
// It is DefaultStatementTimeout unless a test set the package-private
// knob, and it is clamped to HardStatementTimeout either way, so the
// duration that reaches the statement is always a value this package
// computed from its own two constants. That is the property the
// set_config bind rests on and the reason no caller can influence it: the
// knob is an unexported field, and nothing outside this package can write
// one.
func (s *Service) statementBudget() time.Duration {
	budget := s.statementTimeout
	if budget <= 0 {
		budget = DefaultStatementTimeout
	}
	if budget > HardStatementTimeout {
		budget = HardStatementTimeout
	}
	// And a floor, because the value is bound as whole milliseconds and
	// `statement_timeout = 0` means *no timeout* in Postgres: a budget of
	// 500µs truncates to "0ms" and would buy an unbounded statement in
	// exchange for asking for a very short one. A sub-millisecond budget
	// is not a thing this package can express, so the nearest thing it can
	// express is the smallest real bound rather than the absence of one.
	if budget < time.Millisecond {
		budget = time.Millisecond
	}
	return budget
}

// runInTx executes one compiled statement under the two settings that
// make the bounds real rather than intended.
//
// **A read-only transaction** is what makes "the compiler only ever emits
// SELECT" a guarantee instead of a property of the current code. The
// compiler is careful; a transaction that refuses a write is careful
// forever. TestEveryQueryRunsInAReadOnlyTransaction asserts the refusal
// with SQLSTATE 25006.
//
// It is said twice, and the second saying is not the one the plan wrote.
// `BEGIN READ ONLY` (pgx.ReadOnly) refuses on its own, and so does
// `transaction_read_only`; `default_transaction_read_only`, which the
// plan's block set, **does not** — it is the default for transactions
// started *later*, so setting it inside this one is a line that reads as
// protection and provides none. Measured against Postgres rather than
// assumed: with only that setting, an INSERT in this transaction
// succeeds. See Task 7's corrections.
//
// **statement_timeout** bounds the work. Past it Postgres raises 57014,
// which internal/metamodel.IsRetryable already admits and internal/web
// already maps to the `retryable` wire code — so a timed-out view does
// not get a ninth code that means the same thing. What it does get is the
// advice completed, by runFailure: the error names the elapsed budget and
// the three bounds to lower, because "send the same call again" is right
// for contention and wrong for a query that is simply too expensive, and
// the caller cannot tell those apart from the code alone.
//
// **Both settings travel as bind arguments through set_config, not as
// formatted text.** The plan's block spelled the milliseconds into a `SET
// LOCAL` with fmt.Sprintf and called it the one deliberate exception to
// this package's no-value-in-the-statement-text rule; set_config takes
// its value as a parameter, so there is no exception to make. One
// statement, one round trip, and
// TestTheOnlyStringToFragmentConversionsAreTheOnesNamedHere keeps
// watching a package where nothing formats a value into SQL at all.
func (s *Service) runInTx(ctx context.Context, timeout time.Duration, statement string,
	args []any, scan func(pgx.Rows) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin read-only: %w", err)
	}
	// Rolled back always: nothing here writes, and a read-only transaction
	// has nothing to commit.
	defer func() { _ = tx.Rollback(ctx) }()
	// is_local = true, which is what SET LOCAL means: both settings are
	// undone when this transaction ends, so a pooled connection handed to
	// the next caller carries neither.
	//
	// **The settings are read back rather than assumed.** set_config
	// returns the value that landed, and the value that landed is the only
	// thing that bounds the statement: `statement_timeout = 0` is
	// Postgres's spelling of *no timeout*, so a budget that arrived as
	// zero — a knob passed instead of statementBudget, a duration that
	// truncated to nothing — would buy an unbounded run while every table
	// in the documentation says 5s. That is a refusal here rather than a
	// comment, and observeBounds lets the package's own tests assert the
	// value the database is holding through Run's own path.
	var appliedReadOnly, appliedTimeout string
	if err := tx.QueryRow(ctx,
		`SELECT set_config('transaction_read_only', 'on', true),
		        set_config('statement_timeout', $1, true)`,
		strconv.FormatInt(timeout.Milliseconds(), 10)+"ms").
		Scan(&appliedReadOnly, &appliedTimeout); err != nil {
		return fmt.Errorf("bound the transaction: %w", err)
	}
	if appliedTimeout == "0" || appliedReadOnly != "on" {
		return fmt.Errorf("views: the transaction came back unbounded "+
			"(statement_timeout=%q, transaction_read_only=%q) for a budget of %s; "+
			"an unbounded statement is not a view", appliedTimeout, appliedReadOnly, timeout)
	}
	if s.observeBounds != nil {
		s.observeBounds(appliedTimeout, appliedReadOnly)
	}
	rows, err := tx.Query(ctx, statement, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if err := scan(rows); err != nil {
		return err
	}
	// Closed here rather than only by the defer, because pgx settles a
	// failure into rows.Err() when the result is finished with, not when
	// Query returns: a statement whose first response is an error — the
	// read-only refusal below, a statement_timeout that fired before any
	// row — leaves Err() nil until the rows are closed, so a scan that
	// never iterated would return success on a statement the database
	// refused. TestEveryQueryRunsInAReadOnlyTransaction is the test that
	// found this; Close is idempotent, so the defer stays for the paths
	// that return above.
	rows.Close()
	return rows.Err()
}

// TimeoutError is a run that ran out of its statement budget.
//
// It exists to complete the advice, not to add a ninth wire code: it
// unwraps to the *pgconn.PgError carrying 57014, so metamodel.IsRetryable
// still admits it and internal/web still maps it to `retryable`. What the
// code cannot say is which of "the database was busy" and "this query is
// too expensive as written" happened, and only the second has a recovery
// the caller can act on — so the message names the budget that elapsed
// and the three bounds to lower.
//
// **Task 15 has to map this type explicitly.** mcpErrorFor's retryable
// arm deliberately drops the database's own message and substitutes a
// generic one, which is right for a lock wait and would throw this advice
// away; the views tools need an arm for *TimeoutError before that one.
// Recorded in Task 7's corrections and in Task 15's block.
type TimeoutError struct {
	Budget time.Duration
	Limits ResolvedLimits
	err    error
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("this view ran longer than its %s budget and was cancelled: "+
		"send it again if the database was merely busy, but if it fails again the query is "+
		"too expensive as written rather than unlucky — lower max_depth (now %d), max_nodes "+
		"(now %d) or max_edges (now %d), narrow the selector it starts from, or drop a "+
		"traverse step", e.Budget, e.Limits.MaxDepth, e.Limits.MaxNodes, e.Limits.MaxEdges)
}

func (e *TimeoutError) Unwrap() error { return e.err }

// runFailure turns the one database failure this package can say
// something useful about into the sentence that says it, and leaves every
// other error exactly as it arrived.
//
// 57014 is query_canceled, which is what statement_timeout raises. It is
// also what an operator cancelling a backend raises, and the two are
// indistinguishable here — which costs nothing, because the advice
// ("resend; if it fails again, ask for less") is right for both.
func runFailure(err error, budget time.Duration, limits ResolvedLimits) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgErrQueryCanceled {
		return err
	}
	return &TimeoutError{Budget: budget, Limits: limits, err: err}
}

// pgErrQueryCanceled is SQLSTATE 57014, one of the four
// metamodel.IsRetryable admits.
const pgErrQueryCanceled = "57014"
