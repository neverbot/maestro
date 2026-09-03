package views

import (
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
type Service struct {
	pool *pgxpool.Pool
	meta *metamodel.Service
	hub  *realtime.Hub
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
