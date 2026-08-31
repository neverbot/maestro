// Package testutil provides throwaway databases for integration tests.
//
// Every pool lives in its own freshly created database and is dropped on
// cleanup. No test ever touches a live Maestro database.
package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db"
)

// NewPool creates a uniquely named database, migrates it, and drops it when
// the test finishes. It skips the test when TEST_DATABASE_URL is unset.
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}

	name := "maestro_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		admin.Close()
		t.Fatalf("create database: %v", err)
	}

	pool, err := db.NewPool(ctx, replaceDBName(adminURL, name))
	if err != nil {
		admin.Close()
		t.Fatalf("connect test database: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		admin.Close()
		t.Fatalf("migrate: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if _, err := admin.Exec(dropCtx, fmt.Sprintf("DROP DATABASE %q WITH (FORCE)", name)); err != nil {
			t.Logf("drop database %s: %v", name, err)
		}
		admin.Close()
	})

	return pool
}

// replaceDBName swaps the database path of a Postgres URL.
func replaceDBName(url, name string) string {
	cut := strings.LastIndex(url, "/")
	base := url[:cut+1]
	rest := url[cut+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		return base + name + rest[q:]
	}
	return base + name
}
