package metamodel_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/testutil"
)

// area is one database shared by every claim in a test file.
//
// Isolation in this package is by project, not by database: every write
// carries a project_id and newProject hands each claim a game of its
// own, so a claim needs its own *project* and not its own migrated
// template copy. One area per file, one project per claim.
//
// A claim that changes the schema — the deferred constraint that makes a
// commit fail — still takes a database of its own through
// testutil.NewPool, because an altered table is not scoped by project.
type area struct{ pool *pgxpool.Pool }

func newArea(t *testing.T) area {
	t.Helper()
	return area{pool: testutil.NewPool(t)}
}
