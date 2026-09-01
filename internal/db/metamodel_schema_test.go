package db_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/testutil"
)

func TestMetamodelTablesExist(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	for _, table := range []string{"entity_types", "relation_types", "entities", "relations"} {
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

func TestEntityKeyIsUniquePerTypeAndProject(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	var projectID, typeID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ('azeroth', 'Azeroth') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'quest', 'Quest', 'Quests') RETURNING id`, projectID).Scan(&typeID); err != nil {
		t.Fatalf("insert entity type: %v", err)
	}

	insert := `INSERT INTO entities (project_id, entity_type_id, key, name) VALUES ($1, $2, $3, $4)`
	if _, err := pool.Exec(ctx, insert, projectID, typeID, "wanted-hogger", "Wanted: Hogger"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// The uniqueness is case-insensitive, so a re-seed with different casing
	// must collide rather than create a twin.
	if _, err := pool.Exec(ctx, insert, projectID, typeID, "Wanted-Hogger", "Wanted: Hogger"); err == nil {
		t.Fatal("expected a unique violation on a duplicate key")
	}

	// The index is scoped by both project and entity type, so the same key
	// under a different type of the same game, or under the same-named type
	// of another game, is a different entity and must be accepted.
	var otherTypeID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'zone', 'Zone', 'Zones') RETURNING id`, projectID).Scan(&otherTypeID); err != nil {
		t.Fatalf("insert second entity type: %v", err)
	}
	if _, err := pool.Exec(ctx, insert, projectID, otherTypeID, "wanted-hogger", "Wanted: Hogger"); err != nil {
		t.Fatalf("same key under another entity type: %v", err)
	}

	var otherProjectID, otherProjectTypeID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ('outland', 'Outland') RETURNING id`).Scan(&otherProjectID); err != nil {
		t.Fatalf("insert second project: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'quest', 'Quest', 'Quests') RETURNING id`, otherProjectID).Scan(&otherProjectTypeID); err != nil {
		t.Fatalf("insert entity type in second project: %v", err)
	}
	if _, err := pool.Exec(ctx, insert, otherProjectID, otherProjectTypeID, "wanted-hogger", "Wanted: Hogger"); err != nil {
		t.Fatalf("same key in another project: %v", err)
	}
}

// crossProjectFixture seeds two games, each with one entity type, one
// relation type and one entity, and hands back the identifiers the
// cross-project tests need. Every row it creates is legitimate: the
// tests below build the illegal combinations from these pieces.
type crossProjectFixture struct {
	projectA, projectB           string
	entityTypeA, entityTypeB     string
	relationTypeA, relationTypeB string
	entityA1, entityA2, entityB  string
}

func seedTwoProjects(t *testing.T, ctx context.Context, pool *pgxpool.Pool) crossProjectFixture {
	t.Helper()

	var f crossProjectFixture
	seedProject := func(slug, name string, projectID, entityTypeID, relationTypeID *string, entityIDs ...*string) {
		t.Helper()
		if err := pool.QueryRow(ctx,
			`INSERT INTO projects (slug, name) VALUES ($1, $2) RETURNING id`, slug, name).Scan(projectID); err != nil {
			t.Fatalf("insert project %s: %v", slug, err)
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO entity_types (project_id, key, label, label_plural)
			 VALUES ($1, 'quest', 'Quest', 'Quests') RETURNING id`, *projectID).Scan(entityTypeID); err != nil {
			t.Fatalf("insert entity type in %s: %v", slug, err)
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO relation_types (project_id, key, label)
			 VALUES ($1, 'requires', 'Requires') RETURNING id`, *projectID).Scan(relationTypeID); err != nil {
			t.Fatalf("insert relation type in %s: %v", slug, err)
		}
		for i, id := range entityIDs {
			if err := pool.QueryRow(ctx,
				`INSERT INTO entities (project_id, entity_type_id, key, name)
				 VALUES ($1, $2, $3, $3) RETURNING id`,
				*projectID, *entityTypeID, fmt.Sprintf("quest-%d", i)).Scan(id); err != nil {
				t.Fatalf("insert entity %d in %s: %v", i, slug, err)
			}
		}
	}

	seedProject("azeroth-x", "Azeroth", &f.projectA, &f.entityTypeA, &f.relationTypeA, &f.entityA1, &f.entityA2)
	seedProject("outland-x", "Outland", &f.projectB, &f.entityTypeB, &f.relationTypeB, &f.entityB)
	return f
}

