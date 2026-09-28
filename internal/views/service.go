package views

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/roles"
)

// Actor is the metamodel's, aliased rather than redeclared: the audit
// columns on views are the same two columns, filled from the same caller.
type Actor = metamodel.Actor

// Service is the views domain.
type Service struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
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
func New(pool *pgxpool.Pool, hub *realtime.Hub) *Service {
	return &Service{pool: pool, q: dbq.New(pool), meta: metamodel.New(pool, hub), hub: hub}
}

// withTx runs fn inside a transaction, rolling back unless it returns
// nil.
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
