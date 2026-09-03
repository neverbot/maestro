package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/testutil"
)

// viewGame is one seeded game: a project with one entity type, one
// entity, one relation type, one background asset and one view. Every
// cross-game test below needs two of them, and builds its illegal row
// out of one game's view and the other's entity, view, type, asset or
// token.
//
// Every test takes its own throwaway database from testutil.NewPool, so
// no test here can be read as passing because of a row another test
// left behind, and every statement below runs as its own implicit
// transaction on the pool: a refused statement rolls back only itself.
type viewGame struct {
	projectID      string
	entityTypeID   string
	entityID       string
	relationTypeID string
	assetID        string
	viewID         string
}

func seedViewGame(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slug string) viewGame {
	t.Helper()

	var g viewGame
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`, slug).Scan(&g.projectID); err != nil {
		t.Fatalf("insert project %s: %v", slug, err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'quest', 'Quest', 'Quests') RETURNING id`, g.projectID).Scan(&g.entityTypeID); err != nil {
		t.Fatalf("insert entity type in %s: %v", slug, err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO entities (project_id, entity_type_id, key, name)
		 VALUES ($1, $2, 'hogger', 'Wanted: Hogger') RETURNING id`,
		g.projectID, g.entityTypeID).Scan(&g.entityID); err != nil {
		t.Fatalf("insert entity in %s: %v", slug, err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO relation_types (project_id, key, label)
		 VALUES ($1, 'requires', 'Requires') RETURNING id`, g.projectID).Scan(&g.relationTypeID); err != nil {
		t.Fatalf("insert relation type in %s: %v", slug, err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO view_assets (project_id, filename, mime, width, height, bytes)
		 VALUES ($1, 'map.png', 'image/png', 4, 4, '\x00'::bytea) RETURNING id`,
		g.projectID).Scan(&g.assetID); err != nil {
		t.Fatalf("insert asset in %s: %v", slug, err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO views (project_id, key, name, query, renderer)
		 VALUES ($1, 'quest-map', 'Quest map', '{"v":1}'::jsonb, 'graph') RETURNING id`,
		g.projectID).Scan(&g.viewID); err != nil {
		t.Fatalf("insert view in %s: %v", slug, err)
	}
	return g
}

// assertCheckViolation is the 23514 counterpart of
// assertForeignKeyViolation (metamodel_schema_test.go) and
// assertUniqueViolation (documents_schema_test.go). Asserting the
// SQLSTATE rather than merely "an error" is what stops one of these
// tests from passing because the statement was rejected for an
// unrelated reason -- a typo'd column name also produces an error.
func assertCheckViolation(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a check violation, but the statement succeeded")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError, got %T: %v", err, err)
	}
	if pgErr.Code != "23514" {
		t.Fatalf("expected SQLSTATE 23514 (check_violation), got %s: %v", pgErr.Code, err)
	}
}

// countRows fails the test unless the query returns exactly want rows.
// Every positive control in this file uses it, so an "accepted" row can
// never be a statement that succeeded while writing nothing.
func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args []any, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, query, args...).Scan(&got); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if got != want {
		t.Fatalf("expected %d rows, got %d", want, got)
	}
}

func TestViewTablesExist(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	for _, table := range []string{"views", "view_positions", "view_refs", "view_assets"} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			table).Scan(&exists); err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %s was not created", table)
		}
	}
}

// TestAViewPositionCannotCrossGames pins the composite key
// view_positions (entity_id, project_id) -> entities (id, project_id).
// With the single-column key the spec's §5.4 proposed, one game's map
// could pin another game's entity.
func TestAViewPositionCannotCrossGames(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")
	b := seedViewGame(t, ctx, pool, "game-b")

	// The positive control comes first and is counted, so an empty
	// answer below cannot be mistaken for a refusal.
	if _, err := pool.Exec(ctx,
		`INSERT INTO view_positions (view_id, entity_id, project_id, x, y)
		 VALUES ($1, $2, $3, 1, 2)`, a.viewID, a.entityID, a.projectID); err != nil {
		t.Fatalf("a position within one game must be accepted: %v", err)
	}
	countRows(t, ctx, pool,
		`SELECT count(*) FROM view_positions WHERE view_id = $1 AND entity_id = $2`,
		[]any{a.viewID, a.entityID}, 1)

	_, err := pool.Exec(ctx,
		`INSERT INTO view_positions (view_id, entity_id, project_id, x, y)
		 VALUES ($1, $2, $3, 1, 2)`, a.viewID, b.entityID, a.projectID)
	assertForeignKeyViolation(t, err)
}

