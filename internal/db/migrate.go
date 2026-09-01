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

// Migrate brings the schema of the pool's database up to date, then
// refuses to return successfully if the database's own applied version is
// still ahead of what this binary knows about afterward — see
// checkNoSchemaDrift's own doc comment for why Up() alone does not catch
// that case. It is safe to call from multiple processes against the same
// database at once.
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
	return checkNoSchemaDrift(ctx, provider)
}

// checkNoSchemaDrift refuses to let this process serve traffic when the
// database's applied schema version is higher than the highest migration
// this binary's embedded migrations know about — the one case Up() alone
// does not fail on. goose's Up() only ever walks forward through
// migrations it recognizes from its own source list; when an older binary
// talks to a database a newer one has already migrated (a rollback, or a
// rolling deploy that reaches an old replica after a new migration has
// landed elsewhere), those newer, unrecognized rows are simply invisible
// to it — Up() reports success having correctly applied everything it
// knows about, and this process would otherwise go on to serve live
// traffic against a schema shape it does not understand, with no log line
// anywhere pointing at why. Comparing the database's own version
// bookkeeping (GetDBVersion) against the highest version this binary's
// embedded migrations declare (the max of ListSources) turns that into an
// explicit refusal to start instead of a silent mismatch discovered only
// when a query collides with a column or constraint it never expected.
func checkNoSchemaDrift(ctx context.Context, provider *goose.Provider) error {
	dbVersion, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("get database schema version: %w", err)
	}
	var knownVersion int64
	for _, src := range provider.ListSources() {
		if src.Version > knownVersion {
			knownVersion = src.Version
		}
	}
	if dbVersion > knownVersion {
		return fmt.Errorf("database schema version %d is ahead of the highest migration this binary knows about (%d); refusing to serve against a schema this binary does not understand", dbVersion, knownVersion)
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
