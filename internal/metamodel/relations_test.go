package metamodel_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
)

// withVersion is one edge upsert re-aimed at the version a previous write
// landed on. Since 0009 an edge carries `version` and its upsert is a
// compare-and-set, so a test that rewrites the same triple twice has to
// say which revision it is rewriting — exactly as an entity test does.
// It copies rather than mutating, so a shared RelationInput literal keeps
// meaning what its declaration says.
func withVersion(in metamodel.RelationInput, version int32) metamodel.RelationInput {
	in.ExpectedVersion = &version
	return in
}

// seedWorld declares Quest, Zone and Class types plus a few entities of
// each. Every entity goes in through the service, not through
// insertEntity: a row written straight to the table has a NULL search
// vector (Task 4's correction 11), and these tests read edges back
// through the service too.
func seedWorld(t *testing.T, svc *metamodel.Service, project uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	for _, spec := range []struct{ key, label, plural string }{
		{"quest", "Quest", "Quests"},
		{"zone", "Zone", "Zones"},
		{"class", "Class", "Classes"},
	} {
		if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: spec.key, Label: spec.label, LabelPlural: spec.plural,
		}); err != nil {
			t.Fatalf("seed type %s: %v", spec.key, err)
		}
	}
	for _, spec := range []struct{ typeKey, key, name string }{
		{"quest", "hogger", "Wanted: Hogger"},
		{"quest", "kobold-camp", "Kobold Camp"},
		{"zone", "elwynn", "Elwynn Forest"},
		{"class", "mage", "Mage"},
	} {
		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: spec.typeKey, Key: spec.key, Name: spec.name,
		}); err != nil {
			t.Fatalf("seed entity %s: %v", spec.key, err)
		}
	}
}

func TestRelationEndpointsAreCheckedAgainstTheirType(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeKeys: []string{"quest"},
		TargetTypeKeys: []string{"zone"},
		SemanticRole:   "spatial",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "takes_place_in",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
	}); err != nil {
		t.Fatalf("valid relation: %v", err)
	}

	// A Class is not an allowed source for this relation type.
	_, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "takes_place_in",
		Source:  metamodel.Ref{TypeKey: "class", Key: "mage"},
		Target:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
	})
	if !errors.Is(err, metamodel.ErrEndpointTypeMismatch) {
		t.Fatalf("err = %v, want ErrEndpointTypeMismatch", err)
	}
	// The message names the endpoint that is wrong, the type that was
	// offered and the relation type that refused it: without all three a
	// seeding agent with two bad endpoints cannot tell which one to fix.
	if want := `endpoint_type_mismatch: source: entity type "class" cannot be the source of relation type "takes_place_in"`; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}

	// And the target side is checked too, and named as the target.
	_, err = svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "takes_place_in",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "class", Key: "mage"},
	})
	if want := `endpoint_type_mismatch: target: entity type "class" cannot be the target of relation type "takes_place_in"`; err == nil || err.Error() != want {
		t.Fatalf("message = %v, want %q", err, want)
	}
}

// TestAnEmptyEndpointListAcceptsAnyType pins the other half of the
// endpoint rule: empty means "any type", so a relation type that
// declares no lists is not a relation type nothing can instance.
func TestAnEmptyEndpointListAcceptsAnyType(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "relates_to", Label: "relates to",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	for _, pair := range [][2]metamodel.Ref{
		{{TypeKey: "quest", Key: "hogger"}, {TypeKey: "zone", Key: "elwynn"}},
		{{TypeKey: "class", Key: "mage"}, {TypeKey: "quest", Key: "hogger"}},
	} {
		if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
			TypeKey: "relates_to", Source: pair[0], Target: pair[1],
		}); err != nil {
			t.Fatalf("edge %v: %v", pair, err)
		}
	}
}

func TestRelationCarriesItsOwnFields(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	// A metroidvania door: the condition belongs to the edge, not to a room.
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "connects_to", Label: "connects to", SemanticRole: "spatial",
		Schema: metamodel.Schema{{Key: "requires_ability", Type: metamodel.FieldText}},
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	rel, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "connects_to",
		Source:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Fields:  map[string]any{"requires_ability": "mothwing_cloak"},
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(rel.Fields, &stored); err != nil {
		t.Fatalf("decode stored fields %s: %v", rel.Fields, err)
	}
	if stored["requires_ability"] != "mothwing_cloak" {
		t.Fatalf("stored fields = %v, want requires_ability mothwing_cloak", stored)
	}

	_, err = svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "connects_to",
		Source:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Fields:  map[string]any{"requires_ability": 42},
	})
	if !errors.Is(err, metamodel.ErrSchemaViolation) {
		t.Fatalf("err = %v, want ErrSchemaViolation on a bad edge field", err)
	}
	requireFieldError(t, err, "fields.requires_ability", "expected text, got int")
}

func TestRelationUpsertIsIdempotent(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
		Schema: metamodel.Schema{{Key: "note", Type: metamodel.FieldText}},
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	in := metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
	}
	first, err := svc.UpsertRelation(ctx, project, in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("a created edge is version %d, want 1", first.Version)
	}

	// **Idempotent by address, guarded by version** — exactly as an entity
	// is. A second write of the same triple updates the same row rather
	// than laying a second one beside it, and it has to claim the version
	// it is updating: an edge carries `version` since 0009, and a blind
	// re-write of a row somebody else may have edited is the lost update
	// that column exists to refuse.
	if _, err := svc.UpsertRelation(ctx, project, in); !errors.Is(err, metamodel.ErrVersionConflict) {
		t.Fatalf("a re-seed with no expected version: err = %v, want ErrVersionConflict", err)
	}
	second, err := svc.UpsertRelation(ctx, project, withVersion(in, first.Version))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.ID != second.ID {
		t.Fatal("re-seeding an edge created a duplicate")
	}
	if second.Version != 2 {
		t.Fatalf("a rewritten edge is version %d, want 2", second.Version)
	}

	third, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: in.TypeKey, Source: in.Source, Target: in.Target,
		Fields: map[string]any{"note": "rewritten"}, ExpectedVersion: &second.Version,
	})
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	if third.ID != first.ID {
		t.Fatal("rewriting an edge's fields created a duplicate")
	}
	var stored map[string]any
	if err := json.Unmarshal(third.Fields, &stored); err != nil {
		t.Fatalf("decode stored fields: %v", err)
	}
	if stored["note"] != "rewritten" {
		t.Fatalf("stored fields = %v, want the last writer's note", stored)
	}
}

// TestAnEdgeMayJoinAnEntityToItself pins that self-loops are allowed.
// "This zone connects to itself" is unusual; "this championship counts
// towards itself" is a modelling mistake a designer should be able to
// make and then see, and Maestro is not the arbiter of a game's graph.
func TestAnEdgeMayJoinAnEntityToItself(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "connects_to", Label: "connects to",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	self := metamodel.Ref{TypeKey: "zone", Key: "elwynn"}
	row, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "connects_to", Source: self, Target: self,
	})
	if err != nil {
		t.Fatalf("a self-loop must be allowed: %v", err)
	}
	if row.SourceID != row.TargetID {
		t.Fatalf("SourceID %v != TargetID %v on a self-loop", row.SourceID, row.TargetID)
	}
}

func TestPrerequisiteCyclesAreAllowed(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires", SemanticRole: "prerequisite",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	// A prerequisite cycle is a design mistake to surface later, not a write
	// error to block here.
	for _, pair := range [][2]string{{"hogger", "kobold-camp"}, {"kobold-camp", "hogger"}} {
		if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
			TypeKey: "requires",
			Source:  metamodel.Ref{TypeKey: "quest", Key: pair[0]},
			Target:  metamodel.Ref{TypeKey: "quest", Key: pair[1]},
		}); err != nil {
			t.Fatalf("edge %v: %v", pair, err)
		}
	}
}

