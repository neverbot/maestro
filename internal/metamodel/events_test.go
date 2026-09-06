package metamodel_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
)

// receive takes the next event a subscription is delivered, or fails.
func receive(t *testing.T, sub *realtime.Subscription) realtime.Event {
	t.Helper()
	select {
	case got := <-sub.C:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		return realtime.Event{}
	}
}

// requireNothing asserts a subscription is delivered nothing within the
// window. The window is short deliberately: every test using it has
// already arranged for the thing it is waiting on to be blocked, so a
// longer wait would only slow the suite down without testing more.
func requireNothing(t *testing.T, sub *realtime.Subscription, why string) {
	t.Helper()
	select {
	case got := <-sub.C:
		t.Fatalf("%s, but %q was published: %+v", why, got.Kind, got.Payload)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestTypeEventsReachEveryMemberOfTheGameIncludingAgents pins the gating
// decision events.go records for type.upserted and type.removed:
// MinRole empty, HumanOnly false.
//
// Every test in this package but these ones builds the service with a nil
// hub, so before this file no event was ever observed at all — the gating
// fields could have held any value, or (as they did) not existed. The two
// subscribers here are the ones a wrong decision would have silently cut
// out: a viewer, who is excluded by any MinRole above viewer, and a token
// caller, who is excluded by HumanOnly regardless of role.
func TestTypeEventsReachEveryMemberOfTheGameIncludingAgents(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)

	viewer := hub.Subscribe(project, "viewer", false)
	defer hub.Unsubscribe(viewer)
	agent := hub.Subscribe(project, "editor", true)
	defer hub.Unsubscribe(agent)

	typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "Quest", Label: "Quest", LabelPlural: "Quests",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	for name, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
		got := receive(t, sub)
		if got.Kind != "type.upserted" {
			t.Fatalf("%s: Kind = %q, want type.upserted", name, got.Kind)
		}
		if got.ProjectID != project {
			t.Fatalf("%s: ProjectID = %v, want %v", name, got.ProjectID, project)
		}
		assertIdentityPayload(t, name, got, typ.ID, "Quest")
	}

	if err := svc.RemoveEntityType(ctx, project, typ.ID, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for name, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
		got := receive(t, sub)
		if got.Kind != "type.removed" {
			t.Fatalf("%s: Kind = %q, want type.removed", name, got.Kind)
		}
		// A removal announced with an empty key tells a client a type
		// keyed "" is gone, which is not a type it has ever seen.
		assertIdentityPayload(t, name, got, typ.ID, "Quest")
	}
}

// TestNoEventIsPublishedForARefusedWrite covers the refusals that never
// reach the database and the ones that reach it and are rolled back. An
// announcement is an instruction to re-read; announcing a change that did
// not happen makes a client re-read for nothing at best, and at worst
// teaches it that the value it already had is new.
func TestNoEventIsPublishedForARefusedWrite(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)

	sub := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(sub)

	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "malformed key", Label: "Quest", LabelPlural: "Quests",
	}); err == nil {
		t.Fatal("a malformed key must be refused")
	}
	requireNothing(t, sub, "a refused key wrote nothing")

	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := receive(t, sub); got.Kind != "type.upserted" {
		t.Fatalf("Kind = %q, want type.upserted", got.Kind)
	}

	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Renamed", LabelPlural: "Renamed",
		ExpectedVersion: ptrInt32(9),
	}); err == nil {
		t.Fatal("a stale version must be refused")
	}
	requireNothing(t, sub, "a version conflict wrote nothing")

	if err := svc.RemoveEntityType(ctx, project, uuid.New(), false); err == nil {
		t.Fatal("removing an unknown id must be refused")
	}
	requireNothing(t, sub, "removing an unknown type wrote nothing")
}

// TestNoEventIsPublishedWhenTheWriteIsRolledBack is the half of the
// publish-after-commit invariant that a rollback makes observable.
//
// The respelling refusal fires *after* the upsert statement has already
// written the row, inside the transaction, so this is a case where the
// database has seen the change and the caller still gets nothing. A
// publish moved inside the transaction, next to the write it announces,
// would announce a row that is about to vanish.
func TestNoEventIsPublishedWhenTheWriteIsRolledBack(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)

	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "Hogger", Label: "Hogger", LabelPlural: "Hoggers",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	sub := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(sub)

	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "hogger", Label: "MINE", LabelPlural: "MINE",
		ExpectedVersion: ptrInt32(1),
	}); err == nil {
		t.Fatal("a respelled key must be refused")
	}
	requireNothing(t, sub, "the refused write was rolled back")
}

