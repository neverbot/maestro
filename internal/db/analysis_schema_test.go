package db_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/graph"
	"github.com/neverbot/maestro/internal/testutil"
)

// analysisGame is one seeded game with the parents every test below
// needs: a project, an entity type, two entities, a relation type and a
// route with one step. Every cross-game test builds its illegal row out
// of one game's route and the other's entity or token.
type analysisGame struct {
	projectID      uuid.UUID
	entityTypeID   uuid.UUID
	entityID       uuid.UUID
	otherEntityID  uuid.UUID
	relationTypeID uuid.UUID
	routeID        uuid.UUID
}

func seedAnalysisGame(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slug string) analysisGame {
	t.Helper()

	var g analysisGame
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
		`INSERT INTO entities (project_id, entity_type_id, key, name)
		 VALUES ($1, $2, 'kobolds', 'Kobold menace') RETURNING id`,
		g.projectID, g.entityTypeID).Scan(&g.otherEntityID); err != nil {
		t.Fatalf("insert second entity in %s: %v", slug, err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO relation_types (project_id, key, label)
		 VALUES ($1, 'requires', 'Requires') RETURNING id`, g.projectID).Scan(&g.relationTypeID); err != nil {
		t.Fatalf("insert relation type in %s: %v", slug, err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO routes (project_id, key, name) VALUES ($1, 'levelling', 'Levelling') RETURNING id`,
		g.projectID).Scan(&g.routeID); err != nil {
		t.Fatalf("insert route in %s: %v", slug, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO route_steps (route_id, project_id, position, entity_id, entity_type_key, entity_key)
		 VALUES ($1, $2, 1, $3, 'quest', 'hogger')`,
		g.routeID, g.projectID, g.entityID); err != nil {
		t.Fatalf("insert route step in %s: %v", slug, err)
	}
	return g
}

// designVersion reads the game's opaque monotonic design token. Every
// counter test below reads it through this function rather than
// asserting on a literal, because the *value* is not the contract --
// only that it moves.
func designVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(ctx, `SELECT design_version FROM projects WHERE id = $1`, projectID).Scan(&v); err != nil {
		t.Fatalf("read design_version: %v", err)
	}
	return v
}

func TestAnalysisTablesExist(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	for _, table := range []string{"routes", "route_steps"} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			table).Scan(&exists); err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		assert.Must(t, exists, "table %s was not created", table)
	}
}

// TestTheTraitVocabularyConstraintRefusesAnUnknownTrait is the whole
// point of putting the vocabulary in the database rather than only in
// Go: a value outside it must not reach a row.
func TestTheTraitVocabularyConstraintRefusesAnUnknownTrait(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	g := seedAnalysisGame(t, ctx, pool, "azeroth")

	_, err := pool.Exec(ctx,
		`UPDATE relation_types SET analysis_traits = ARRAY['teleports']::text[] WHERE id = $1`, g.relationTypeID)
	assertCheckViolation(t, err)

	// A single unknown trait hidden among known ones is the shape a
	// typo actually takes, and <@ is a subset test, so it has to be
	// refused too.
	_, err = pool.Exec(ctx,
		`UPDATE relation_types SET analysis_traits = ARRAY['prerequisite_of','teleports']::text[] WHERE id = $1`,
		g.relationTypeID)
	assertCheckViolation(t, err)

	if _, err := pool.Exec(ctx,
		`UPDATE relation_types SET analysis_traits = ARRAY['prerequisite_of','acyclic']::text[] WHERE id = $1`,
		g.relationTypeID); err != nil {
		t.Fatalf("a legitimate trait pair was refused: %v", err)
	}
	var got []string
	if err := pool.QueryRow(ctx,
		`SELECT analysis_traits FROM relation_types WHERE id = $1`, g.relationTypeID).Scan(&got); err != nil {
		t.Fatalf("read traits back: %v", err)
	}
	assert.Must(t, len(got) == 2 && got[0] == "prerequisite_of" && got[1] == "acyclic", "read back %v, want [prerequisite_of acyclic]", got)

	// Every word of the vocabulary is accepted, so the constraint cannot
	// pass by admitting only the two this test happened to pick.
	for _, trait := range []string{
		"prerequisite_of", "unlocks", "containment", "ordering", "symmetric", "acyclic", "annotation",
	} {
		if _, err := pool.Exec(ctx,
			`UPDATE relation_types SET analysis_traits = ARRAY[$2]::text[] WHERE id = $1`,
			g.relationTypeID, trait); err != nil {
			t.Fatalf("the vocabulary refused %q, which is in it: %v", trait, err)
		}
	}
}

// TestAnEmptyTraitArrayIsRefusedAndNullIsNot pins the distinction the
// cardinality half of the constraint exists for: NULL is "nobody has
// said anything about this type", {annotation} is "this type is
// deliberately inert", and {} is neither -- it is a third spelling that
// would make the two indistinguishable to every reader.
func TestAnEmptyTraitArrayIsRefusedAndNullIsNot(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	g := seedAnalysisGame(t, ctx, pool, "azeroth")

	_, err := pool.Exec(ctx,
		`UPDATE relation_types SET analysis_traits = ARRAY[]::text[] WHERE id = $1`, g.relationTypeID)
	assertCheckViolation(t, err)

	if _, err := pool.Exec(ctx,
		`UPDATE relation_types SET analysis_traits = NULL WHERE id = $1`, g.relationTypeID); err != nil {
		t.Fatalf("NULL is undeclared and must be accepted: %v", err)
	}
	var isNull bool
	if err := pool.QueryRow(ctx,
		`SELECT analysis_traits IS NULL FROM relation_types WHERE id = $1`, g.relationTypeID).Scan(&isNull); err != nil {
		t.Fatalf("read traits back: %v", err)
	}
	assert.Must(t, isNull, "traits set to NULL did not read back as NULL")

	// The inert spelling, which is what {} would otherwise be mistaken
	// for.
	if _, err := pool.Exec(ctx,
		`UPDATE relation_types SET analysis_traits = ARRAY['annotation']::text[] WHERE id = $1`,
		g.relationTypeID); err != nil {
		t.Fatalf("{annotation} is the inert declaration and must be accepted: %v", err)
	}
}

// TestANewRelationTypeHasNoTraitsAndThatIsNotAnError pins the default:
// every relation type in every game that existed before this migration
// reads as undeclared, and an undeclared type is a state the engine has
// to be able to represent rather than a broken row.
func TestANewRelationTypeHasNoTraitsAndThatIsNotAnError(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	g := seedAnalysisGame(t, ctx, pool, "azeroth")

	var isNull bool
	if err := pool.QueryRow(ctx,
		`SELECT analysis_traits IS NULL FROM relation_types WHERE id = $1`, g.relationTypeID).Scan(&isNull); err != nil {
		t.Fatalf("read traits: %v", err)
	}
	assert.Must(t, isNull, "a freshly inserted relation type must read as undeclared (NULL), not as a declared empty set")
}

// TestARouteStepCannotNameAnEntityFromAnotherGame is the isolation the
// composite key exists for. Without project_id in the key a step could
// point at another game's entity and a check would report that step as
// reachable on the strength of a graph the caller cannot see.
func TestARouteStepCannotNameAnEntityFromAnotherGame(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedAnalysisGame(t, ctx, pool, "azeroth")
	b := seedAnalysisGame(t, ctx, pool, "outland")

	_, err := pool.Exec(ctx,
		`INSERT INTO route_steps (route_id, project_id, position, entity_id, entity_type_key, entity_key)
		 VALUES ($1, $2, 2, $3, 'quest', 'hogger')`,
		a.routeID, a.projectID, b.entityID)
	assertForeignKeyViolation(t, err)

	// Positive control: the same statement with this game's own entity
	// lands, so the refusal above is about the game and not about the
	// statement.
	if _, err := pool.Exec(ctx,
		`INSERT INTO route_steps (route_id, project_id, position, entity_id, entity_type_key, entity_key)
		 VALUES ($1, $2, 2, $3, 'quest', 'kobolds')`,
		a.routeID, a.projectID, a.otherEntityID); err != nil {
		t.Fatalf("a step naming this game's own entity was refused: %v", err)
	}
}

// TestARouteStepCannotBorrowAnotherGamesRoute is the other half of the
// same composite key: a step's route and its project must agree.
func TestARouteStepCannotBorrowAnotherGamesRoute(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedAnalysisGame(t, ctx, pool, "azeroth")
	b := seedAnalysisGame(t, ctx, pool, "outland")

	_, err := pool.Exec(ctx,
		`INSERT INTO route_steps (route_id, project_id, position, entity_id, entity_type_key, entity_key)
		 VALUES ($1, $2, 9, $3, 'quest', 'hogger')`,
		b.routeID, a.projectID, a.entityID)
	assertForeignKeyViolation(t, err)
}

// TestDeletingAnEntityLeavesItsRouteStepWithATombstone is the argument
// for SET NULL rather than CASCADE, asserted rather than stated.
func TestDeletingAnEntityLeavesItsRouteStepWithATombstone(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	g := seedAnalysisGame(t, ctx, pool, "azeroth")

	if _, err := pool.Exec(ctx, `DELETE FROM entities WHERE id = $1`, g.entityID); err != nil {
		t.Fatalf("delete entity: %v", err)
	}

	var (
		entityID      *uuid.UUID
		entityTypeKey string
		entityKey     string
		projectID     uuid.UUID
	)
	if err := pool.QueryRow(ctx,
		`SELECT entity_id, entity_type_key, entity_key, project_id
		   FROM route_steps WHERE route_id = $1 AND position = 1`, g.routeID).
		Scan(&entityID, &entityTypeKey, &entityKey, &projectID); err != nil {
		t.Fatalf("the step did not survive its entity's deletion: %v", err)
	}
	if entityID != nil {
		t.Fatalf("entity_id was not nulled, got %v", *entityID)
	}
	assert.Must(t, entityTypeKey == "quest" && entityKey == "hogger", "the tombstone pair was rewritten: got (%q, %q), want (quest, hogger)", entityTypeKey, entityKey)
	// project_id must survive untouched: a bare SET NULL on the
	// composite key would have tried to null it too, and it is NOT NULL.
	assert.Must(t, projectID == g.projectID, "project_id changed on the step: got %v, want %v", projectID, g.projectID)
}

// TestARouteCannotRecordAnotherGamesToken mirrors
// TestCannotRecordAnotherProjectsToken for the new table: the audit
// column's key is composite for the same reason every other one is.
func TestARouteCannotRecordAnotherGamesToken(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedAnalysisGame(t, ctx, pool, "azeroth")
	b := seedAnalysisGame(t, ctx, pool, "outland")
	foreign := seedToken(t, ctx, pool, b.projectID.String(), "outland-token")
	own := seedToken(t, ctx, pool, a.projectID.String(), "azeroth-token")

	_, err := pool.Exec(ctx,
		`UPDATE routes SET updated_by_token_id = $2 WHERE id = $1`, a.routeID, foreign)
	assertForeignKeyViolation(t, err)

	if _, err := pool.Exec(ctx,
		`UPDATE routes SET updated_by_token_id = $2 WHERE id = $1`, a.routeID, own); err != nil {
		t.Fatalf("a route recording its own game's token was refused: %v", err)
	}
}

// TestRevokingATokenNullsOnlyTheTokenColumnOfARoute pins that the SET
// NULL names its column. A bare SET NULL on the composite key would try
// to null project_id as well, and would fail at token-revocation time --
// which a designer meets and a migration test does not.
func TestRevokingATokenNullsOnlyTheTokenColumnOfARoute(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	g := seedAnalysisGame(t, ctx, pool, "azeroth")
	token := seedToken(t, ctx, pool, g.projectID.String(), "azeroth-token")

	if _, err := pool.Exec(ctx,
		`UPDATE routes SET updated_by_token_id = $2 WHERE id = $1`, g.routeID, token); err != nil {
		t.Fatalf("record token: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM api_tokens WHERE id = $1`, token); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	var (
		tokenID   *uuid.UUID
		projectID uuid.UUID
	)
	if err := pool.QueryRow(ctx,
		`SELECT updated_by_token_id, project_id FROM routes WHERE id = $1`, g.routeID).
		Scan(&tokenID, &projectID); err != nil {
		t.Fatalf("the route did not survive its token's revocation: %v", err)
	}
	if tokenID != nil {
		t.Fatalf("updated_by_token_id was not nulled, got %v", *tokenID)
	}
	assert.Must(t, projectID == g.projectID, "project_id was nulled or changed: got %v, want %v", projectID, g.projectID)
}

// TestARouteKeyIsUniquePerGameWithoutRegardToCase pins routes_key_key:
// a re-seed with different casing must collide with the existing row
// rather than create a twin, exactly as entity types, documents and
// views do.
func TestARouteKeyIsUniquePerGameWithoutRegardToCase(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedAnalysisGame(t, ctx, pool, "azeroth")
	b := seedAnalysisGame(t, ctx, pool, "outland")

	_, err := pool.Exec(ctx,
		`INSERT INTO routes (project_id, key, name) VALUES ($1, 'Levelling', 'Levelling again')`, a.projectID)
	assertUniqueViolation(t, err)

	// The other game may use the same key: uniqueness is per game, and
	// without this control the index could be global and still pass.
	countRows(t, ctx, pool, `SELECT count(*) FROM routes WHERE project_id = $1 AND lower(key) = 'levelling'`,
		[]any{b.projectID}, 1)
}

// TestDeletingARouteTakesItsSteps pins the CASCADE on the step's key to
// its route. Steps have no life of their own: a step without its route
// is a row nothing can address.
func TestDeletingARouteTakesItsSteps(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	g := seedAnalysisGame(t, ctx, pool, "azeroth")

	countRows(t, ctx, pool, `SELECT count(*) FROM route_steps WHERE route_id = $1`, []any{g.routeID}, 1)
	if _, err := pool.Exec(ctx, `DELETE FROM routes WHERE id = $1`, g.routeID); err != nil {
		t.Fatalf("delete route: %v", err)
	}
	countRows(t, ctx, pool, `SELECT count(*) FROM route_steps WHERE route_id = $1`, []any{g.routeID}, 0)
	// The entity the step named is untouched: deleting a route is not a
	// content deletion.
	countRows(t, ctx, pool, `SELECT count(*) FROM entities WHERE id = $1`, []any{g.entityID}, 1)
}

// designTables is the set of tables a *game's design* is made of -- the
// four the design counter watches. Every other project-scoped table is
// classified below with the reason it is not one, so that adding a
// fifth project-scoped table anywhere in this repository fails a test in
// a package its author did not touch and forces the decision to be made
// rather than skipped.
var designTables = map[string]string{
	"entity_types":   "a game's kinds of thing",
	"relation_types": "a game's kinds of edge, and where analysis_traits live",
	"entities":       "a game's content",
	"relations":      "a game's edges",
}

// notDesignTables is every other project-scoped table, with the argument
// for its exclusion. These are not bookkeeping: routes especially must
// stay out, because a check that marked every route in the game stale --
// including the one it had just checked -- would be a mechanism that
// invalidates its own output.
var notDesignTables = map[string]string{
	"api_tokens":        "credentials, not content",
	"invites":           "identity, not content",
	"memberships":       "identity, not content",
	"documents":         "prose about the design, judged stale by its own version",
	"document_versions": "history of the above",
	"document_links":    "an index over the above",
	"views":             "a stored question, which caches nothing and cannot go stale",
	"view_positions":    "a human's arrangement of a picture",
	"view_refs":         "a dependency index over views",
	"view_assets":       "background images",
	"routes":            "a stored verdict; watching it would invalidate its own output",
	"route_steps":       "part of a route, above",
}

// TestEveryMetamodelTableCarriesTheDesignVersionTriggers reads the list
// of project-scoped tables **from the database** rather than from a
// second Go literal, so that the assertion cannot fall behind the
// schema.
func TestEveryMetamodelTableCarriesTheDesignVersionTriggers(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx,
		`SELECT c.relname
		   FROM pg_class c
		   JOIN pg_namespace n ON n.oid = c.relnamespace
		   JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'project_id'
		                      AND a.attnum > 0 AND NOT a.attisdropped
		  WHERE c.relkind = 'r' AND n.nspname = 'public'
		  ORDER BY c.relname`)
	assert.Must(t, err == nil, "list project-scoped tables: %v", err)
	var scoped []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		scoped = append(scoped, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("list project-scoped tables: %v", err)
	}
	assert.Must(t, len(scoped) != 0, "no project-scoped tables found; this test would assert nothing")

	// Triggers, by table, from the catalogue -- matched on the function
	// they call rather than on their names, so renaming one does not
	// silently empty this assertion.
	type trig struct {
		name  string
		ttype int16
	}
	byTable := map[string][]trig{}
	trows, err := pool.Query(ctx,
		`SELECT c.relname, t.tgname, t.tgtype
		   FROM pg_trigger t
		   JOIN pg_class c ON c.oid = t.tgrelid
		   JOIN pg_proc  p ON p.oid = t.tgfoid
		  WHERE NOT t.tgisinternal AND p.proname = 'bump_design_version'`)
	assert.Must(t, err == nil, "list design-version triggers: %v", err)
	for trows.Next() {
		var table string
		var tr trig
		if err := trows.Scan(&table, &tr.name, &tr.ttype); err != nil {
			t.Fatalf("scan trigger: %v", err)
		}
		byTable[table] = append(byTable[table], tr)
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		t.Fatalf("list design-version triggers: %v", err)
	}

	for _, table := range scoped {
		_, isDesign := designTables[table]
		_, isNot := notDesignTables[table]
		switch {
		case isDesign && isNot:
			t.Fatalf("%s is classified both ways", table)
		case !isDesign && !isNot:
			t.Fatalf("table %s is project-scoped and this test has no opinion about it: "+
				"decide whether it is part of a game's design and add it to designTables "+
				"or notDesignTables in this file", table)
		case isNot:
			if got := byTable[table]; len(got) != 0 {
				t.Fatalf("%s carries design-version triggers %v but is excluded because it is %s",
					table, got, notDesignTables[table])
			}
			continue
		}

		got := byTable[table]
		if len(got) != 3 {
			t.Fatalf("%s (%s) carries %d design-version triggers, want 3 (insert, update, delete): %v",
				table, designTables[table], len(got), got)
		}
		events := int16(0)
		for _, tr := range got {
			assert.Must(t, tr.ttype&1 == 0, "%s.%s is FOR EACH ROW; the counter is maintained per statement, "+
				"which is what makes a thousand-row write cost one UPDATE", table, tr.name)
			assert.Must(t, tr.ttype&2 == 0, "%s.%s is a BEFORE trigger; a transition table needs AFTER", table, tr.name)
			events |= tr.ttype & (4 | 8 | 16)
		}
		assert.Must(t, events == 4|8|16, "%s's design-version triggers cover events %d, want insert, update and delete (28)",
			table, events)
	}
}