// TestEachMissingPieceOfAnEdgeIsNamed covers the failure modes an edge
// has and an entity does not: three parents, any of which may be absent.
// A bare not_found would leave a seeding agent unable to tell which of
// the three to go and create.
func TestEachMissingPieceOfAnEdgeIsNamed(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	hogger := metamodel.Ref{TypeKey: "quest", Key: "hogger"}

	for _, tc := range []struct {
		name string
		in   metamodel.RelationInput
		want string
	}{
		{
			name: "unknown relation type",
			in:   metamodel.RelationInput{TypeKey: "nosuch", Source: hogger, Target: hogger},
			want: `not_found: no relation type "nosuch" in this game`,
		},
		{
			name: "unknown source entity type",
			in: metamodel.RelationInput{TypeKey: "requires", Target: hogger,
				Source: metamodel.Ref{TypeKey: "nosuch", Key: "hogger"}},
			want: `not_found: source: no entity type "nosuch" in this game`,
		},
		{
			name: "unknown source entity",
			in: metamodel.RelationInput{TypeKey: "requires", Target: hogger,
				Source: metamodel.Ref{TypeKey: "quest", Key: "nosuch"}},
			want: `not_found: source: no entity "nosuch" of type "quest" in this game`,
		},
		{
			name: "unknown target entity",
			in: metamodel.RelationInput{TypeKey: "requires", Source: hogger,
				Target: metamodel.Ref{TypeKey: "zone", Key: "nosuch"}},
			want: `not_found: target: no entity "nosuch" of type "zone" in this game`,
		},
		{
			// Both ends in one answer, as checkEndpointTypes reports both
			// endpoint lists in one pass: an agent with two bad endpoints
			// otherwise fixes one, resends, and is told about the other.
			name: "both endpoints missing",
			in: metamodel.RelationInput{TypeKey: "requires",
				Source: metamodel.Ref{TypeKey: "nosuch", Key: "hogger"},
				Target: metamodel.Ref{TypeKey: "quest", Key: "nosuch"}},
			want: `not_found: source: no entity type "nosuch" in this game; ` +
				`target: no entity "nosuch" of type "quest" in this game`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.UpsertRelation(ctx, project, tc.in)
			if !errors.Is(err, metamodel.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			if err.Error() != tc.want {
				t.Fatalf("message = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

// TestAnEndpointInAnotherGameIsNotFound is the isolation case an edge
// adds: both endpoints are resolved within the project, so an entity of
// another game reads as absent rather than as somebody else's row. The
// composite foreign keys are the backstop; this pins that the resolution
// never gets that far.
func TestAnEndpointInAnotherGameIsNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, pool), newProject(t, pool)
	seedWorld(t, svc, mine)

	// Only the other game has a "raid".
	if _, err := svc.UpsertEntityType(ctx, theirs, metamodel.EntityTypeInput{
		Key: "raid", Label: "Raid", LabelPlural: "Raids",
	}); err != nil {
		t.Fatalf("their type: %v", err)
	}
	if _, err := svc.UpsertEntity(ctx, theirs, metamodel.EntityInput{
		TypeKey: "raid", Key: "molten-core", Name: "Molten Core",
	}); err != nil {
		t.Fatalf("their entity: %v", err)
	}
	if _, err := svc.UpsertRelationType(ctx, mine, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	_, err := svc.UpsertRelation(ctx, mine, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "raid", Key: "molten-core"},
	})
	if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if want := `not_found: target: no entity type "raid" in this game`; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}

// TestARelationTypeEndpointListMustNameTypesOfThisGame pins that the
// endpoint lists are checked when they are declared. They are plain
// uuid[] columns with no foreign key of their own, so an id belonging to
// another game — or to nothing at all — would otherwise be stored and
// silently match no entity ever, leaving a relation type nothing can
// instance and a refusal ("cannot be the source of") that names the
// wrong problem.
func TestARelationTypeEndpointListMustNameTypesOfThisGame(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, pool), newProject(t, pool)
	seedWorld(t, svc, mine)

	foreign, err := svc.UpsertEntityType(ctx, theirs, metamodel.EntityTypeInput{
		Key: "raid", Label: "Raid", LabelPlural: "Raids",
	})
	if err != nil {
		t.Fatalf("their type: %v", err)
	}
	_, err = svc.UpsertRelationType(ctx, mine, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeKeys: []string{"quest"},
		TargetTypeKeys: []string{foreign.Key},
	})
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	requireFieldError(t, err, "target_type_keys[0]",
		"names no entity type of this game: "+foreign.Key)

	if _, err := svc.RelationTypeByKey(ctx, mine, "takes_place_in"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("the refused declaration must store nothing, got %v", err)
	}
}

// TestALockTimeoutOnTheEndpointCheckIsNotReportedAsInvalidInput closes
// the locking verification's finding 1.
//
// checkEndpointTypes now takes a FOR SHARE lock on every endpoint id it
// finds (correction 22,
// TestARelationTypeCreatedDuringATypeRemovalCannotKeepTheRemovedID
// above), and that lock can be parked behind another transaction's row
// lock and cancelled by lock_timeout — SQLSTATE 55P03. Before this fix,
// any error from the lock query, cancellation included, was folded into
// a FieldError and reported as invalid_input: a caller told its ids were
// wrong when nothing about them was ever checked, and told not to resend
// unchanged when resending unchanged is exactly the right recovery for
// contention. This test holds a real row lock, gives the upsert's
// connection a real lock_timeout, and proves the cancellation reaches
// the caller as an ordinary Go error carrying the SQLSTATE — not a
// ValidationError, and not ErrInvalidInput.
func TestALockTimeoutOnTheEndpointCheckIsNotReportedAsInvalidInput(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	zone, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "zone", Label: "Zone", LabelPlural: "Zones",
	})
	if err != nil {
		t.Fatalf("seed zone type: %v", err)
	}

	// A pool of the test's own, every connection given a short
	// lock_timeout on the session Postgres itself starts it with — the
	// same knob an operator sets in postgresql.conf, so this proves the
	// exact deployment shape finding 1 describes rather than a synthetic
	// stand-in for it.
	cfg, err := pgxpool.ParseConfig(pool.Config().ConnString())
	if err != nil {
		t.Fatalf("parse pool config: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET lock_timeout = '300ms'")
		return err
	}
	timeoutPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open lock_timeout pool: %v", err)
	}
	defer timeoutPool.Close()
	timeoutSvc := metamodel.New(timeoutPool, nil)

	// Hold a conflicting lock on zone's own row, uncommitted, from a
	// connection outside every pool under test.
	blocker := standaloneConn(t, pool)
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocking transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT id FROM entity_types WHERE id = $1 FOR UPDATE`, zone.ID); err != nil {
		t.Fatalf("hold the zone row: %v", err)
	}

	// The FOR SHARE in checkEndpointTypes blocks on FOR UPDATE above and
	// is cancelled by this connection's own lock_timeout; the call
	// returns on its own, no polling or second goroutine required.
	_, err = timeoutSvc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
		TargetTypeKeys: []string{"zone"},
	})
	if err == nil {
		t.Fatal("UpsertRelationType succeeded; the endpoint lock did not block it")
	}

	var validation *metamodel.ValidationError
	if errors.As(err, &validation) {
		t.Fatalf("a lock timeout was reported as a ValidationError (code %q): %v", validation.Code, err)
	}
	if errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("a lock timeout was reported as ErrInvalidInput: %v", err)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("the SQLSTATE did not survive: got %v", err)
	}
	if pgErr.Code != "55P03" {
		t.Fatalf("pgErr.Code = %q, want 55P03 (lock_timeout)", pgErr.Code)
	}

	// Task 7's third decision, proved on a real lock timeout rather than
	// on a hand-built PgError: contention is retryable, so the surface
	// above this package can tell an agent to resend the same call
	// instead of reporting it broken.
	// TestIsRetryableNamesTheFourContentionStatesAndNothingElse covers
	// the classification; this covers the wiring that carries the
	// SQLSTATE out to it through UpsertRelationType's own wrapping.
	if !metamodel.IsRetryable(err) {
		t.Fatalf("a lock timeout must be retryable, got %v", err)
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("release the held row: %v", err)
	}
}

// TestUpsertRelationTypeRefusesARespelledKeyAndAMalformedOne is Task 3's
// corrections 15, 23 and 25 for relation types: a respelling is refused
// after the write as well as before it, a key or label problem is
// invalid_input and not schema_violation, and both are reported in one
// pass.
func TestUpsertRelationTypeRefusesARespelledKeyAndAMalformedOne(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	_, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{Key: "takes place in"})
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) || len(invalid.Fields) != 2 {
		t.Fatalf("want the key and the label reported together, got %v", err)
	}

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "Takes_Place_In", Label: "takes place in",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "MINE", ExpectedVersion: ptrInt32(1),
	})
	requireFieldError(t, err, "key",
		`"takes_place_in" already exists here spelled "Takes_Place_In", and keys are matched `+
			`without regard to case: use "Takes_Place_In" to update it, or pick a key that `+
			`differs by more than capitalisation`)
}

// TestUpsertRelationTypeRejectsStaleVersion is correction 4 for relation
// types: the DO UPDATE is guarded, so a blind re-declaration of a type
// somebody else has edited is refused rather than landing.
func TestUpsertRelationTypeRejectsStaleVersion(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// No ExpectedVersion at all is the blind overwrite, and is refused
	// too.
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "renamed",
	}); !errors.Is(err, metamodel.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "renamed", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("the matching version must be accepted: %v", err)
	}
}

// TestTheReportedCurrentRelationTypeVersionIsTheOneTheWriteWouldHaveMet
// is the relation-type twin of the entity-type test of the same shape,
// and the only thing FOR UPDATE on GetRelationTypeByKeyForUpdate buys:
// the guarded DO UPDATE refuses every lost update on its own, but
// without the lock the number the caller is told to merge onto comes
// from its own stale snapshot, and it retries into the same refusal
// forever.
func TestTheReportedCurrentRelationTypeVersionIsTheOneTheWriteWouldHaveMet(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A rival holds the row's lock and has already advanced it to 2.
	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := rival.Exec(ctx,
		`UPDATE relation_types SET version = version + 1 WHERE project_id = $1 AND key = 'requires'`,
		project); err != nil {
		t.Fatalf("rival update: %v", err)
	}

	// No ExpectedVersion, so the refusal is decided by the locked read
	// alone: the guarded DO UPDATE never runs, and the number reported is
	// the read's answer rather than the guard's.
	done := make(chan error, 1)
	go func() {
		_, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
			Key: "requires", Label: "renamed",
		})
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("the upsert returned %v without waiting for the rival's row lock", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := rival.Commit(ctx); err != nil {
		t.Fatalf("rival commit: %v", err)
	}

	select {
	case err := <-done:
		var conflict *metamodel.VersionConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want a *metamodel.VersionConflictError", err)
		}
		if conflict.Current != 2 {
			t.Fatalf("Current = %d, want 2: the caller must be told the version its own write "+
				"would have met, not the one visible before the rival committed", conflict.Current)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival committed")
	}
}

func TestDeletingAnEntityDeletesItsRelations(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{Key: "requires", Label: "requires"}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
	}); err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	if err := svc.RemoveEntity(ctx, project, "quest", "hogger"); err != nil {
		t.Fatalf("RemoveEntity: %v", err)
	}

	rels, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(rels.Relations) != 0 {
		t.Fatalf("%d relations survived their entity", len(rels.Relations))
	}
}

// TestRemoveRelationTypeRefusesWhileItHasEdges covers both arms of the
// deletion rule the design spec states: a type with live relations is
// refused without cascade, and takes its edges with it when cascade is
// asked for.
func TestRemoveRelationTypeRefusesWhileItHasEdges(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	typ, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	})
	if err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
	}); err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	if err := svc.RemoveRelationType(ctx, project, typ.ID, false); !errors.Is(err, metamodel.ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse", err)
	}
	if _, err := svc.RelationTypeByKey(ctx, project, "requires"); err != nil {
		t.Fatalf("the refused removal must leave the type: %v", err)
	}

	if err := svc.RemoveRelationType(ctx, project, typ.ID, true); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	rels, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(rels.Relations) != 0 {
		t.Fatalf("%d relations survived their cascaded type", len(rels.Relations))
	}
}

// TestRelationsAreScopedToTheirProject pins the isolation of the
// statements that address one relation, and of the listing.
func TestRelationsAreScopedToTheirProject(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, pool), newProject(t, pool)
	seedWorld(t, svc, mine)

	if _, err := svc.UpsertRelationType(ctx, mine, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	if _, err := svc.UpsertRelation(ctx, mine, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
	}); err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	rels, err := svc.ListRelations(ctx, theirs, metamodel.RelationFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(rels.Relations) != 0 {
		t.Fatalf("another game's listing returned %d edges", len(rels.Relations))
	}
	// Knowing the address is not enough: the removal filters on the
	// project too, and the other game does not even hold the type.
	kobold := metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"}
	hogger := metamodel.Ref{TypeKey: "quest", Key: "hogger"}
	if err := svc.RemoveRelation(ctx, theirs, "requires", kobold, hogger); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := svc.RemoveRelation(ctx, mine, "requires", kobold, hogger); err != nil {
		t.Fatalf("the owning game must be able to remove it: %v", err)
	}
}

// TestRelationTypesAreScopedToTheirProject is the same pin for the
// relation-type statements that address a row by its id.
func TestRelationTypesAreScopedToTheirProject(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, pool), newProject(t, pool)

	typ, err := svc.UpsertRelationType(ctx, mine, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	})
	if err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	if err := svc.RemoveRelationType(ctx, theirs, typ.ID, false); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := svc.RelationTypeByKey(ctx, theirs, "requires"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	types, err := svc.ListRelationTypes(ctx, theirs)
	if err != nil {
		t.Fatalf("ListRelationTypes: %v", err)
	}
	if len(types) != 0 {
		t.Fatalf("another game's listing returned %d relation types", len(types))
	}

	// Both games may use the key, and each sees only its own.
	if _, err := svc.UpsertRelationType(ctx, theirs, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("the other game must be free to use the same key: %v", err)
	}
}

// TestARelationActorFromAnotherGameIsNamed covers the database's own
// backstop on relations and relation types, as its entity twin does.
func TestARelationActorFromAnotherGameIsNamed(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, pool), newProject(t, pool)
	seedWorld(t, svc, mine)

	foreign := newToken(t, pool, theirs)
	_, err := svc.UpsertRelationType(ctx, mine, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires", Actor: metamodel.Actor{TokenID: &foreign},
	})
	if !errors.Is(err, metamodel.ErrActorNotInGame) {
		t.Fatalf("relation type: err = %v, want ErrActorNotInGame", err)
	}

	if _, err := svc.UpsertRelationType(ctx, mine, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	_, err = svc.UpsertRelation(ctx, mine, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Actor:   metamodel.Actor{TokenID: &foreign},
	})
	if !errors.Is(err, metamodel.ErrActorNotInGame) {
		t.Fatalf("relation: err = %v, want ErrActorNotInGame", err)
	}
	rels, err := svc.ListRelations(ctx, mine, metamodel.RelationFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(rels.Relations) != 0 {
		t.Fatalf("the refused write stored %d edges", len(rels.Relations))
	}

	// The same token against its own game is an ordinary write, so the
	// mapping cannot be refusing token actors in general.
	own := newToken(t, pool, mine)
	row, err := svc.UpsertRelation(ctx, mine, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Actor:   metamodel.Actor{TokenID: &own},
	})
	if err != nil {
		t.Fatalf("a token writing to its own game: %v", err)
	}
	if row.UpdatedByTokenID == nil || *row.UpdatedByTokenID != own {
		t.Fatalf("UpdatedByTokenID = %v, want %v", row.UpdatedByTokenID, own)
	}
}

// TestListRelationsFiltersByTypeAndEndpoint pins the filters the listing
// offers, each on its own: a filter silently ignored would hand a caller
// every edge in the game and look like a correct answer.
func TestListRelationsFiltersByTypeAndEndpoint(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	for _, key := range []string{"requires", "connects_to"} {
		if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
			Key: key, Label: key,
		}); err != nil {
			t.Fatalf("UpsertRelationType %s: %v", key, err)
		}
	}
	hogger, err := svc.EntityByKey(ctx, project, "quest", "hogger")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	kobold, err := svc.EntityByKey(ctx, project, "quest", "kobold-camp")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	for _, spec := range []struct {
		typeKey      string
		source, dest metamodel.Ref
	}{
		{"requires", metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"}, metamodel.Ref{TypeKey: "quest", Key: "hogger"}},
		{"connects_to", metamodel.Ref{TypeKey: "zone", Key: "elwynn"}, metamodel.Ref{TypeKey: "quest", Key: "hogger"}},
		{"connects_to", metamodel.Ref{TypeKey: "class", Key: "mage"}, metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"}},
	} {
		if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
			TypeKey: spec.typeKey, Source: spec.source, Target: spec.dest,
		}); err != nil {
			t.Fatalf("edge %s: %v", spec.typeKey, err)
		}
	}

	for _, tc := range []struct {
		name   string
		filter metamodel.RelationFilter
		want   int
	}{
		{"unfiltered", metamodel.RelationFilter{}, 3},
		{"by type", metamodel.RelationFilter{TypeKey: "connects_to"}, 2},
		{"by source", metamodel.RelationFilter{
			Source: &metamodel.Ref{TypeKey: "quest", Key: kobold.Key}}, 1},
		{"by target", metamodel.RelationFilter{
			Target: &metamodel.Ref{TypeKey: "quest", Key: hogger.Key}}, 2},
		{"by type and target", metamodel.RelationFilter{TypeKey: "connects_to",
			Target: &metamodel.Ref{TypeKey: "quest", Key: hogger.Key}}, 1},
		// A limit the caller chose is the limit it gets: neither 0 nor
		// the default, so a listing that ignored Limit or folded every
		// value onto a bound would return all three.
		{"an explicit limit below the default", metamodel.RelationFilter{Limit: 2}, 2},
		// Over the cap clamps to the cap, not to the default. There are
		// three edges here, so what this pins is that asking for too much
		// still returns everything there is rather than nothing extra;
		// the arithmetic either side of the bound is
		// TestARelationPageAsksForTooMuchAndGetsTheCap's.
		{"over the cap", metamodel.RelationFilter{Limit: 501}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := svc.ListRelations(ctx, project, tc.filter)
			if err != nil {
				t.Fatalf("ListRelations: %v", err)
			}
			if len(rows.Relations) != tc.want {
				t.Fatalf("got %d edges, want %d", len(rows.Relations), tc.want)
			}
		})
	}

	// An unknown relation type in the filter is not an empty listing: a
	// caller that mistyped a key must hear about the key, not be told
	// this game has no such edges. The message is asserted and not just
	// the sentinel — the whole point of the decision is the key, and a
	// test that only matches ErrNotFound stays green against exactly the
	// bare "not_found" this refuses to return.
	_, err = svc.ListRelations(ctx, project, metamodel.RelationFilter{TypeKey: "nosuch"})
	if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if got, want := err.Error(), `not_found: no relation type "nosuch" in this game`; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

// TestARelationTypeKeyFilterIsBoundedBeforePostgresSeesIt is
// TestATypeKeyFilterIsBoundedBeforePostgresSeesIt's sibling for
// ListRelations: its type_key filter reached RelationTypeByKey
// unbounded before this test existed, and an invalid UTF-8 byte in the
// key would reach Postgres as a byte sequence it refuses outright
// (SQLSTATE 22021), landing as internal_error over a value the caller
// itself supplied.
func TestARelationTypeKeyFilterIsBoundedBeforePostgresSeesIt(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	_, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{
		TypeKey: "requires\x80",
	})
	requireFieldError(t, err, "type_key",
		"must be letters, digits, underscores or hyphens, starting with a letter or a digit")
}

// TestARelationsCursorWithAForgedNonTimestampSortIsMalformed reaches the
// arm ListRelations' comment above time.Parse says is reachable but had
// no test: a cursor whose fingerprint agrees — so it passes
// paging.Decode — and whose sort half is not RFC 3339, which only
// ListRelations itself can catch, one step past paging.Decode. A
// forged, empty sort half lands the same way, since the empty string
// does not parse as a timestamp either.
func TestARelationsCursorWithAForgedNonTimestampSortIsMalformed(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "takes_place_in",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
	}); err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	// A real cursor, so its fingerprint is the one this project's
	// unfiltered relation listing actually checks against. Limit: 1
	// forces the one edge above to fill the page.
	page, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{Limit: 1})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("a full page did not carry a cursor to forge from")
	}
	raw, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal cursor: %v", err)
	}

	for _, tc := range []struct {
		name string
		sort string
	}{
		{"not a timestamp at all", `"not-a-timestamp"`},
		{"the empty sort half", `""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forged := map[string]json.RawMessage{
				"n": json.RawMessage(tc.sort),
				"i": fields["i"],
				"f": fields["f"],
			}
			forgedRaw, err := json.Marshal(forged)
			if err != nil {
				t.Fatalf("marshal forged cursor: %v", err)
			}
			forgedCursor := base64.RawURLEncoding.EncodeToString(forgedRaw)

			_, err = svc.ListRelations(ctx, project,
				metamodel.RelationFilter{Limit: 1, Cursor: forgedCursor})
			if !errors.Is(err, metamodel.ErrInvalidInput) {
				t.Fatalf("err = %v, want invalid_input", err)
			}
			var ve *metamodel.ValidationError
			if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Path != "cursor" {
				t.Fatalf("err = %v, want a ValidationError at path \"cursor\"", err)
			}
			const want = "is malformed (it carries no creation time): page from the cursor " +
				"a previous call returned, or omit it to start"
			if ve.Fields[0].Message != want {
				t.Fatalf("message = %q, want %q", ve.Fields[0].Message, want)
			}
		})
	}
}

