package metamodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/roles"
)

// Actor records who performed a write, for the audit columns. Both fields
// are optional and both may be set at once: a human editing through the UI
// carries a UserID, an agent carries the TokenID of the credential it
// authenticated with, and a token always belongs to the member who minted
// it. A token id that belongs to another game is rejected by the database,
// not here (0004_metamodel.sql's composite keys).
type Actor struct {
	UserID  *uuid.UUID
	TokenID *uuid.UUID
}

// Service is the metamodel domain.
type Service struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
	hub  *realtime.Hub
}

// New builds the service. The hub may be nil, in which case nothing is
// published; the package's own tests run that way.
func New(pool *pgxpool.Pool, hub *realtime.Hub) *Service {
	return &Service{pool: pool, q: dbq.New(pool), hub: hub}
}

// publish emits a change event, if a hub is attached.
//
// minRole and humanOnly are passed explicitly rather than inferred from
// kind, exactly as internal/web's own publish does, so each call site
// shows the gating it chose instead of inheriting one from a table three
// files away. The values themselves are named constants declared beside
// the kind they belong to, in events.go, which is where the reasoning
// for each lives; a helper that could not express these fields at all —
// the shape this package shipped with — silently made every event as
// open as the hub's zero value, whether or not that was the right answer.
//
// A payload carries only the identity of what changed — a key, an id —
// and never a value a client could then treat as current. Publication
// order is not commit order (internal/web/publish.go's package comment
// works through why), so a payload holding, say, a type's new label could
// stably tell a client the wrong label with nothing to signal it. The
// client re-reads instead.
//
// **Every caller must call this after withTx has returned, never from
// inside fn.** An event published inside the transaction announces a
// change that may still roll back, and a subscriber that re-reads on
// hearing it — which is the only thing this hub's payloads let it do —
// would read the state before the change and cache it as the state
// after. The hub itself cannot enforce that; the metamodel's own tests
// pin it, including the last placement — a publish as the final
// statement inside fn, which differs from the correct one only by the
// commit that follows: TestNoEventIsPublishedWhenTheCommitFails installs
// a deferred constraint in the test's own throwaway database so the
// commit, and only the commit, fails. The other two are
// TestNoEventIsPublishedWhenTheWriteIsRolledBack and
// TestNothingIsAnnouncedWhileTheTransactionIsStillOpen.
func (s *Service) publish(projectID uuid.UUID, kind string, minRole roles.Role, humanOnly bool, payload any) {
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

// withTx runs fn inside a transaction, rolling back unless it returns nil.
//
// Every mutation in this package needs one: a write and the re-validation
// sweep that follows it are one change, and half of it landing would leave
// a type declaring a schema its own entities were never re-checked against.
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

// decodeFields turns a stored jsonb blob back into a value map.
func decodeFields(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode fields: %w", err)
	}
	return out, nil
}

// actorConstraintViolation recognises a write refused because its Actor
// does not belong to the project being written to, and returns
// ErrActorNotInGame; every other error passes through unchanged.
//
// It matches on the constraint's column rather than its full generated
// name (entity_types_updated_by_token_id_project_id_fkey today) so that
// Tasks 4, 5 and 6 get the same mapping for entities, relation types and
// relations without four near-identical name lists to keep in step: every
// table in 0004_metamodel.sql carries the same two audit columns under
// the same two constraint shapes. A missing updated_by_user_id is folded
// in with it — a user id that resolves to no row is the same class of
// fault, an actor this instance cannot vouch for, arriving from the same
// place.
func actorConstraintViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return err
	}
	if strings.Contains(pgErr.ConstraintName, "updated_by_token_id") ||
		strings.Contains(pgErr.ConstraintName, "updated_by_user_id") {
		return ErrActorNotInGame
	}
	return err
}

// notFound maps pgx's no-rows sentinel onto the domain's, leaving every
// other error wrapped with what was being looked up.
func notFound(err error, what string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", what, err)
}