// TestEveryMetamodelWriteBumpsTheDesignVersion is the behavioural half:
// twelve cases, four tables by insert, update and delete, each asserting
// the counter **strictly increased**.
func TestEveryMetamodelWriteBumpsTheDesignVersion(t *testing.T) {
	t.Parallel()

	// Each case is (insert, update, delete) over one table, expressed as
	// statements against a game seeded by seedAnalysisGame. They run in
	// their own database each, so a case cannot be carried by a
	// neighbour's writes.
	cases := []struct {
		table  string
		insert func(g analysisGame) (string, []any)
		update func(g analysisGame) (string, []any)
		del    func(g analysisGame) (string, []any)
	}{
		{
			table: "entity_types",
			insert: func(g analysisGame) (string, []any) {
				return `INSERT INTO entity_types (project_id, key, label, label_plural)
				        VALUES ($1, 'zone', 'Zone', 'Zones')`, []any{g.projectID}
			},
			update: func(g analysisGame) (string, []any) {
				return `UPDATE entity_types SET label = 'Quests!' WHERE id = $1`, []any{g.entityTypeID}
			},
			del: func(g analysisGame) (string, []any) {
				return `DELETE FROM entity_types WHERE project_id = $1 AND key = 'zone'`, []any{g.projectID}
			},
		},
		{
			table: "relation_types",
			insert: func(g analysisGame) (string, []any) {
				return `INSERT INTO relation_types (project_id, key, label) VALUES ($1, 'unlocks', 'Unlocks')`,
					[]any{g.projectID}
			},
			update: func(g analysisGame) (string, []any) {
				return `UPDATE relation_types SET analysis_traits = ARRAY['prerequisite_of']::text[] WHERE id = $1`,
					[]any{g.relationTypeID}
			},
			del: func(g analysisGame) (string, []any) {
				return `DELETE FROM relation_types WHERE project_id = $1 AND key = 'unlocks'`, []any{g.projectID}
			},
		},
		{
			table: "entities",
			insert: func(g analysisGame) (string, []any) {
				return `INSERT INTO entities (project_id, entity_type_id, key, name) VALUES ($1, $2, 'defias', 'Defias')`,
					[]any{g.projectID, g.entityTypeID}
			},
			update: func(g analysisGame) (string, []any) {
				return `UPDATE entities SET name = 'Wanted: Hogger (again)' WHERE id = $1`, []any{g.entityID}
			},
			del: func(g analysisGame) (string, []any) {
				return `DELETE FROM entities WHERE project_id = $1 AND key = 'defias'`, []any{g.projectID}
			},
		},
		{
			table: "relations",
			insert: func(g analysisGame) (string, []any) {
				return `INSERT INTO relations (project_id, relation_type_id, source_id, target_id) VALUES ($1, $2, $3, $4)`,
					[]any{g.projectID, g.relationTypeID, g.entityID, g.otherEntityID}
			},
			update: func(g analysisGame) (string, []any) {
				return `UPDATE relations SET invalid = true WHERE project_id = $1`, []any{g.projectID}
			},
			del: func(g analysisGame) (string, []any) {
				return `DELETE FROM relations WHERE project_id = $1`, []any{g.projectID}
			},
		},
	}

	for _, tc := range cases {
		arms := []struct {
			name string
			stmt func(g analysisGame) (string, []any)
		}{
			{"insert", tc.insert},
			{"update", tc.update},
			{"delete", tc.del},
		}
		for i, arm := range arms {
			t.Run(tc.table+"/"+arm.name, func(t *testing.T) {
				t.Parallel()
				pool := testutil.NewPool(t)
				ctx := context.Background()
				g := seedAnalysisGame(t, ctx, pool, "azeroth")

				// Update and delete need the row the insert makes, so
				// the earlier arms run first as setup and only the arm
				// under test is measured.
				for _, prior := range arms[:i] {
					if prior.name == "update" {
						continue
					}
					sql, args := prior.stmt(g)
					if _, err := pool.Exec(ctx, sql, args...); err != nil {
						t.Fatalf("setup %s on %s: %v", prior.name, tc.table, err)
					}
				}

				before := designVersion(t, ctx, pool, g.projectID)
				sql, args := arm.stmt(g)
				tag, err := pool.Exec(ctx, sql, args...)
				assert.Must(t, err == nil, "%s on %s: %v", arm.name, tc.table, err)
				// A statement that wrote nothing would leave the counter
				// alone and be reported as a trigger failure, which is
				// the wrong diagnosis; assert the write landed first.
				assert.Must(t, tag.RowsAffected() != 0, "%s on %s affected no rows, so this case asserts nothing", arm.name, tc.table)
				after := designVersion(t, ctx, pool, g.projectID)
				assert.Must(t, after > before, "%s on %s left design_version at %d (was %d); the counter did not move",
					arm.name, tc.table, after, before)
			})
		}
	}
}