// assertForeignKeyViolation fails unless err is a Postgres foreign key
// violation (SQLSTATE 23503). A nil error means the database accepted a
// row that spans two games; any other error means the statement was
// rejected for the wrong reason and the test would otherwise pass without
// proving anything.
func assertForeignKeyViolation(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a foreign key violation, but the insert succeeded")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError, got %T: %v", err, err)
	}
	if pgErr.Code != "23503" {
		t.Fatalf("expected SQLSTATE 23503 (foreign_key_violation), got %s: %v", pgErr.Code, err)
	}
}

// TestEntityCannotUseAnotherProjectsEntityType pins isolation in SQL, not
// in Go: the composite key entities (entity_type_id, project_id) ->
// entity_types (id, project_id) makes a borrowed entity type unwritable
// no matter what the handler layer forgets to check.
func TestEntityCannotUseAnotherProjectsEntityType(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	f := seedTwoProjects(t, ctx, pool)

	_, err := pool.Exec(ctx,
		`INSERT INTO entities (project_id, entity_type_id, key, name) VALUES ($1, $2, 'stolen', 'Stolen')`,
		f.projectA, f.entityTypeB)
	assertForeignKeyViolation(t, err)

	// The legitimate case still works.
	if _, err := pool.Exec(ctx,
		`INSERT INTO entities (project_id, entity_type_id, key, name) VALUES ($1, $2, 'legit', 'Legit')`,
		f.projectA, f.entityTypeA); err != nil {
		t.Fatalf("same-project entity type: %v", err)
	}
}

// TestRelationCannotUseAnotherProjectsRelationType covers the same rule
// for the edge's type.
func TestRelationCannotUseAnotherProjectsRelationType(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	f := seedTwoProjects(t, ctx, pool)

	insert := `INSERT INTO relations (project_id, relation_type_id, source_id, target_id) VALUES ($1, $2, $3, $4)`
	_, err := pool.Exec(ctx, insert, f.projectA, f.relationTypeB, f.entityA1, f.entityA2)
	assertForeignKeyViolation(t, err)

	if _, err := pool.Exec(ctx, insert, f.projectA, f.relationTypeA, f.entityA1, f.entityA2); err != nil {
		t.Fatalf("same-project relation: %v", err)
	}
}

// TestRelationCannotPointAtAnotherProjectsSource keeps an edge's tail
// inside its own game.
func TestRelationCannotPointAtAnotherProjectsSource(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	f := seedTwoProjects(t, ctx, pool)

	_, err := pool.Exec(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id) VALUES ($1, $2, $3, $4)`,
		f.projectA, f.relationTypeA, f.entityB, f.entityA2)
	assertForeignKeyViolation(t, err)
}

// TestRelationCannotPointAtAnotherProjectsTarget keeps an edge's head
// inside its own game.
func TestRelationCannotPointAtAnotherProjectsTarget(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	f := seedTwoProjects(t, ctx, pool)

	_, err := pool.Exec(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id) VALUES ($1, $2, $3, $4)`,
		f.projectA, f.relationTypeA, f.entityA1, f.entityB)
	assertForeignKeyViolation(t, err)
}

