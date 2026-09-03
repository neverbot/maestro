package views

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// game is one seeded game plus the services that write to it. Every
// database test in this package builds one; the fixture is the spec's
// MMORPG worked example, because that is the example the definition of
// done names and a fixture nobody uses end to end is a fixture nobody
// checks.
type game struct {
	pool      *pgxpool.Pool
	meta      *metamodel.Service
	views     *Service
	projectID uuid.UUID
}

// newGame seeds classes, quests and zones with the relations the spec's
// §3.1 example walks: available_to (quest -> class), requires
// (quest -> quest), takes_place_in (quest -> zone).
//
// It seeds **two** games, in one database, and returns both. The second
// is not decoration: every isolation test in this package needs a second
// game with the same keys in it, and a fixture that seeds one game lets a
// missing project filter pass every test in the file.
func newGame(t *testing.T) (*game, *game) {
	t.Helper()
	pool := testutil.NewPool(t)
	build := func(slug string) *game {
		ctx := context.Background()
		var projectID uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`, slug).
			Scan(&projectID); err != nil {
			t.Fatalf("insert project %s: %v", slug, err)
		}
		g := &game{
			pool:      pool,
			meta:      metamodel.New(pool, nil),
			views:     New(pool, nil),
			projectID: projectID,
		}
		g.seed(t)
		return g
	}
	return build("azeroth"), build("outland")
}

func (g *game) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	minLevel := metamodel.Field{Key: "min_level", Type: metamodel.FieldNumber}
	tags := metamodel.Field{Key: "tags", Type: metamodel.FieldListText}
	rank := metamodel.Field{Key: "rank", Type: metamodel.FieldEnum,
		Options: []string{"common", "rare", "epic"}}
	// difficulty carries declared bounds, which min_level deliberately does
	// not: TestADeclaredRangeDoesNotRefuseAComparisonOutsideIt needs a
	// field whose range a query can legally ask outside of, and a fixture
	// where no field declares one cannot tell the two rules apart.
	low, high := 1.0, 10.0
	difficulty := metamodel.Field{Key: "difficulty", Type: metamodel.FieldNumber,
		Min: &low, Max: &high}

	for _, spec := range []struct {
		key, label, plural string
		schema             metamodel.Schema
	}{
		{"class", "Class", "Classes", nil},
		{"zone", "Zone", "Zones", nil},
		{"quest", "Quest", "Quests", metamodel.Schema{minLevel, tags, rank, difficulty}},
	} {
		if _, err := g.meta.UpsertEntityType(ctx, g.projectID, metamodel.EntityTypeInput{
			Key: spec.key, Label: spec.label, LabelPlural: spec.plural, Schema: spec.schema,
		}); err != nil {
			t.Fatalf("seed type %s: %v", spec.key, err)
		}
	}
	for _, spec := range []struct{ key, label string }{
		{"available_to", "Available to"}, {"requires", "Requires"},
		{"takes_place_in", "Takes place in"},
	} {
		if _, err := g.meta.UpsertRelationType(ctx, g.projectID, metamodel.RelationTypeInput{
			Key: spec.key, Label: spec.label,
		}); err != nil {
			t.Fatalf("seed relation type %s: %v", spec.key, err)
		}
	}
	g.entity(t, "class", "mage", "Mage", nil)
	g.entity(t, "zone", "elwynn", "Elwynn Forest", nil)
	g.entity(t, "zone", "westfall", "Westfall", nil)
	g.entity(t, "quest", "hogger", "Wanted: Hogger",
		map[string]any{"min_level": 22, "rank": "rare", "tags": []any{"kill", "elite"}})
	g.entity(t, "quest", "defias", "The Defias Brotherhood",
		map[string]any{"min_level": 28, "rank": "epic", "tags": []any{"chain"}})
	g.entity(t, "quest", "cook", "Cooking for Bruises",
		map[string]any{"min_level": 12, "rank": "common", "tags": []any{}})
	g.relate(t, "available_to", "quest", "hogger", "class", "mage")
	g.relate(t, "available_to", "quest", "defias", "class", "mage")
	g.relate(t, "available_to", "quest", "cook", "class", "mage")
	g.relate(t, "requires", "quest", "defias", "quest", "hogger")
	g.relate(t, "takes_place_in", "quest", "hogger", "zone", "elwynn")
	g.relate(t, "takes_place_in", "quest", "defias", "zone", "westfall")
}

func (g *game) entity(t *testing.T, typeKey, key, name string, fields map[string]any) uuid.UUID {
	t.Helper()
	row, err := g.meta.UpsertEntity(context.Background(), g.projectID, metamodel.EntityInput{
		TypeKey: typeKey, Key: key, Name: name, Fields: fields,
	})
	if err != nil {
		t.Fatalf("seed entity %s/%s: %v", typeKey, key, err)
	}
	return row.ID
}

func (g *game) relate(t *testing.T, relKey, srcType, srcKey, tgtType, tgtKey string) {
	t.Helper()
	_, err := g.meta.UpsertRelation(context.Background(), g.projectID, metamodel.RelationInput{
		TypeKey: relKey,
		Source:  metamodel.Ref{TypeKey: srcType, Key: srcKey},
		Target:  metamodel.Ref{TypeKey: tgtType, Key: tgtKey},
	})
	if err != nil {
		t.Fatalf("seed relation %s: %v", relKey, err)
	}
}

// mustParse parses a query that the test asserts is well-formed. A test
// that means to exercise resolution must not be silently exercising
// ParseQuery instead.
func mustParse(t *testing.T, doc string) *Query {
	t.Helper()
	q, err := ParseQuery([]byte(doc))
	if err != nil {
		t.Fatalf("this query must parse; the test means to exercise resolution: %v", err)
	}
	return q
}
