package metamodel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/testutil"
)

// The tests in this file are in the package rather than beside it
// because what they pin is not reachable from outside it: the mapping of
// a raw constraint violation, and the fact that a write runs against a
// transaction's own handle. Both are claims the package's doc comments
// make, and neither has a public path that can be driven to it.

// TestARaceOnAnEdgesParentsNamesWhichParentIsGone pins the mapping of the
// three composite foreign keys under `relations`.
//
// The race itself — a rival transaction deleting an endpoint between
// upsertRelationWith's lookup and its insert — cannot be staged from a
// test without a hook inside that function, because the lookups re-read
// under READ COMMITTED and would see the deletion themselves. What can be
// staged, and is what actually decides the outcome, is the error the
// database raises: each parent is given a missing id in turn, against the
// real schema, so the constraint names are the ones Postgres generates
// and not a guess.
func TestARaceOnAnEdgesParentsNamesWhichParentIsGone(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := New(pool, nil)
	ctx := context.Background()

	var project uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`,
		"azeroth-"+uuid.NewString()[:8]).Scan(&project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	if _, err := svc.UpsertEntityType(ctx, project, EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("UpsertEntityType: %v", err)
	}
	relType, err := svc.UpsertRelationType(ctx, project, RelationTypeInput{
		Key: "requires", Label: "requires",
	})
	if err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	source, err := svc.UpsertEntity(ctx, project, EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
	})
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	target, err := svc.UpsertEntity(ctx, project, EntityInput{
		TypeKey: "quest", Key: "kobold-camp", Name: "Kobold Camp",
	})
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	in := RelationInput{
		TypeKey: "requires",
		Source:  Ref{TypeKey: "quest", Key: "hogger"},
		Target:  Ref{TypeKey: "quest", Key: "kobold-camp"},
	}
	gone := uuid.New()

	for _, tc := range []struct {
		name   string
		params dbq.UpsertRelationParams
		want   string
	}{
		{
			name: "relation type",
			params: dbq.UpsertRelationParams{
				ProjectID: project, RelationTypeID: gone,
				SourceID: source.ID, TargetID: target.ID, Fields: []byte(`{}`),
			},
			want: `not_found: no relation type "requires" in this game`,
		},
		{
			name: "source",
			params: dbq.UpsertRelationParams{
				ProjectID: project, RelationTypeID: relType.ID,
				SourceID: gone, TargetID: target.ID, Fields: []byte(`{}`),
			},
			want: `not_found: source: no entity "hogger" of type "quest" in this game`,
		},
		{
			name: "target",
			params: dbq.UpsertRelationParams{
				ProjectID: project, RelationTypeID: relType.ID,
				SourceID: source.ID, TargetID: gone, Fields: []byte(`{}`),
			},
			want: `not_found: target: no entity "kobold-camp" of type "quest" in this game`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(ctx) }()

			_, err = dbq.New(tx).UpsertRelation(ctx, tc.params)
			if err == nil {
				t.Fatal("the insert was accepted with a parent that does not exist")
			}
			mapped := edgeParentViolation(err, in)
			if !errors.Is(mapped, ErrNotFound) {
				t.Fatalf("mapped = %v, want ErrNotFound", mapped)
			}
			if mapped.Error() != tc.want {
				t.Fatalf("message = %q, want %q", mapped.Error(), tc.want)
			}
		})
	}
}

// TestAnEdgeResolvesItsEndpointsAgainstItsOwnTransaction pins
// upsertRelationWith's central claim: it takes a queries handle so that a
// caller writing entities and the edges between them in one transaction
// sees rows that transaction has written and not yet committed.
//
// No public caller can reach this yet — UpsertRelations writes edges and
// nothing else, so the external
// TestAnAtomicRelationBatchLandsEveryEdgeOfTheBatch pre-seeds both
// endpoints through committed calls and would stay green with every
// lookup routed through the pool. Task 9's seeding of a whole game is
// where a public path arrives. Until then the claim is pinned here, at
// the only level where it is true: the entity below is invisible to any
// other connection while this test runs.
func TestAnEdgeResolvesItsEndpointsAgainstItsOwnTransaction(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := New(pool, nil)
	ctx := context.Background()

	var project uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`,
		"azeroth-"+uuid.NewString()[:8]).Scan(&project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := svc.UpsertEntityType(ctx, project, EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("UpsertEntityType: %v", err)
	}
	if _, err := svc.UpsertRelationType(ctx, project, RelationTypeInput{
		Key: "requires", Label: "requires",
	}); err != nil {
		t.Fatalf("UpsertRelationType: %v", err)
	}
	if _, err := svc.UpsertEntity(ctx, project, EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
	}); err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}

	err := svc.withTx(ctx, func(q *dbq.Queries) error {
		// Written inside this transaction and committed nowhere: an edge
		// resolving its endpoints against the pool cannot see it.
		if _, err := svc.upsertEntityWith(ctx, q, project, EntityInput{
			TypeKey: "quest", Key: "kobold-camp", Name: "Kobold Camp",
		}); err != nil {
			return err
		}
		_, err := svc.upsertRelationWith(ctx, q, project, RelationInput{
			TypeKey: "requires",
			Source:  Ref{TypeKey: "quest", Key: "hogger"},
			Target:  Ref{TypeKey: "quest", Key: "kobold-camp"},
		})
		return err
	})
	if err != nil {
		t.Fatalf("an edge could not see the entity its own transaction wrote: %v", err)
	}
}

// TestTheGenericNotFoundIsNotUsedWhereACallerSuppliedAKey is a reminder
// in test form: the three by-key accessors name what they did not find,
// because their caller supplied a key and a bare "not_found" tells it
// nothing about which of the keys it sent was wrong.
func TestTheGenericNotFoundIsNotUsedWhereACallerSuppliedAKey(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := New(pool, nil)
	ctx := context.Background()

	var project uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`,
		"azeroth-"+uuid.NewString()[:8]).Scan(&project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := svc.UpsertEntityType(ctx, project, EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("UpsertEntityType: %v", err)
	}

	for _, tc := range []struct {
		name string
		call func() error
		want string
	}{
		{"entity type", func() error {
			_, err := svc.EntityTypeByKey(ctx, project, "nosuch")
			return err
		}, `not_found: no entity type "nosuch" in this game`},
		{"relation type", func() error {
			_, err := svc.RelationTypeByKey(ctx, project, "nosuch")
			return err
		}, `not_found: no relation type "nosuch" in this game`},
		{"entity", func() error {
			_, err := svc.EntityByKey(ctx, project, "quest", "nosuch")
			return err
		}, `not_found: no entity "nosuch" of type "quest" in this game`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			if err.Error() != tc.want {
				t.Fatalf("message = %q, want %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "nosuch") {
				t.Fatalf("message %q does not name the key the caller sent", err.Error())
			}
		})
	}
}