// seedToken creates a user and an api_token owned by the given project
// and returns the token id. Tokens are project-scoped, so a token from
// one game must never be recordable as the last editor of another
// game's content.
func seedToken(t *testing.T, ctx context.Context, pool *pgxpool.Pool, projectID, label string) string {
	t.Helper()

	var userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, display_name, password_hash)
		 VALUES ($1, $1, 'x') RETURNING id`, label+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("insert user for %s: %v", label, err)
	}
	var tokenID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO api_tokens (token_hash, token_hint, project_id, user_id, label)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		[]byte(label), label, projectID, userID, label).Scan(&tokenID); err != nil {
		t.Fatalf("insert token %s: %v", label, err)
	}
	return tokenID
}

// TestCannotRecordAnotherProjectsToken pins the last isolation hole in
// the schema: updated_by_token_id points at api_tokens, which is
// project-scoped, so the key has to be composite like every other key
// from these tables to a project-scoped parent. Without it a UI
// rendering "last edited by <token label>" would show another game's
// token label.
func TestCannotRecordAnotherProjectsToken(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	f := seedTwoProjects(t, ctx, pool)

	tokenA := seedToken(t, ctx, pool, f.projectA, "token-a")
	tokenB := seedToken(t, ctx, pool, f.projectB, "token-b")

	cases := []struct {
		name string
		sql  string
		args func(token string) []any
	}{
		{
			name: "entity_types",
			sql: `INSERT INTO entity_types (project_id, key, label, label_plural, updated_by_token_id)
			      VALUES ($1, $2, 'Zone', 'Zones', $3)`,
			args: func(token string) []any { return []any{f.projectA, "zone-" + token[:8], token} },
		},
		{
			name: "relation_types",
			sql: `INSERT INTO relation_types (project_id, key, label, updated_by_token_id)
			      VALUES ($1, $2, 'Unlocks', $3)`,
			args: func(token string) []any { return []any{f.projectA, "unlocks-" + token[:8], token} },
		},
		{
			name: "entities",
			sql: `INSERT INTO entities (project_id, entity_type_id, key, name, updated_by_token_id)
			      VALUES ($1, $2, $3, 'Quest', $4)`,
			args: func(token string) []any {
				return []any{f.projectA, f.entityTypeA, "quest-" + token[:8], token}
			},
		},
		{
			name: "relations",
			sql: `INSERT INTO relations (project_id, relation_type_id, source_id, target_id, updated_by_token_id)
			      VALUES ($1, $2, $3, $4, $5)`,
			args: func(token string) []any {
				return []any{f.projectA, f.relationTypeA, f.entityA1, f.entityA2, token}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A token owned by game B on a row owned by game A.
			_, err := pool.Exec(ctx, tc.sql, tc.args(tokenB)...)
			assertForeignKeyViolation(t, err)

			// The game's own token is accepted.
			if _, err := pool.Exec(ctx, tc.sql, tc.args(tokenA)...); err != nil {
				t.Fatalf("same-project token on %s: %v", tc.name, err)
			}
		})
	}
}

// TestUpdatedAtTriggerFires keeps the metamodel tables on the same
// mechanism as the Core tables: updated_at comes from a trigger, not
// from every query remembering to write `updated_at = now()`. Two
// mechanisms for one column diverge silently.
func TestUpdatedAtTriggerFires(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	f := seedTwoProjects(t, ctx, pool)

	var relationID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		f.projectA, f.relationTypeA, f.entityA1, f.entityA2).Scan(&relationID); err != nil {
		t.Fatalf("insert relation: %v", err)
	}

	cases := []struct {
		table  string
		id     string
		update string
	}{
		{"entity_types", f.entityTypeA, `UPDATE entity_types SET label = 'Renamed' WHERE id = $1`},
		{"relation_types", f.relationTypeA, `UPDATE relation_types SET label = 'Renamed' WHERE id = $1`},
		{"entities", f.entityA1, `UPDATE entities SET name = 'Renamed' WHERE id = $1`},
		{"relations", relationID, `UPDATE relations SET fields = '{"note":"x"}'::jsonb WHERE id = $1`},
	}

	for _, tc := range cases {
		t.Run(tc.table, func(t *testing.T) {
			var before, after time.Time
			//nolint:gosec // the table name comes from this test's own literal list.
			read := fmt.Sprintf(`SELECT updated_at FROM %s WHERE id = $1`, tc.table)
			if err := pool.QueryRow(ctx, read, tc.id).Scan(&before); err != nil {
				t.Fatalf("read updated_at before: %v", err)
			}
			// The update deliberately does not set updated_at: the point
			// is that the trigger does it.
			if _, err := pool.Exec(ctx, tc.update, tc.id); err != nil {
				t.Fatalf("update %s: %v", tc.table, err)
			}
			if err := pool.QueryRow(ctx, read, tc.id).Scan(&after); err != nil {
				t.Fatalf("read updated_at after: %v", err)
			}
			if !after.After(before) {
				t.Fatalf("updated_at did not move on %s: %s -> %s", tc.table, before, after)
			}
		})
	}
}

// TestDuplicateRelationEdgeIsRejected pins relations_edge_key. It is
// load-bearing for the relation upsert, whose ON CONFLICT names exactly
// these three columns: were the index dropped, every upsert would fail
// at runtime rather than here.
func TestDuplicateRelationEdgeIsRejected(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	f := seedTwoProjects(t, ctx, pool)

	insert := `INSERT INTO relations (project_id, relation_type_id, source_id, target_id) VALUES ($1, $2, $3, $4)`
	if _, err := pool.Exec(ctx, insert, f.projectA, f.relationTypeA, f.entityA1, f.entityA2); err != nil {
		t.Fatalf("first edge: %v", err)
	}

	_, err := pool.Exec(ctx, insert, f.projectA, f.relationTypeA, f.entityA1, f.entityA2)
	if err == nil {
		t.Fatal("expected a unique violation on a duplicate edge, but the insert succeeded")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError, got %T: %v", err, err)
	}
	if pgErr.Code != "23505" {
		t.Fatalf("expected SQLSTATE 23505 (unique_violation), got %s: %v", pgErr.Code, err)
	}

	// The reverse direction is a different edge and must be accepted.
	if _, err := pool.Exec(ctx, insert, f.projectA, f.relationTypeA, f.entityA2, f.entityA1); err != nil {
		t.Fatalf("reversed edge: %v", err)
	}
}

// TestDeletingAnEntityTypeWithInstancesIsRejected pins the RESTRICT on
// entities -> entity_types. Flipped to CASCADE it would silently delete
// every entity of the type instead of failing loudly.
func TestDeletingAnEntityTypeWithInstancesIsRejected(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	f := seedTwoProjects(t, ctx, pool)

	_, err := pool.Exec(ctx, `DELETE FROM entity_types WHERE id = $1`, f.entityTypeA)
	assertForeignKeyViolation(t, err)

	// Once its instances are gone the type is deletable.
	if _, err := pool.Exec(ctx, `DELETE FROM entities WHERE entity_type_id = $1`, f.entityTypeA); err != nil {
		t.Fatalf("delete entities: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM entity_types WHERE id = $1`, f.entityTypeA); err != nil {
		t.Fatalf("delete empty entity type: %v", err)
	}
}

