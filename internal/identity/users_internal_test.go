package identity

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/testutil"
)

func internalTestConfig() config.Config {
	return config.Config{
		RegistrationMode: config.RegistrationInviteOnly,
		Argon2:           config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
	}
}

// TestBootstrapFirstAdminConcurrentBootIsSafe simulates several replicas
// booting simultaneously against an empty database. A sync.WaitGroup start
// gate forces every goroutine to reach BootstrapFirstAdmin's count check at
// the same time, so the count-then-insert race is genuinely exercised
// instead of degenerating into a sequence of count>0 short-circuits (which
// would make the test pass without ever touching the code path it claims to
// cover). bootstrapRaceHook confirms that degeneration did not happen.
func TestBootstrapFirstAdminConcurrentBootIsSafe(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := internalTestConfig()
	cfg.FirstAdminEmail = "race@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := New(pool, cfg)
	ctx := context.Background()

	var raceHits int32
	prevHook := bootstrapRaceHook
	bootstrapRaceHook = func() { atomic.AddInt32(&raceHits, 1) }
	t.Cleanup(func() { bootstrapRaceHook = prevHook })

	const n = 8
	var ready sync.WaitGroup
	ready.Add(n)
	var start sync.WaitGroup
	start.Add(1)

	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			ready.Done()
			start.Wait()
			errs <- svc.BootstrapFirstAdmin(ctx)
		}()
	}
	ready.Wait() // every goroutine is parked at the gate
	start.Done() // release them all at once

	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent BootstrapFirstAdmin: %v", err)
		}
	}

	if atomic.LoadInt32(&raceHits) == 0 {
		t.Fatal("no goroutine observed ErrEmailTaken; the start gate did not force a genuine race, so this test proved nothing about concurrency safety")
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE lower(email) = lower($1)", "race@studio.com").Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Fatalf("got %d admin rows, want exactly 1", count)
	}
}

func TestWithTxCommitsOnSuccess(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := New(pool, internalTestConfig())
	ctx := context.Background()

	err := svc.withTx(ctx, func(q *dbq.Queries) error {
		_, err := q.CreateUser(ctx, dbq.CreateUserParams{
			Email:        "tx-commit@studio.com",
			DisplayName:  "Tx",
			PasswordHash: "irrelevant-for-this-test",
			IsAdmin:      false,
		})
		return err
	})
	if err != nil {
		t.Fatalf("withTx: %v", err)
	}

	count, err := svc.q.CountUsers(ctx)
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1 after a committed transaction", count)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := New(pool, internalTestConfig())
	ctx := context.Background()
	sentinel := errors.New("boom")

	err := svc.withTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.CreateUser(ctx, dbq.CreateUserParams{
			Email:        "tx-rollback@studio.com",
			DisplayName:  "Tx",
			PasswordHash: "irrelevant-for-this-test",
			IsAdmin:      false,
		}); err != nil {
			return err
		}
		// The insert above succeeded inside the transaction; returning an
		// error here must undo it, proving withTx rolls back on any
		// non-nil return from fn, not just on a failing statement.
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the sentinel error", err)
	}

	count, err := svc.q.CountUsers(ctx)
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0 after a rolled-back transaction", count)
	}
}
