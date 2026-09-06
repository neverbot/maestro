package analysis

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// game is one throwaway database, one project in it, and the two
// services these tests drive: this package's, and the metamodel's, which
// is how a fixture declares the relation types the resolver then reads.
type game struct {
	pool      *pgxpool.Pool
	analysis  *Service
	meta      *metamodel.Service
	projectID uuid.UUID
}

// newGame builds that fixture. The hub is nil: nothing in this package's
// own tests subscribes, and internal/web is where publication is
// asserted end to end.
func newGame(t *testing.T) game {
	t.Helper()
	pool := testutil.NewPool(t)
	slug := "azeroth-" + uuid.NewString()[:8]
	var projectID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`, slug).
		Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return game{
		pool:      pool,
		analysis:  New(pool, nil),
		meta:      metamodel.New(pool, nil),
		projectID: projectID,
	}
}

// declareRelationType declares one relation type with the traits and the
// role a test is about. Either may be empty: a type with neither is the
// undeclared case, which several tests need as a control.
func (g game) declareRelationType(t *testing.T, key, role string, traits []string) uuid.UUID {
	t.Helper()
	// A redeclaration of a type that already exists carries its current
	// version, because an upsert here is a compare-and-set. A fixture
	// that could not redeclare a type would not be able to write the
	// control half of TestAnAnnotationTypeIsNotFollowedAtAll, which is
	// the same type read once as inert and once as a gate.
	in := metamodel.RelationTypeInput{
		Key: key, Label: key, SemanticRole: role, AnalysisTraits: traits,
	}
	if current, err := g.meta.RelationTypeByKey(context.Background(), g.projectID, key); err == nil {
		version := current.Version
		in.ExpectedVersion = &version
	}
	row, err := g.meta.UpsertRelationType(context.Background(), g.projectID, in)
	if err != nil {
		t.Fatalf("declare relation type %q: %v", key, err)
	}
	return row.ID
}

// readSourceFile reads one of this package's own files, for the tests
// whose subject is an argument recorded in a comment rather than a value
// a call returns. A decision with no argument beside it is one the next
// task makes differently, so "the argument is there" is worth a test in
// exactly the places the plan spent a paragraph on the count of
// something.
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// declareEntityType, entity and edge are the three writes a reachability
// fixture makes. They go through internal/metamodel rather than raw SQL
// on purpose: a fixture that inserted rows the product's own writer
// would have refused is a fixture testing a game that cannot exist.
func (g game) declareEntityType(t *testing.T, key string) uuid.UUID {
	t.Helper()
	row, err := g.meta.UpsertEntityType(context.Background(), g.projectID,
		metamodel.EntityTypeInput{Key: key, Label: key, LabelPlural: key + "s"})
	if err != nil {
		t.Fatalf("declare entity type %q: %v", key, err)
	}
	return row.ID
}

func (g game) entity(t *testing.T, typeKey, key string) uuid.UUID {
	t.Helper()
	row, err := g.meta.UpsertEntity(context.Background(), g.projectID,
		metamodel.EntityInput{TypeKey: typeKey, Key: key, Name: key})
	if err != nil {
		t.Fatalf("write entity %s/%s: %v", typeKey, key, err)
	}
	return row.ID
}

// edge writes one relation between two entities of one type, addressed
// the way the product addresses them.
func (g game) edge(t *testing.T, relTypeKey, typeKey, source, target string) uuid.UUID {
	t.Helper()
	row, err := g.meta.UpsertRelation(context.Background(), g.projectID,
		metamodel.RelationInput{
			TypeKey: relTypeKey,
			Source:  metamodel.Ref{TypeKey: typeKey, Key: source},
			Target:  metamodel.Ref{TypeKey: typeKey, Key: target},
		})
	if err != nil {
		t.Fatalf("write edge %s: %s -> %s: %v", relTypeKey, source, target, err)
	}
	return row.ID
}

// semantics resolves the game's relation types the way every analysis
// does, so no test builds a Semantics by hand and thereby tests a
// reading the resolver would never produce.
func (g game) semantics(t *testing.T) Semantics {
	t.Helper()
	sem, err := g.analysis.Resolve(context.Background(), g.projectID, ResolveInput{})
	if err != nil {
		t.Fatalf("resolve the game's semantics: %v", err)
	}
	return sem
}

// reach runs one closure over the game, with the semantics the resolver
// produced.
func (g game) reach(t *testing.T, p Params) Reach {
	t.Helper()
	p.ProjectID = g.projectID
	if p.Semantics.ByType == nil {
		p.Semantics = g.semantics(t)
	}
	got, err := g.analysis.Reach(context.Background(), p)
	if err != nil {
		t.Fatalf("reach: %v", err)
	}
	return got
}

// reached says whether one entity, addressed by key, is in a closure.
func (g game) reached(t *testing.T, r Reach, typeKey, key string) bool {
	t.Helper()
	row, err := g.meta.EntityByKey(context.Background(), g.projectID, typeKey, key)
	if err != nil {
		t.Fatalf("read back %s/%s: %v", typeKey, key, err)
	}
	return r.Reached[row.ID]
}

func boolPtr(v bool) *bool { return &v }

// sibling is a second game in the **same** database, which is what every
// isolation test in this package needs: testutil.NewPool builds one
// throwaway database per call, so two newGame fixtures could never leak
// into each other and a test built on them would assert nothing.
func (g game) sibling(t *testing.T) game {
	t.Helper()
	slug := "outland-" + uuid.NewString()[:8]
	var projectID uuid.UUID
	if err := g.pool.QueryRow(context.Background(),
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`, slug).
		Scan(&projectID); err != nil {
		t.Fatalf("create the second game: %v", err)
	}
	return game{pool: g.pool, analysis: g.analysis, meta: g.meta, projectID: projectID}
}