// TestAViewPositionCannotBorrowAnotherGamesView is the same hole one
// step along: the other key out of view_positions. Closing only the
// entity side would leave a position row in game A naming game B's view.
func TestAViewPositionCannotBorrowAnotherGamesView(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")
	b := seedViewGame(t, ctx, pool, "game-b")

	if _, err := pool.Exec(ctx,
		`INSERT INTO view_positions (view_id, entity_id, project_id, x, y)
		 VALUES ($1, $2, $3, 1, 2)`, a.viewID, a.entityID, a.projectID); err != nil {
		t.Fatalf("a position within one game must be accepted: %v", err)
	}
	countRows(t, ctx, pool,
		`SELECT count(*) FROM view_positions WHERE project_id = $1`, []any{a.projectID}, 1)

	_, err := pool.Exec(ctx,
		`INSERT INTO view_positions (view_id, entity_id, project_id, x, y)
		 VALUES ($1, $2, $3, 1, 2)`, b.viewID, a.entityID, a.projectID)
	assertForeignKeyViolation(t, err)
}

// TestAViewCannotUseAnotherGamesBackground pins the composite key
// views (background_asset_id, project_id) -> view_assets (id,
// project_id). Without it a view would serve another game's image
// through the asset route.
func TestAViewCannotUseAnotherGamesBackground(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")
	b := seedViewGame(t, ctx, pool, "game-b")

	if _, err := pool.Exec(ctx,
		`UPDATE views SET background_asset_id = $1 WHERE id = $2`, a.assetID, a.viewID); err != nil {
		t.Fatalf("a background from the same game must be accepted: %v", err)
	}
	countRows(t, ctx, pool,
		`SELECT count(*) FROM views WHERE id = $1 AND background_asset_id = $2`,
		[]any{a.viewID, a.assetID}, 1)

	_, err := pool.Exec(ctx,
		`UPDATE views SET background_asset_id = $1 WHERE id = $2`, b.assetID, a.viewID)
	assertForeignKeyViolation(t, err)
}

