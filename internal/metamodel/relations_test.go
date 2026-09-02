package metamodel_test

import (
	"bytes"
	"context"
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
		SourceTypeIDs: []uuid.UUID{quest.ID},
		TargetTypeIDs: []uuid.UUID{zone.ID},
		SemanticRole:  "spatial",
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
	_, err = svc.UpsertRelation(ctx, project, metamodel.RelationInput{
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
	second, err := svc.UpsertRelation(ctx, project, in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.ID != second.ID {
		t.Fatal("re-seeding an edge created a duplicate")
	}

	// An edge carries no version, so a second write of the same edge with
	// different fields is not a conflict: it is the operation, and the
	// last writer wins. RelationInput records that this is a decision.
	third, err := svc.UpsertRelation(ctx, project, metamodel.RelationInput{
		TypeKey: in.TypeKey, Source: in.Source, Target: in.Target,
		Fields: map[string]any{"note": "rewritten"},
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
	quest, err := svc.EntityTypeByKey(ctx, mine, "quest")
	if err != nil {
		t.Fatalf("quest type: %v", err)
	}

	_, err = svc.UpsertRelationType(ctx, mine, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeIDs: []uuid.UUID{quest.ID},
		TargetTypeIDs: []uuid.UUID{foreign.ID},
	})
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	requireFieldError(t, err, "target_type_ids[0]",
		"names no entity type of this game: "+foreign.ID.String())

	if _, err := svc.RelationTypeByKey(ctx, mine, "takes_place_in"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("the refused declaration must store nothing, got %v", err)
	}
}

// TestALockTimeoutOnTheEndpointCheckIsNotReportedAsInvalidInput closes
// the locking verification's finding 1.
//
// checkEndpointTypes now takes a FOR SHARE lock on every endpoint id it
// finds (correction 22, TestARelationTypeCreatedDuringATypeRemovalCannot…
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
		TargetTypeIDs: []uuid.UUID{zone.ID},
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

	hogger, err := svc.EntityByKey(ctx, project, "quest", "hogger")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if err := svc.RemoveEntity(ctx, project, hogger.ID); err != nil {
		t.Fatalf("RemoveEntity: %v", err)
	}

	rels, err := svc.ListRelations(ctx, project, metamodel.RelationFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("%d relations survived their entity", len(rels))
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
	if len(rels) != 0 {
		t.Fatalf("%d relations survived their cascaded type", len(rels))
	}
}

// TestRelationsAreScopedToTheirProject pins the isolation of the
// statements that address a relation by an id, and of the listing.
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
	edge, err := svc.UpsertRelation(ctx, mine, metamodel.RelationInput{
		TypeKey: "requires",
		Source:  metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "hogger"},
	})
	if err != nil {
		t.Fatalf("UpsertRelation: %v", err)
	}

	rels, err := svc.ListRelations(ctx, theirs, metamodel.RelationFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("another game's listing returned %d edges", len(rels))
	}
	// A leaked id is not enough: the removal filters on the project too.
	if err := svc.RemoveRelation(ctx, theirs, edge.ID); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := svc.RemoveRelation(ctx, mine, edge.ID); err != nil {
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
	if len(rels) != 0 {
		t.Fatalf("the refused write stored %d edges", len(rels))
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
		{"by source", metamodel.RelationFilter{SourceID: &kobold.ID}, 1},
		{"by target", metamodel.RelationFilter{TargetID: &hogger.ID}, 2},
		{"by type and target", metamodel.RelationFilter{TypeKey: "connects_to", TargetID: &hogger.ID}, 1},
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
			if len(rows) != tc.want {
				t.Fatalf("got %d edges, want %d", len(rows), tc.want)
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
	if len(rels) != 0 {
		t.Fatalf("%d edges survived a rolled-back atomic batch", len(rels))
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
	if len(rels) != 0 {
		t.Fatalf("%d edges landed from a batch refused whole", len(rels))
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

	for _, mode := range []metamodel.BulkMode{metamodel.BulkPartial, metamodel.BulkAtomic} {
		result, err := svc.UpsertRelations(ctx, project, []metamodel.RelationInput{
			{TypeKey: "requires", Source: hogger, Target: kobold},
		}, mode)
		if err != nil {
			t.Fatalf("%s batch: %v", mode, err)
		}
		if len(result.Succeeded) != 1 {
			t.Fatalf("%s batch landed %d edges, want 1", mode, len(result.Succeeded))
		}
		for name, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
			assertRelationPayload(t, name+" "+string(mode), receive(t, sub),
				"relation.upserted", result.Succeeded[0].ID, "Requires")
		}
	}

	if err := svc.RemoveRelation(ctx, project, edge.ID); err != nil {
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
			Key: "requires", Label: "mine", ExpectedVersion: ptrInt32(3),
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

// TestARelationTypeCreationRacingAnotherSpellingIsRefusedAfterTheWrite
// is the one path the locked pre-read cannot cover, and the only thing
// the post-write spelling check catches on its own: on the creation path
// there is nothing to lock, so a writer whose expected version happens
// to match the version the winner lands on passes both the read and the
// guarded DO UPDATE and updates a row it never saw, stored under a
// different spelling. The upsert returns the row it touched and key is
// not in the SET list, so comparing the stored spelling to the submitted
// one after the write closes it, and withTx rolls the write back.
func TestARelationTypeCreationRacingAnotherSpellingIsRefusedAfterTheWrite(t *testing.T) {
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

	result := make(chan error, 1)
	go func() {
		// Version 1 is what the rival's fresh row will carry, so the
		// guard passes and only the spelling is left to refuse this.
		_, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
			Key: "requires", Label: "mine", ExpectedVersion: ptrInt32(1),
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
		SourceTypeIDs: []uuid.UUID{quest.ID},
		TargetTypeIDs: []uuid.UUID{zone.ID},
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
		SourceTypeIDs:   stored.SourceTypeIds,
		TargetTypeIDs:   stored.TargetTypeIds,
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

// TestConcurrentEditsToOneEdgesFieldsAreLostSilently pins, deliberately,
// what the two linked decisions on RelationInput and UpsertRelation cost
// together.
//
// Neither is being reversed and this test is not a bug report: it is the
// evidence for a doc comment that would otherwise be an assertion. An
// edge carries no `version`, so there is no compare-and-set to refuse a
// stale write; and an edge's uniqueness index forbids parallel edges,
// which is what sends a game's multiplicity into the edge's own fields —
// the `passages: ["door", "vent"]` that UpsertRelation offers by name.
// Put together, the field a designer was told to use for multiplicity is
// the one field in the metamodel with no protection at all.
//
// Two writers extend one list here. What the test asserts is the whole
// loss: one row, the second write whole, no error, and two events a
// subscriber cannot tell apart — so nothing anywhere in the system
// records that a write was lost. If Task 7 adds `version` to `relations`
// this test goes red, which is the correct outcome: the decision it pins
// will have been reversed, and both doc comments need rewriting with it.
func TestConcurrentEditsToOneEdgesFieldsAreLostSilently(t *testing.T) {
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
	firstEvent := receive(t, sub)

	// The designer who was extending the same list at the same time, and
	// never read the first write. There is no ExpectedVersion to send.
	second, err := svc.UpsertRelation(ctx, project, edge("vent"))
	if err != nil {
		t.Fatalf("second write was refused, so edges are no longer last-writer-wins: %v", err)
	}
	secondEvent := receive(t, sub)

	if first.ID != second.ID {
		t.Fatalf("two row ids (%s, %s): parallel edges are no longer refused, and the "+
			"decision that pushes multiplicity into edge fields no longer holds", first.ID, second.ID)
	}
	if got, want := string(second.Fields), `{"passages": "vent"}`; got != want {
		t.Fatalf("fields = %s, want %s — the second write did not take the row whole", got, want)
	}

	// The silence is the point: the two events are identical, so a
	// subscriber watching this edge sees "it changed" twice and has
	// nothing that says the first change was overwritten unread.
	if firstEvent.Kind != secondEvent.Kind {
		t.Fatalf("kinds = %q, %q — a lost update is now announced differently",
			firstEvent.Kind, secondEvent.Kind)
	}
	firstJSON, err := json.Marshal(firstEvent.Payload)
	if err != nil {
		t.Fatalf("marshal first payload: %v", err)
	}
	secondJSON, err := json.Marshal(secondEvent.Payload)
	if err != nil {
		t.Fatalf("marshal second payload: %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("payloads differ (%s, %s): something now distinguishes the write that "+
			"was overwritten, and the doc comments claiming the loss is silent are stale",
			firstJSON, secondJSON)
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
		SourceTypeIDs: []uuid.UUID{quest.ID},
		TargetTypeIDs: []uuid.UUID{zone.ID},
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
