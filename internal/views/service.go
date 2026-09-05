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
// needs a pool rather than a *dbq.Queries. Task 11's saved-view CRUD
// added that dbq handle beside these — every statement it runs is
// generated, and the pool stays for the compiler alone. The hub stays
// because it cannot be rebuilt from anything else the service holds and
// New's contract is where it arrives.
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
//
// **The metamodel handle it builds gets the same hub**, and that is a
// correction rather than a convenience. This package calls read
// accessors on it almost everywhere, and the rule that keeps a views
// write path publishing through this package's own events still holds —
// but RemoveTypeReportingViews composes a metamodel *write*, and with a
// nil hub there that removal announced nothing: a designer watching a
// game would never learn a type had gone, because the one code path a
// transport can reach the report through is this one. The event is the
// metamodel's own, published by the metamodel's own code for a change it
// made; what a nil hub bought was silence, not a boundary.
// TestRemovingATypeThroughTheViewsReportStillAnnouncesIt pins it.
func New(pool *pgxpool.Pool, hub *realtime.Hub) *Service {
	return &Service{pool: pool, q: dbq.New(pool), meta: metamodel.New(pool, hub), hub: hub}
}

// withTx runs fn inside a transaction, rolling back unless it returns
// nil.
//
// UpsertView is what needs one, and needs it for the invariant
// view_refs exist to hold: a view's stored query and the dependency
// index over that query are one change, so a refs rewrite that landed
// beside a query that rolled back — or the reverse — would leave the
// index pointing at a document nobody wrote.
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
// kind, exactly as internal/metamodel's and internal/web's own publish
// do, so each call site shows the gating it chose instead of inheriting
// one from a table three files away. The values are named constants
// declared beside the kind they belong to, in events.go, which is where
// the reasoning for each lives.
//
// A payload carries only the identity of what changed and never a value
// a client could then treat as current; see viewEvent, which argues the
// one field of its three that is not an address.
//
// **Every caller must call this after withTx has returned, never from
// inside fn.** An event published inside the transaction announces a
// change that may still roll back, and a subscriber that re-reads on
// hearing it — the only thing this hub's payloads let it do — would read
// the state before the change and cache it as the state after. The hub
// cannot enforce that; TestNoViewEventIsPublishedWhenTheCommitFails
// pins the one placement a refusal test cannot catch, a publish written
// as the last statement inside fn, by making the commit and only the
// commit fail.
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
