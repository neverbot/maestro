// This file's tests live in package db (not db_test) so they can reach the
// unexported migrateDown. It cannot import internal/testutil: testutil
// imports this package to call Migrate, so a package-db test importing
// testutil back would be an import cycle. newTestDatabase below is a
// deliberately minimal, single-purpose stand-in for testutil.NewPool used
// by exactly the one test that needs migrateDown.
package db

import (
	"context"
	"io/fs"
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
// means calling it once per migration file, not once. The count comes
// from the embedded directory rather than from a literal: it used to be a
// hand-written four, which meant every new migration made this test fail
// in a way that looked like a broken rollback and was really a stale
// number. A new migration still needs its own entry in Task 8's file
// structure table; it no longer needs an edit here.
func TestMigrateUpDownUp(t *testing.T) {
	t.Parallel()
	pool := newTestDatabase(t)
	ctx := context.Background()

	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found; this test would assert nothing")
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for i := len(files) - 1; i >= 0; i-- {
		if err := migrateDown(ctx, pool); err != nil {
			t.Fatalf("migrateDown (%s): %v", files[i], err)
		}
	}

	var exists bool
	err = pool.QueryRow(ctx,
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

// TestMigrateRefusesToServeAgainstANewerSchema pins checkNoSchemaDrift:
// a database whose goose_db_version bookkeeping already names a version
// higher than any migration this binary's embedded migrations/ directory
// knows about (the shape a rollback, or an old replica in a rolling
// deploy, actually produces) must make Migrate fail loudly, not succeed
// having quietly done nothing. Before this test existed, Up() alone
// returned nil in exactly this case — goose only ever walks forward
// through the sources it recognizes, so a version row it has never heard
// of is invisible to it, not an error.
func TestMigrateRefusesToServeAgainstANewerSchema(t *testing.T) {
	t.Parallel()
	pool := newTestDatabase(t)
	ctx := context.Background()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Simulate a migration this binary has never heard of already having
	// been applied by some other (newer) binary against this same
	// database — goose's own bookkeeping table, not a real migration
	// file, is all that has to say so.
	const futureVersion = 99999999
	if _, err := pool.Exec(ctx,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`,
		futureVersion,
	); err != nil {
		t.Fatalf("insert future goose_db_version row: %v", err)
	}

	err := Migrate(ctx, pool)
	if err == nil {
		t.Fatal("Migrate succeeded against a database ahead of this binary's known migrations, want an error")
	}
	if !strings.Contains(err.Error(), "ahead of") {
		t.Fatalf("err = %v, want it to explain the schema is ahead of this binary", err)
	}
}

// TestTheAnalysisMigrationRollsBackToTheSchemaBeforeIt is the sharper
// half of TestMigrateUpDownUp, for the one migration in this repository
// whose Down arm has to undo four different *kinds* of object: a column
// with a check constraint, a column on a Core table, a plpgsql function
// with twelve triggers hanging off it, and two tables.
//
// TestMigrateUpDownUp asserts that every Down arm *runs* and that a
// re-up lands; it asserts nothing about what any single arm removed,
// because it only checks that `users` is gone at the bottom. A Down arm
// that dropped the tables and left the triggers behind would pass it and
// would leave a database that fails on the next write with "function
// bump_design_version() does not exist". A migration is the one thing in
// this repository that is hard to take back, so its reversal is asserted
// object by object.
func TestTheAnalysisMigrationRollsBackToTheSchemaBeforeIt(t *testing.T) {
	t.Parallel()
	pool := newTestDatabase(t)
	ctx := context.Background()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Each probe is (what it is, the statement that counts it). A count
	// of zero after the rollback and non-zero before it is the whole
	// assertion, and the "before" half is what stops a probe that is
	// simply spelled wrong from reading as a clean reversal.
	probes := []struct {
		what  string
		count string
	}{
		{"relation_types.analysis_traits", `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'relation_types' AND column_name = 'analysis_traits'`},
		{"the trait vocabulary constraint", `SELECT count(*) FROM pg_constraint
			WHERE conname = 'relation_types_traits_vocab'`},
		{"projects.design_version", `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'projects' AND column_name = 'design_version'`},
		{"the bump_design_version function", `SELECT count(*) FROM pg_proc p
			JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = 'public' AND p.proname = 'bump_design_version'`},
		{"the design-version triggers", `SELECT count(*) FROM pg_trigger
			WHERE NOT tgisinternal AND tgname LIKE '%_design_version_%'`},
		{"the routes and route_steps tables", `SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name IN ('routes', 'route_steps')`},
	}

	before := make([]int, len(probes))
	for i, p := range probes {
		if err := pool.QueryRow(ctx, p.count).Scan(&before[i]); err != nil {
			t.Fatalf("count %s: %v", p.what, err)
		}
		if before[i] == 0 {
			t.Fatalf("%s is absent before the rollback; this probe would assert nothing", p.what)
		}
	}

	if err := migrateDown(ctx, pool); err != nil {
		t.Fatalf("migrateDown: %v", err)
	}

	for i, p := range probes {
		var got int
		if err := pool.QueryRow(ctx, p.count).Scan(&got); err != nil {
			t.Fatalf("count %s after rollback: %v", p.what, err)
		}
		if got != 0 {
			t.Fatalf("%s survived the rollback: %d left of %d", p.what, got, before[i])
		}
	}

	// The rolled-back schema is a working one, not merely an emptier
	// one: a write to the four tables the triggers watched must still
	// land with the function they called gone.
	var projectID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ('azeroth', 'Azeroth') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatalf("insert project after rollback: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO relation_types (project_id, key, label) VALUES ($1, 'requires', 'Requires')`,
		projectID); err != nil {
		t.Fatalf("insert relation type after rollback: %v", err)
	}

	// And the re-up restores every object, so the arm is reversible in
	// both directions rather than merely destructive.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate after rollback: %v", err)
	}
	for i, p := range probes {
		var got int
		if err := pool.QueryRow(ctx, p.count).Scan(&got); err != nil {
			t.Fatalf("count %s after re-up: %v", p.what, err)
		}
		if got != before[i] {
			t.Fatalf("%s came back as %d after the re-up, want %d", p.what, got, before[i])
		}
	}
}
