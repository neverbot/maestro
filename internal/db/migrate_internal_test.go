// This file's tests live in package db (not db_test) so they can reach the
// unexported migrateDown. It cannot import internal/testutil: testutil
// imports this package to call Migrate, so a package-db test importing
// testutil back would be an import cycle. newTestDatabase below is a
// deliberately minimal, single-purpose stand-in for testutil.NewPool used
// by exactly the one test that needs migrateDown.
package db

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestDatabase creates a uniquely named, unmigrated database and drops
// it on cleanup. It skips the test when TEST_DATABASE_URL is unset.
func newTestDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()

	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminDB, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	t.Cleanup(adminDB.Close)

	name := "maestro_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ident := pgx.Identifier{name}.Sanitize()

	if _, err := adminDB.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if _, err := adminDB.Exec(dropCtx, "DROP DATABASE "+ident+" WITH (FORCE)"); err != nil {
			t.Logf("drop database %s: %v", name, err)
		}
	})

	u, err := url.Parse(adminURL)
	if err != nil || u.Scheme == "" {
		t.Fatalf("TEST_DATABASE_URL must be a postgres:// URL, got %q", adminURL)
	}
	u.Path = "/" + name

	pool, err := NewPool(ctx, u.String())
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// TestMigrateUpDownUp exercises the Down side of every migration, so a
// broken rollback is caught here rather than mid-incident. It is the only
// place in the codebase able to call migrateDown.
//
// migrateDown rolls back a single migration (the most recent one goose
// hasn't already rolled back), so tearing down everything Migrate applied
// means calling it once per migration file, not once. This is currently
// three calls because there are currently three migrations (0001, 0002,
// 0003); a fourth migration needs a fourth call here, the same way it
// needs its own entry in Task 8's file structure table.
func TestMigrateUpDownUp(t *testing.T) {
	t.Parallel()
	pool := newTestDatabase(t)
	ctx := context.Background()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := migrateDown(ctx, pool); err != nil {
		t.Fatalf("migrateDown (0003): %v", err)
	}
	if err := migrateDown(ctx, pool); err != nil {
		t.Fatalf("migrateDown (0002): %v", err)
	}
	if err := migrateDown(ctx, pool); err != nil {
		t.Fatalf("migrateDown (0001): %v", err)
	}

	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'users')`,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("query users after down: %v", err)
	}
	if exists {
		t.Fatalf("table users still exists after migrateDown")
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate after down: %v", err)
	}

	err = pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'users')`,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("query users after re-up: %v", err)
	}
	if !exists {
		t.Fatalf("table users was not recreated after re-up")
	}
}