// TestBulkRelationsLandTheGoodEdgesAndReportTheRest is the partial-mode
// contract for edges, and the leakage check with it: a failing item's
// message names its own problem and none of its neighbours' content.
func TestBulkRelationsLandTheGoodEdgesAndReportTheRest(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
		Schema: metamodel.Schema{{Key: "note", Type: metamodel.FieldText}},
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	result, err := svc.UpsertRelations(ctx, project, []metamodel.RelationInput{
		{TypeKey: "requires",
			Source: metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
			Target: metamodel.Ref{TypeKey: "quest", Key: "hogger"},
			Fields: map[string]any{"note": "secret-first-note"}},
		{TypeKey: "requires",
			Source: metamodel.Ref{TypeKey: "quest", Key: "hogger"},
			Target: metamodel.Ref{TypeKey: "zone", Key: "nosuch"}},
		{TypeKey: "requires",
			Source: metamodel.Ref{TypeKey: "class", Key: "mage"},
			Target: metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
			Fields: map[string]any{"note": 42}},
	}, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("UpsertRelations: %v", err)
	}
	if len(result.Succeeded) != 1 {
		t.Fatalf("%d edges landed, want 1", len(result.Succeeded))
	}
	if len(result.Failed) != 2 {
		t.Fatalf("failures = %+v, want 2", result.Failed)
	}
	if result.Failed[0].Index != 1 || result.Failed[0].Code != "not_found" {
		t.Fatalf("failure 0 = %+v, want index 1 not_found", result.Failed[0])
	}
	if want := `not_found: target: no entity "nosuch" of type "zone" in this game`; result.Failed[0].Message != want {
		t.Fatalf("failure 0 message = %q, want %q", result.Failed[0].Message, want)
	}
	if result.Failed[1].Index != 2 || result.Failed[1].Code != "schema_violation" {
		t.Fatalf("failure 1 = %+v, want index 2 schema_violation", result.Failed[1])
	}
	for _, f := range result.Failed {
		if strings.Contains(f.Message, "secret-first-note") {
			t.Fatalf("a failure message carries another item's values: %q", f.Message)
		}
	}
}

// TestAnAtomicRelationBatchRollsBackWhole is the other mode: one bad
// edge and nothing lands, with the failing item named.
//
// It runs with a hub attached, so it also pins the publication
// discipline on this path: item 0 is written inside the transaction and
// rolled back with it, and an event announcing it would tell every
// subscriber to go and re-read an edge that does not exist.
func TestAnAtomicRelationBatchRollsBackWhole(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	// Subscribed after the declaration above, so the only event this
	// subscription could ever see is one from the batch itself.
	sub := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(sub)

	_, err := svc.UpsertRelations(ctx, project, []metamodel.RelationInput{
		{TypeKey: "requires",
			Source: metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
			Target: metamodel.Ref{TypeKey: "quest", Key: "hogger"}},
		{TypeKey: "requires",
			Source: metamodel.Ref{TypeKey: "quest", Key: "hogger"},
			Target: metamodel.Ref{TypeKey: "zone", Key: "nosuch"}},
	}, metamodel.BulkAtomic)
	if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "item 1") {
		t.Fatalf("err = %v, want the failing item named", err)
	}
	rels, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(rels.Relations) != 0 {
		t.Fatalf("%d edges survived a rolled-back atomic batch", len(rels.Relations))
	}
	requireNothing(t, sub, "a rolled-back batch wrote nothing")
}

