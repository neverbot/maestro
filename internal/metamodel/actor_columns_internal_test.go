package metamodel

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/testutil"
)

// TestEveryCompositeTokenKeyIsNamedInActorColumns is the sweep rather
// than a list, and it exists because the list has now been one column
// short three times.
func TestEveryCompositeTokenKeyIsNamedInActorColumns(t *testing.T) {
	pool := testutil.NewPool(t)
	rows, err := pool.Query(context.Background(), `
		SELECT conname
		FROM pg_constraint
		WHERE contype = 'f' AND confrelid = 'api_tokens'::regclass
		ORDER BY conname`)
	assert.Must(t, err == nil, "read the foreign keys into api_tokens: %v", err)
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan a constraint name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the constraint names: %v", err)
	}
	// The vacuity guard: a query that found nothing would pass the loop
	// below without asserting anything, which is how a sweep test dies
	// quietly. There are nine such keys today across three migrations,
	// under three distinct column prefixes.
	assert.Must(t, len(names) >= 9, "found %d foreign keys into api_tokens (%v), want at least the nine "+
		"the migrations declare: this query has stopped finding them",
		len(names), names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			err := ActorConstraintViolation(&pgconn.PgError{
				Code: "23503", ConstraintName: name,
			})
			assert.Must(t, errors.Is(err, ErrActorNotInGame), "a 23503 over %q maps to %v, want ErrActorNotInGame: no prefix "+
				"in actorColumns (%v) matches it, so this refusal reaches a log as a "+
				"generated constraint name", name, err, actorColumns)
		})
	}

	// The negative control in the same test: a 23503 over a key that is
	// not an actor column passes through unchanged, so the assertions
	// above cannot be passing because the scan says yes to everything.
	other := &pgconn.PgError{Code: "23503", ConstraintName: "view_refs_view_id_fkey"}
	if err := ActorConstraintViolation(other); errors.Is(err, ErrActorNotInGame) {
		t.Fatal("a foreign key that is not an actor column must pass through unchanged")
	}
	// And a prefix that matches under a code that is not 23503 is not a
	// scope violation either.
	notNullish := &pgconn.PgError{Code: "23502", ConstraintName: "updated_by_token_id"}
	if err := ActorConstraintViolation(notNullish); errors.Is(err, ErrActorNotInGame) {
		t.Fatal("only 23503 is a foreign-key violation; every other code passes through")
	}
}