// TestNothingIsAnnouncedWhileTheTransactionIsStillOpen is the other half,
// and the one that needs the transaction held open on purpose.
//
// A rival transaction takes a row lock on one of the type's entities.
// The schema edit under test writes its type row, then sweeps — and the
// sweep's UPDATE blocks on that lock, so the whole transaction sits open,
// past its own write, for as long as the test wants. Nothing may be on
// the wire in that window: a subscriber told now would re-read a database
// that still holds the old schema and cache that as the new one.
//
// What this test cannot pin is a publish placed as the very last
// statement inside withTx's callback, which differs from the correct
// placement only by the commit that immediately follows it: only a
// failing commit tells those two apart. That is not out of reach —
// TestNoEventIsPublishedWhenTheCommitFails makes the commit fail on
// demand with a deferred constraint — and this test covers every
// earlier placement, which is where a publish actually tends to drift
// to: next to the write it announces.
func TestNothingIsAnnouncedWhileTheTransactionIsStillOpen(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)

	typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}},
	})
	if err != nil {
		t.Fatalf("declare type: %v", err)
	}
	// The entity carries an undeclared field, so the sweep will flip it to
	// invalid — meaning the sweep really does issue the UPDATE that the
	// rival's lock blocks, rather than skipping it under the invalid <>
	// guard.
	entity := insertEntity(t, pool, project, typ.ID, "hogger", map[string]any{
		"min_level": 10, "summary": "x",
	})

	rival, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = rival.Rollback(ctx) }()
	var locked uuid.UUID
	if err := rival.QueryRow(ctx,
		`SELECT id FROM entities WHERE id = $1 FOR UPDATE`, entity).Scan(&locked); err != nil {
		t.Fatalf("rival lock: %v", err)
	}

	sub := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(sub)

	result := make(chan error, 1)
	go func() {
		_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
			Schema:          metamodel.Schema{{Key: "min_level", Type: metamodel.FieldNumber}},
			ExpectedVersion: ptrInt32(1),
		})
		result <- err
	}()

	requireNothing(t, sub, "the edit's transaction is still open on the sweep")
	select {
	case err := <-result:
		t.Fatalf("the upsert returned %v; the rival's lock should have held it in the sweep", err)
	default:
	}

	if err := rival.Rollback(ctx); err != nil {
		t.Fatalf("rival rollback: %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the upsert never returned after the rival released its lock")
	}
	if got := receive(t, sub); got.Kind != "type.upserted" {
		t.Fatalf("Kind = %q, want type.upserted", got.Kind)
	}
}

