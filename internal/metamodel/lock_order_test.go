package metamodel_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// holdEntityTypeRow opens a transaction on a connection of the test's own
// and takes the named row lock on one entity_types row, holding it until
// the returned function is called.
//
// It stands in for the *first* lock of whichever writer the test is not
// running: FOR KEY SHARE is what UpsertEntity takes on the type it is
// writing into, FOR UPDATE is what RemoveEntityType takes on the type it
// is about to delete. Staging one side as a plain SQL lock is what makes
// these tests deterministic — the writer under test parks against a lock
// that is already held, instead of two goroutines being raced and hoping.
func holdEntityTypeRow(t *testing.T, conn *pgx.Conn, id uuid.UUID, mode string) (release func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the holding transaction: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_ = tx.Rollback(ctx)
		}
	})
	if _, err := tx.Exec(ctx,
		"SELECT id FROM entity_types WHERE id = $1 "+mode, id); err != nil {
		t.Fatalf("hold entity_types %s: %v", mode, err)
	}
	return func() {
		released = true
		if err := tx.Rollback(ctx); err != nil {
			t.Fatalf("release the held entity type row: %v", err)
		}
	}
}

// entityRowIsUnlocked reports whether one entity row can be locked right
// now, from a connection of the test's own.
//
// This is the assertion both staged tests turn on, and it is the only
// thing that tells a *correct* lock order from a merely blocked writer:
// a writer parked on the entity type's row has taken no entity lock yet,
// so this succeeds; a writer that locked its entity row first and then
// went to the type is holding exactly what a cascading removal needs,
// which is the cycle. NOWAIT rather than a timeout so the answer is
// immediate and cannot itself join the wait graph.
func entityRowIsUnlocked(t *testing.T, conn *pgx.Conn, id uuid.UUID) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the probing transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, "SELECT id FROM entities WHERE id = $1 FOR UPDATE NOWAIT", id)
	if err == nil {
		return true
	}
	// 55P03 is lock_not_available, which is what NOWAIT raises when
	// somebody else holds the row. Anything else is a real failure and
	// must not be read as "locked".
	if strings.Contains(err.Error(), "55P03") {
		return false
	}
	t.Fatalf("probe the entity row: %v", err)
	return false
}

// seedTypeAndEntity declares a type with no required fields and writes
// one entity into it, returning both ids and the entity's version.
func seedTypeAndEntity(t *testing.T, svc *metamodel.Service, project uuid.UUID) (typeID, entityID uuid.UUID, version int32) {
	t.Helper()
	ctx := context.Background()
	typ, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
	})
	if err != nil {
		t.Fatalf("seed quest type: %v", err)
	}
	row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
	})
	if err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	return typ.ID, row.ID, row.Version
}

// TestAnEntityWriteTakesTheTypeRowBeforeItsOwnRow pins one half of the
// lock order that keeps UpsertEntity out of a cycle with
// RemoveEntityType(cascade).
//
// UpsertEntity ends in an INSERT ... ON CONFLICT whose foreign key takes
// FOR KEY SHARE on the entity type's row no matter what. The question is
// only *when*: taken after GetEntityByKeyForUpdate, the write holds an
// entity row while it waits for the type, which is the other side of the
// removal's own wait — measured at 15 deadlocks in 15 seconds across 8
// workers. GetEntityTypeByKeyForKeyShare moves it in front.
//
// The staging holds the type row FOR UPDATE, which is what a removal
// holds, and then asks whether the parked write is sitting on its entity
// row. Under the correct order it is not: it never got that far.
func TestAnEntityWriteTakesTheTypeRowBeforeItsOwnRow(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	project := newProject(t, pool)
	typeID, entityID, version := seedTypeAndEntity(t, svc, project)

	holder := standaloneConn(t, pool)
	probe := standaloneConn(t, pool)
	watcher := standaloneConn(t, pool)

	release := holdEntityTypeRow(t, holder, typeID, "FOR UPDATE")

	upsertErr := make(chan error, 1)
	go func() {
		_, err := svc.UpsertEntity(context.Background(), project, metamodel.EntityInput{
			TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger, again",
			ExpectedVersion: &version,
		})
		upsertErr <- err
	}()

	waitFor(t, "the entity write to park on the type row", func() bool {
		return lockWaiters(t, watcher) >= 1
	})
	if !entityRowIsUnlocked(t, probe, entityID) {
		t.Fatal("the entity write is holding its own row while it waits for the entity type: " +
			"that is the lock order RemoveEntityType deadlocks against")
	}

	release()
	if err := <-upsertErr; err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
}

