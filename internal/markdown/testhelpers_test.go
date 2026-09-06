package markdown_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
)

// newService gives a test its own throwaway database, a markdown
// service and a metamodel service over the same pool. The hub is real,
// so the event tests have something to subscribe to; a test that does
// not care simply ignores it.
func newService(t *testing.T) (*markdown.Service, *metamodel.Service, *realtime.Hub, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	return markdown.New(pool, hub), metamodel.New(pool, hub), hub, pool
}

// newGame inserts a game with a plain INSERT through the pool the test
// already holds. There is deliberately no production "create a bare
// project for tests" API: it would ship in the binary and be callable
// over MCP.
func newGame(t *testing.T, pool *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`, slug).Scan(&id); err != nil {
		t.Fatalf("insert project %s: %v", slug, err)
	}
	return id
}

// requireFieldError asserts the exact path and the exact message of a
// refusal. Every negative test in this package goes through it: a test
// that asserts only err != nil is not a test of the thing it is named
// after, and six mutations once survived twenty-six such tests in the
// metamodel package.
func requireFieldError(t *testing.T, err error, wantPath, wantMessage string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want a refusal at %q, got nil", wantPath)
	}
	var v *metamodel.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("want a *metamodel.ValidationError, got %#v", err)
	}
	for _, f := range v.Fields {
		if f.Path == wantPath && strings.Contains(f.Message, wantMessage) {
			return
		}
	}
	t.Fatalf("no problem at %q containing %q; got %v", wantPath, wantMessage, v.Fields)
}

// requireMissing is requireFieldError for a *markdown.MissingError,
// which is not a *metamodel.ValidationError and so does not go through
// that helper. It exists because this package's not_found refusals
// differ from each other only in their message — "already deleted" and
// "never here" carry the same sentinel and the same path — so a test
// asserting the sentinel alone cannot tell them apart, and both have
// different recoveries for the caller.
func requireMissing(t *testing.T, err error, wantPath, wantMessage string) {
	t.Helper()
	var missing *markdown.MissingError
	if !errors.As(err, &missing) {
		t.Fatalf("want a *markdown.MissingError, got %#v", err)
	}
	if missing.Path != wantPath || !strings.Contains(missing.Message, wantMessage) {
		t.Fatalf("missing = (%q, %q), want path %q with a message containing %q",
			missing.Path, missing.Message, wantPath, wantMessage)
	}
}

// requireConflict is requireFieldError for a *markdown.ConflictError,
// which is neither a ValidationError nor a MissingError. It returns the
// conflict so a caller can go on to assert the version, the author and
// the payload it publishes.
func requireConflict(t *testing.T, err error) *markdown.ConflictError {
	t.Helper()
	var conflict *markdown.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want a *markdown.ConflictError, got %#v", err)
	}
	return conflict
}

func ptrInt32(v int32) *int32 { return &v }

func ptrString(v string) *string { return &v }