// TestAViewRefCannotUseAnotherGamesEntityType pins the composite key on
// the dependency index. A ref row is what a staleness report reads, so a
// borrowed type id would report another game's rename as this game's.
func TestAViewRefCannotUseAnotherGamesEntityType(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")
	b := seedViewGame(t, ctx, pool, "game-b")

	if _, err := pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, entity_type_id, pointer)
		 VALUES ($1, $2, 'entity_type', 'quest', $3, '/from/0/type')`,
		a.viewID, a.projectID, a.entityTypeID); err != nil {
		t.Fatalf("a ref within one game must be accepted: %v", err)
	}
	countRows(t, ctx, pool,
		`SELECT count(*) FROM view_refs WHERE view_id = $1 AND entity_type_id = $2`,
		[]any{a.viewID, a.entityTypeID}, 1)

	_, err := pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, entity_type_id, pointer)
		 VALUES ($1, $2, 'entity_type', 'quest', $3, '/from/1/type')`,
		a.viewID, a.projectID, b.entityTypeID)
	assertForeignKeyViolation(t, err)

	// And the relation_type arm of the same table, which is the same
	// hole one step along.
	_, err = pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, relation_type_id, pointer)
		 VALUES ($1, $2, 'relation_type', 'requires', $3, '/traverse/0/via')`,
		a.viewID, a.projectID, b.relationTypeID)
	assertForeignKeyViolation(t, err)
}

// TestAViewCannotRecordAnotherGamesToken pins the composite audit key.
// api_tokens is project-scoped, so a UI rendering "last edited by
// <token label>" must not be able to name another game's token.
func TestAViewCannotRecordAnotherGamesToken(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")
	b := seedViewGame(t, ctx, pool, "game-b")
	tokenA := seedToken(t, ctx, pool, a.projectID, "view-a")
	tokenB := seedToken(t, ctx, pool, b.projectID, "view-b")

	if _, err := pool.Exec(ctx,
		`UPDATE views SET updated_by_token_id = $1 WHERE id = $2`, tokenA, a.viewID); err != nil {
		t.Fatalf("this game's own token must be accepted: %v", err)
	}
	countRows(t, ctx, pool,
		`SELECT count(*) FROM views WHERE id = $1 AND updated_by_token_id = $2`,
		[]any{a.viewID, tokenA}, 1)

	_, err := pool.Exec(ctx,
		`UPDATE views SET updated_by_token_id = $1 WHERE id = $2`, tokenB, a.viewID)
	assertForeignKeyViolation(t, err)
}

// TestAnAssetCannotRecordAnotherGamesToken is that same key on the
// other table that carries one. Closing it on views alone would leave
// the hole one step along.
func TestAnAssetCannotRecordAnotherGamesToken(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")
	b := seedViewGame(t, ctx, pool, "game-b")
	tokenA := seedToken(t, ctx, pool, a.projectID, "asset-a")
	tokenB := seedToken(t, ctx, pool, b.projectID, "asset-b")

	if _, err := pool.Exec(ctx,
		`UPDATE view_assets SET created_by_token_id = $1 WHERE id = $2`, tokenA, a.assetID); err != nil {
		t.Fatalf("this game's own token must be accepted: %v", err)
	}
	countRows(t, ctx, pool,
		`SELECT count(*) FROM view_assets WHERE id = $1 AND created_by_token_id = $2`,
		[]any{a.assetID, tokenA}, 1)

	_, err := pool.Exec(ctx,
		`UPDATE view_assets SET created_by_token_id = $1 WHERE id = $2`, tokenB, a.assetID)
	assertForeignKeyViolation(t, err)
}

// TestDeletingAnEntityTypeNullsTheRefAndKeepsItsKey is the one this
// whole table exists for: a deleted type must leave a readable dead
// reference, not disappear. ON DELETE CASCADE here would make "which
// views did that break" unanswerable.
func TestDeletingAnEntityTypeNullsTheRefAndKeepsItsKey(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	if _, err := pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, entity_type_id, pointer)
		 VALUES ($1, $2, 'entity_type', 'quest', $3, '/from/0/type')`,
		a.viewID, a.projectID, a.entityTypeID); err != nil {
		t.Fatalf("insert ref: %v", err)
	}
	// The entity has to go first: entities key into entity_types with
	// ON DELETE RESTRICT, which is 0004_metamodel.sql's own decision.
	if _, err := pool.Exec(ctx, `DELETE FROM entities WHERE id = $1`, a.entityID); err != nil {
		t.Fatalf("delete entity: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM entity_types WHERE id = $1`, a.entityTypeID); err != nil {
		t.Fatalf("deleting a type that a view references must be allowed: %v", err)
	}

	var (
		typeID    *string
		refKey    string
		pointer   string
		projectID string
	)
	if err := pool.QueryRow(ctx,
		`SELECT entity_type_id, ref_key, pointer, project_id FROM view_refs WHERE view_id = $1`,
		a.viewID).Scan(&typeID, &refKey, &pointer, &projectID); err != nil {
		t.Fatalf("the ref row must survive its type: %v", err)
	}
	if typeID != nil {
		t.Fatalf("entity_type_id must be null after the type is deleted, got %v", *typeID)
	}
	if refKey != "quest" || pointer != "/from/0/type" {
		t.Fatalf("the key and the pointer must survive: got %q at %q", refKey, pointer)
	}
	if projectID != a.projectID {
		t.Fatalf("project_id must be untouched by the SET NULL, got %v want %v", projectID, a.projectID)
	}
}