// TestABatchThatRepeatsAnEdgeIsDiagnosedAsSuch is Task 4's correction 18
// for edges, and the reason bulkSpec keeps identity and repeated apart.
// An edge has no key of its own, so "give one of the two a different
// key" is advice that means nothing here; what the caller has to be told
// is that its two items are one edge.
//
// The repetition is folded exactly as relations_edge_key folds it: the
// two endpoints are resolved through case-insensitive key lookups, so
// two items spelling a key differently address one row, and the third
// item below is not a repeat because reversing an edge's endpoints is a
// different edge.
func TestABatchThatRepeatsAnEdgeIsDiagnosedAsSuch(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	kobold := metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"}
	hogger := metamodel.Ref{TypeKey: "quest", Key: "hogger"}

	items := []metamodel.RelationInput{
		{TypeKey: "requires", Source: kobold, Target: hogger},
		{TypeKey: "REQUIRES", Source: metamodel.Ref{TypeKey: "QUEST", Key: "Kobold-Camp"}, Target: hogger},
		{TypeKey: "requires", Source: hogger, Target: kobold},
	}

	result, err := svc.UpsertRelations(ctx, project, items, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("UpsertRelations: %v", err)
	}
	if len(result.Succeeded) != 2 {
		t.Fatalf("%d edges landed, want 2 — the repeat is the only refusal", len(result.Succeeded))
	}
	if len(result.Failed) != 1 || result.Failed[0].Index != 1 || result.Failed[0].Code != "invalid_input" {
		t.Fatalf("failures = %+v, want one invalid_input at index 1", result.Failed)
	}
	if want := `items[1]: an edge of type "REQUIRES" from "QUEST"/"Kobold-Camp" to "quest"/"hogger" ` +
		`is already addressed by item 0 of this batch, and keys are matched without regard to case: ` +
		`an edge is identified by its type and its two endpoints, so the two items are one edge — ` +
		`merge their fields into one item, or point one of them at a different pair`; result.Failed[0].Message != "invalid_input: "+want {
		t.Fatalf("message = %q, want %q", result.Failed[0].Message, "invalid_input: "+want)
	}

	// Atomic refuses the whole batch instead, before anything is written.
	other := newProject(t, pool)
	seedWorld(t, svc, other)
	if _, err := svc.UpsertRelationType(ctx, other, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	_, err = svc.UpsertRelations(ctx, other, items, metamodel.BulkAtomic)
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	rels, err := svc.ListRelations(ctx, other, metamodel.RelationFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(rels.Relations) != 0 {
		t.Fatalf("%d edges landed from a batch refused whole", len(rels.Relations))
	}
}

// TestAnAtomicRelationBatchLandsEveryEdgeOfTheBatch pins the ordinary
// success of atomic mode: every item lands, and Succeeded reports them
// all.
//
// It used to be called …SeesItsOwnEntities and claimed to pin that the
// batch resolves its endpoints against its own transaction. It did not:
// both entities are seeded here through separate committed calls, and
// UpsertRelations has no path that writes an entity, so routing every
// lookup in upsertRelationWith through the pool leaves this green. That
// claim needs a transaction shared between an entity write and an edge
// write, which no public caller has until Task 9 seeds a whole game in
// one call — it is pinned in the package's own
// TestAnEdgeResolvesItsEndpointsAgainstItsOwnTransaction, which is the
// only level where it is true today.
func TestAnAtomicRelationBatchLandsEveryEdgeOfTheBatch(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	result, err := svc.UpsertRelations(ctx, project, []metamodel.RelationInput{
		{TypeKey: "requires",
			Source: metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
			Target: metamodel.Ref{TypeKey: "quest", Key: "hogger"}},
		{TypeKey: "requires",
			Source: metamodel.Ref{TypeKey: "class", Key: "mage"},
			Target: metamodel.Ref{TypeKey: "quest", Key: "hogger"}},
	}, metamodel.BulkAtomic)
	if err != nil {
		t.Fatalf("UpsertRelations: %v", err)
	}
	if len(result.Succeeded) != 2 {
		t.Fatalf("%d edges landed, want 2", len(result.Succeeded))
	}
}

// TestRelationEventsReachEveryMemberOfTheGameIncludingAgents pins the
// gating events.go records for relation_type.* and relation.*: MinRole
// empty, HumanOnly false. Both subscribers here are the ones a wrong
// decision would silently cut out — a viewer, excluded by any MinRole
// above viewer, and a token caller, excluded by HumanOnly regardless of
// role.
//
// It also pins that every payload carries the *stored* spelling of the
// relation type key, on all three write paths, and that a removal
// carries the same identity an upsert does. Keys are matched without
// regard to case, so an event repeating the caller's spelling would name
// an identity no other reader of the game sees.
func TestRelationEventsReachEveryMemberOfTheGameIncludingAgents(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	viewer := hub.Subscribe(project, "viewer", false)
	defer hub.Unsubscribe(viewer)
	agent := hub.Subscribe(project, "editor", true)
	defer hub.Unsubscribe(agent)

	typ, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "Requires", Label: "requires",
	})
	if err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	for name, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
		got := receive(t, sub)
		if got.Kind != "relation_type.upserted" {
			t.Fatalf("%s: Kind = %q, want relation_type.upserted", name, got.Kind)
		}
		assertIdentityPayload(t, name, got, typ.ID, "Requires")
	}

	// Addressed with a different casing on every write path below; every
	// event must still name "Requires".
	kobold := metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"}
	hogger := metamodel.Ref{TypeKey: "quest", Key: "hogger"}
	edge, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires", Source: kobold, Target: hogger,
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}
	for name, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
		assertRelationPayload(t, name, receive(t, sub), "relation.upserted", edge.ID, "Requires")
	}

	// The second pass rewrites the edge the first one created, so it has
	// to claim the version that write landed on: since 0009 an edge upsert
	// is a compare-and-set like every other write here.
	var back int32
	for _, mode := range []metamodel.BulkMode{metamodel.BulkPartial, metamodel.BulkAtomic} {
		item := metamodel.RelationInput{TypeKey: "requires", Source: hogger, Target: kobold}
		if back > 0 {
			item.ExpectedVersion = &back
		}
		result, err := svc.UpsertRelations(ctx, project, []metamodel.RelationInput{item}, mode)
		if err != nil {
			t.Fatalf("%s batch: %v", mode, err)
		}
		if len(result.Succeeded) != 1 {
			t.Fatalf("%s batch landed %d edges, want 1", mode, len(result.Succeeded))
		}
		back = result.Succeeded[0].Version
		for name, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
			assertRelationPayload(t, name+" "+string(mode), receive(t, sub),
				"relation.upserted", result.Succeeded[0].ID, "Requires")
		}
	}

	if err := svc.RemoveRelation(ctx, project, "requires", kobold, hogger); err != nil {
		t.Fatalf("RemoveRelation: %v", err)
	}
	for name, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
		// A removal announced with an empty type key tells a client an
		// edge of a type it has never seen is gone.
		assertRelationPayload(t, name, receive(t, sub), "relation.removed", edge.ID, "Requires")
	}

	if err := svc.RemoveRelationType(ctx, project, typ.ID, true); err != nil {
		t.Fatalf("RemoveRelationType: %v", err)
	}
	for name, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
		got := receive(t, sub)
		if got.Kind != "relation_type.removed" {
			t.Fatalf("%s: Kind = %q, want relation_type.removed", name, got.Kind)
		}
		assertIdentityPayload(t, name, got, typ.ID, "Requires")
	}
}

// TestNoRelationEventIsPublishedForARefusedWrite: an announcement is an
// instruction to re-read, and announcing a change that did not happen
// teaches a client that what it already had is new.
func TestNoRelationEventIsPublishedForARefusedWrite(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	sub := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(sub)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "malformed key", Label: "requires",
	}); err == nil {
		t.Fatal("a malformed key must be refused")
	}
	requireNothing(t, sub, "a refused relation type wrote nothing")

	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "nosuch",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
	}); err == nil {
		t.Fatal("an unknown relation type must be refused")
	}
	requireNothing(t, sub, "a refused edge wrote nothing")
}

