package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/roles"
)

// Actor is the metamodel's, aliased rather than redeclared: the audit
// columns on routes are the same two columns, filled from the same
// caller.
type Actor = metamodel.Actor

// dbqRelationType is the catalogue row this package reads, named locally
// so the resolver's signature says what it takes rather than carrying a
// generated type's name through every helper. It is an alias and not a
// copy: a field added to the table reaches this package with no edit.
type dbqRelationType = dbq.RelationType

// Service is the analysis domain.
//
// It holds a metamodel service rather than its own catalogue reads, and
// that is this package's isolation decision made once: ListRelationTypes
// and RelationTypeByKey already take a project id and already filter on
// it in SQL, so reading through them is one implementation of the rule
// instead of a fourth copy of it. It holds the pool as well, because the
// three read-only analyses run SQL that internal/graph emits at run time
// and therefore cannot go through sqlc — exactly as internal/views does
// for its compiler — and it holds a dbq handle for the route CRUD, every
// statement of which is generated.
type Service struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
	meta *metamodel.Service
	hub  *realtime.Hub
	// statementTimeout is the per-run statement budget, unexported and
	// settable from nowhere but this package's own bounds tests. That is
	// what lets runInTx say the value reaching Postgres is one this
	// package computed from its own constants. Zero means
	// DefaultStatementTimeout; anything above HardStatementTimeout is
	// clamped to it — see statementBudget, and note that this is the one
	// clamp in the package and it is not a caller's argument.
	statementTimeout time.Duration
	// observeBounds, when set, is handed the two settings **the database
	// reported back** after runInTx installed them, not the values Go
	// computed. It is written only by this package's bounds tests, and it
	// is not decoration: on the default path the knob is zero, `0ms`
	// means *no timeout at all* in Postgres, and a run that quietly lost
	// its bound looks exactly like one that kept it. Nothing in
	// production sets it.
	observeBounds func(statementTimeout, readOnly string)
}

// New builds the service. The hub may be nil, in which case nothing is
// published; this package's own tests run that way.
//
// The metamodel handle it builds gets the same hub, for the reason
// internal/views/service.go records: a composed metamodel write must
// still announce itself, and a nil hub there buys silence rather than a
// boundary.
func New(pool *pgxpool.Pool, hub *realtime.Hub) *Service {
	return &Service{pool: pool, q: dbq.New(pool), meta: metamodel.New(pool, hub), hub: hub}
}

// withTx runs fn inside a read-write transaction, rolling back unless it
// returns nil. The route CRUD needs one: a route's row and its whole
// ordered step list are one change, and a step rewrite that landed beside
// a route that rolled back would leave a walk nobody authored.
func (s *Service) withTx(ctx context.Context, fn func(*dbq.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(dbq.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// publish emits a change event, if a hub is attached.
//
// minRole and humanOnly are passed explicitly rather than inferred from
// kind, exactly as internal/metamodel's and internal/views' own publish
// do, so each call site shows the gating it chose instead of inheriting
// one from a table three files away.
//
// **Every caller must call this after withTx has returned, never from
// inside fn.** An event published inside the transaction announces a
// change that may still roll back, and a subscriber that re-reads on
// hearing it — the only thing this hub's payloads let it do — would read
// the state before the change and cache it as the state after.
func (s *Service) publish(projectID uuid.UUID, kind string, minRole roles.Role,
	humanOnly bool, payload any,
) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(realtime.Event{
		ProjectID: projectID,
		Kind:      kind,
		MinRole:   string(minRole),
		HumanOnly: humanOnly,
		Payload:   payload,
	})
}

// statementBudget is the timeout one analysis gets.
//
// It is DefaultStatementTimeout unless a test set the package-private
// knob, and it is clamped to HardStatementTimeout either way — so a knob
// set past the ceiling cannot buy a longer run than the documentation
// promises. **This is the package's only clamp, and it is not a
// caller's argument**: nothing on any surface can set it, so there is no
// caller to mislead about what bound their answer was computed under.
// Every bound a caller *can* state is refused rather than clamped; see
// bounds.go.
func (s *Service) statementBudget() time.Duration {
	budget := s.statementTimeout
	if budget <= 0 {
		budget = DefaultStatementTimeout
	}
	if budget > HardStatementTimeout {
		budget = HardStatementTimeout
	}
	return budget
}

// runInTx executes one statement under the two settings that make an
// analysis an analysis: a statement timeout, and a transaction that
// cannot write.
//
// **`SET LOCAL default_transaction_read_only` does not make its own
// transaction read-only.** That is a measurement internal/views made and
// this package inherits rather than re-derives: the setting is consulted
// when a transaction *starts*, so setting it inside one changes nothing
// about that one. What works is `transaction_read_only`, set with
// set_config, on a transaction already begun in read-only mode —
// belt and braces, because the BeginTx option alone would be silently
// lost by any future refactor that reached for a plain Begin.
// TestAnAnalysisTransactionIsActuallyReadOnly asserts the refusal
// (SQLSTATE 25006) rather than the settings, because the question is not
// whether two lines ran but whether a write that reached this path would
// be stopped.
//
// **The settings are read back rather than assumed.** set_config returns
// the value that landed, and the value that landed is the only thing
// that bounds the statement: `statement_timeout = 0` is Postgres's
// spelling of *no timeout*, so a budget that arrived as zero would buy
// an unbounded run while every table in the documentation says five
// seconds.
func (s *Service) runInTx(ctx context.Context, timeout time.Duration, statement string,
	args []any, scan func(pgx.Rows) error,
) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin read-only: %w", err)
	}
	// Rolled back always: nothing here writes, and a read-only
	// transaction has nothing to commit.
	defer func() { _ = tx.Rollback(ctx) }()

	var appliedReadOnly, appliedTimeout string
	if err := tx.QueryRow(ctx,
		`SELECT set_config('transaction_read_only', 'on', true),
		        set_config('statement_timeout', $1, true)`,
		strconv.FormatInt(timeout.Milliseconds(), 10)+"ms").
		Scan(&appliedReadOnly, &appliedTimeout); err != nil {
		return fmt.Errorf("bound the transaction: %w", err)
	}
	if appliedTimeout == "0" || appliedReadOnly != "on" {
		return fmt.Errorf("analysis: the transaction came back unbounded "+
			"(statement_timeout=%q, transaction_read_only=%q) for a budget of %s; "+
			"an unbounded walk over a whole game is not an analysis",
			appliedTimeout, appliedReadOnly, timeout)
	}
	if s.observeBounds != nil {
		s.observeBounds(appliedTimeout, appliedReadOnly)
	}

	rows, err := tx.Query(ctx, statement, args...)
	if err != nil {
		return timedOut(err, timeout)
	}
	defer rows.Close()
	if err := scan(rows); err != nil {
		return timedOut(err, timeout)
	}
	// Closed here rather than only by the defer, because pgx settles a
	// failure into rows.Err() when the result is finished with, not when
	// Query returns: a statement whose first response is an error — the
	// read-only refusal, a statement_timeout that fired before any row —
	// leaves Err() nil until the rows are closed, so a scan that never
	// iterated would return success on a statement the database refused.
	// Close is idempotent, so the defer stays for the paths that return
	// above. internal/views found this the hard way and this package does
	// not get to find it again.
	rows.Close()
	return timedOut(rows.Err(), timeout)
}

