package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
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

// TestTheSearchBackfillIsExact pins the one claim
// 0006_weighted_entity_search.sql rests on and that nothing else would
// catch: the vector its UPDATE builds from a row's *old, unweighted*
// column is identical to the vector UpsertEntity writes for the same row
// from now on. If it were not, a game seeded before the migration would
// rank differently from the same game re-seeded after it, silently and
// with nothing to signal why.
//
// **It runs the shipped files against real rows — review finding M3.**
// The first version of this test wrote both SQL expressions out in its
// own literal and compared them, and said "change either and this test
// says so". It did not: the reviewer changed the *actual migration's*
// UPDATE to a non-equal expression and the whole suite stayed green,
// because nothing in it ever applied 0006 to a row. It proved an
// algebra identity about a copy.
//
// So the migration's Up arm is read out of the file that ships and
// executed, and the comparison is against `dbq.UpsertEntity` — the
// generated caller of the statement that ships. Neither side is written
// out here any more, and a change to either one that breaks the
// identity fails this test.
//
// The order is the real one: a database is migrated, and content is
// seeded into it afterwards. That also keeps the migration's unqualified
// UPDATE off the re-seeded rows, which it would otherwise weight a
// second time — a fact worth knowing about 0006 and the reason it is a
// one-shot rather than something to re-run.
func TestTheSearchBackfillIsExact(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	var projectID, typeID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ('azeroth', 'Azeroth') RETURNING id`).
		Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'quest', 'Quest', 'Quests') RETURNING id`, projectID).
		Scan(&typeID); err != nil {
		t.Fatalf("insert entity type: %v", err)
	}

	// Every shape a stored row can take that the identity could break
	// on. searchTextOf (internal/metamodel) is what produces the text
	// half in production; here the test supplies it directly, because
	// what is under test is the vector algebra and not the flattening.
	shapes := []struct{ label, name, text string }{
		{"ordinary", "Gnoll Pack", "A gnoll camp led by a gnoll chieftain."},
		{"no text at all", "Gnoll Pack", ""},
		{"fields that repeat the name", "Gnoll", "Gnoll gnoll gnoll"},
		{"an empty name", "", "A camp with no name."},
		{"a name of nothing but spaces", "   ", "A camp with a blank name."},
		{"punctuation only", "!?&", "..."},
		{"unicode", "Château d'Ombrage", "Un château hanté par des goules."},
		{"emoji", "🐺 Pack", "🐺🐺🐺 everywhere"},
		{"a word too long to index", strings.Repeat("z", 3000), strings.Repeat("y", 3000)},
		{"a very long body", "Gnoll Pack", strings.Repeat("gnoll pack ", 5000)},
		{"text that is only whitespace", "Gnoll", "   \t  "},
		{"a name that is also the whole text", "Hogger", "Hogger"},
	}

	// The pre-0006 write path, spelled here because it no longer exists
	// anywhere else: this is exactly what Task 6's UpsertEntity stored.
	const oldWritePath = `INSERT INTO entities (project_id, entity_type_id, key, name, search)
		VALUES ($1, $2, $3, $4, to_tsvector('simple', $4::text || ' ' || $5::text))`
	for i, shape := range shapes {
		if _, err := pool.Exec(ctx, oldWritePath, projectID, typeID,
			fmt.Sprintf("migrated-%02d", i), shape.name, shape.text); err != nil {
			t.Fatalf("seed %s as a pre-migration row: %v", shape.label, err)
		}
	}
	// A row whose vector is NULL, which is why the migration coalesces:
	// it must not be the thing that empties an index.
	if _, err := pool.Exec(ctx,
		`INSERT INTO entities (project_id, entity_type_id, key, name, search)
		 VALUES ($1, $2, 'no-vector', 'No Vector', NULL)`, projectID, typeID); err != nil {
		t.Fatalf("seed a row with no vector: %v", err)
	}

	if _, err := pool.Exec(ctx, gooseArm(t, "0006_weighted_entity_search.sql", "Up")); err != nil {
		t.Fatalf("apply the migration's Up arm: %v", err)
	}

	// Now the rows a game seeded after the migration would hold, written
	// through the statement that actually ships.
	q := dbq.New(pool)
	for i, shape := range shapes {
		if _, err := q.UpsertEntity(ctx, dbq.UpsertEntityParams{
			ProjectID: projectID, EntityTypeID: typeID,
			Key:  fmt.Sprintf("reseeded-%02d", i),
			Name: shape.name, Fields: []byte("{}"), SearchText: shape.text,
			ExpectedVersion: -1,
		}); err != nil {
			t.Fatalf("re-seed %s: %v", shape.label, err)
		}
	}

	for i, shape := range shapes {
		t.Run(shape.label, func(t *testing.T) {
			var same bool
			var migrated string
			if err := pool.QueryRow(ctx, `
				SELECT m.search = r.search, m.search::text
				FROM entities m, entities r
				WHERE m.project_id = $1 AND m.key = $2
				  AND r.project_id = $1 AND r.key = $3`,
				projectID, fmt.Sprintf("migrated-%02d", i), fmt.Sprintf("reseeded-%02d", i),
			).Scan(&same, &migrated); err != nil {
				t.Fatalf("compare: %v", err)
			}
			if !same {
				t.Fatalf("the migrated row's vector differs from the re-seeded row's: %s", migrated)
			}
		})
	}

	// The NULL row keeps a usable vector rather than staying NULL: the
	// name alone, under label A, which is what coalesce buys.
	t.Run("a row that had no vector", func(t *testing.T) {
		var got string
		if err := pool.QueryRow(ctx,
			`SELECT search::text FROM entities WHERE project_id = $1 AND key = 'no-vector'`,
			projectID).Scan(&got); err != nil {
			t.Fatalf("read the backfilled vector: %v", err)
		}
		if !strings.Contains(got, "'no':1A") || !strings.Contains(got, "'vector':2A") {
			t.Fatalf("vector = %s, want the name indexed under label A", got)
		}
	})

	// The Down arm is shipped too, and a downgrade must leave a working
	// search rather than a NULL column. It ranks by presence rather than
	// by frequency until rows are rewritten, which 0006 says out loud.
	if _, err := pool.Exec(ctx, gooseArm(t, "0006_weighted_entity_search.sql", "Down")); err != nil {
		t.Fatalf("apply the migration's Down arm: %v", err)
	}
	var unweighted int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM entities WHERE project_id = $1 AND (search IS NULL OR search::text LIKE '%A%')`,
		projectID).Scan(&unweighted); err != nil {
		t.Fatalf("count downgraded rows: %v", err)
	}
	if unweighted != 0 {
		t.Fatalf("%d rows still carry a weight or a NULL vector after the Down arm", unweighted)
	}
}

// gooseArm returns one arm of a shipped migration, read from the file
// that ships rather than from a copy.
//
// It parses the goose annotations rather than importing goose's own
// parser, which is unexported: the format here is two `-- +goose Up` /
// `-- +goose Down` markers and plain statements between them, and the
// migrations in this repository use nothing else. A migration that grows
// a StatementBegin block will need this to grow with it, and will say so
// by failing.
func gooseArm(t *testing.T, file, arm string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("migrations", file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	body := string(raw)
	if strings.Contains(body, "+goose StatementBegin") {
		t.Fatalf("%s uses a StatementBegin block, which this parser does not handle", file)
	}
	start := strings.Index(body, "-- +goose "+arm)
	if start < 0 {
		t.Fatalf("%s has no %s arm", file, arm)
	}
	rest := body[start+len("-- +goose "+arm):]
	if end := strings.Index(rest, "-- +goose "); end >= 0 {
		rest = rest[:end]
	}
	sql := strings.TrimSpace(rest)
	if sql == "" {
		t.Fatalf("%s's %s arm is empty", file, arm)
	}
	return sql
}

// TestRelationsCarryAnInvalidFlagAndAVersion pins 0009's two columns at
// the level the migration made the claim: their existence, their types,
// their NOT NULL, and the defaults an existing edge is read back under.
//
// The defaults are the load-bearing half. 0009 back-fills nothing — it is
// a pure DDL change — so a row written before it must read as "presumed
// valid, never edited": `invalid false` because the write path had always
// validated an edge against its type's schema, and `version 1` because
// that is what an INSERT lands on and therefore what the first
// compare-and-set against such a row has to expect. A default of `true`,
// or a nullable column, would have made every pre-existing edge in every
// deployed game unwritable or wrongly flagged on the day of the upgrade.
func TestRelationsCarryAnInvalidFlagAndAVersion(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	for _, want := range []struct {
		column, dataType, def string
	}{
		{"invalid", "boolean", "false"},
		{"version", "integer", "1"},
	} {
		var dataType, nullable string
		var def *string
		if err := pool.QueryRow(ctx,
			`SELECT data_type, is_nullable, column_default
			   FROM information_schema.columns
			  WHERE table_schema = 'public' AND table_name = 'relations' AND column_name = $1`,
			want.column).Scan(&dataType, &nullable, &def); err != nil {
			t.Fatalf("relations has no %s column: %v", want.column, err)
		}
		if dataType != want.dataType {
			t.Fatalf("relations.%s is %s, want %s", want.column, dataType, want.dataType)
		}
		if nullable != "NO" {
			t.Fatalf("relations.%s is nullable: an edge with no verdict and no revision "+
				"is a row nothing can compare against", want.column)
		}
		if def == nil || *def != want.def {
			t.Fatalf("relations.%s defaults to %v, want %s — 0009 back-fills nothing, so "+
				"the default is what every existing edge reads back as",
				want.column, def, want.def)
		}
	}

	// An edge inserted with neither column named reads back under both
	// defaults, which is the state 0009 leaves every pre-existing row in.
	projectID, _, sourceID, targetID, relTypeID := seedEdgeParents(t, pool)
	var invalid bool
	var version int32
	if err := pool.QueryRow(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id)
		 VALUES ($1, $2, $3, $4) RETURNING invalid, version`,
		projectID, relTypeID, sourceID, targetID).Scan(&invalid, &version); err != nil {
		t.Fatalf("insert edge: %v", err)
	}
	if invalid || version != 1 {
		t.Fatalf("a fresh edge reads back invalid=%v version=%d, want false and 1",
			invalid, version)
	}
}

