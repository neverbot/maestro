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

// ActorConstraintViolation recognises a write refused because its Actor
// does not belong to the project being written to, and returns
// ErrActorNotInGame; every other error passes through unchanged.
var actorColumns = []string{
	"updated_by_token_id", "updated_by_user_id",
	"created_by_token_id", "created_by_user_id",
	"author_token_id", "author_user_id",
}

func ActorConstraintViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		return err
	}
	for _, column := range actorColumns {
		if strings.Contains(pgErr.ConstraintName, column) {
			return ErrActorNotInGame
		}
	}
	return err
}

// searchLimitExceeded recognises a write refused because a value was too
// large for something the database had to build from it, and reports it
// as the caller's own input rather than as a server fault; every other
// error passes through unchanged.
func searchLimitExceeded(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "54000" {
		return err
	}
	return fmt.Errorf("%w: a value of this row is too large to index (%s): %w",
		ErrInvalidInput, pgErr.Message, err)
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

// missingByID is what a removal says when the id it was given names no
// row of this game — review finding L4.
func missingByID(what string, id uuid.UUID) error {
	return fmt.Errorf("%w: this game has no %s with id %s", ErrNotFound, what, id)
}

// notFoundByID is notFound for a lookup addressed by a caller-supplied
// id: no-rows becomes a named ErrNotFound rather than the bare
// sentinel, and anything else keeps its context.
func notFoundByID(err error, what string, id uuid.UUID, doing string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return missingByID(what, id)
	}
	return fmt.Errorf("%s: %w", doing, err)
}

// stillInUse is what a removal without cascade says when rows still
// point at the type — the other half of finding L4.
func stillInUse(what, key, holders string) error {
	return fmt.Errorf("%w: the %s %q still has %s; remove them first, "+
		"or pass cascade to remove them along with it", ErrInUse, what, key, holders)
}

// retryableSQLStates are the SQLSTATEs IsRetryable admits.
var retryableSQLStates = map[string]bool{
	"40001": true,
	"40P01": true,
	"55P03": true,
	"57014": true,
}

// IsRetryable reports whether err is a database failure that the
// identical call, resent unchanged, may survive.
func IsRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return retryableSQLStates[pgErr.Code]
}