// assertRelationPayload checks the kind and the identity of a relation
// event: its id and the stored spelling of its type key.
func assertRelationPayload(t *testing.T, who string, e realtime.Event, wantKind string, wantID uuid.UUID, wantTypeKey string) {
	t.Helper()
	if e.Kind != wantKind {
		t.Fatalf("%s: Kind = %q, want %q", who, e.Kind, wantKind)
	}
	raw, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatalf("%s: marshal payload: %v", who, err)
	}
	var got struct {
		ID       uuid.UUID `json:"id"`
		TypeKey  string    `json:"type_key"`
		SourceID uuid.UUID `json:"source_id"`
		TargetID uuid.UUID `json:"target_id"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: decode payload %s: %v", who, raw, err)
	}
	if got.ID != wantID {
		t.Fatalf("%s: payload id = %v, want %v", who, got.ID, wantID)
	}
	if got.TypeKey != wantTypeKey {
		t.Fatalf("%s: payload type_key = %q, want %q", who, got.TypeKey, wantTypeKey)
	}
	if got.SourceID == uuid.Nil || got.TargetID == uuid.Nil {
		t.Fatalf("%s: payload carries no endpoints: %s", who, raw)
	}
}

// TestARelationTypeCreationThatLosesTheRaceForItsKeyIsRefused pins the
// compare-and-set in the SQL, which is the only thing standing between
// the loser of a creation race and a silent overwrite: a writer creating
// a key it has never seen has no version to expect and no row to lock,
// so its own read cannot see the rival at all. An unguarded DO UPDATE
// turns that writer into an overwrite of a type it never read.
//
// The rival is an open transaction rather than a second goroutine, so
// the interleaving is the test's and not the scheduler's.
func TestARelationTypeCreationThatLosesTheRaceForItsKeyIsRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = rival.Rollback(ctx) }()
	if _, err := rival.Exec(ctx,
		`INSERT INTO relation_types (project_id, key, label) VALUES ($1, 'requires', 'requires')`,
		project); err != nil {
		t.Fatalf("rival insert: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
			Key: "requires", Label: "Mine",
		})
		result <- err
	}()

	select {
	case err := <-result:
		t.Fatalf("the upsert returned %v before the rival committed; it should have blocked", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := rival.Commit(ctx); err != nil {
		t.Fatalf("rival commit: %v", err)
	}

	var conflict *metamodel.VersionConflictError
	select {
	case err := <-result:
		if !errors.As(err, &conflict) || conflict.Current != 1 {
			t.Fatalf("err = %v, want a *VersionConflictError carrying version 1", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival committed")
	}

	row, err := svc.RelationTypeByKey(ctx, project, "requires")
	if err != nil {
		t.Fatalf("RelationTypeByKey: %v", err)
	}
	if row.Label != "requires" || row.Version != 1 {
		t.Fatalf("the losing writer overwrote the row: %+v", row)
	}
}

// TestARelationTypeLosingItsKeyToAnotherSpellingIsNamedAsARespelling
// reaches conflictOnRelationTypeKey's respelling arm, which no other
// test does: the rival commits "Requires" while this writer holds a
// version that row does not have, so the guarded DO UPDATE matches
// nothing and the re-read is the only thing left that can say what
// actually happened. Without it the caller is told to merge onto a
// version, retries, and is refused again for a reason it was never told.
func TestARelationTypeLosingItsKeyToAnotherSpellingIsNamedAsARespelling(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = rival.Rollback(ctx) }()
	if _, err := rival.Exec(ctx,
		`INSERT INTO relation_types (project_id, key, label, version)
		 VALUES ($1, 'Requires', 'theirs', 7)`, project); err != nil {
		t.Fatalf("rival insert: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
			Key: "requires", Label: "mine",
		})
		result <- err
	}()

	select {
	case err := <-result:
		t.Fatalf("the upsert returned %v before the rival committed; it should have blocked", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := rival.Commit(ctx); err != nil {
		t.Fatalf("rival commit: %v", err)
	}

	select {
	case err := <-result:
		requireFieldError(t, err, "key",
			`"requires" already exists here spelled "Requires", and keys are matched `+
				`without regard to case: use "Requires" to update it, or pick a key that `+
				`differs by more than capitalisation`)
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival committed")
	}
}

// TestARelationTypeCreationRacingAnotherSpellingIsRefused is the one
// path the locked pre-read cannot cover: on the creation path there is
// nothing to lock, so a writer racing a creator reaches the write, meets
// the folding unique index, and must be refused by something that ran
// after it. conflictOnRelationTypeKey is that something — a creating
// caller passes noVersion, so the guarded DO UPDATE matches nothing and
// the re-read names both spellings — and withTx rolls the write back.
//
// It used to end at the *post-write* spelling check instead, by claiming
// the version the winner lands on. That claim is now refused before the
// write (metamodel.RemovedError), which is why this test stages the race
// without one; the post-write check went with it.
func TestARelationTypeCreationRacingAnotherSpellingIsRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = rival.Rollback(ctx) }()
	if _, err := rival.Exec(ctx,
		`INSERT INTO relation_types (project_id, key, label) VALUES ($1, 'Requires', 'theirs')`,
		project); err != nil {
		t.Fatalf("rival insert: %v", err)
	}

	// **No ExpectedVersion, and that is what routes this race.** It used
	// to carry one — the version the rival's row would land on — so the
	// guarded DO UPDATE matched and the *post-write* spelling check was
	// the thing that refused. A version claim against a row the locked
	// read cannot see is now refused before the write reaches the
	// database at all (metamodel.RemovedError), which is a different
	// answer to a different question, so the race this test is about is
	// staged the way it actually happens to a seeding agent: two
	// creations, neither claiming a version, one losing to the folding
	// unique index. The guard is then a guaranteed mismatch, and
	// conflictOn* re-reads and names the spelling.
	result := make(chan error, 1)
	go func() {
		_, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
			Key: "requires", Label: "mine",
		})
		result <- err
	}()

	select {
	case err := <-result:
		t.Fatalf("the upsert returned %v before the rival committed; it should have blocked", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := rival.Commit(ctx); err != nil {
		t.Fatalf("rival commit: %v", err)
	}

	select {
	case err := <-result:
		requireFieldError(t, err, "key",
			`"requires" already exists here spelled "Requires", and keys are matched `+
				`without regard to case: use "Requires" to update it, or pick a key that `+
				`differs by more than capitalisation`)
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival committed")
	}

	row, err := svc.RelationTypeByKey(ctx, project, "requires")
	if err != nil {
		t.Fatalf("RelationTypeByKey: %v", err)
	}
	if row.Label != "theirs" || row.Version != 1 {
		t.Fatalf("the refused write was not rolled back: %+v", row)
	}
}

// TestTheRelationQueriesThatAddressARowByIDAreScopedToTheProject pins
// the project filters TestRelationsAreScopedToTheirProject cannot see,
// for the reason its entity twin records: the service path reads the
// edge, then its type, then deletes, so each of the three filters masks
// the others and only mutating all three at once is observable through
// the service. Each is asserted here over the query itself.
//
// GetRelationTypeByID is in the list too: RemoveRelationType is reached
// by an id and its cascade delete is the widest write in this file.
func TestTheRelationQueriesThatAddressARowByIDAreScopedToTheProject(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, pool), newProject(t, pool)
	seedWorld(t, svc, mine)

	typ, err := svc.UpsertRelationType(ctx, mine, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	})
	if err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	edge, err := svc.UpsertRelation(ctx, mine, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}
	q := dbq.New(pool)

	if _, err := q.GetRelationByID(ctx, dbq.GetRelationByIDParams{
		ProjectID: theirs, ID: edge.ID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetRelationByID handed another game's edge out: err = %v, want pgx.ErrNoRows", err)
	}
	if _, err := q.GetRelationTypeByID(ctx, dbq.GetRelationTypeByIDParams{
		ProjectID: theirs, ID: typ.ID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetRelationTypeByID handed another game's type out: err = %v, want pgx.ErrNoRows", err)
	}
	if n, err := q.CountRelationsOfType(ctx, dbq.CountRelationsOfTypeParams{
		ProjectID: theirs, RelationTypeID: typ.ID,
	}); err != nil || n != 0 {
		t.Fatalf("CountRelationsOfType counted %d of another game's edges (%v)", n, err)
	}
	if err := q.DeleteRelationsOfType(ctx, dbq.DeleteRelationsOfTypeParams{
		ProjectID: theirs, RelationTypeID: typ.ID,
	}); err != nil {
		t.Fatalf("DeleteRelationsOfType: %v", err)
	}
	if rows, err := q.DeleteRelation(ctx, dbq.DeleteRelationParams{
		ProjectID: theirs, ID: edge.ID,
	}); err != nil || rows != 0 {
		t.Fatalf("DeleteRelation removed %d of another game's edges (%v)", rows, err)
	}
	if rows, err := q.DeleteRelationType(ctx, dbq.DeleteRelationTypeParams{
		ProjectID: theirs, ID: typ.ID,
	}); err != nil || rows != 0 {
		t.Fatalf("DeleteRelationType removed %d of another game's types (%v)", rows, err)
	}

	// The owning game still reads, counts and deletes its own rows, so no
	// assertion above can be passing because a filter refuses everyone.
	if _, err := q.GetRelationByID(ctx, dbq.GetRelationByIDParams{ProjectID: mine, ID: edge.ID}); err != nil {
		t.Fatalf("the owning game cannot read its own edge: %v", err)
	}
	if _, err := q.GetRelationTypeByID(ctx, dbq.GetRelationTypeByIDParams{ProjectID: mine, ID: typ.ID}); err != nil {
		t.Fatalf("the owning game cannot read its own relation type: %v", err)
	}
	if n, err := q.CountRelationsOfType(ctx, dbq.CountRelationsOfTypeParams{
		ProjectID: mine, RelationTypeID: typ.ID,
	}); err != nil || n != 1 {
		t.Fatalf("CountRelationsOfType counted %d of the owning game's edges (%v)", n, err)
	}
	if rows, err := q.DeleteRelation(ctx, dbq.DeleteRelationParams{
		ProjectID: mine, ID: edge.ID,
	}); err != nil || rows != 1 {
		t.Fatalf("the owning game cannot delete its own edge: %d rows, %v", rows, err)
	}
	if rows, err := q.DeleteRelationType(ctx, dbq.DeleteRelationTypeParams{
		ProjectID: mine, ID: typ.ID,
	}); err != nil || rows != 1 {
		t.Fatalf("the owning game cannot delete its own relation type: %d rows, %v", rows, err)
	}
}

// TestRemovingAnEntityTypePrunesItFromEveryEndpointList closes the
// invariant Task 5 created.
//
// source_type_ids and target_type_ids are plain uuid[] with no foreign
// key, so an id in them survives the entity type it names. All three
// consequences are checked here, because they are exactly the failure
// correction 6 claims to have fixed: the rule becomes unsatisfiable, the
// refusal a designer then meets names the wrong problem — "entity type
// \"zone\" cannot be the target" while zone visibly is the declared
// target, because the stored id is the *old* zone's — and the relation
// type cannot be repaired through its own API, since re-declaring it with
// the list it currently holds is refused as invalid_input.
func TestRemovingAnEntityTypePrunesItFromEveryEndpointList(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	quest, err := svc.EntityTypeByKey(ctx, project, "quest")
	if err != nil {
		t.Fatalf("quest type: %v", err)
	}
	zone, err := svc.EntityTypeByKey(ctx, project, "zone")
	if err != nil {
		t.Fatalf("zone type: %v", err)
	}
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeKeys: []string{"quest"},
		TargetTypeKeys: []string{"zone"},
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	// Cascade, because the seeded world has a zone entity: this is the
	// ordinary "I got the vocabulary wrong, start that type again" move.
	if err := svc.RemoveEntityType(ctx, project, zone.ID, true); err != nil {
		t.Fatalf("RemoveEntityType: %v", err)
	}

	stored, err := svc.RelationTypeByKey(ctx, project, "takes_place_in")
	if err != nil {
		t.Fatalf("RelationTypeByKey: %v", err)
	}
	for _, id := range stored.TargetTypeIds {
		if id == zone.ID {
			t.Fatal("the removed entity type is still declared as a target")
		}
	}
	// The source list names a type that still exists and must be left
	// exactly as it was: pruning is not a licence to empty the row.
	if len(stored.SourceTypeIds) != 1 || stored.SourceTypeIds[0] != quest.ID {
		t.Fatalf("source_type_ids = %v, want [%v]", stored.SourceTypeIds, quest.ID)
	}

	// The type is repairable through its own API. Re-declaring it with
	// the list it currently holds must be accepted; while the dangling id
	// was there this was refused as invalid_input, so the only way to fix
	// the row was to know the new type's id out of band.
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeKeys:  []string{"quest"},
		ExpectedVersion: &stored.Version,
	}); err != nil {
		t.Fatalf("a relation type could not be re-declared with the list it holds: %v", err)
	}

	// And the rule it holds can be satisfied again. A designer who
	// recreates zone and wires an edge is not refused with a message
	// naming a type that is visibly declared.
	newZone, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "zone", Label: "Zone", LabelPlural: "Zones",
	})
	if err != nil {
		t.Fatalf("re-create zone: %v", err)
	}
	if newZone.ID == zone.ID {
		t.Fatal("the recreated type reused the removed type's id; this test proves nothing")
	}
	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "zone", Key: "elwynn", Name: "Elwynn Forest",
	}); err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "takes_place_in",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
	}); err != nil {
		t.Fatalf("an edge was refused against a rule nothing could satisfy: %v", err)
	}
}

// TestConcurrentEditsToOneEdgesFieldsAreRefused is the inverse of the
// test that used to stand here, and the inversion is the point.
//
// The test it replaces — named, in Task 7, for the loss it pinned rather
// than for the guarantee this one pins — asserted the cost of two linked
// decisions: an edge carried no `version`, so there was no
// compare-and-set to refuse a stale write, and an edge's uniqueness index
// forbids parallel edges, which sends a game's multiplicity into the
// edge's own fields — the `passages: ["door", "vent"]` UpsertRelation
// offers by name. Together they left the one field a designer was *told*
// to use for multiplicity with no protection at all, and the old test
// asserted the whole loss: one row, the second write whole, no error, two
// events a subscriber could not tell apart. It said in as many words that
// adding `version` to `relations` would turn it red and that the correct
// response would be to rewrite it. 0009 added the column; this is that
// rewrite.
//
// Two writers extend one list. The second, which never read the first,
// is refused with the version it has to merge onto — and the row still
// holds the first writer's value, which is the half that says the refusal
// happened before the write rather than after it.
func TestConcurrentEditsToOneEdgesFieldsAreRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "connects_to", Label: "connects to",
		Schema: metamodel.Schema{{Key: "passages", Type: metamodel.FieldText}},
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	sub := hub.Subscribe(project, "viewer", false)
	defer hub.Unsubscribe(sub)

	edge := func(passages string) metamodel.RelationInput {
		return metamodel.RelationInput{
			TypeKey: "connects_to",
			Source:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
			Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
			Fields:  map[string]any{"passages": passages},
		}
	}

	// The designer who wrote "door" first.
	first, err := svc.UpsertRelation(ctx, project, edge("door"))
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if firstEvent := receive(t, sub); firstEvent.Kind != "relation.upserted" {
		t.Fatalf("first event kind = %q, want relation.upserted", firstEvent.Kind)
	}

	// The designer who was extending the same list at the same time, and
	// never read the first write. There is no ExpectedVersion to send,
	// and that is now a refusal rather than an overwrite.
	_, err = svc.UpsertRelation(ctx, project, edge("vent"))
	var conflict *metamodel.VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("second write: err = %v, want a VersionConflictError — an unversioned "+
			"rewrite of an edge is a lost update and must be refused", err)
	}
	if conflict.Current != first.Version {
		t.Fatalf("conflict reports version %d, want %d: a caller told the wrong number "+
			"retries into the same refusal", conflict.Current, first.Version)
	}

	// **The refusal happened before the write.** A conflict raised after
	// the row had already been overwritten would report the same error
	// and lose the same value, so the error alone does not prove the fix.
	stored, err := svc.RelationByEdge(ctx, project, "connects_to",
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
		metamodel.Ref{TypeKey: "quest", Key: "hogger"})
	if err != nil {
		t.Fatalf("RelationByEdge: %v", err)
	}
	if got, want := string(stored.Fields), `{"passages": "door"}`; got != want {
		t.Fatalf("fields = %s, want %s — the refused write took the row anyway", got, want)
	}
	if stored.Version != first.Version {
		t.Fatalf("version = %d, want %d — a refused write moved the version",
			stored.Version, first.Version)
	}

	// And nothing was announced: an event for a write that did not happen
	// would send every subscriber to re-read a row that did not change.
	requireNothing(t, sub, "a refused write publishes nothing")

	// The merge the caller is told to make does land, and it takes the row
	// whole — the edge is still one row, which is the other half of the
	// pair: parallel edges are still refused, so multiplicity still lives
	// in this field and is now protected.
	merged, err := svc.UpsertRelation(ctx, project,
		withVersion(edge("door, vent"), conflict.Current))
	if err != nil {
		t.Fatalf("merged write: %v", err)
	}
	if merged.ID != first.ID {
		t.Fatalf("two row ids (%s, %s): parallel edges are no longer refused, and the "+
			"decision that pushes multiplicity into edge fields no longer holds",
			first.ID, merged.ID)
	}
	if merged.Version != first.Version+1 {
		t.Fatalf("version = %d, want %d", merged.Version, first.Version+1)
	}
}

// TestBothBadEndsOfAnEdgeAreAnsweredInOnePass extends the one-pass rule
// from resolution to the endpoint *rule*.
//
// Reporting both missing ends together was only half the claim
// `upsertRelationWith` made: the two `endpointAllowed` checks still
// returned one at a time, so two wrongly-typed ends cost two round trips,
// and — worse — a caller with one missing end and one wrongly-typed end
// heard only the `not_found`, fixed it, resent, and only then heard the
// mismatch. That is the hidden second hop the comment claimed had been
// removed, inside the code the comment sits on.
//
// **Which code wins when the two halves disagree**: `not_found`. A
// caller cannot act on the mismatch first — the entity it names does not
// exist, and creating it is the step that decides which type the end
// will even have — so `not_found` is the code that describes the work to
// do next, and the mismatch travels with it in the message so the second
// attempt already knows about it. `failureFor` orders `ErrNotFound`
// above `ErrEndpointTypeMismatch` and both halves are wrapped, so that
// falls out of the existing switch rather than needing a rule of its
// own; this test is what pins it there.
func TestBothBadEndsOfAnEdgeAreAnsweredInOnePass(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeKeys: []string{"quest"},
		TargetTypeKeys: []string{"zone"},
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}

	t.Run("two wrongly typed ends", func(t *testing.T) {
		_, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
			TypeKey: "takes_place_in",
			Source:  metamodel.Ref{TypeKey: "class", Key: "mage"},
			Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		})
		if !errors.Is(err, metamodel.ErrEndpointTypeMismatch) {
			t.Fatalf("err = %v, want ErrEndpointTypeMismatch", err)
		}
		want := `endpoint_type_mismatch: source: entity type "class" cannot be the source of relation type "takes_place_in"; ` +
			`target: entity type "quest" cannot be the target of relation type "takes_place_in"`
		if err.Error() != want {
			t.Fatalf("message = %q, want %q", err.Error(), want)
		}
	})

	t.Run("one missing end and one wrongly typed end", func(t *testing.T) {
		in := metamodel.RelationInput{
			TypeKey: "takes_place_in",
			Source:  metamodel.Ref{TypeKey: "quest", Key: "nosuch"},
			Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		}
		_, err := svc.UpsertRelation(ctx, project, in)
		want := `not_found: source: no entity "nosuch" of type "quest" in this game; ` +
			`endpoint_type_mismatch: target: entity type "quest" cannot be the target of relation type "takes_place_in"`
		if err == nil || err.Error() != want {
			t.Fatalf("message = %v, want %q", err, want)
		}
		// Both halves stay reachable, so a caller asking about either
		// sentinel is answered truthfully.
		if !errors.Is(err, metamodel.ErrNotFound) || !errors.Is(err, metamodel.ErrEndpointTypeMismatch) {
			t.Fatalf("err = %v, want it to match both sentinels", err)
		}

		// The wire code an agent branches on, through the only path that
		// exposes it. not_found wins: the missing row is the one piece of
		// work that has to happen first.
		result, err := svc.UpsertRelations(ctx, project, []metamodel.RelationInput{in},
			metamodel.BulkPartial)
		if err != nil {
			t.Fatalf("UpsertRelations: %v", err)
		}
		if len(result.Failed) != 1 {
			t.Fatalf("Failed = %+v, want one failure", result.Failed)
		}
		if result.Failed[0].Code != "not_found" {
			t.Fatalf("Code = %q, want not_found", result.Failed[0].Code)
		}
		if result.Failed[0].Message != want {
			t.Fatalf("bulk message = %q, want %q", result.Failed[0].Message, want)
		}
	})
}

// TestABulkEdgeWriteReportsWhatLandedInAWireShape is the edge half of
// Task 7's answer to "what does a successful batch tell an agent"; see
// TestABulkWriteReportsWhatLandedInAWireShape for the entity half and
// the argument.
//
// What an edge reports differs from an entity's in exactly one way, and
// it is a decision rather than an omission: **there is no version**.
// relations has no version column at all (Task 5's decision, recorded on
// RelationInput), so the only things a caller cannot derive from what it
// sent are the edge's own id — which is what relations.remove takes —
// and the two endpoint ids the refs resolved to.
func TestABulkEdgeWriteReportsWhatLandedInAWireShape(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
	}); err != nil {
		t.Fatalf("seed relation type: %v", err)
	}

	result, err := svc.UpsertRelations(ctx, project, []metamodel.RelationInput{
		{
			TypeKey: "takes_place_in",
			Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
			Target:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
		},
	}, metamodel.BulkPartial)
	if err != nil {
		t.Fatalf("UpsertRelations: %v", err)
	}
	if len(result.Written) != 1 {
		t.Fatalf("written = %+v, want the one edge that landed", result.Written)
	}
	w := result.Written[0]
	if w.TypeKey != "takes_place_in" {
		t.Fatalf("written.TypeKey = %q, want the stored relation type key", w.TypeKey)
	}
	row := result.Succeeded[0]
	if w.ID != row.ID || w.SourceID != row.SourceID || w.TargetID != row.TargetID {
		t.Fatalf("written = %+v, want the edge's own id and both endpoint ids", w)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Written []struct {
			TypeKey  string `json:"type_key"`
			ID       string `json:"id"`
			SourceID string `json:"source_id"`
			TargetID string `json:"target_id"`
		} `json:"written"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if len(decoded.Written) != 1 || decoded.Written[0].ID != row.ID.String() {
		t.Fatalf("marshalled result %s carries no record of the edge that landed", raw)
	}
	if decoded.Written[0].SourceID != row.SourceID.String() ||
		decoded.Written[0].TargetID != row.TargetID.String() {
		t.Fatalf("marshalled written = %+v, want both endpoint ids", decoded.Written[0])
	}
	if strings.Contains(string(raw), "updated_by") {
		t.Fatalf("marshalled result %s carries database columns", raw)
	}
}