// TestDeletingARelationTypeNullsOnlyItsOwnRefColumn pins the second SET
// NULL of the same table. The two keys are separate constraints, so the
// entity_type test above proves nothing about this one.
func TestDeletingARelationTypeNullsOnlyItsOwnRefColumn(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	if _, err := pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, relation_type_id, pointer)
		 VALUES ($1, $2, 'relation_type', 'requires', $3, '/traverse/0/via')`,
		a.viewID, a.projectID, a.relationTypeID); err != nil {
		t.Fatalf("insert ref: %v", err)
	}
	// A second row on the other arm, which must be untouched by the
	// deletion below.
	if _, err := pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, entity_type_id, pointer)
		 VALUES ($1, $2, 'entity_type', 'quest', $3, '/from/0/type')`,
		a.viewID, a.projectID, a.entityTypeID); err != nil {
		t.Fatalf("insert entity_type ref: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM relation_types WHERE id = $1`, a.relationTypeID); err != nil {
		t.Fatalf("deleting a relation type a view references must be allowed: %v", err)
	}

	var relationTypeID *string
	var refKey string
	if err := pool.QueryRow(ctx,
		`SELECT relation_type_id, ref_key FROM view_refs WHERE pointer = '/traverse/0/via'`).
		Scan(&relationTypeID, &refKey); err != nil {
		t.Fatalf("the ref row must survive its type: %v", err)
	}
	if relationTypeID != nil {
		t.Fatalf("relation_type_id must be null after the type is deleted, got %v", *relationTypeID)
	}
	if refKey != "requires" {
		t.Fatalf("the key must survive: got %q", refKey)
	}

	var entityTypeID *string
	if err := pool.QueryRow(ctx,
		`SELECT entity_type_id FROM view_refs WHERE pointer = '/from/0/type'`).Scan(&entityTypeID); err != nil {
		t.Fatalf("read the entity_type ref: %v", err)
	}
	if entityTypeID == nil || *entityTypeID != a.entityTypeID {
		t.Fatalf("the entity_type ref must be untouched, got %v", entityTypeID)
	}
}