// TestDeletingATokenClearsOnlyTheTokenColumn covers the one subtlety of
// making updated_by_token_id composite: a bare ON DELETE SET NULL would
// try to null project_id too, which is NOT NULL. The key names its
// column, so revoking a token must leave the row and its project alone.
func TestDeletingATokenClearsOnlyTheTokenColumn(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	f := seedTwoProjects(t, ctx, pool)
	tokenA := seedToken(t, ctx, pool, f.projectA, "token-a")

	if _, err := pool.Exec(ctx,
		`UPDATE entities SET updated_by_token_id = $1 WHERE id = $2`, tokenA, f.entityA1); err != nil {
		t.Fatalf("stamp entity with token: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM api_tokens WHERE id = $1`, tokenA); err != nil {
		t.Fatalf("delete token: %v", err)
	}

	var tokenID *string
	var projectID string
	if err := pool.QueryRow(ctx,
		`SELECT updated_by_token_id, project_id FROM entities WHERE id = $1`,
		f.entityA1).Scan(&tokenID, &projectID); err != nil {
		t.Fatalf("read entity after token deletion: %v", err)
	}
	if tokenID != nil {
		t.Fatalf("updated_by_token_id = %q, want NULL after the token was deleted", *tokenID)
	}
	if projectID != f.projectA {
		t.Fatalf("project_id = %q, want %q; the SET NULL must not touch it", projectID, f.projectA)
	}
}
