package markdown

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/roles"
)

// Actor records who performed a write, for the audit columns. It is
// metamodel.Actor, not a second copy: the two domains record the same
// two kinds of actor into the same two shapes of column, and a token id
// belonging to another game is refused by the database in both
// (0007_documents.sql's composite keys), not here.
type Actor = metamodel.Actor

// Service is the markdown domain.
type Service struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
	hub  *realtime.Hub
}

// New builds the service. The hub may be nil, in which case nothing is
// published.
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

// withTx runs fn inside a transaction, rolling back unless it returns
// nil. Every mutation here needs one: a document row and the version row
// that records it are one change, and half of it landing would leave a
// document whose current_version names a snapshot that does not exist.
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

// The bounds on the two short strings a caller may attach to a write.
const (
	// MaxKindLen bounds `kind`, the free-text grouping label a project
	// puts on a document ("lore", "script", "pitch"). Maestro attaches
	// no meaning to it and ships no vocabulary — the same rule that
	// forbids a built-in Quest type forbids a built-in Lore kind.
	MaxKindLen = 64

	// MaxMessageLen bounds the "why this edit" line a version records.
	// It is a commit message, not a changelog entry: the change itself
	// is in the diff.
	MaxMessageLen = 500
)

// checkShortText is the one rule every single-line caller string in this
// package obeys, reported at the argument's own path.
func checkShortText(path, value string, max int) []metamodel.FieldError {
	problem := func(message string) []metamodel.FieldError {
		return []metamodel.FieldError{{Path: path, Message: message}}
	}
	if len(value) > max {
		return problem(fmt.Sprintf("must be at most %d bytes, and this one is %d", max, len(value)))
	}
	fault, bad := metamodel.CheckText(value, "")
	switch {
	case !bad:
		return nil
	case fault.InvalidUTF8:
		return problem("is not valid UTF-8: a byte in it does not decode as any character, " +
			"and Postgres refuses that outright")
	default:
		return problem(fmt.Sprintf(
			"holds a control character (%U at byte %d): this is one line of text",
			fault.Rune, fault.Offset))
	}
}

// notFound maps pgx's no-rows sentinel onto a named miss, leaving every
// other error wrapped with what was being looked up.
func notFound(err error, missing error, doing string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return missing
	}
	return fmt.Errorf("%s: %w", doing, err)
}

// oversizeForIndex recognises a write refused because a value was too
// large for the generated search vector, and reports it as the caller's
// own input rather than as a server fault; every other error passes
// through unchanged.
func oversizeForIndex(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "54000" {
		return err
	}
	return invalidInput("content", fmt.Sprintf(
		"is too large to index (%s): write less in one document", pgErr.Message))
}

// pathRespellingError is what a caller sees when its path matches an
// existing document's only case-insensitively.
func pathRespellingError(requested, stored string) error {
	return invalidInput("path", fmt.Sprintf(
		"%q already exists here spelled %q, and paths are matched without regard to case: "+
			"use %q to update it, or pick a path that differs by more than capitalisation",
		requested, stored, stored))
}