// TestTheDesignVersionIsMonotonicAndNotACountOfChanges pins the
// coarseness the trigger approach accepts, as behaviour rather than as a
// comment: three rows written by one statement move the counter at least
// once, and nothing promises three.
func TestTheDesignVersionIsMonotonicAndNotACountOfChanges(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	g := seedAnalysisGame(t, ctx, pool, "azeroth")

	before := designVersion(t, ctx, pool, g.projectID)
	tag, err := pool.Exec(ctx,
		`INSERT INTO entities (project_id, entity_type_id, key, name)
		 SELECT $1, $2, 'bulk-' || i, 'Bulk' FROM generate_series(1, 3) AS i`,
		g.projectID, g.entityTypeID)
	assert.Must(t, err == nil, "bulk insert: %v", err)
	assert.Must(t, tag.RowsAffected() == 3, "the bulk insert wrote %d rows, want 3", tag.RowsAffected())
	after := designVersion(t, ctx, pool, g.projectID)
	assert.Must(t, after > before, "design_version did not move over a three-row statement: %d -> %d", before, after)
	if after-before != 1 {
		// Not a failure of the schema, but of this test's own claim: if
		// a statement ever moves the counter more than once, the
		// "per statement" wording above is wrong and the comment has to
		// change with the behaviour.
		t.Fatalf("a single statement moved design_version by %d; the documented granularity is per statement",
			after-before)
	}
}