// TestRevokingATokenNullsOnlyTheTokenColumnOfAView pins the column list
// on the SET NULL. A bare SET NULL would try to null project_id too,
// which is NOT NULL -- and it would fail at token-revocation time, not
// at migration time.
func TestRevokingATokenNullsOnlyTheTokenColumnOfAView(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")
	tokenID := seedToken(t, ctx, pool, a.projectID, "revoke-me")

	var userID string
	if err := pool.QueryRow(ctx, `SELECT user_id FROM api_tokens WHERE id = $1`, tokenID).Scan(&userID); err != nil {
		t.Fatalf("read the token's user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE views SET updated_by_token_id = $1, updated_by_user_id = $2 WHERE id = $3`,
		tokenID, userID, a.viewID); err != nil {
		t.Fatalf("stamp the audit columns: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE view_assets SET created_by_token_id = $1 WHERE id = $2`, tokenID, a.assetID); err != nil {
		t.Fatalf("stamp the asset's audit column: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM api_tokens WHERE id = $1`, tokenID); err != nil {
		t.Fatalf("revoking a token must not fail: %v", err)
	}

	var (
		gotToken   *string
		gotUser    *string
		gotProject string
	)
	if err := pool.QueryRow(ctx,
		`SELECT updated_by_token_id, updated_by_user_id, project_id FROM views WHERE id = $1`,
		a.viewID).Scan(&gotToken, &gotUser, &gotProject); err != nil {
		t.Fatalf("read the view back: %v", err)
	}
	if gotToken != nil {
		t.Fatalf("updated_by_token_id must be null, got %v", *gotToken)
	}
	if gotUser == nil || *gotUser != userID {
		t.Fatalf("updated_by_user_id must be untouched, got %v", gotUser)
	}
	if gotProject != a.projectID {
		t.Fatalf("project_id must be untouched by the SET NULL, got %v want %v", gotProject, a.projectID)
	}

	// The same key on view_assets, which is a separate constraint.
	var assetToken *string
	var assetProject string
	if err := pool.QueryRow(ctx,
		`SELECT created_by_token_id, project_id FROM view_assets WHERE id = $1`,
		a.assetID).Scan(&assetToken, &assetProject); err != nil {
		t.Fatalf("read the asset back: %v", err)
	}
	if assetToken != nil {
		t.Fatalf("created_by_token_id must be null, got %v", *assetToken)
	}
	if assetProject != a.projectID {
		t.Fatalf("the asset's project_id must be untouched, got %v want %v", assetProject, a.projectID)
	}
}

// TestDeletingAnAssetNullsTheBackgroundOfEveryView pins the SET NULL
// column list on the background key: deleting an image must leave the
// view, minus its background, not delete the view and not fail.
func TestDeletingAnAssetNullsTheBackgroundOfEveryView(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	var secondView string
	if err := pool.QueryRow(ctx,
		`INSERT INTO views (project_id, key, name, query, renderer, background_asset_id)
		 VALUES ($1, 'fast-travel', 'Fast travel', '{"v":1}'::jsonb, 'graph', $2) RETURNING id`,
		a.projectID, a.assetID).Scan(&secondView); err != nil {
		t.Fatalf("insert second view: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE views SET background_asset_id = $1 WHERE id = $2`, a.assetID, a.viewID); err != nil {
		t.Fatalf("set the background: %v", err)
	}
	countRows(t, ctx, pool,
		`SELECT count(*) FROM views WHERE background_asset_id = $1`, []any{a.assetID}, 2)

	if _, err := pool.Exec(ctx, `DELETE FROM view_assets WHERE id = $1`, a.assetID); err != nil {
		t.Fatalf("deleting an asset a view points at must be allowed: %v", err)
	}

	countRows(t, ctx, pool,
		`SELECT count(*) FROM views WHERE project_id = $1`, []any{a.projectID}, 2)
	countRows(t, ctx, pool,
		`SELECT count(*) FROM views WHERE project_id = $1 AND background_asset_id IS NULL`,
		[]any{a.projectID}, 2)
	countRows(t, ctx, pool,
		`SELECT count(*) FROM views WHERE project_id = $1`, []any{a.projectID}, 2)
}

// TestDeletingAnEntityDropsItsPositionsAndKeepsTheView pins the CASCADE
// on the entity side. A position is a cache of a human's arrangement and
// never a claim that anything exists, so cutting a quest must take its
// pin with it and leave the map.
func TestDeletingAnEntityDropsItsPositionsAndKeepsTheView(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	if _, err := pool.Exec(ctx,
		`INSERT INTO view_positions (view_id, entity_id, project_id, x, y)
		 VALUES ($1, $2, $3, 3, 4)`, a.viewID, a.entityID, a.projectID); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	countRows(t, ctx, pool,
		`SELECT count(*) FROM view_positions WHERE view_id = $1`, []any{a.viewID}, 1)

	if _, err := pool.Exec(ctx, `DELETE FROM entities WHERE id = $1`, a.entityID); err != nil {
		t.Fatalf("deleting a pinned entity must be allowed: %v", err)
	}
	countRows(t, ctx, pool,
		`SELECT count(*) FROM view_positions WHERE view_id = $1`, []any{a.viewID}, 0)
	countRows(t, ctx, pool,
		`SELECT count(*) FROM views WHERE id = $1`, []any{a.viewID}, 1)
}

// TestDeletingAViewTakesItsPositionsAndRefs pins the CASCADE on the view
// side of both child tables: a deleted view leaves no orphan rows.
func TestDeletingAViewTakesItsPositionsAndRefs(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	if _, err := pool.Exec(ctx,
		`INSERT INTO view_positions (view_id, entity_id, project_id, x, y)
		 VALUES ($1, $2, $3, 3, 4)`, a.viewID, a.entityID, a.projectID); err != nil {
		t.Fatalf("insert position: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, entity_type_id, pointer)
		 VALUES ($1, $2, 'entity_type', 'quest', $3, '/from/0/type')`,
		a.viewID, a.projectID, a.entityTypeID); err != nil {
		t.Fatalf("insert ref: %v", err)
	}
	countRows(t, ctx, pool, `SELECT count(*) FROM view_positions`, nil, 1)
	countRows(t, ctx, pool, `SELECT count(*) FROM view_refs`, nil, 1)

	if _, err := pool.Exec(ctx, `DELETE FROM views WHERE id = $1`, a.viewID); err != nil {
		t.Fatalf("delete view: %v", err)
	}
	countRows(t, ctx, pool, `SELECT count(*) FROM view_positions`, nil, 0)
	countRows(t, ctx, pool, `SELECT count(*) FROM view_refs`, nil, 0)
	// The entity and its type outlive the view that drew them.
	countRows(t, ctx, pool, `SELECT count(*) FROM entities WHERE id = $1`, []any{a.entityID}, 1)
}

// TestAViewKeyIsUniquePerGameWithoutRegardToCase pins views_key_key. The
// key is the handle a seeding script re-runs against, so a second run
// under different casing has to collide with the row it already wrote
// instead of leaving a twin beside it -- and two games must still be
// able to use the same key.
func TestAViewKeyIsUniquePerGameWithoutRegardToCase(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")
	b := seedViewGame(t, ctx, pool, "game-b")

	// The other game already holds the same key, seeded above: that is
	// the positive control for "per game".
	countRows(t, ctx, pool,
		`SELECT count(*) FROM views WHERE lower(key) = 'quest-map'`, nil, 2)
	countRows(t, ctx, pool,
		`SELECT count(*) FROM views WHERE project_id = $1`, []any{b.projectID}, 1)

	_, err := pool.Exec(ctx,
		`INSERT INTO views (project_id, key, name, query, renderer)
		 VALUES ($1, 'Quest-Map', 'Twin', '{"v":1}'::jsonb, 'graph')`, a.projectID)
	assertUniqueViolation(t, err)
}

// TestOneRefPerPointerPerView pins view_refs_key. A pointer addresses
// exactly one position in one document, so a second row for it would be
// a rewrite that did not delete the first.
func TestOneRefPerPointerPerView(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")
	b := seedViewGame(t, ctx, pool, "game-b")

	insert := `INSERT INTO view_refs (view_id, project_id, kind, ref_key, entity_type_id, pointer)
	           VALUES ($1, $2, 'entity_type', 'quest', $3, '/from/0/type')`
	if _, err := pool.Exec(ctx, insert, a.viewID, a.projectID, a.entityTypeID); err != nil {
		t.Fatalf("the first ref must be accepted: %v", err)
	}
	// The same pointer under a different view is a different row, which
	// is the positive control for "per view".
	if _, err := pool.Exec(ctx, insert, b.viewID, b.projectID, b.entityTypeID); err != nil {
		t.Fatalf("the same pointer in another view must be accepted: %v", err)
	}
	countRows(t, ctx, pool, `SELECT count(*) FROM view_refs WHERE pointer = '/from/0/type'`, nil, 2)

	_, err := pool.Exec(ctx, insert, a.viewID, a.projectID, a.entityTypeID)
	assertUniqueViolation(t, err)
}

// TestNonFiniteCoordinatesAreRefused pins the x and y CHECKs. NaN
// compares greater than every other double in Postgres, so
// `x < 'Infinity'` is false for it and one conjunction refuses all three
// values.
func TestNonFiniteCoordinatesAreRefused(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	insert := `INSERT INTO view_positions (view_id, entity_id, project_id, x, y)
	           VALUES ($1, $2, $3, $4::double precision, $5::double precision)
	           ON CONFLICT (view_id, entity_id)
	           DO UPDATE SET x = excluded.x, y = excluded.y`

	// A finite pair is accepted and written, so a refusal below cannot
	// be the statement failing for an unrelated reason.
	if _, err := pool.Exec(ctx, insert, a.viewID, a.entityID, a.projectID, "1.5", "-2.5"); err != nil {
		t.Fatalf("a finite coordinate must be accepted: %v", err)
	}
	countRows(t, ctx, pool,
		`SELECT count(*) FROM view_positions WHERE view_id = $1 AND x = 1.5 AND y = -2.5`,
		[]any{a.viewID}, 1)

	for _, value := range []string{"NaN", "Infinity", "-Infinity"} {
		t.Run("x="+value, func(t *testing.T) {
			_, err := pool.Exec(ctx, insert, a.viewID, a.entityID, a.projectID, value, "0")
			assertCheckViolation(t, err)
		})
		t.Run("y="+value, func(t *testing.T) {
			_, err := pool.Exec(ctx, insert, a.viewID, a.entityID, a.projectID, "0", value)
			assertCheckViolation(t, err)
		})
	}

	// And the accepted row is still the one that was written: none of
	// the refusals above left a partial update behind.
	countRows(t, ctx, pool,
		`SELECT count(*) FROM view_positions WHERE view_id = $1 AND x = 1.5 AND y = -2.5`,
		[]any{a.viewID}, 1)
}

// TestARefCannotClaimOneKindAndCarryTheOthersID pins the kind CHECK.
// kind says which of the two id columns a row uses, and a row claiming
// one kind while carrying the other's id would make a staleness query
// filtering on kind miss it.
func TestARefCannotClaimOneKindAndCarryTheOthersID(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	if _, err := pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, relation_type_id, pointer)
		 VALUES ($1, $2, 'relation_type', 'requires', $3, '/traverse/0/via')`,
		a.viewID, a.projectID, a.relationTypeID); err != nil {
		t.Fatalf("a well-formed relation_type ref must be accepted: %v", err)
	}
	countRows(t, ctx, pool,
		`SELECT count(*) FROM view_refs WHERE kind = 'relation_type'`, nil, 1)

	// A null id on either arm is legal: that is what a deleted type
	// looks like, and refusing it would make the SET NULL unwritable.
	if _, err := pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, pointer)
		 VALUES ($1, $2, 'entity_type', 'gone', '/from/0/type')`,
		a.viewID, a.projectID); err != nil {
		t.Fatalf("a ref with a null id must be accepted: %v", err)
	}

	_, err := pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, entity_type_id, pointer)
		 VALUES ($1, $2, 'relation_type', 'requires', $3, '/traverse/1/via')`,
		a.viewID, a.projectID, a.entityTypeID)
	assertCheckViolation(t, err)

	// The mirror image, which is the same hole one step along.
	_, err = pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, relation_type_id, pointer)
		 VALUES ($1, $2, 'entity_type', 'quest', $3, '/from/1/type')`,
		a.viewID, a.projectID, a.relationTypeID)
	assertCheckViolation(t, err)

	// And an unknown kind entirely.
	_, err = pool.Exec(ctx,
		`INSERT INTO view_refs (view_id, project_id, kind, ref_key, pointer)
		 VALUES ($1, $2, 'field', 'min_level', '/where/field')`,
		a.viewID, a.projectID)
	assertCheckViolation(t, err)
}

// TestAnAssetMimeOutsideTheClosedListIsRefused pins the mime CHECK. The
// list is closed on purpose and SVG is not in it: an SVG served inline
// is a script-execution vector.
func TestAnAssetMimeOutsideTheClosedListIsRefused(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	insert := `INSERT INTO view_assets (project_id, filename, mime, width, height, bytes)
	           VALUES ($1, 'map', $2, 4, 4, '\x00'::bytea)`
	for _, mime := range []string{"image/png", "image/jpeg", "image/webp"} {
		if _, err := pool.Exec(ctx, insert, a.projectID, mime); err != nil {
			t.Fatalf("%s must be accepted: %v", mime, err)
		}
	}
	// Three here plus the one seedViewGame wrote.
	countRows(t, ctx, pool,
		`SELECT count(*) FROM view_assets WHERE project_id = $1`, []any{a.projectID}, 4)

	for _, mime := range []string{"image/svg+xml", "text/html", "IMAGE/PNG", ""} {
		t.Run(mime, func(t *testing.T) {
			_, err := pool.Exec(ctx, insert, a.projectID, mime)
			assertCheckViolation(t, err)
		})
	}

	// A non-positive dimension is refused by the same kind of CHECK.
	_, err := pool.Exec(ctx,
		`INSERT INTO view_assets (project_id, filename, mime, width, height, bytes)
		 VALUES ($1, 'map', 'image/png', 0, 4, '\x00'::bytea)`, a.projectID)
	assertCheckViolation(t, err)
}

// TestAnUnknownLayoutModeIsRefused pins the layout_mode CHECK. Its three
// values are a closed contract with the client, which is why they are in
// the database while the renderer catalogue -- expected to grow -- is
// not.
func TestAnUnknownLayoutModeIsRefused(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	for _, mode := range []string{"auto", "manual", "mixed"} {
		if _, err := pool.Exec(ctx,
			`UPDATE views SET layout_mode = $1 WHERE id = $2`, mode, a.viewID); err != nil {
			t.Fatalf("%s must be accepted: %v", mode, err)
		}
		countRows(t, ctx, pool,
			`SELECT count(*) FROM views WHERE id = $1 AND layout_mode = $2`,
			[]any{a.viewID, mode}, 1)
	}

	// The default is mixed, and the renderer has no CHECK at all: a
	// renderer name this migration has never heard of is accepted,
	// because the catalogue is Go's.
	var mode string
	if err := pool.QueryRow(ctx,
		`INSERT INTO views (project_id, key, name, query, renderer)
		 VALUES ($1, 'defaults', 'Defaults', '{"v":1}'::jsonb, 'a-renderer-that-does-not-exist-yet')
		 RETURNING layout_mode`, a.projectID).Scan(&mode); err != nil {
		t.Fatalf("an unknown renderer must be accepted by the schema: %v", err)
	}
	if mode != "mixed" {
		t.Fatalf("layout_mode must default to mixed, got %q", mode)
	}

	_, err := pool.Exec(ctx, `UPDATE views SET layout_mode = 'diagonal' WHERE id = $1`, a.viewID)
	assertCheckViolation(t, err)

	// background_scale carries a CHECK of the same kind.
	_, err = pool.Exec(ctx, `UPDATE views SET background_scale = 0 WHERE id = $1`, a.viewID)
	assertCheckViolation(t, err)
}

// TestTheViewsUpdatedAtTriggerFires and its view_positions counterpart
// pin the set_updated_at triggers. Without them updated_at would depend
// on every query remembering a clause, which is a divergence with a
// silent failure mode: the metamodel's tables shipped without the
// trigger and had to gain it.
func TestTheViewsUpdatedAtTriggerFires(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	var before, after time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM views WHERE id = $1`, a.viewID).Scan(&before); err != nil {
		t.Fatalf("read updated_at before: %v", err)
	}
	// The update deliberately does not set updated_at: the point is
	// that the trigger does it.
	if _, err := pool.Exec(ctx, `UPDATE views SET name = 'Renamed' WHERE id = $1`, a.viewID); err != nil {
		t.Fatalf("update view: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM views WHERE id = $1`, a.viewID).Scan(&after); err != nil {
		t.Fatalf("read updated_at after: %v", err)
	}
	if !after.After(before) {
		t.Fatalf("updated_at did not move on views: %s -> %s", before, after)
	}
}

func TestTheViewPositionsUpdatedAtTriggerFires(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	if _, err := pool.Exec(ctx,
		`INSERT INTO view_positions (view_id, entity_id, project_id, x, y)
		 VALUES ($1, $2, $3, 1, 1)`, a.viewID, a.entityID, a.projectID); err != nil {
		t.Fatalf("insert position: %v", err)
	}

	read := `SELECT updated_at FROM view_positions WHERE view_id = $1 AND entity_id = $2`
	var before, after time.Time
	if err := pool.QueryRow(ctx, read, a.viewID, a.entityID).Scan(&before); err != nil {
		t.Fatalf("read updated_at before: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE view_positions SET x = 9 WHERE view_id = $1 AND entity_id = $2`,
		a.viewID, a.entityID); err != nil {
		t.Fatalf("update position: %v", err)
	}
	if err := pool.QueryRow(ctx, read, a.viewID, a.entityID).Scan(&after); err != nil {
		t.Fatalf("read updated_at after: %v", err)
	}
	if !after.After(before) {
		t.Fatalf("updated_at did not move on view_positions: %s -> %s", before, after)
	}
}

// TestRelationsTypeTargetIndexExists pins relations_type_target_idx
// itself, not just the query shape it serves. Every other index this
// migration adds is exercised indirectly by a CASCADE or SET NULL test
// above; this one backs no constraint and nothing in this file's own
// suite would turn red if `DROP INDEX relations_type_target_idx` were
// added to a later migration. Repo convention leaves migration indexes
// unpinned elsewhere too (0004_metamodel.sql, 0005_entity_listing_index,
// 0007_documents.sql), but this is the most-argued line in 0008 -- a
// measured, deliberately-added index rather than one that falls out of
// a constraint -- so it gets the explicit pin the convention otherwise
// skips.
func TestRelationsTypeTargetIndexExists(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = 'public'
		 AND tablename = 'relations' AND indexname = 'relations_type_target_idx')`).
		Scan(&exists); err != nil {
		t.Fatalf("query pg_indexes: %v", err)
	}
	if !exists {
		t.Fatal("relations_type_target_idx was not created")
	}
}

// TestAViewPositionDefaultsToPinned pins view_positions.pinned's default
// of true. Task 13's layout contract (mixed mode: pinned nodes are
// fixed, the rest are laid out around them) reads this column, and
// before this test the only place its default was stated was prose in
// this file's own header comment.
func TestAViewPositionDefaultsToPinned(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedViewGame(t, ctx, pool, "game-a")

	if _, err := pool.Exec(ctx,
		`INSERT INTO view_positions (view_id, entity_id, project_id, x, y)
		 VALUES ($1, $2, $3, 1, 1)`, a.viewID, a.entityID, a.projectID); err != nil {
		t.Fatalf("insert position without specifying pinned: %v", err)
	}

	var pinned bool
	if err := pool.QueryRow(ctx,
		`SELECT pinned FROM view_positions WHERE view_id = $1 AND entity_id = $2`,
		a.viewID, a.entityID).Scan(&pinned); err != nil {
		t.Fatalf("read pinned: %v", err)
	}
	if !pinned {
		t.Fatal("view_positions.pinned did not default to true")
	}
}
