package db_test

import (
	"context"
	"testing"

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
