package analysis

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// game is one throwaway database, one project in it, and the two
// services these tests drive: this package's, and the metamodel's, which
// is how a fixture declares the relation types the resolver then reads.
type game struct {
	pool      *pgxpool.Pool
	analysis  *Service
	meta      *metamodel.Service
	projectID uuid.UUID
}

// newGame builds that fixture. The hub is nil: nothing in this package's
// own tests subscribes, and internal/web is where publication is
// asserted end to end.
func newGame(t *testing.T) game {
	t.Helper()
	pool := testutil.NewPool(t)
	slug := "azeroth-" + uuid.NewString()[:8]
	var projectID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`, slug).
		Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return game{
		pool:      pool,
		analysis:  New(pool, nil),
		meta:      metamodel.New(pool, nil),
		projectID: projectID,
	}
}

// declareRelationType declares one relation type with the traits and the
// role a test is about. Either may be empty: a type with neither is the
// undeclared case, which several tests need as a control.
func (g game) declareRelationType(t *testing.T, key, role string, traits []string) uuid.UUID {
	t.Helper()
	row, err := g.meta.UpsertRelationType(context.Background(), g.projectID,
		metamodel.RelationTypeInput{
			Key: key, Label: key, SemanticRole: role, AnalysisTraits: traits,
		})
	if err != nil {
		t.Fatalf("declare relation type %q: %v", key, err)
	}
	return row.ID
}

// readSourceFile reads one of this package's own files, for the tests
// whose subject is an argument recorded in a comment rather than a value
// a call returns. A decision with no argument beside it is one the next
// task makes differently, so "the argument is there" is worth a test in
// exactly the places the plan spent a paragraph on the count of
// something.
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}