// assertIdentityPayload checks the {id, key} an event carries. The
// payload type is unexported, so the assertion goes through the JSON
// shape, which is the only thing a client ever sees and the only thing
// this package promises.
func assertIdentityPayload(t *testing.T, who string, e realtime.Event, wantID uuid.UUID, wantKey string) {
	t.Helper()
	raw, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatalf("%s: marshal payload: %v", who, err)
	}
	var got struct {
		ID  uuid.UUID `json:"id"`
		Key string    `json:"key"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: decode payload %s: %v", who, raw, err)
	}
	if got.ID != wantID {
		t.Fatalf("%s: payload id = %v, want %v", who, got.ID, wantID)
	}
	if got.Key != wantKey {
		t.Fatalf("%s: payload key = %q, want %q", who, got.Key, wantKey)
	}
}

// TestNoEventIsPublishedWhenTheCommitFails is the last placement
// the two tests above cannot reach: a publish sitting as the very last
// statement inside withTx's callback, which differs from the correct
// placement only by the commit that immediately follows it.
//
// It is reachable because testutil.NewPool hands every test its own
// throwaway database, so this test may install a constraint in it that
// no other test sees. A deferred foreign key from entity_types.id to
// projects.id is satisfied by nothing — an entity type's id is not a
// project id — but being DEFERRABLE INITIALLY DEFERRED it is checked at
// COMMIT and not before, so every statement inside the transaction
// succeeds and only the commit fails, with SQLSTATE 23503. That is
// exactly the window a last-statement publish would announce into: the
// database has accepted every write, and then thrown all of them away.
//
// The constraint is added while entity_types is empty, because ADD
// CONSTRAINT validates the rows already stored.
func TestNoEventIsPublishedWhenTheCommitFails(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)

	if _, err := pool.Exec(ctx,
		`ALTER TABLE entity_types ADD CONSTRAINT zz_fail_at_commit
		   FOREIGN KEY (id) REFERENCES projects (id) DEFERRABLE INITIALLY DEFERRED`); err != nil {
		t.Fatalf("install the deferred constraint: %v", err)
	}

	sub := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(sub)

	_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	})
	if err == nil {
		t.Fatal("the commit must fail under the deferred constraint")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("err = %v, want a deferred foreign-key violation at commit", err)
	}
	requireNothing(t, sub, "the transaction never committed")

	if _, err := svc.EntityTypeByKey(ctx, project, "quest"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("EntityTypeByKey = %v, want ErrNotFound: nothing was stored", err)
	}
}

// TestAPrunedEndpointListIsAnnouncedToTheRowsOwnSubscribers pins the
// fourth thing a removal changes.
//
// `RemoveEntityType` prunes the removed id out of every relation type's
// endpoint lists, which is a write to rows the caller never named. It is
// not covered by the cascade note events.go makes about edges: nothing
// is deleted here, the relation type is still there, and what changed is
// the rule it states. Announcing it as `relation_type.upserted` — the
// event a caller-visible edit of the same columns publishes — is what
// keeps Task 8's rendering of an endpoint rule from showing a list the
// database no longer holds. Nothing bumps `version`, so a subscriber
// diffing versions would not see it either.
//
// The relation type that names no removed type is the control: it is not
// touched, so it must not be announced.
func TestAPrunedEndpointListIsAnnouncedToTheRowsOwnSubscribers(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)

	zone, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "zone", Label: "Zone", LabelPlural: "Zones",
	})
	if err != nil {
		t.Fatalf("seed zone: %v", err)
	}
	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	}); err != nil {
		t.Fatalf("seed quest: %v", err)
	}
	takesPlaceIn, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in",
		SourceTypeKeys: []string{"quest"},
		TargetTypeKeys: []string{"zone"},
	})
	if err != nil {
		t.Fatalf("seed takes_place_in: %v", err)
	}
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "requires", Label: "requires",
		SourceTypeKeys: []string{"quest"},
		TargetTypeKeys: []string{"quest"},
	}); err != nil {
		t.Fatalf("seed requires: %v", err)
	}

	sub := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(sub)

	if err := svc.RemoveEntityType(ctx, project, zone.ID, true); err != nil {
		t.Fatalf("RemoveEntityType: %v", err)
	}

	removed := receive(t, sub)
	if removed.Kind != "type.removed" {
		t.Fatalf("first event = %q, want type.removed", removed.Kind)
	}
	assertIdentityPayload(t, "removal", removed, zone.ID, "zone")

	pruned := receive(t, sub)
	if pruned.Kind != "relation_type.upserted" {
		t.Fatalf("second event = %q, want relation_type.upserted for the pruned rule", pruned.Kind)
	}
	assertIdentityPayload(t, "prune", pruned, takesPlaceIn.ID, "takes_place_in")

	requireNothing(t, sub, "the relation type that never named zone was not touched")
}

// TestPrunedEndpointListsArePublishedInSortOrderNotDatabaseOrder closes
// the locking verification's finding 2: RemoveEntityType's
// `sort.Slice(pruned, …)` over `pruned[i].Key < pruned[j].Key` is
// defensive against RETURNING order the database does not promise, but
// nothing pinned it — the whole suite, including the test correction 23
// wrote for it, passes with the sort deleted, because
// PruneEntityTypeFromEndpointLists' actual plan today is an index scan
// on relation_types_key_key, which already returns rows in the order
// that index stores them.
//
// That index order is not this sort's order, either, which is the
// second half of the finding: the index orders by `lower(key)`, and this
// sort compares raw `Key` in byte order. Both are deterministic, so
// nothing here is a bug, but they disagree whenever a folded-lowercase
// and a byte-order comparison would put two keys in different places —
// exactly what an uppercase-led key next to a lowercase one guarantees.
// "AppleQuest" and "zone_rule" fold to "applequest" < "zone_rule" but
// compare raw as "AppleQuest" < "zone_rule" is also true — deliberately
// not the pair used here. This test instead uses "Zone_rel" and
// "apple_rel": raw byte order puts capital "Z" (0x5A) before lowercase
// "a" (0x61), so sort.Slice orders them [Zone_rel, apple_rel]; folded
// order reverses them, [apple_rel, Zone_rel], and a probe against this
// suite's own database confirmed the index scan's RETURNING arrives in
// exactly that folded order. The two are opposite sequences, so this
// test fails the moment either the sort is removed (the database's own
// order, folded, comes through instead) or the sort is changed to fold
// case (it would then agree with the database and stop proving the sort
// does anything). Removing `sort.Slice(pruned, …)` from RemoveEntityType
// was verified to turn this test red.
func TestPrunedEndpointListsArePublishedInSortOrderNotDatabaseOrder(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)

	zone, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "zone", Label: "Zone", LabelPlural: "Zones",
	})
	if err != nil {
		t.Fatalf("seed zone: %v", err)
	}

	// Declared in the order that would, if RemoveEntityType published
	// PruneEntityTypeFromEndpointLists' own RETURNING order unsorted,
	// arrive apple_rel-then-Zone_rel — the reverse of what the sort
	// guarantees.
	zoneRel, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "Zone_rel", Label: "Zone_rel", TargetTypeKeys: []string{"zone"},
	})
	if err != nil {
		t.Fatalf("seed Zone_rel: %v", err)
	}
	appleRel, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "apple_rel", Label: "apple_rel", TargetTypeKeys: []string{"zone"},
	})
	if err != nil {
		t.Fatalf("seed apple_rel: %v", err)
	}

	sub := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(sub)

	if err := svc.RemoveEntityType(ctx, project, zone.ID, true); err != nil {
		t.Fatalf("RemoveEntityType: %v", err)
	}

	removed := receive(t, sub)
	if removed.Kind != "type.removed" {
		t.Fatalf("first event = %q, want type.removed", removed.Kind)
	}

	// Byte order, not folded order: "Zone_rel" sorts before "apple_rel"
	// because 'Z' < 'a' as raw bytes, and RemoveEntityType's sort must
	// publish in that order for this to pass.
	first := receive(t, sub)
	assertIdentityPayload(t, "first prune event", first, zoneRel.ID, "Zone_rel")
	second := receive(t, sub)
	assertIdentityPayload(t, "second prune event", second, appleRel.ID, "apple_rel")
}
