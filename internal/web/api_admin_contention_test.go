package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/testutil"
	"github.com/neverbot/maestro/internal/web"
)

func newAdminContentionServer(t *testing.T) (*web.Server, *identity.Service, *projects.Service, *metamodel.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewPool(t)
	cfg := config.Config{
		SessionTTL: testConfig().SessionTTL,
		InviteTTL:  testConfig().InviteTTL,
		Argon2:     testConfig().Argon2,
	}
	ids := identity.New(pool, cfg)
	projSvc := projects.New(pool)
	mm := metamodel.New(pool, nil)
	srv := web.NewServer(web.Options{
		Version: "test", Config: cfg, Identity: ids, Projects: projSvc,
		Metamodel: mm, Markdown: markdown.New(pool, nil),
	})
	return srv, ids, projSvc, mm, pool
}

func TestAGameDeletionDeadlockedByAContentWriteIsRetryable(t *testing.T) {
	srv, ids, projSvc, mm, pool := newAdminContentionServer(t)
	ctx := context.Background()

	// Every connection this pool hands out after the reset detects a
	// deadlock 50ms after it starts waiting; the holder below raises its
	// own session's threshold far past the end of the test. That decides
	// *which* side of the cycle Postgres aborts — the deletion, which is
	// the side under test — without changing the cycle itself.
	var dbName string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER DATABASE "`+dbName+`" SET deadlock_timeout = '50ms'`); err != nil {
		t.Fatalf("ALTER DATABASE: %v", err)
	}
	pool.Reset()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")
	project, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := mm.UpsertEntityType(ctx, project.ID, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("UpsertEntityType: %v", err)
	}
	if _, err := mm.UpsertEntity(ctx, project.ID, metamodel.EntityInput{
		TypeKey: "quest", Key: "q-1", Name: "Quest",
	}); err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SET deadlock_timeout = '30s'"); err != nil {
		t.Fatalf("SET deadlock_timeout: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var held int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM (SELECT id FROM entities WHERE project_id = $1 FOR UPDATE) locked", project.ID).Scan(&held); err != nil {
		t.Fatalf("lock the entity rows: %v", err)
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodDelete, "/api/games/azeroth?confirm=azeroth", nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		done <- rec
	}()

	waitForBlockedDelete(t, pool, dbName)
	// The other half of the cycle: the writer that already holds an
	// entity row now needs the game row the deletion is holding, which
	// is what an insert's own foreign key takes on it.
	_, _ = tx.Exec(ctx, "SELECT 1 FROM projects WHERE id = $1 FOR KEY SHARE", project.ID)

	rec := <-done
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusServiceUnavailable || body.Error != "retryable" {
		t.Fatalf("a deletion deadlocked by a concurrent content write answered %d %s (%s); "+
			"want 503 retryable", rec.Code, body.Error, body.Message)
	}
}

// waitForBlockedDelete blocks until the deletion is actually waiting on a
// lock, so the second half of the cycle is taken after the first, not
// before.
func waitForBlockedDelete(t *testing.T, pool *pgxpool.Pool, dbName string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = $1 AND wait_event_type = 'Lock' AND query ILIKE '%DELETE FROM projects%'`, dbName).Scan(&n); err != nil {
			t.Fatalf("pg_stat_activity: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the deletion never blocked on the lock the content write holds")
}
