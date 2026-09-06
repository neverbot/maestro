package identity_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/testutil"
)

func testConfig() config.Config {
	return config.Config{
		RegistrationMode: config.RegistrationInviteOnly,
		Argon2:           config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16},
		// Long enough that no test's session or invite outlives the test
		// itself; the exact value only matters to
		// TestIssueSessionSetsExpiryFromConfig and
		// TestCreateInviteUsesConfiguredDefaultTTL.
		SessionTTL: 24 * time.Hour,
		InviteTTL:  24 * time.Hour,
	}
}

func TestCreateUserAndAuthenticate(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())
	ctx := context.Background()

	user, err := svc.CreateUser(ctx, identity.CreateUserRequest{
		Email:       "Designer@Studio.com",
		DisplayName: "Designer",
		Password:    "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.Email != "designer@studio.com" {
		t.Fatalf("Email = %q, want it lower-cased", user.Email)
	}
	if user.IsAdmin {
		t.Fatal("CreateUser must never create an admin")
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

	if _, err := svc.CreateUser(ctx, identity.CreateUserRequest{Email: "dup@studio.com", DisplayName: "One", Password: "password12345"}); err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}
	_, err := svc.CreateUser(ctx, identity.CreateUserRequest{Email: "DUP@studio.com", DisplayName: "Two", Password: "password12345"})
	if !errors.Is(err, identity.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestCreateUserRejectsDisallowedDomain(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.AllowedEmailDomains = []string{"studio.com"}
	svc := identity.New(pool, cfg)

	_, err := svc.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: "outsider@elsewhere.com", DisplayName: "Outsider", Password: "password12345",
	})
	if !errors.Is(err, identity.ErrEmailNotAllowed) {
		t.Fatalf("err = %v, want ErrEmailNotAllowed", err)
	}
}

func TestCreateUserRejectsShortPassword(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())

	_, err := svc.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: "short@studio.com", DisplayName: "Short", Password: "tooshort",
	})
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
	if _, err := svc.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: "runes-short@studio.com", DisplayName: "Runes", Password: tooShort,
	}); !errors.Is(err, identity.ErrPasswordInvalid) {
		t.Fatalf("8-rune password: err = %v, want ErrPasswordInvalid (rune count, not byte count, must gate this)", err)
	}

	// 12 Chinese characters: 12 runes, 36 bytes. Must be accepted: the rune
	// count clears the minimum even though the byte count is well above 12.
	longEnough := "密码密码密码密码密码密码"
	if _, err := svc.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: "runes-ok@studio.com", DisplayName: "Runes", Password: longEnough,
	}); err != nil {
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
	_, err := svc.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: "huge@studio.com", DisplayName: "Huge", Password: string(huge),
	})
	if !errors.Is(err, identity.ErrPasswordInvalid) {
		t.Fatalf("err = %v, want ErrPasswordInvalid", err)
	}
}

func TestCreateUserRejectsInvalidEmail(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())

	// "@" alone: no local part, no domain, and far too short to be a real
	// address. This must be rejected structurally, not merely rejected by
	// coincidence of some other check.
	_, err := svc.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: "@", DisplayName: "Nobody", Password: "password12345",
	})
	if !errors.Is(err, identity.ErrEmailInvalid) {
		t.Fatalf("err = %v, want ErrEmailInvalid", err)
	}
}

func TestCreateUserRejectsEmptyDisplayName(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())

	_, err := svc.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: "noname@studio.com", DisplayName: "   ", Password: "password12345",
	})
	if !errors.Is(err, identity.ErrDisplayNameInvalid) {
		t.Fatalf("err = %v, want ErrDisplayNameInvalid", err)
	}
}

func TestCreateUserRejectsOverlongDisplayName(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := identity.New(pool, testConfig())

	huge := make([]rune, 5000)
	for i := range huge {
		huge[i] = 'a'
	}
	_, err := svc.CreateUser(context.Background(), identity.CreateUserRequest{
		Email: "hugename@studio.com", DisplayName: string(huge), Password: "password12345",
	})
	if !errors.Is(err, identity.ErrDisplayNameInvalid) {
		t.Fatalf("err = %v, want ErrDisplayNameInvalid", err)
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

func TestBootstrapFirstAdminNamesTheEnvVarOnFailure(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := testConfig()
	cfg.FirstAdminEmail = "boss@studio.com"
	cfg.FirstAdminPassword = "short"
	svc := identity.New(pool, cfg)

	err := svc.BootstrapFirstAdmin(context.Background())
	if err == nil {
		t.Fatal("want an error for a too-short FIRST_ADMIN_PASSWORD")
	}
	if !errors.Is(err, identity.ErrPasswordInvalid) {
		t.Fatalf("err = %v, want it to wrap ErrPasswordInvalid", err)
	}
	if got := err.Error(); !strings.Contains(got, "FIRST_ADMIN_PASSWORD") {
		t.Fatalf("err = %q, want it to name FIRST_ADMIN_PASSWORD", got)
	}
}

// dbNow reads the database server's own clock.
//
// **Every assertion about a stored expiry goes through it, and that is
// the point.** Expiries in this package are written by Postgres (now() +
// an interval) and judged by Postgres (expires_at > now()), so a test
// that bracketed one with this process' time.Now() would be asserting
// that two machines' clocks agree — the assumption
// TestRegisterWithExpiredInviteReportsExpired used to fail under load
// on, and the one CreateInvite's own comment in identity.sql exists to
// remove. Against the local test database that disagreement measured
// about 1.6ms, which is enough to redden a bracket and nowhere near
// enough to be noticed as a cause.
//
// clock_timestamp() rather than now(): now() is transaction start time,
// which for a bracket read *around* another call would be the wrong
// instant on both sides.
func dbNow(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var at time.Time
	if err := pool.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&at); err != nil {
		t.Fatalf("read the database clock: %v", err)
	}
	return at
}