// TestARelationTypeSemanticRoleIsCheckedHereAndNotOnlyByTheDatabase
// closes review finding H3.
//
// `semantic_role` was validated nowhere in Go; the only guard was the
// CHECK constraint 0004_metamodel.sql puts on the column, and a value
// outside the list reached an agent as `internal_error` with a
// check-constraint violation in the operator's log. That is the exact
// shape the error vocabulary exists to prevent: something the caller
// typed, that the caller can fix, reported as a server fault with no
// path and no list of what would have been accepted.
func TestARelationTypeSemanticRoleIsCheckedHereAndNotOnlyByTheDatabase(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	_, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires", SemanticRole: "nonsense",
	})
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	requireFieldError(t, err, "semantic_role",
		`must be one of "prerequisite", "unlock", "containment", "spatial", `+
			`"availability", "reward", or omitted: a relation type need not classify itself`)

	if _, err := svc.RelationTypeByKey(ctx, project, "requires"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("the refused declaration must store nothing, got %v", err)
	}

	// Every role the column's CHECK admits is accepted here, so the two
	// lists cannot drift apart silently.
	for _, role := range metamodel.SemanticRoles {
		key := "rel_" + role
		if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
			Key: key, Label: role, SemanticRole: role,
		}); err != nil {
			t.Fatalf("semantic role %q: %v", role, err)
		}
	}
}

// TestARemovalSaysWhatItCouldNotFindAndWhatStillHoldsIt closes review
// finding L4.
//
// All four removals answered an unknown id with a bare `ErrNotFound`,
// whose message is the string "not_found" — so the wire report was
// `{"error":"not_found","message":"not_found"}`, a code repeated as
// prose. `types.remove` on a type that still has entities was worse in
// the same way: `{"error":"in_use","message":"in_use"}`, with nothing
// about how many rows, or that `cascade` is the way through.
// `entities.get` and `types.get` have named their misses since Task 4;
// these four are now held to the same standard.
func TestARemovalSaysWhatItCouldNotFindAndWhatStillHoldsIt(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	// The two type removals still take an id, because the views service
	// needs one to find the saved views a removal breaks and takes it
	// before the delete; Metamodel 14 moved the *tools* onto keys and
	// left the resolution on the server, which is where this function
	// sits. The two content removals now address a row the way every
	// other reader does, so what their messages have to name is the
	// address the caller sent rather than an id it never had.
	ghost := uuid.New()
	gRef := metamodel.Ref{TypeKey: "quest", Key: "ghost"}
	seedTakesPlaceIn(t, svc, project)
	for _, tc := range []struct {
		name   string
		want   []string
		remove func() error
	}{
		{"an entity type", []string{"entity type", ghost.String()}, func() error {
			return svc.RemoveEntityType(ctx, project, ghost, false)
		}},
		{"a relation type", []string{"relation type", ghost.String()}, func() error {
			return svc.RemoveRelationType(ctx, project, ghost, false)
		}},
		{"an entity", []string{"quest", "ghost"}, func() error {
			return svc.RemoveEntity(ctx, project, "quest", "ghost")
		}},
		{"an entity under an undeclared type", []string{"entity type", "monster"}, func() error {
			return svc.RemoveEntity(ctx, project, "monster", "ghost")
		}},
		{"a relation of an undeclared type", []string{"relation type", "eats"}, func() error {
			return svc.RemoveRelation(ctx, project, "eats", gRef, gRef)
		}},
		{"a relation whose endpoints are not there", []string{"quest", "ghost"}, func() error {
			return svc.RemoveRelation(ctx, project, "takes_place_in", gRef, gRef)
		}},
		{"a relation between two real entities that has no edge", []string{"takes_place_in", "hogger"}, func() error {
			return svc.RemoveRelation(ctx, project, "takes_place_in",
				metamodel.Ref{TypeKey: "quest", Key: "hogger"},
				metamodel.Ref{TypeKey: "zone", Key: "elwynn"})
		}},
	} {
		t.Run("removing "+tc.name+" that is not there names it", func(t *testing.T) {
			err := tc.remove()
			if !errors.Is(err, metamodel.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("message = %q, want it to name %q", err, want)
				}
			}
			if err.Error() == "not_found" {
				t.Fatalf("message = %q, which is the code repeated as prose", err)
			}
		})
	}

	// The in-use refusals say how many rows hold the type and that
	// cascade is the way through, which is the whole recovery.
	quest, err := svc.EntityTypeByKey(ctx, project, "quest")
	if err != nil {
		t.Fatalf("quest type: %v", err)
	}
	// seedWorld left two quests behind, and both hold the type.
	err = svc.RemoveEntityType(ctx, project, quest.ID, false)
	if !errors.Is(err, metamodel.ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse", err)
	}
	for _, want := range []string{"quest", "2 entit", "cascade"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message = %q, want it to mention %q", err, want)
		}
	}

	relate(t, svc, project, "takes_place_in",
		metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"})
	takesPlaceIn, err := svc.RelationTypeByKey(ctx, project, "takes_place_in")
	if err != nil {
		t.Fatalf("relation type: %v", err)
	}
	err = svc.RemoveRelationType(ctx, project, takesPlaceIn.ID, false)
	if !errors.Is(err, metamodel.ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse", err)
	}
	for _, want := range []string{"takes_place_in", "1 edge", "cascade"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message = %q, want it to mention %q", err, want)
		}
	}
}

// TestAnEdgeIsReadableByTheTripleItWasWrittenUnder is the read half of
// TestRelationCarriesItsOwnFields, which until Metamodel 12 had none:
// the values an edge carries were validated on write, stored, and
// reachable by nothing but SQL. The two endpoints and the type key here
// are the same three strings UpsertRelation took, because addressing an
// edge by the address it was written under is the whole point — a
// caller holding the ids already has ListRelations.
func TestAnEdgeIsReadableByTheTripleItWasWrittenUnder(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "connects_to", Label: "connects to", SemanticRole: "spatial",
		Schema: metamodel.Schema{
			{Key: "requires_ability", Type: metamodel.FieldText},
			{Key: "one_way", Type: metamodel.FieldBool},
		},
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	written, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "connects_to",
		Source:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Fields:  map[string]any{"requires_ability": "mothwing_cloak", "one_way": true},
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	row, err := svc.RelationByEdge(ctx, project, "connects_to",
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
		metamodel.Ref{TypeKey: "quest", Key: "hogger"})
	if err != nil {
		t.Fatalf("RelationByEdge: %v", err)
	}
	if row.ID != written.ID {
		t.Fatalf("read edge %s, want the one just written, %s", row.ID, written.ID)
	}
	var stored map[string]any
	if err := json.Unmarshal(row.Fields, &stored); err != nil {
		t.Fatalf("decode stored fields %s: %v", row.Fields, err)
	}
	if stored["requires_ability"] != "mothwing_cloak" || stored["one_way"] != true {
		t.Fatalf("read back %v, want requires_ability mothwing_cloak and one_way true", stored)
	}

	// The direction is part of the address: the same pair the other way
	// round is a different edge, and there is no such edge here.
	if _, err := svc.RelationByEdge(ctx, project, "connects_to",
		metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"}); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("the reversed pair answered %v, want ErrNotFound", err)
	}
}

