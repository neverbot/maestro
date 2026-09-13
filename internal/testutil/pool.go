// Package testutil provides throwaway databases for integration tests.
//
// Every pool lives in its own freshly created database and is dropped on
// cleanup. No test ever touches a live Maestro database.
package testutil

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db"
)

var (
	adminOnce sync.Once
	adminPool *pgxpool.Pool
	adminErr  error
)

// staleAfter is how old an abandoned test database has to be before a
// later run drops it. An hour is far longer than any test in this
// repository takes (the slowest package is under seven minutes) and far
// shorter than the time it takes for leftovers to matter, so a sweep can
// never touch a database another run is still using.
const staleAfter = time.Hour

// sweepStale drops the test databases that earlier runs abandoned.
//
// Every database here is created by newDatabase and dropped by the
// cleanup it registers — but a run that is interrupted never reaches its
// cleanup, and each leftover keeps a migrated schema on disk. They are
// identified by the millisecond stamp in their own name, so this cannot
// mistake a live one for a dead one and does not need to ask Postgres
// when a database was made (it does not record it).
//
// It runs once per process, before the first test database is created,
// and never fails a test: a leftover is untidy and a refused DROP is
// not a reason to stop.
var sweepOnce sync.Once

func sweepStale(adminDB *pgxpool.Pool) {
	sweepOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rows, err := adminDB.Query(ctx,
			`SELECT datname FROM pg_database WHERE datname LIKE 'maestro_test\_%'`)
		if err != nil {
			return
		}
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				continue
			}
			names = append(names, name)
		}
		rows.Close()
		cutoff := time.Now().Add(-staleAfter).UnixMilli()
		for _, name := range names {
			made, ok := stampOf(name)
			// A name with no stamp is from before this was written, and
			// is therefore older than any run in flight.
			if ok && made > cutoff {
				continue
			}
			_, _ = adminDB.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		}
	})
}

// stampOf reads the millisecond stamp out of `maestro_test_<millis>_<id>`.
func stampOf(name string) (int64, bool) {
	rest, ok := strings.CutPrefix(name, "maestro_test_")
	if !ok {
		return 0, false
	}
	digits, _, ok := strings.Cut(rest, "_")
	if !ok {
		return 0, false
	}
	made, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return made, true
}

// admin returns the pool shared by every test in this process for creating
// and dropping per-test databases. Opening one admin pool instead of one
// per test keeps the connection count low on a shared TEST_DATABASE_URL
// server under parallel tests; it is intentionally never closed and dies
// with the process.
func admin(t *testing.T, adminURL string) *pgxpool.Pool {
	t.Helper()

	adminOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		adminPool, adminErr = pgxpool.New(ctx, adminURL)
		if adminErr != nil {
			adminErr = fmt.Errorf("open admin pool: %w", adminErr)
			return
		}
		if err := adminPool.Ping(ctx); err != nil {
			adminErr = fmt.Errorf("ping admin database (is TEST_DATABASE_URL listening?): %w", err)
		}
	})

	if adminErr != nil {
		t.Fatalf("%v", adminErr)
	}
	return adminPool
}

// newDatabase creates a uniquely named, empty (unmigrated) database and
// registers its drop on test cleanup, returning its connection URL. It
// skips the test when TEST_DATABASE_URL is unset. Both NewPool and
// NewDatabaseURL share this rather than duplicating the create/cleanup
// logic; they differ only in what they do with the resulting URL — NewPool
// connects and migrates it itself, NewDatabaseURL hands the bare URL back
// so a caller that needs to exercise its own connect-and-migrate path (as
// cmd/maestro's own start-up sequence does) can do so against a database
// this package still owns the lifecycle of.
func newDatabase(t *testing.T) (testURL, name string) {
	t.Helper()

	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	adminDB := admin(t, adminURL)
	sweepStale(adminDB)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// **The name carries when it was made**, so a sweep can tell a
	// database this run is using from one a killed run left behind. See
	// sweepStale: a test that is interrupted — a Ctrl-C, a timeout, a
	// laptop closing — never reaches its own cleanup, and sixty-two of
	// them had accumulated to half a gigabyte before anybody looked.
	name = fmt.Sprintf("maestro_test_%d_%s", time.Now().UnixMilli(), strings.ReplaceAll(uuid.NewString(), "-", ""))
	ident := pgx.Identifier{name}.Sanitize()

	if _, err := adminDB.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		if strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("create database %s: %v (does the TEST_DATABASE_URL user have CREATEDB?)", name, err)
		}
		t.Fatalf("create database %s: %v", name, err)
	}

	// Register the drop immediately, before anything that can fail below.
	// A failing migration must not leak the database it was about to test.
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if _, err := adminDB.Exec(dropCtx, "DROP DATABASE "+ident+" WITH (FORCE)"); err != nil {
			t.Logf("drop database %s: %v", name, err)
		}
	})

	var err error
	testURL, err = replaceDBName(adminURL, name)
	if err != nil {
		t.Fatalf("build test database URL: %v", err)
	}
	return testURL, name
}

// NewDatabaseURL creates a uniquely named, empty (unmigrated) database and
// returns its connection URL, dropping it when the test finishes. Unlike
// NewPool, it does not connect or migrate — it exists for a caller that
// runs its own connect-and-migrate sequence against the URL, such as
// cmd/maestro's main_test.go exercising db.NewPool and db.Migrate through
// run() exactly as the real binary does. It skips the test when
// TEST_DATABASE_URL is unset.
func NewDatabaseURL(t *testing.T) string {
	t.Helper()
	testURL, _ := newDatabase(t)
	return testURL
}

// NewPool creates a uniquely named database, migrates it, and drops it when
// the test finishes. It skips the test when TEST_DATABASE_URL is unset.
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	testURL, name := newDatabase(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ctx is cancelled when NewPool returns, but the pool it configures
	// outlives that: harmless today since pgxpool is lazy (MaxConns is set
	// below, MinConns stays at its default of 0 and no BeforeConnect hook
	// runs eagerly), so nothing dials until a test issues a query on its
	// own context. That stops being true the moment MinConns or a
	// BeforeConnect hook is added here.
	cfg, err := pgxpool.ParseConfig(testURL)
	if err != nil {
		t.Fatalf("parse test database config: %v", err)
	}
	cfg.MaxConns = 4

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	// Standing guard against a mis-rewritten URL silently pointing this
	// test at whatever database TEST_DATABASE_URL named.
	var current string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&current); err != nil {
		t.Fatalf("verify current database: %v", err)
	}
	if current != name {
		t.Fatalf("connected to database %q, want %q; TEST_DATABASE_URL was not rewritten correctly", current, name)
	}

	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return pool
}

// replaceDBName returns rawURL with its path replaced by name, using a real
// URL parse rather than string slicing so that a slash inside a query
// parameter (e.g. sslrootcert=/etc/ssl/ca.pem) cannot leave the database
// name untouched. It errors on anything that isn't a URL, such as a
// keyword/value DSN ("host=... dbname=..."), which pgx accepts but this
// function cannot safely rewrite.
func replaceDBName(rawURL, name string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" {
		return "", fmt.Errorf("TEST_DATABASE_URL must be a postgres:// URL, got %q", rawURL)
	}
	u.Path = "/" + name
	return u.String(), nil
}