// TestDeletingAnEntityBumpsTheCounterThroughTheRelationsCascade is the
// case a Go-side bump misses, which is why the counter is a trigger.
func TestDeletingAnEntityBumpsTheCounterThroughTheRelationsCascade(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	g := seedAnalysisGame(t, ctx, pool, "azeroth")

	if _, err := pool.Exec(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id) VALUES ($1, $2, $3, $4)`,
		g.projectID, g.relationTypeID, g.entityID, g.otherEntityID); err != nil {
		t.Fatalf("insert edge: %v", err)
	}

	before := designVersion(t, ctx, pool, g.projectID)
	if _, err := pool.Exec(ctx, `DELETE FROM entities WHERE id = $1`, g.otherEntityID); err != nil {
		t.Fatalf("delete entity: %v", err)
	}
	countRows(t, ctx, pool, `SELECT count(*) FROM relations WHERE project_id = $1`, []any{g.projectID}, 0)

	after := designVersion(t, ctx, pool, g.projectID)
	assert.Must(t, after-before >= 2, "design_version moved by %d over an entity deletion that also removed an edge; "+
		"the cascaded relations delete did not fire its own trigger, which is the case a "+
		"Go-side bump would miss", after-before)
}

// TestAWriteToAnotherGameDoesNotMoveThisGamesDesignVersion is the
// isolation half, and its positive control is what makes it real: a
// trigger that updated *nothing at all* would pass the first assertion
// on its own.
func TestAWriteToAnotherGameDoesNotMoveThisGamesDesignVersion(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	a := seedAnalysisGame(t, ctx, pool, "azeroth")
	b := seedAnalysisGame(t, ctx, pool, "outland")

	beforeA := designVersion(t, ctx, pool, a.projectID)
	beforeB := designVersion(t, ctx, pool, b.projectID)

	if _, err := pool.Exec(ctx,
		`INSERT INTO entities (project_id, entity_type_id, key, name) VALUES ($1, $2, 'defias', 'Defias')`,
		b.projectID, b.entityTypeID); err != nil {
		t.Fatalf("write to the other game: %v", err)
	}

	if got := designVersion(t, ctx, pool, a.projectID); got != beforeA {
		t.Fatalf("a write to another game moved this game's design_version: %d -> %d", beforeA, got)
	}
	if got := designVersion(t, ctx, pool, b.projectID); got <= beforeB {
		t.Fatalf("the written game's own design_version did not move: %d -> %d; "+
			"without this control a trigger that updates nothing passes the assertion above", beforeB, got)
	}
}

// TestTwoConcurrentWritesToOneGameBothLandAndTheCounterMovesTwice is the
// measurement of cost (b) in 0013_analysis.sql: every write to a game now
// updates that game's projects row, so two writers to one game serialise
// on it for the remainder of their transactions.
func TestTwoConcurrentWritesToOneGameBothLandAndTheCounterMovesTwice(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	g := seedAnalysisGame(t, ctx, pool, "azeroth")

	before := designVersion(t, ctx, pool, g.projectID)

	tx1, err := pool.Begin(ctx)
	assert.Must(t, err == nil, "begin tx1: %v", err)
	if _, err := tx1.Exec(ctx,
		`INSERT INTO entities (project_id, entity_type_id, key, name) VALUES ($1, $2, 'first', 'First')`,
		g.projectID, g.entityTypeID); err != nil {
		t.Fatalf("tx1 insert: %v", err)
	}

	var (
		wg       sync.WaitGroup
		blocked  time.Duration
		tx2Err   error
		hold     = 150 * time.Millisecond
		started  = make(chan struct{})
		finished time.Time
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tx2, err := pool.Begin(ctx)
		if err != nil {
			tx2Err = err
			close(started)
			return
		}
		close(started)
		start := time.Now()
		if _, err := tx2.Exec(ctx,
			`INSERT INTO entities (project_id, entity_type_id, key, name) VALUES ($1, $2, 'second', 'Second')`,
			g.projectID, g.entityTypeID); err != nil {
			tx2Err = err
			_ = tx2.Rollback(ctx)
			return
		}
		blocked = time.Since(start)
		finished = time.Now()
		tx2Err = tx2.Commit(ctx)
	}()

	<-started
	time.Sleep(hold)
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("tx1 commit: %v", err)
	}
	wg.Wait()
	assert.Must(t, tx2Err == nil, "tx2: %v", tx2Err)
	_ = finished

	// The serialisation is the point of the measurement: the second
	// writer waited on the first, rather than both proceeding and one
	// bump being lost.
	if blocked < hold/2 {
		t.Logf("the second write did not visibly block (%s); the row lock may not be the "+
			"bottleneck on this machine, which is worth knowing but is not a failure", blocked)
	} else {
		t.Logf("the second write blocked for %s behind a transaction held open for %s", blocked, hold)
	}

	countRows(t, ctx, pool,
		`SELECT count(*) FROM entities WHERE project_id = $1 AND key IN ('first', 'second')`,
		[]any{g.projectID}, 2)
	after := designVersion(t, ctx, pool, g.projectID)
	assert.Must(t, after-before == 2, "design_version moved by %d over two concurrent single-row writes, want 2", after-before)
}

// TestDeletingAGameDoesNotFailOnItsOwnDesignVersionTrigger walks the one
// path where the trigger fires against a projects row the same command
// is deleting: the cascade out of projects into all four watched tables.
// The UPDATE inside bump_design_version then matches nothing and the
// delete proceeds. Asserted rather than assumed, because the failure
// mode -- a game that cannot be deleted -- is one an administrator meets
// and no other test in this repository would reach.
func TestDeletingAGameDoesNotFailOnItsOwnDesignVersionTrigger(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()
	g := seedAnalysisGame(t, ctx, pool, "azeroth")
	other := seedAnalysisGame(t, ctx, pool, "outland")

	if _, err := pool.Exec(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id) VALUES ($1, $2, $3, $4)`,
		g.projectID, g.relationTypeID, g.entityID, g.otherEntityID); err != nil {
		t.Fatalf("insert edge: %v", err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, g.projectID); err != nil {
		t.Fatalf("delete game: %v", err)
	}
	countRows(t, ctx, pool, `SELECT count(*) FROM projects WHERE id = $1`, []any{g.projectID}, 0)
	countRows(t, ctx, pool, `SELECT count(*) FROM entities WHERE project_id = $1`, []any{g.projectID}, 0)
	// The other game is untouched, which is the control that stops this
	// passing because the cascade removed more than it should have.
	countRows(t, ctx, pool, `SELECT count(*) FROM entities WHERE project_id = $1`, []any{other.projectID}, 2)
}