// invalidate marks every edge of a relation type invalid the way the
// product does: by adding a required field to the type's schema, which
// is what makes an existing row stop fitting its declaration. Forging
// `invalid = true` by hand would test a state the writer cannot produce.
func (g game) invalidate(t *testing.T, relTypeKey string) {
	t.Helper()
	current, err := g.meta.RelationTypeByKey(context.Background(), g.projectID, relTypeKey)
	if err != nil {
		t.Fatalf("read back %q: %v", relTypeKey, err)
	}
	version := current.Version
	_, err = g.meta.UpsertRelationType(context.Background(), g.projectID,
		metamodel.RelationTypeInput{
			// The traits are carried over: an upsert replaces the row,
			// so redeclaring without them would clear the column and the
			// type would stop being a gate -- which is a different
			// fixture from the one this helper claims to build.
			Key: relTypeKey, Label: relTypeKey, ExpectedVersion: &version,
			AnalysisTraits: current.AnalysisTraits,
			Schema:         metamodel.Schema{{Key: "difficulty", Type: metamodel.FieldText, Required: true}},
		})
	if err != nil {
		t.Fatalf("add a required field to %q: %v", relTypeKey, err)
	}
	var invalid int
	if err := g.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM relations r JOIN relation_types rt ON rt.id = r.relation_type_id
		 WHERE r.project_id = $1 AND lower(rt.key) = lower($2) AND r.invalid`,
		g.projectID, relTypeKey).Scan(&invalid); err != nil {
		t.Fatalf("count the invalidated edges: %v", err)
	}
	if invalid == 0 {
		t.Fatalf("no edge of %q came back invalid, so the fixture is not testing what it says",
			relTypeKey)
	}
}

// route writes one route with the given steps, **through the product's
// own writer**.
//
// It used to insert the two tables by hand, because the route CRUD was a
// later task. It is not any more, and a fixture that wrote rows the
// product's own writer would have refused is a fixture testing a game
// that cannot exist -- the same reason declareEntityType, entity and
// edge all go through internal/metamodel. Task 5's seed-route tests
// therefore now run against routes an agent could actually have
// authored.
func (g game) route(t *testing.T, key string, steps []SeedRef) {
	t.Helper()
	in := RouteInput{Key: key, Name: key, ExpectedVersion: new(int32)}
	for _, step := range steps {
		in.Steps = append(in.Steps, RouteStepInput{EntityType: step.EntityType, Key: step.Key})
	}
	if _, err := g.analysis.UpsertRoute(context.Background(), g.projectID, in); err != nil {
		t.Fatalf("write route %q: %v", key, err)
	}
}
