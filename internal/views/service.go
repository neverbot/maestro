package views

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
)

// Actor is the metamodel's, aliased rather than redeclared: the audit
// columns on views are the same two columns, filled from the same caller.
type Actor = metamodel.Actor

// Service is the views domain.
//
// It holds a metamodel service rather than its own dbq handle for the
// game's vocabulary, and that is the isolation decision this package
// makes once: EntityTypeByKey, ListEntityTypes, RelationTypeByKey and
// ListRelationTypes already take a project id and already filter on it in
// SQL, so reading through them is one implementation of the rule instead
// of a fourth copy of it. LoadCatalogue names the two it uses.
// It holds the pool as well, and only since Task 6: the compiler builds
// its statement at run time, so a compiled query is the one thing in this
// package that cannot go through sqlc and therefore the one thing that
// needs a pool rather than a *dbq.Queries. Task 11's saved-view CRUD adds
// that dbq handle beside these. The hub stays because it cannot be
// rebuilt from anything else the service holds and New's contract is
// where it arrives.
type Service struct {
	pool *pgxpool.Pool
	meta *metamodel.Service
	hub  *realtime.Hub
	// statementTimeout is the per-run statement budget, and it is
	// unexported and settable from nowhere but this package because that
	// is what lets runInTx say the value reaching Postgres is one this
	// package computed from its own constants. Zero means
	// DefaultStatementTimeout; anything above HardStatementTimeout is
	// clamped to it (statementBudget). The package's own bounds tests are
	// what write it, so that the timeout path can be exercised without a
	// slow query — nothing else does.
	statementTimeout time.Duration
	// observeBounds, when set, is handed the two settings the database
	// reported back after runInTx installed them — the values Postgres is
	// actually holding for this transaction, not the values Go computed.
	// It is unexported and written only by this package's bounds tests,
	// which is the only way to assert *through Run's own path* that the
	// budget reaching the statement is statementBudget's and not the raw
	// knob: on the default path the knob is zero, `0ms` means no timeout
	// at all in Postgres, and a run that quietly lost its bound looks
	// exactly like one that kept it. Nothing in production sets it.
	observeBounds func(statementTimeout, readOnly string)
}

// New builds the service. The hub may be nil, in which case nothing is
// published; the package's own tests run that way.
//
// The metamodel handle it builds is deliberately given no hub. This
// package calls only read accessors on it, and the rule that keeps that
// true is that a views write path publishes through this package's own
// events (Task 11) rather than through a second hub two packages away —
// not that the metamodel service here is incapable of writing.
func New(pool *pgxpool.Pool, hub *realtime.Hub) *Service {
	return &Service{pool: pool, meta: metamodel.New(pool, nil), hub: hub}
}