// TestEachMissingPieceOfAnEdgeRead names what a reader got wrong, the
// same way TestEachMissingPieceOfAnEdgeIsNamed does for a write: three
// addresses in one call means three ways to be wrong, and "not found"
// alone leaves a caller guessing which.
func TestEachMissingPieceOfAnEdgeRead(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
	}); err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	for _, tc := range []struct {
		name           string
		typeKey        string
		source, target metamodel.Ref
		want           string
	}{
		{
			name:    "unknown relation type",
			typeKey: "unlocks",
			source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
			target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
			want:    `no relation type "unlocks"`,
		},
		{
			name:    "unknown source entity",
			typeKey: "requires",
			source:  metamodel.Ref{TypeKey: "quest", Key: "nowhere"},
			target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
			want:    `no entity "nowhere"`,
		},
		{
			name:    "unknown target entity type",
			typeKey: "requires",
			source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
			target:  metamodel.Ref{TypeKey: "talent", Key: "hogger"},
			want:    `no entity type "talent"`,
		},
		{
			name:    "every piece exists and the edge does not",
			typeKey: "requires",
			source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
			target:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
			want:    `no "requires" edge`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.RelationByEdge(ctx, project, tc.typeKey, tc.source, tc.target)
			if !errors.Is(err, metamodel.ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestAnEdgeReadIsScopedToItsGameAndBoundsItsKeys covers the two things
// a by-key read owes that a by-id read does not: another game's edge is
// not readable by the same triple, and a key too malformed ever to have
// been stored is a caller's own invalid_input rather than a Postgres
// error escaping as a server fault — the rule ListRelations' own type
// key filter already follows.
func TestAnEdgeReadIsScopedToItsGameAndBoundsItsKeys(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, pool), newProject(t, pool)
	seedWorld(t, svc, mine)
	seedWorld(t, svc, theirs)

	for _, project := range []uuid.UUID{mine, theirs} {
		if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
			Key: "requires", Label: "requires",
		}); err != nil {
			t.Fatalf("UpsertRelationType: %v", err)
		}
	}
	if _, err := svc.UpsertRelation(ctx, mine, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
	}); err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	// The neighbouring game declares the same vocabulary and the same
	// entities, so every lookup on the way to the edge succeeds there;
	// only the edge itself is missing, which is what makes this a real
	// scoping assertion and not a lookup failing early.
	//
	// What it does *not* prove is that GetRelationByEdge's own
	// `project_id` filter is load-bearing. It is not: deleting that
	// clause from the statement leaves this case green, measured. The
	// isolation comes one step earlier — the relation type and both
	// endpoints are resolved inside the calling game, so the triple
	// handed to the statement is already this game's, and the two games'
	// identically-keyed "requires" types have different ids. The filter
	// stays as the backstop GetRelationByID has for the same reason, and
	// this comment is here so that nobody later reads a passing test as
	// evidence for it.
	if _, err := svc.RelationByEdge(ctx, theirs, "requires",
		metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		metamodel.Ref{TypeKey: "quest", Key: "hogger"}); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("another game read this edge: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.RelationByEdge(ctx, mine, "requires",
		metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		metamodel.Ref{TypeKey: "quest", Key: "hogger"}); err != nil {
		t.Fatalf("the owning game cannot read its own edge: %v", err)
	}

	for _, tc := range []struct{ name, path, typeKey, sourceKey string }{
		{"relation type key", "type_key", "requires\x00", "kobold-camp"},
		{"endpoint key", "source.key", "requires", "kobold\x00camp"},
	} {
		t.Run(tc.name+" is bounded before Postgres sees it", func(t *testing.T) {
			_, err := svc.RelationByEdge(ctx, mine, tc.typeKey,
				metamodel.Ref{TypeKey: "quest", Key: tc.sourceKey},
				metamodel.Ref{TypeKey: "quest", Key: "hogger"})
			if !errors.Is(err, metamodel.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			requireFieldError(t, err, tc.path,
				"must be letters, digits, underscores or hyphens, starting with a letter or a digit")
		})
	}
}

// TestUpsertRelationsConflictPathCannotWriteAnotherGamesEdge drives the
// statement directly, because no caller of this package can reach the
// hole it would leave.
//
// UpsertRelation writes project_id as a column value and leans on
// 0004_metamodel.sql's composite foreign keys for the insert path — but
// the conflict target is (relation_type_id, source_id, target_id), which
// names no project, and project_id is not in the SET list. So a stored
// row keeps its own project id, every key stays satisfied, and the
// DO UPDATE is an update of another game's edge that hands the caller
// that game's row back. Measured before the guard existed: this game's
// project id with another game's three ids overwrote that game's fields
// and returned its row, project id included — a cross-game write and a
// cross-game read in one statement.
//
// It is unreachable from Service.UpsertRelation, which resolves the type
// and both endpoints by key inside the project first, exactly as
// SetPositions does in internal/views — and exactly as there, the guard
// stays and is asserted here, because a statement that is safe only
// because of how today's caller happens to address it is a trap for
// tomorrow's.
func TestUpsertRelationsConflictPathCannotWriteAnotherGamesEdge(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, pool), newProject(t, pool)
	seedWorld(t, svc, mine)
	seedWorld(t, svc, theirs)

	for _, project := range []uuid.UUID{mine, theirs} {
		if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
			Key: "requires", Label: "requires",
		}); err != nil {
			t.Fatalf("UpsertRelationType: %v", err)
		}
	}
	edge, err := svc.UpsertRelation(ctx, mine, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}
	q := dbq.New(pool)
	stored, err := q.GetRelationByID(ctx, dbq.GetRelationByIDParams{ProjectID: mine, ID: edge.ID})
	if err != nil {
		t.Fatalf("read the stored edge: %v", err)
	}

	// The other game, holding this edge's three ids and its own project
	// id, meets the guard: no row updated, and therefore no row returned.
	//
	// **It sends the edge's real version**, deliberately. Since 0009 the
	// DO UPDATE carries a second guard — `relations.version =
	// expected_version` — and a version this caller did not know would
	// refuse the statement on its own, leaving the project filter
	// untested and this test green for the wrong reason. With the true
	// version the version guard is satisfied and the project filter is
	// the only thing left standing between the two games.
	row, err := q.UpsertRelation(ctx, dbq.UpsertRelationParams{
		ProjectID:       theirs,
		RelationTypeID:  stored.RelationTypeID,
		SourceID:        stored.SourceID,
		TargetID:        stored.TargetID,
		Fields:          []byte(`{"note":"theirs"}`),
		ExpectedVersion: stored.Version,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("UpsertRelation returned %+v (err = %v) for another game's edge, want no rows",
			row, err)
	}

	// The positive control, in both directions: the edge is untouched,
	// and the owning game can still write it.
	after, err := q.GetRelationByID(ctx, dbq.GetRelationByIDParams{ProjectID: mine, ID: edge.ID})
	if err != nil {
		t.Fatalf("read the edge back: %v", err)
	}
	if string(after.Fields) != string(stored.Fields) {
		t.Fatalf("the edge's fields are %s after another game's write, want %s",
			after.Fields, stored.Fields)
	}
	if after.Version != stored.Version {
		t.Fatalf("the edge is version %d after another game's write, want %d",
			after.Version, stored.Version)
	}
	mineRow, err := q.UpsertRelation(ctx, dbq.UpsertRelationParams{
		ProjectID:       mine,
		RelationTypeID:  stored.RelationTypeID,
		SourceID:        stored.SourceID,
		TargetID:        stored.TargetID,
		Fields:          []byte(`{"note":"mine"}`),
		ExpectedVersion: stored.Version,
	})
	if err != nil {
		t.Fatalf("the owning game cannot write its own edge: %v", err)
	}
	if mineRow.Version != stored.Version+1 {
		t.Fatalf("the owning game's write left version %d, want %d",
			mineRow.Version, stored.Version+1)
	}
}

