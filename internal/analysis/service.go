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
	// beforeWalk, when set, runs inside CheckRoute between the read of
	// the game's design version and the first walk. It is written only
	// by this package's own check tests and nothing in production sets
	// it.
	beforeWalk func()
}

// New builds the service. The hub may be nil, in which case nothing is
// published; this package's own tests run that way.
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