// timedOut is the one place a statement that exhausted its budget
// becomes the answer errors.go argues for.
//
// **No `analysis_timeout` code.** SQLSTATE 57014 is already in
// metamodel's retryableSQLStates and metamodel.IsRetryable already
// answers for it, so the error that comes back is already `retryable`,
// whose recovery is "change nothing and resend" -- and what this adds is
// the half a bare cancellation does not carry: the budget it exhausted
// and the four arguments that narrow a run. internal/views extended a
// message rather than adding a code four days before this package
// existed; adding one here would be the standing defect in its purest
// form. errors.go carries the argument at length.
//
// It wraps rather than replaces, so metamodel.IsRetryable -- which reads
// the *pgconn.PgError through errors.As -- still answers true however
// many layers of fmt.Errorf a path adds above it. A test that only
// asserted the sentence would pass over an error that had stopped being
// retryable, so TestATimedOutAnalysisIsRetryableAndSaysWhichBoundToLower
// asserts both.
//
// **It sits in runInTx and not in one analysis**, because every analysis
// in this package reads through that one function and a per-analysis
// wrap is the shape this repository forgets on the fourth call site.
func timedOut(err error, budget time.Duration) error {
	if err == nil || !metamodel.IsRetryable(err) {
		return err
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != statementTimeoutSQLState {
		// Retryable for another reason -- a serialisation failure, a
		// deadlock -- and those recover by resending unchanged, with no
		// bound to lower. Passed through rather than dressed up as a
		// timeout.
		return err
	}
	return fmt.Errorf("this analysis exhausted its %s budget and Postgres cancelled it. "+
		"Nothing you sent is wrong, so resending unchanged may work; what makes it "+
		"likely to is narrowing the run with `max_depth`, `entity_types` or "+
		"`relation_types` -- or, the one that helps most, `seed_entity_types`, since a "+
		"walk seeded from every entity in the game is the expensive shape by "+
		"construction: %w", budget, err)
}

// statementTimeoutSQLState is Postgres's query_canceled, which is what a
// statement_timeout raises. Spelled once, beside the only reader.
const statementTimeoutSQLState = "57014"

// DecodeArgs decodes one analysis call's arguments and **refuses any
// member it does not know**.
//
// This is O5's reservation. Views reserved its own extension point by
// making the query decoder refuse unknown top-level keys, so a `source`
// key can later be added as an additive change to a document that would
// previously have been refused; this package's inputs are flat structs
// with no such document, and this is the equivalent.
//
// It is also the more immediate defence: `{"seed_entites": ["a"]}` is
// the typo an agent will actually make, and silently ignoring it would
// answer "your entire game is unreachable" to a caller who did supply
// seeds. That is a wrong answer in the right shape, which is the worst
// thing this engine can produce.
func DecodeArgs(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return unknownField(err)
	}
	// Anything after the first value is refused too: a body carrying two
	// JSON documents is not an argument object with a typo in it, and
	// accepting the first would silently discard the second.
	if dec.More() {
		return invalidInput("", "carries data after the arguments object")
	}
	return nil
}