// relationState reads back the columns the re-validation sweep is
// allowed and not allowed to touch. It is entityState's twin, and it
// reads one column entityState does not: since 0009 an edge carries a
// version, and a sweep must not move it.
func relationState(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) (
	invalid bool, version int32, fields string, updatedAt string,
) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT invalid, version, fields::text, updated_at::text FROM relations WHERE id = $1`, id).
		Scan(&invalid, &version, &fields, &updatedAt)
	if err != nil {
		t.Fatalf("read relation: %v", err)
	}
	return invalid, version, fields, updatedAt
}

// seedEdgeWorld declares a relation type with the given schema and
// returns a helper that writes one edge between two of seedWorld's
// quests, addressed by an index so a test can hold several.
func seedEdgeWorld(t *testing.T, svc *metamodel.Service, project uuid.UUID,
	schema metamodel.Schema,
) {
	t.Helper()
	if _, err := svc.UpsertRelationType(context.Background(), project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires", Schema: schema,
	}); err != nil {
		t.Fatalf("declare relation type: %v", err)
	}
}

// narrowRequires re-declares the "requires" relation type with a new
// schema, at the version its previous declaration landed on.
func narrowRequires(t *testing.T, svc *metamodel.Service, project uuid.UUID,
	schema metamodel.Schema, version int32,
) {
	t.Helper()
	if _, err := svc.UpsertRelationType(context.Background(), project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires", Schema: schema,
		ExpectedVersion: &version,
	}); err != nil {
		t.Fatalf("re-declare relation type at version %d: %v", version, err)
	}
}

// TestAnEdgeSchemaChangeFlagsTheEdgesThatStopFittingWithoutTouchingThem is
// TestSchemaChangeFlagsRowsInvalidWithoutTouchingThem for relations, and
// it exists because until 0009 there was no answer to give: a relation
// type carries a field schema exactly as an entity type does, and editing
// it left every existing edge unjudged, with no column to record a
// verdict in.
//
// Both halves are asserted, and the second is the one a write-only flag
// would pass without: the edge that still fits keeps its values, its
// version and its updated_at, so a validation pass cannot read as an edit
// of content nobody edited.
func TestAnEdgeSchemaChangeFlagsTheEdgesThatStopFittingWithoutTouchingThem(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedEdgeWorld(t, svc, project, metamodel.Schema{
		{Key: "note", Type: metamodel.FieldText},
		{Key: "difficulty", Type: metamodel.FieldNumber},
	})

	carrying, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Fields:  map[string]any{"note": "chain", "difficulty": 3},
	})
	if err != nil {
		t.Fatalf("write the edge that will stop fitting: %v", err)
	}
	sparse, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Fields:  map[string]any{"note": "still fits"},
	})
	if err != nil {
		t.Fatalf("write the edge that will keep fitting: %v", err)
	}
	_, sparseVersionBefore, sparseFieldsBefore, sparseUpdatedBefore := relationState(t, pool, sparse.ID)

	// "difficulty" is dropped from the declaration, so the edge carrying
	// it no longer fits and the one that never had it still does.
	narrowRequires(t, svc, project, metamodel.Schema{
		{Key: "note", Type: metamodel.FieldText},
	}, 1)

	invalid, version, fields, _ := relationState(t, pool, carrying.ID)
	if !invalid {
		t.Fatal("an edge carrying an undeclared field must be flagged invalid")
	}
	if version != carrying.Version {
		t.Fatalf("the sweep moved the flagged edge's version: %d -> %d",
			carrying.Version, version)
	}
	if !strings.Contains(fields, "difficulty") {
		t.Fatalf("the sweep deleted the values it flagged: %s", fields)
	}

	invalid, version, fieldsAfter, updatedAfter := relationState(t, pool, sparse.ID)
	if invalid {
		t.Fatal("an edge that still fits its schema must not be flagged")
	}
	if fieldsAfter != sparseFieldsBefore {
		t.Fatalf("the sweep rewrote stored values: %s -> %s", sparseFieldsBefore, fieldsAfter)
	}
	if version != sparseVersionBefore {
		t.Fatalf("the sweep moved a version: %d -> %d", sparseVersionBefore, version)
	}
	// A row whose verdict has not changed must not be rewritten at all: a
	// validation pass is not an edit, and a moved updated_at says it was.
	if updatedAfter != sparseUpdatedBefore {
		t.Fatalf("the sweep touched updated_at: %s -> %s", sparseUpdatedBefore, updatedAfter)
	}
}

// TestAnEdgeSchemaChangeDoesNotBackFillDeclaredDefaults is the edge half
// of TestSchemaChangeDoesNotBackFillDeclaredDefaults, and it pins the
// half of the shared rule that is easiest to lose: revalidate calls
// CheckValues rather than Validate precisely so there is no normalised
// map in scope to write back.
func TestAnEdgeSchemaChangeDoesNotBackFillDeclaredDefaults(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedEdgeWorld(t, svc, project, metamodel.Schema{{Key: "note", Type: metamodel.FieldText}})

	edge, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Fields:  map[string]any{"note": "chain"},
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	narrowRequires(t, svc, project, metamodel.Schema{
		{Key: "note", Type: metamodel.FieldText},
		{Key: "hidden", Type: metamodel.FieldBool, HasDefault: true, Default: false},
	}, 1)

	invalid, _, fields, _ := relationState(t, pool, edge.ID)
	if invalid {
		t.Fatal("an edge missing a field that has a default still fits the schema")
	}
	if strings.Contains(fields, "hidden") {
		t.Fatalf("the sweep back-filled the default: %s", fields)
	}
}

// TestAnEdgeSchemaChangeClearsTheFlagWhenTheEdgeFitsAgain is the edge half
// of TestSchemaChangeClearsTheFlagWhenTheRowFitsAgain. Declaring the
// missing field is how a designer fixes the flag, so the sweep has to
// clear it as readily as it sets it — a sweep that only ever set the flag
// would leave a list of things to fix that never empties.
func TestAnEdgeSchemaChangeClearsTheFlagWhenTheEdgeFitsAgain(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedEdgeWorld(t, svc, project, metamodel.Schema{
		{Key: "note", Type: metamodel.FieldText},
	})

	edge, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Fields:  map[string]any{"note": "chain"},
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	narrowRequires(t, svc, project, nil, 1)
	if invalid, _, _, _ := relationState(t, pool, edge.ID); !invalid {
		t.Fatal("the edge must be flagged while note is undeclared")
	}

	narrowRequires(t, svc, project, metamodel.Schema{
		{Key: "note", Type: metamodel.FieldText},
	}, 2)
	if invalid, _, _, _ := relationState(t, pool, edge.ID); invalid {
		t.Fatal("the edge fits the widened schema and must not stay flagged")
	}
}

// TestRewritingAFlaggedEdgeClearsItsFlag pins the write path's half of
// the rule: UpsertRelation resets `invalid` to false because the values
// it just wrote were validated against the type's current schema, exactly
// as UpsertEntity does.
//
// Without it a designer who fixed a flagged edge would be told it is
// still broken, forever, and the only way back would be to delete and
// re-create it.
func TestRewritingAFlaggedEdgeClearsItsFlag(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedEdgeWorld(t, svc, project, metamodel.Schema{
		{Key: "note", Type: metamodel.FieldText},
		{Key: "difficulty", Type: metamodel.FieldNumber},
	})

	in := metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Fields:  map[string]any{"note": "chain", "difficulty": 3},
	}
	edge, err := svc.UpsertRelation(ctx, project, in)
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}
	narrowRequires(t, svc, project, metamodel.Schema{
		{Key: "note", Type: metamodel.FieldText},
	}, 1)
	if invalid, _, _, _ := relationState(t, pool, edge.ID); !invalid {
		t.Fatal("the edge must be flagged before the rewrite, or this test proves nothing")
	}

	fixed := withVersion(in, edge.Version)
	fixed.Fields = map[string]any{"note": "chain"}
	rewritten, err := svc.UpsertRelation(ctx, project, fixed)
	if err != nil {
		t.Fatalf("rewrite the flagged edge: %v", err)
	}
	if rewritten.Invalid {
		t.Fatal("the returned row still carries the flag after a validated write")
	}
	if invalid, _, _, _ := relationState(t, pool, edge.ID); invalid {
		t.Fatal("a rewritten edge that fits its schema must not stay flagged")
	}
}

// TestInvalidEdgesAreFindableThroughTheListing is the read half of the
// flag, and it is the half a write-only column ships without.
//
// An agent that has just narrowed a relation type has to be able to ask
// "which edges did that break", the same way it asks it of entities. All
// three states of the filter are asserted, because a filter that ignored
// its argument would satisfy any one of them on its own.
func TestInvalidEdgesAreFindableThroughTheListing(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedEdgeWorld(t, svc, project, metamodel.Schema{
		{Key: "note", Type: metamodel.FieldText},
		{Key: "difficulty", Type: metamodel.FieldNumber},
	})

	broken, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Fields:  map[string]any{"note": "chain", "difficulty": 3},
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}
	intact, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Fields:  map[string]any{"note": "fine"},
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}
	narrowRequires(t, svc, project, metamodel.Schema{
		{Key: "note", Type: metamodel.FieldText},
	}, 1)

	yes, no := true, false
	for _, tc := range []struct {
		name   string
		filter *bool
		want   []uuid.UUID
	}{
		{"no opinion", nil, []uuid.UUID{broken.ID, intact.ID}},
		{"only the broken ones", &yes, []uuid.UUID{broken.ID}},
		{"only the intact ones", &no, []uuid.UUID{intact.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{Invalid: tc.filter})
			if err != nil {
				t.Fatalf("ListRelations: %v", err)
			}
			got := make(map[uuid.UUID]bool, len(page.Relations))
			for _, row := range page.Relations {
				got[row.ID] = true
			}
			if len(got) != len(tc.want) {
				t.Fatalf("listed %d edges, want %d", len(got), len(tc.want))
			}
			for _, id := range tc.want {
				if !got[id] {
					t.Fatalf("edge %s is missing from the listing", id)
				}
			}
		})
	}
}

// TestARelationsCursorCannotCrossTheInvalidFilter pins that the invalid
// filter is part of the listing's cursor fingerprint, exactly as it is on
// the two entity listings.
//
// Every filter of one listing shares one sort order, so a cursor carried
// from "the broken edges" to "all edges" would page perfectly and answer
// a different question. A fingerprint that ignored the filter would leave
// this test's cursor accepted and this whole listing silently mixing two
// questions.
func TestARelationsCursorCannotCrossTheInvalidFilter(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedEdgeWorld(t, svc, project, nil)

	for _, pair := range [][2]string{
		{"kobold-camp", "hogger"}, {"hogger", "kobold-camp"},
	} {
		if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
			TypeKey: "requires",
			Source:  metamodel.Ref{TypeKey: "quest", Key: pair[0]},
			Target:  metamodel.Ref{TypeKey: "quest", Key: pair[1]},
		}); err != nil {
			t.Fatalf("UpsertRelation: %v", err)
		}
	}

	yes := true
	page, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{Limit: 1})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("the unfiltered listing issued no cursor, so there is nothing to carry")
	}
	_, err = svc.ListRelations(ctx, project, metamodel.RelationFilter{
		Invalid: &yes, Cursor: page.NextCursor, Limit: 1,
	})
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input: a cursor from the unfiltered listing "+
			"must not page the invalid one", err)
	}
}

// TestRelationCountsCarryTheInvalidTally pins the count a game summary
// reads. Until 0009 RelationCountsByType answered with a bare total and
// its doc comment argued that an edge could not be invalid; the number is
// what a designer's "what do I have to go and fix" is built from, and a
// summary answering it for entities alone answers it wrongly.
func TestRelationCountsCarryTheInvalidTally(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedEdgeWorld(t, svc, project, metamodel.Schema{
		{Key: "note", Type: metamodel.FieldText},
		{Key: "difficulty", Type: metamodel.FieldNumber},
	})

	typ, err := svc.RelationTypeByKey(ctx, project, "requires")
	if err != nil {
		t.Fatalf("RelationTypeByKey: %v", err)
	}
	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Fields:  map[string]any{"note": "chain", "difficulty": 3},
	}); err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}
	if _, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Fields:  map[string]any{"note": "fine"},
	}); err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	counts, err := svc.RelationCountsByType(ctx, project)
	if err != nil {
		t.Fatalf("RelationCountsByType: %v", err)
	}
	if got := counts[typ.ID]; got.Total != 2 || got.Invalid != 0 {
		t.Fatalf("before the schema edit: %+v, want {Total:2 Invalid:0}", got)
	}

	narrowRequires(t, svc, project, metamodel.Schema{
		{Key: "note", Type: metamodel.FieldText},
	}, 1)

	counts, err = svc.RelationCountsByType(ctx, project)
	if err != nil {
		t.Fatalf("RelationCountsByType: %v", err)
	}
	if got := counts[typ.ID]; got.Total != 2 || got.Invalid != 1 {
		t.Fatalf("after the schema edit: %+v, want {Total:2 Invalid:1}", got)
	}
}

// TestAnEdgeSweepDoesNotReachAnotherGamesEdges pins the project filter on
// the two statements the sweep runs. Two games declare the same relation
// type key and hold an edge each; narrowing one game's declaration must
// flag that game's edge and leave the other alone.
//
// The relation type ids differ between the games, so the type filter
// alone would already do it — which is exactly why this is asserted
// rather than assumed: the whole file's rule is that every statement
// carries the project, and a sweep is the one place a missing filter
// would rewrite content in a game the caller cannot see.
func TestAnEdgeSweepDoesNotReachAnotherGamesEdges(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine, theirs := newProject(t, pool), newProject(t, pool)
	edges := map[uuid.UUID]uuid.UUID{}
	for _, project := range []uuid.UUID{mine, theirs} {
		seedWorld(t, svc, project)
		seedEdgeWorld(t, svc, project, metamodel.Schema{
			{Key: "note", Type: metamodel.FieldText},
		})
		edge, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
			TypeKey: "requires",
			Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
			Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
			Fields:  map[string]any{"note": "chain"},
		})
		if err != nil {
			t.Fatalf("UpsertRelation: %v", err)
		}
		edges[project] = edge.ID
	}

	narrowRequires(t, svc, mine, nil, 1)

	if invalid, _, _, _ := relationState(t, pool, edges[mine]); !invalid {
		t.Fatal("the edited game's own edge was not flagged")
	}
	if invalid, _, _, _ := relationState(t, pool, edges[theirs]); invalid {
		t.Fatal("a schema edit in one game flagged another game's edge")
	}
}

// TestTheEdgeUpsertStatementRefusesAStaleVersion drives dbq.UpsertRelation
// directly, because nothing reachable through the service can observe the
// SQL guard on its own.
//
// upsertRelationWith takes a locked read first and refuses a stale
// version in Go, so every sequential caller is answered before the
// statement runs — measured, not assumed: making the DO UPDATE's
// `relations.version = expected_version` clause trivially true left
// TestConcurrentEditsToOneEdgesFieldsAreRefused green. The clause is not
// redundant, it is the half that survives a race: on the creation path
// there is nothing to lock, so two writers can both pass the Go check and
// only the guard stands between them. A guard nothing pins is a guard the
// next edit of this statement deletes.
func TestTheEdgeUpsertStatementRefusesAStaleVersion(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedEdgeWorld(t, svc, project, metamodel.Schema{{Key: "note", Type: metamodel.FieldText}})

	edge, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		Fields:  map[string]any{"note": "chain"},
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	q := dbq.New(pool)
	params := dbq.UpsertRelationParams{
		ProjectID:       project,
		RelationTypeID:  edge.RelationTypeID,
		SourceID:        edge.SourceID,
		TargetID:        edge.TargetID,
		Fields:          []byte(`{"note":"stale"}`),
		ExpectedVersion: edge.Version + 1,
	}
	if row, err := q.UpsertRelation(ctx, params); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("UpsertRelation returned %+v (err = %v) for a version that does not "+
			"match, want no rows", row, err)
	}
	if _, _, fields, _ := relationState(t, pool, edge.ID); fields != `{"note": "chain"}` {
		t.Fatalf("the refused statement wrote anyway: %s", fields)
	}

	// The positive control: the same statement with the true version does
	// land, so the refusal above is the guard and not some other clause.
	params.ExpectedVersion = edge.Version
	row, err := q.UpsertRelation(ctx, params)
	if err != nil {
		t.Fatalf("UpsertRelation with the true version: %v", err)
	}
	if row.Version != edge.Version+1 {
		t.Fatalf("version = %d, want %d", row.Version, edge.Version+1)
	}
}
