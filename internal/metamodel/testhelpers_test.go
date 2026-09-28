package metamodel_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/testutil"
)

// area is one database shared by every claim in a test file.
type area struct{ pool *pgxpool.Pool }

func newArea(t *testing.T) area {
	t.Helper()
	return area{pool: testutil.NewPool(t)}
}
