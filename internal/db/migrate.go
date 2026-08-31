package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// newProvider builds a goose provider scoped to sqlDB.
//
// It uses goose's instance-scoped provider API (rather than the
// package-level API, which relies on shared mutable state and races under
// -race when multiple pools migrate concurrently) and takes a Postgres
// session advisory lock, so that several replicas migrating the same
// database at start-up serialize instead of stepping on each other.
func newProvider(sqlDB *sql.DB) (*goose.Provider, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("sub migrations fs: %w", err)
	}

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("new session locker: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("new provider: %w", err)
	}
	return provider, nil
}

// Migrate brings the schema of the pool's database up to date. It is safe
// to call from multiple processes against the same database at once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()

	provider, err := newProvider(sqlDB)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// migrateDown rolls back the single most recent migration. It is
// unexported and used only by this package's own tests to exercise the
// Down side of a migration; nothing outside internal/db can reach it.
func migrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()

	provider, err := newProvider(sqlDB)
	if err != nil {
		return err
	}
	if _, err := provider.Down(ctx); err != nil {
		return fmt.Errorf("migrate down: %w", err)
	}
	return nil
}
