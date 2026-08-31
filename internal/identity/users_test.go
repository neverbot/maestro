package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/testutil"
)

func testConfig() config.Config {
	return config.Config{
		RegistrationMode: config.RegistrationInviteOnly,
		Argon2:           config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
	}
}

func TestCreateUserAndAuthenticate(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, "Designer@Studio.com", "Designer", "hunter2hunter2", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.Email != "designer@studio.com" {
		t.Fatalf("Email = %q, want it lower-cased", user.Email)
	}

	got, err := svc.Authenticate(ctx, "designer@studio.com", "hunter2hunter2")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != user.ID {
		t.Fatalf("Authenticate returned a different user")
	}

	if _, err := svc.Authenticate(ctx, "designer@studio.com", "wrong"); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.Authenticate(ctx, "nobody@studio.com", "hunter2hunter2"); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("unknown email must fail with ErrInvalidCredentials, got %v", err)
	}
}

func TestCreateUserRejectsDuplicateEmail(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, "dup@studio.com", "One", "password12345", false); err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}
	_, err := svc.CreateUser(ctx, "DUP@studio.com", "Two", "password12345", false)
	if !errors.Is(err, identity.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestCreateUserRejectsDisallowedDomain(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.AllowedEmailDomains = []string{"studio.com"}
	svc := identity.New(pool, cfg)

	_, err := svc.CreateUser(context.Background(), "outsider@elsewhere.com", "Outsider", "password12345", false)
	if !errors.Is(err, identity.ErrEmailNotAllowed) {
		t.Fatalf("err = %v, want ErrEmailNotAllowed", err)
	}
}

func TestCreateUserRejectsShortPassword(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())

	_, err := svc.CreateUser(context.Background(), "short@studio.com", "Short", "tooshort", false)
	if !errors.Is(err, identity.ErrPasswordInvalid) {
		t.Fatalf("err = %v, want ErrPasswordInvalid", err)
	}
}

func TestCreateUserCountsRunesNotBytes(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())

	// 8 Chinese characters: 8 runes but 24 bytes. A byte-length check would
	// wrongly accept this as "long enough"; a rune-count check correctly
	// rejects it as too short.
	tooShort := "密码密码密码密码"
	if _, err := svc.CreateUser(context.Background(), "runes-short@studio.com", "Runes", tooShort, false); !errors.Is(err, identity.ErrPasswordInvalid) {
		t.Fatalf("8-rune password: err = %v, want ErrPasswordInvalid (rune count, not byte count, must gate this)", err)
	}

	// 12 Chinese characters: 12 runes, 36 bytes. Must be accepted: the rune
	// count clears the minimum even though the byte count is well above 12.
	longEnough := "密码密码密码密码密码密码"
	if _, err := svc.CreateUser(context.Background(), "runes-ok@studio.com", "Runes", longEnough, false); err != nil {
		t.Fatalf("12-rune multi-byte password should be accepted: %v", err)
	}
}

func TestCreateUserRejectsOverlongPassword(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())

	huge := make([]byte, 2000)
	for i := range huge {
		huge[i] = 'a'
	}
	_, err := svc.CreateUser(context.Background(), "huge@studio.com", "Huge", string(huge), false)
	if !errors.Is(err, identity.ErrPasswordInvalid) {
		t.Fatalf("err = %v, want ErrPasswordInvalid", err)
	}
}

func TestBootstrapFirstAdminRunsOnceAndIsAdmin(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "boss@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("BootstrapFirstAdmin: %v", err)
	}
	admin, err := svc.Authenticate(ctx, "boss@studio.com", "password12345")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !admin.IsAdmin {
		t.Fatal("the bootstrapped user is not an admin")
	}

	// Running it again on a populated instance must be a no-op, not an error.
	if err := svc.BootstrapFirstAdmin(ctx); err != nil {
		t.Fatalf("second BootstrapFirstAdmin: %v", err)
	}
}

func TestBootstrapFirstAdminConcurrentBootIsSafe(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "race@studio.com"
	cfg.FirstAdminPassword = "password12345"
	svc := identity.New(pool, cfg)
	ctx := context.Background()

	// Two replicas booting simultaneously against an empty database must
	// not both succeed in creating distinct admin rows, and must not crash
	// the process; exactly one CreateUser should win, the other should see
	// ErrEmailTaken and treat that as bootstrap-already-done.
	const n = 5
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			errs <- svc.BootstrapFirstAdmin(ctx)
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent BootstrapFirstAdmin: %v", err)
		}
	}

	count, err := poolCountUsersByEmail(ctx, pool, "race@studio.com")
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Fatalf("got %d admin rows, want exactly 1", count)
	}
}

func poolCountUsersByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE lower(email) = lower($1)", email).Scan(&n)
	return n, err
}