// TestTheEdgeSweepSeeksAnIndexRatherThanScanning is the measurement
// 0009's "no new index" claim rests on, run rather than quoted.
//
// The re-validation sweep reads `relations WHERE project_id = $1 AND
// relation_type_id = $2`. entities needed `entities_type_idx` for its own
// sweep because `entities_key_key` leads with project_id and only then
// entity_type_id; relations already has *two* indexes leading with
// relation_type_id — `relations_edge_key` (0004, the unique triple) and
// `relations_type_target_idx` (0008) — so the index 0009 would otherwise
// have added already exists twice over, and which of the two the planner
// picks is its business.
//
// **So the assertion is on the shape of the plan, not on an index
// name.** Naming one would pin a choice this test has no opinion about
// and would go red on a planner that made the other, equally good, one.
// What it must catch is the sequential scan a sweep would fall back to
// if both indexes were dropped or reshaped away from that leading
// column, which is the outcome 0009 says cannot happen.
func TestTheEdgeSweepSeeksAnIndexRatherThanScanning(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	projectID, entityTypeID, sourceID, _, relTypeID := seedEdgeParents(t, pool)
	// **The fixture has to be a game, not one type's worth of rows.** A
	// sweep is selective — it reads the edges of *one* relation type out
	// of a game that has several — and on a table where every row matches
	// the filter, Postgres reads sequentially whatever indexes exist and
	// is right to. Twenty types of five hundred edges each is what makes
	// the plan below about the index rather than about the fixture.
	if _, err := pool.Exec(ctx,
		`INSERT INTO entities (project_id, entity_type_id, key, name)
		 SELECT $1, $2, 'filler-' || i, 'Filler' FROM generate_series(1, 500) AS i`,
		projectID, entityTypeID); err != nil {
		t.Fatalf("seed entities: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO relation_types (project_id, key, label)
		 SELECT $1, 'other-' || i, 'Other' FROM generate_series(1, 19) AS i`,
		projectID); err != nil {
		t.Fatalf("seed relation types: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id)
		 SELECT $1, rt.id, $2, e.id
		   FROM relation_types rt, entities e
		  WHERE rt.project_id = $1 AND e.project_id = $1 AND e.key LIKE 'filler-%'`,
		projectID, sourceID); err != nil {
		t.Fatalf("seed edges: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE relations`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	rows, err := pool.Query(ctx,
		`EXPLAIN SELECT id, fields FROM relations
		  WHERE project_id = $1 AND relation_type_id = $2 ORDER BY id`,
		projectID, relTypeID)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if strings.Contains(plan.String(), "Seq Scan on relations") {
		t.Fatalf("the sweep scans the whole table, so 0009's \"no new index\" claim is "+
			"wrong and the sweep needs one of its own:\n%s", plan.String())
	}
	if !strings.Contains(plan.String(), "Index Scan") {
		t.Fatalf("the sweep's plan reads no index at all:\n%s", plan.String())
	}
}

// seedEdgeParents makes the three parents an edge needs plus two entities
// to join, and hands back everything a caller needs to insert one.
func seedEdgeParents(t *testing.T, pool *pgxpool.Pool) (
	projectID, entityTypeID, sourceID, targetID, relTypeID uuid.UUID,
) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ('azeroth', 'Azeroth') RETURNING id`).
		Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'quest', 'Quest', 'Quests') RETURNING id`, projectID).
		Scan(&entityTypeID); err != nil {
		t.Fatalf("insert entity type: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO relation_types (project_id, key, label)
		 VALUES ($1, 'requires', 'requires') RETURNING id`, projectID).
		Scan(&relTypeID); err != nil {
		t.Fatalf("insert relation type: %v", err)
	}
	for _, spec := range []struct {
		key string
		out *uuid.UUID
	}{{"hogger", &sourceID}, {"kobolds", &targetID}} {
		if err := pool.QueryRow(ctx,
			`INSERT INTO entities (project_id, entity_type_id, key, name)
			 VALUES ($1, $2, $3, $3) RETURNING id`, projectID, entityTypeID, spec.key).
			Scan(spec.out); err != nil {
			t.Fatalf("insert entity %s: %v", spec.key, err)
		}
	}
	return projectID, entityTypeID, sourceID, targetID, relTypeID
}