// TestTheReachabilityWalkSeeksAnIndexRatherThanScanning is the
// measurement 0013_analysis.sql's "no relations_project_type_idx" claim
// rests on, run rather than quoted.
func TestTheReachabilityWalkSeeksAnIndexRatherThanScanning(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx := context.Background()

	projectID, entityTypeID, sourceID, _, relTypeID := seedEdgeParents(t, pool)

	// A game, not one type's worth of rows: a walk is selective, and on
	// a table where every row matches the filter Postgres reads
	// sequentially and is right to. Twenty relation types over five
	// hundred entities is what makes the plan below about the indexes
	// rather than about the fixture.
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

	// The normalisation the reachability walk rides in EdgePredicate:
	// forward types followed source to target, reversed types target to
	// source, symmetric types either way. Direction Any, one arm, per
	// internal/graph's own argument for that shape.
	w := graph.Walk{
		Name:            "w",
		ProjectID:       projectID,
		SeedSQL:         `SELECT id FROM entities WHERE project_id = $1 AND lower(key) = lower($2)`,
		SeedArgs:        []any{projectID, "hogger"},
		RelationTypeIDs: []uuid.UUID{relTypeID},
		Direction:       graph.Any,
		EdgePredicate: `((r.relation_type_id = ANY($1::uuid[]) AND r.source_id = w.id)
		                 OR (r.relation_type_id = ANY($2::uuid[]) AND r.target_id = w.id)
		                 OR (r.relation_type_id = ANY($3::uuid[])))`,
		EdgeArgs: []any{
			[]uuid.UUID{relTypeID},
			[]uuid.UUID{},
			[]uuid.UUID{},
		},
		MaxDepth: 8,
		MaxRows:  1000,
	}
	body, args := graph.WalkCTE(w)
	stmt := "EXPLAIN WITH RECURSIVE " + body + "\nSELECT id, depth FROM " + graph.ReadFrom(w)

	rows, err := pool.Query(ctx, stmt, args...)
	assert.Must(t, err == nil, "explain the reachability walk: %v\n%s", err, stmt)
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
	assert.Must(t, plan.Len() != 0, "EXPLAIN returned no plan; this test would assert nothing")
	assert.Must(t, !strings.Contains(plan.String(), "Seq Scan on relations"), "the reachability walk sequentially scans relations; "+
		"0013_analysis.sql's decision not to add relations_project_type_idx rests on it not doing that:\n%s",
		plan.String())
	t.Logf("reachability walk plan:\n%s", plan.String())
}
