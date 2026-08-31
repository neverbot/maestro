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

// NewPool creates a uniquely named database, migrates it, and drops it when
// the test finishes. It skips the test when TEST_DATABASE_URL is unset.
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	adminDB := admin(t, adminURL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name := "maestro_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	testURL, err := replaceDBName(adminURL, name)
	if err != nil {
		t.Fatalf("build test database URL: %v", err)
	}

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
