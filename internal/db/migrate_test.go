package db_test

import (
	"context"
	"testing"

	"github.com/neverbot/maestro/internal/testutil"
)

func TestMigrationsCreateIdentityTables(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()

	for _, table := range []string{"users", "projects", "memberships", "sessions", "invites", "api_tokens"} {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`,
			table).Scan(&exists)
		if err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %s was not created", table)
		}
	}
}
