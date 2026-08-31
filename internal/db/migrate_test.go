package db_test

import (
	"context"
	"testing"

	"github.com/neverbot/maestro/internal/db"
	"github.com/neverbot/maestro/internal/testutil"
)

func TestMigrationsCreateIdentityTables(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	for _, table := range []string{"users", "projects", "memberships", "sessions", "invites", "api_tokens"} {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			table).Scan(&exists)
		if err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %s was not created", table)
		}
	}
}

// TestMigrateIsIdempotent guards against the instance-scoped provider
// reintroducing goose's package-global state: running Migrate twice against
// the same already-migrated database must be a clean no-op, not an error.
func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	// testutil.NewPool already ran Migrate once; running it again here
	// must succeed without applying anything a second time.
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate call: %v", err)
	}
}