// TestACascadingRemovalTakesTheTypeRowBeforeAnyEntity pins the other
// half. Both writers have to take entity_types first or the order is
// still inverted, and reverting only this half puts the deadlocks back
// (measured: 16 in 15 seconds with the entity write already fixed).
//
// The staging holds the type row FOR KEY SHARE, which is what an entity
// write holds, and then asks whether the parked removal has already
// locked the entities it is going to delete. Under the correct order it
// has not: GetEntityTypeByIDForUpdate parks it before DeleteEntitiesOfType
// runs at all.
func TestACascadingRemovalTakesTheTypeRowBeforeAnyEntity(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	project := newProject(t, pool)
	typeID, entityID, _ := seedTypeAndEntity(t, svc, project)

	holder := standaloneConn(t, pool)
	probe := standaloneConn(t, pool)
	watcher := standaloneConn(t, pool)

	release := holdEntityTypeRow(t, holder, typeID, "FOR KEY SHARE")

	removeErr := make(chan error, 1)
	go func() {
		removeErr <- svc.RemoveEntityType(context.Background(), project, typeID, true)
	}()

	waitFor(t, "the removal to park on the type row", func() bool {
		return lockWaiters(t, watcher) >= 1
	})
	if !entityRowIsUnlocked(t, probe, entityID) {
		t.Fatal("the removal has locked an entity before reaching the entity type: " +
			"that is the lock order UpsertEntity deadlocks against")
	}

	release()
	if err := <-removeErr; err != nil {
		t.Fatalf("RemoveEntityType: %v", err)
	}
	if entityTypeExists(t, watcher, typeID) {
		t.Fatal("the type is still there after a removal that reported success")
	}
}

// TestUpsertEntityAndACascadingRemovalDoNotDeadlock is the harness the
// defect was filed with, kept and shortened.
//
// The two staged tests above pin the *rule*; this one measures the
// symptom, because a rule can be stated correctly and still leave a
// third statement crossing it. Eight workers alternate entity writes
// over a small shared key space against repeated
// RemoveEntityType(cascade), which is the operation pair the original
// bisection isolated (entity+entityType 0, entityType+removal 0,
// entity+removal 8 in 15 seconds).
//
// **It is a stress test and it runs by default, deliberately.** Three
// seconds is enough to be red every time the order is wrong — reverting
// either half of the fix produced 3, 3, 3, 5 and 7 deadlocks over five
// runs of exactly this shape — and cheap enough that no gate is needed
// to keep it out of an ordinary `go test ./...`. A gated test is a test
// that stops running, and this repository has no gate to hang it on
// anyway. What it cannot claim is a *proof* of absence, which is what
// the two staged tests are for.
//
// It asserts on the traffic as well as on the failures: a race that
// silently stopped racing — a type that never came back, an upsert that
// refused every time — would otherwise report zero deadlocks and pass
// for the wrong reason.
func TestUpsertEntityAndACascadingRemovalDoNotDeadlock(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	const workers = 8
	deadline := time.Now().Add(3 * time.Second)

	var writes, removals atomic.Int64
	var mu sync.Mutex
	unexpected := map[string]int{}
	note := func(op string, err error) {
		if err == nil {
			return
		}
		// A write racing a removal legitimately meets these: the type is
		// gone, or another worker moved the row first. They are the
		// answers this call is supposed to give, and they are not what
		// this test measures.
		if errors.Is(err, metamodel.ErrNotFound) || errors.Is(err, metamodel.ErrVersionConflict) ||
			errors.Is(err, metamodel.ErrInUse) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		unexpected[op+": "+err.Error()]++
	}

	ensureType := func() {
		_, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
			Key: "quest", Label: "Quest", LabelPlural: "Quests",
		})
		note("upsert type", err)
	}
	ensureType()

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				if w%2 == 0 {
					// A small shared key space, so writers meet each
					// other on the entity rows as well as meeting the
					// removals on the type.
					key := fmt.Sprintf("q-%d", i%4)
					in := metamodel.EntityInput{TypeKey: "quest", Key: key, Name: "Quest"}
					if row, err := svc.EntityByKey(ctx, project, "quest", key); err == nil {
						v := row.Version
						in.ExpectedVersion = &v
					}
					if _, err := svc.UpsertEntity(ctx, project, in); err != nil {
						note("upsert entity", err)
						continue
					}
					writes.Add(1)
					continue
				}
				typ, err := svc.EntityTypeByKey(ctx, project, "quest")
				if err != nil {
					note("read type", err)
					ensureType()
					continue
				}
				if err := svc.RemoveEntityType(ctx, project, typ.ID, true); err != nil {
					note("remove type", err)
				} else {
					removals.Add(1)
				}
				ensureType()
			}
		}(w)
	}
	wg.Wait()

	if len(unexpected) > 0 {
		for what, n := range unexpected {
			t.Errorf("%d x %s", n, what)
		}
		t.Fatal("entity writes racing a cascading type removal must not fail this way; " +
			"a deadlock (SQLSTATE 40P01) here means the two writers no longer take " +
			"entity_types before entities")
	}
	// The race has to have actually happened. Both numbers being large
	// is what says the workers were contending rather than, say, every
	// write failing against a type that was never recreated.
	if writes.Load() < 50 || removals.Load() < 50 {
		t.Fatalf("the workers barely raced: %d entity writes, %d removals",
			writes.Load(), removals.Load())
	}
}
