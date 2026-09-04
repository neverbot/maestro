package metamodel

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/testutil"
)

// TestEveryCompositeTokenKeyIsNamedInActorColumns is the sweep rather
// than a list, and it exists because the list has now been one column
// short three times.
//
// actorColumns started as the two `updated_by_` names, which was
// complete for 0004_metamodel.sql; 0007_documents.sql added `author_`
// on document_versions and 0008_views.sql added `created_by_` on
// view_assets, and each was noticed only when a foreign token's write
// came back as a raw SQLSTATE 23503 over a generated constraint name.
// Three tables, three separate discoveries, one list.
//
// So this test asks the database rather than repeating the names: every
// FOREIGN KEY in the schema whose target is api_tokens is an actor
// column by construction — api_tokens is scoped to a project and
// nothing else references it — and the mapping must recognise the
// constraint name Postgres generated for it. A migration that adds a
// fourth prefix fails here, in the same run that creates it, instead of
// two sub-projects later.
//
// The refusal itself is synthesised rather than provoked: provoking one
// requires a write path per table, three of which do not exist yet, and
// the thing under test is the name scan and not the database's
// willingness to refuse — which the per-domain tests
// (TestTheUploaderIsRecordedAndAForeignTokenIsRefused,
// TestADocumentWrittenWithAnotherGamesTokenIsRefusedAsSuch) already
// drive end to end.
func TestEveryCompositeTokenKeyIsNamedInActorColumns(t *testing.T) {
	pool := testutil.NewPool(t)
	rows, err := pool.Query(context.Background(), `
		SELECT conname
		FROM pg_constraint
		WHERE contype = 'f' AND confrelid = 'api_tokens'::regclass
		ORDER BY conname`)
	if err != nil {
		t.Fatalf("read the foreign keys into api_tokens: %v", err)
	}
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
	if len(names) < 9 {
		t.Fatalf("found %d foreign keys into api_tokens (%v), want at least the nine "+
			"the migrations declare: this query has stopped finding them",
			len(names), names)
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			err := ActorConstraintViolation(&pgconn.PgError{
				Code: "23503", ConstraintName: name,
			})
			if !errors.Is(err, ErrActorNotInGame) {
				t.Fatalf("a 23503 over %q maps to %v, want ErrActorNotInGame: no prefix "+
					"in actorColumns (%v) matches it, so this refusal reaches a log as a "+
					"generated constraint name", name, err, actorColumns)
			}
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
