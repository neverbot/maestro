package db_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

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
