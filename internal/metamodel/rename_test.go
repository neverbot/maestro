package metamodel_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
)

// seedRenameableType declares one entity type and returns it.
func seedRenameableType(t *testing.T, svc *metamodel.Service, project uuid.UUID, key string) uuid.UUID {
	t.Helper()
	row, err := svc.UpsertEntityType(context.Background(), project, metamodel.EntityTypeInput{
		Key: key, Label: "Quest", LabelPlural: "Quests",
	})
	if err != nil {
		t.Fatalf("seed type %s: %v", key, err)
	}
	return row.ID
}

// rename is the call under test, with the version read for the caller so
// each test states only what it is about.
func rename(t *testing.T, svc *metamodel.Service, project uuid.UUID, from, to string) error {
	t.Helper()
	ctx := context.Background()
	row, err := svc.EntityTypeByKey(ctx, project, from)
	if err != nil {
		t.Fatalf("read %s before renaming it: %v", from, err)
	}
	_, err = svc.RenameEntityType(ctx, project, metamodel.RenameInput{
		From: from, To: to, ExpectedVersion: &row.Version,
	})
	return err
}

// TestARenamedTypeIsTheSameRowUnderANewKey is the whole point of the
// call, and it asserts the two halves separately because only one of
// them is new.
//
// The new half is that the *row* survives: the id is the one the type
// had, so every entity, every endpoint rule and every saved view
// reference that names this id still names this type. The workaround a
// rename replaces — declare the new type, move every entity, delete the
// old — is exactly the thing that cannot say that: it produces a new id
// and takes the history with the deleted row.
func TestARenamedTypeIsTheSameRowUnderANewKey(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	id := seedRenameableType(t, svc, project, "quest")

	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
	}); err != nil {
		t.Fatalf("seed an entity of the type: %v", err)
	}

	if err := rename(t, svc, project, "quest", "mission"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	moved, err := svc.EntityTypeByKey(ctx, project, "mission")
	if err != nil {
		t.Fatalf("read the renamed type: %v", err)
	}
	if moved.ID != id {
		t.Fatalf("the renamed type has id %s, want the id it already had (%s): a rename "+
			"that produced a new row would take every reference to the old one with it",
			moved.ID, id)
	}
	if moved.Version != 2 {
		t.Fatalf("Version = %d, want 2: a rename is a write and advances the version like "+
			"any other", moved.Version)
	}
	// The old key is free, which is the other half of "one row moved"
	// rather than "a second row appeared".
	if _, err := svc.EntityTypeByKey(ctx, project, "quest"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("reading the old key = %v, want not_found: the type moved, it was not copied", err)
	}

	// The entity is still an entity of this type, addressed under the
	// new type key. Nothing rewrote it: it points at the id.
	got, err := svc.EntityByKey(ctx, project, "mission", "hogger")
	if err != nil {
		t.Fatalf("read the entity under the new type key: %v", err)
	}
	if got.EntityTypeID != id {
		t.Fatalf("the entity names type %s, want %s", got.EntityTypeID, id)
	}
}

// TestARenamedTypeCarriesTheSpellingItWasGiven pins that the stored key
// is the caller's own spelling, verbatim. `to` is a value being stored,
// not an address being matched, and the catalogue neither folds it nor
// rewrites it — the same promise rowKeyPattern's comment makes about a
// declaration.
func TestARenamedTypeCarriesTheSpellingItWasGiven(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedRenameableType(t, svc, project, "quest")

	if err := rename(t, svc, project, "quest", "Main_Mission"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	row, err := svc.EntityTypeByKey(ctx, project, "main_mission")
	if err != nil {
		t.Fatalf("read the renamed type without regard to case: %v", err)
	}
	if row.Key != "Main_Mission" {
		t.Fatalf("Key = %q, want the spelling the rename asked for", row.Key)
	}
}

// TestARenameAddressesTheOldKeyWithoutRegardToCase pins that `from` is
// an address and not a value.
//
// Every other by-key read in this package matches case-insensitively,
// and a rename must too: a caller holding `Boss` from a listing and
// spelling it `boss` is addressing the same row. What it must *not* do
// is refuse it as a respelling the way the write path does — there is no
// spelling being stored on that side of the call, so there is nothing to
// refuse. Without this test, `from` could be matched exactly and every
// other test in this file would still pass.
func TestARenameAddressesTheOldKeyWithoutRegardToCase(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedRenameableType(t, svc, project, "Boss")

	if err := rename(t, svc, project, "boss", "elite"); err != nil {
		t.Fatalf("renaming Boss addressed as boss: %v", err)
	}
	if _, err := svc.EntityTypeByKey(ctx, project, "elite"); err != nil {
		t.Fatalf("read the renamed type: %v", err)
	}
}

// TestARenameOntoATakenKeyIsRefusedAtTheDestination pins the refusal and
// the shape it takes: invalid_input at `to`, not a version conflict.
//
// The distinction is the one every occupied-destination refusal in this
// repository makes: a conflict tells a caller to merge onto a version
// and try again, and no amount of retrying frees a key another type
// holds.
func TestARenameOntoATakenKeyIsRefusedAtTheDestination(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedRenameableType(t, svc, project, "quest")
	seedRenameableType(t, svc, project, "mission")

	err := rename(t, svc, project, "quest", "mission")
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input at the destination", err)
	}
	if !strings.Contains(err.Error(), "to:") || !strings.Contains(err.Error(), "never merges") {
		t.Fatalf("err = %v, want the refusal reported at `to` and saying a rename does "+
			"not merge two types", err)
	}
	// Both types are still there, untouched: the refusal rolled back.
	for _, key := range []string{"quest", "mission"} {
		row, err := svc.EntityTypeByKey(ctx, project, key)
		if err != nil {
			t.Fatalf("read %s after the refused rename: %v", key, err)
		}
		if row.Version != 1 {
			t.Fatalf("%s is at version %d, want 1: the refused rename moved a row", key, row.Version)
		}
	}
}

// TestARenameOntoADifferentlySpelledTakenKeyNamesTheStoredSpelling is
// the destination refusal's second half, and it is the one a caller
// cannot diagnose on its own: `Mission` is taken and `mission` looks
// free from the outside, because only the folding unique index knows
// they are one key. Naming the stored spelling is what turns "that is
// taken" into something the caller can check.
func TestARenameOntoADifferentlySpelledTakenKeyNamesTheStoredSpelling(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	project := newProject(t, pool)
	seedRenameableType(t, svc, project, "quest")
	seedRenameableType(t, svc, project, "Mission")

	err := rename(t, svc, project, "quest", "mission")
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input", err)
	}
	if !strings.Contains(err.Error(), `"Mission"`) {
		t.Fatalf("err = %v, want the stored spelling named", err)
	}
}

// TestACaseOnlyRenameIsRefusedAsOneAddress pins the decision
// renameToSameAddressError argues, and it is deliberately the same
// decision internal/markdown's Move made for a document path hours
// earlier and keyRespellingError made for a row key before that.
//
// entity_types_key_key is UNIQUE (project_id, lower(key)), so `quest` and
// `Quest` are one address written two ways. A rename between them changes
// no address at all: it rewrites a stored display string, advances a
// version and publishes an event announcing a change no reader can
// observe. All three refusals now agree.
func TestACaseOnlyRenameIsRefusedAsOneAddress(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedRenameableType(t, svc, project, "quest")

	err := rename(t, svc, project, "quest", "Quest")
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input", err)
	}
	if !strings.Contains(err.Error(), "capitalisation") {
		t.Fatalf("err = %v, want the refusal to say why the two spellings are one key", err)
	}
	row, err := svc.EntityTypeByKey(ctx, project, "quest")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.Key != "quest" || row.Version != 1 {
		t.Fatalf("the type is (%q, v%d), want the first spelling standing at version 1",
			row.Key, row.Version)
	}
}

// TestRenamingATypeToTheKeyItAlreadyHasIsRefused separates the identical
// case from the case-only one, because "differs only in capitalisation"
// is a confusing thing to tell a caller that sent one string twice.
func TestRenamingATypeToTheKeyItAlreadyHasIsRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	project := newProject(t, pool)
	seedRenameableType(t, svc, project, "quest")

	err := rename(t, svc, project, "quest", "quest")
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input", err)
	}
	if strings.Contains(err.Error(), "capitalisation") {
		t.Fatalf("err = %v, want the identical-key wording, not the case-only one", err)
	}
	if !strings.Contains(err.Error(), "changes nothing") {
		t.Fatalf("err = %v, want it to say the rename changes nothing", err)
	}
}

// TestRenamingATypeThisGameDoesNotHaveIsNotFound pins that the source is
// judged by existence before it is judged by version: a caller that
// mistyped `from` must not be told to merge onto a version.
func TestRenamingATypeThisGameDoesNotHaveIsNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	_, err := svc.RenameEntityType(ctx, project, metamodel.RenameInput{
		From: "quest", To: "mission", ExpectedVersion: ptrInt32(1),
	})
	if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want not_found", err)
	}
	if !strings.Contains(err.Error(), `"quest"`) {
		t.Fatalf("err = %v, want the key the caller sent named back", err)
	}
}

// TestARenameNeedsTheVersionItRead pins both halves of the version
// claim: absent is invalid_input at its own path, and wrong is a
// version_conflict carrying the number to merge onto.
//
// A rename is guarded for the reason a document move is: it advances the
// version, so an unguarded one would land on top of an edit the caller
// never read — and it would do it to the one column every other reader
// of this game spells out loud.
func TestARenameNeedsTheVersionItRead(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedRenameableType(t, svc, project, "quest")

	_, err := svc.RenameEntityType(ctx, project, metamodel.RenameInput{From: "quest", To: "mission"})
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input for the missing version", err)
	}
	if !strings.Contains(err.Error(), "expected_version") {
		t.Fatalf("err = %v, want the problem reported at expected_version", err)
	}

	_, err = svc.RenameEntityType(ctx, project, metamodel.RenameInput{
		From: "quest", To: "mission", ExpectedVersion: ptrInt32(7),
	})
	var conflict *metamodel.VersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want a version conflict", err)
	}
	if conflict.Current != 1 {
		t.Fatalf("Current = %d, want the version the write would have met", conflict.Current)
	}
	if _, err := svc.EntityTypeByKey(ctx, project, "quest"); err != nil {
		t.Fatalf("the refused rename moved the row: %v", err)
	}
}

// TestARenameJudgesBothKeysInOnePass pins the rule every multi-argument
// write here obeys: a caller that mistyped both ends hears about both,
// rather than fixing one, calling again and learning about the other.
func TestARenameJudgesBothKeysInOnePass(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	_, err := svc.RenameEntityType(ctx, project, metamodel.RenameInput{
		From: "-bad", To: "also bad", ExpectedVersion: ptrInt32(1),
	})
	var problems *metamodel.ValidationError
	if !errors.As(err, &problems) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	paths := map[string]bool{}
	for _, f := range problems.Fields {
		paths[f.Path] = true
	}
	if !paths["from"] || !paths["to"] {
		t.Fatalf("problems = %v, want one at `from` and one at `to`", problems.Fields)
	}
}

// TestTheCaseOnlyRefusalIsJudgedAfterTheKeysAreValidated pins the
// ordering renameToSameAddressError's comment argues, and it is the
// detail internal/markdown's Move records for exactly the same
// comparison: strings.EqualFold means what SQL's lower() means only
// because rowKeyPattern has already guaranteed both values are ASCII.
//
// A malformed pair that also folds together must therefore be reported
// as malformed. Moving the fold check above the validation turns this
// test red — the caller would be told its two keys are one address when
// neither is a key at all.
func TestTheCaseOnlyRefusalIsJudgedAfterTheKeysAreValidated(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	_, err := svc.RenameEntityType(ctx, project, metamodel.RenameInput{
		From: "not a key", To: "NOT A KEY", ExpectedVersion: ptrInt32(1),
	})
	var problems *metamodel.ValidationError
	if !errors.As(err, &problems) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	if strings.Contains(err.Error(), "capitalisation") {
		t.Fatalf("err = %v, want the two keys judged as keys before they are compared "+
			"as addresses", err)
	}
	if len(problems.Fields) != 2 {
		t.Fatalf("problems = %v, want both keys reported", problems.Fields)
	}
}

// TestARenamePublishesBothSpellings pins the payload, and the reason it
// carries two keys rather than one: a subscriber holding the type under
// its old key cannot act on an event that names only the new one.
func TestARenamePublishesBothSpellings(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)
	id := seedRenameableType(t, svc, project, "quest")

	// Subscribed after the declaration, so the only event this
	// subscription can see is the rename's. A viewer and a token caller,
	// because those are the two a wrong gating decision silently cuts out.
	viewer := hub.Subscribe(project, "viewer", false)
	defer hub.Unsubscribe(viewer)
	agent := hub.Subscribe(project, "editor", true)
	defer hub.Unsubscribe(agent)

	row, err := svc.EntityTypeByKey(ctx, project, "quest")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := svc.RenameEntityType(ctx, project, metamodel.RenameInput{
		From: "quest", To: "mission", ExpectedVersion: &row.Version,
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}

	for name, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
		got := receive(t, sub)
		if got.Kind != "type.renamed" {
			t.Fatalf("%s: Kind = %q, want type.renamed", name, got.Kind)
		}
		var payload struct {
			ID   uuid.UUID `json:"id"`
			From string    `json:"from"`
			To   string    `json:"to"`
		}
		raw, err := json.Marshal(got.Payload)
		if err != nil {
			t.Fatalf("%s: re-encode the payload: %v", name, err)
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("%s: decode the payload: %v", name, err)
		}
		if payload.ID != id || payload.From != "quest" || payload.To != "mission" {
			t.Fatalf("%s: payload = %+v, want the id and both spellings", name, payload)
		}
	}
}

// TestARefusedRenamePublishesNothing is the other half of the event
// contract, and it is what a publish inside the transaction would fail:
// a subscriber told a type was renamed and then finding it under its old
// key has no way to recover.
func TestARefusedRenamePublishesNothing(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)
	seedRenameableType(t, svc, project, "quest")
	seedRenameableType(t, svc, project, "mission")

	sub := hub.Subscribe(project, "owner", false)
	defer hub.Unsubscribe(sub)

	row, err := svc.EntityTypeByKey(ctx, project, "quest")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := svc.RenameEntityType(ctx, project, metamodel.RenameInput{
		From: "quest", To: "mission", ExpectedVersion: &row.Version,
	}); err == nil {
		t.Fatal("renaming onto a taken key must be refused")
	}
	requireNothing(t, sub, "a refused rename changes nothing")
}

// TestTwoOppositeRenamesDoNotDeadlock pins the lock ordering
// judgeRename applies, and it is the same rule and the same failure
// internal/markdown's lockBothEnds records for a document move.
//
// Renaming `a` to `b` while another caller renames `b` to `a` has each
// transaction wanting the row the other holds. Locking the two ends in
// from-then-to order closes the cycle and Postgres breaks it with
// SQLSTATE 40P01; locking them in folded-key order — a value both
// transactions compute the same way — removes it. One of the two calls
// wins and the other is refused, but neither may deadlock.
func TestTwoOppositeRenamesDoNotDeadlock(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedRenameableType(t, svc, project, "alpha")
	seedRenameableType(t, svc, project, "beta")

	// Enough rounds that an inverted order is red essentially every run:
	// with the ends locked from-then-to this loop deadlocks well inside
	// the first dozen iterations.
	for round := 0; round < 40; round++ {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		pairs := [2][2]string{{"alpha", "beta"}, {"beta", "alpha"}}
		for i, pair := range pairs {
			wg.Add(1)
			go func(i int, from, to string) {
				defer wg.Done()
				row, err := svc.EntityTypeByKey(ctx, project, from)
				if err != nil {
					errs[i] = err
					return
				}
				_, errs[i] = svc.RenameEntityType(ctx, project, metamodel.RenameInput{
					From: from, To: to, ExpectedVersion: &row.Version,
				})
			}(i, pair[0], pair[1])
		}
		wg.Wait()
		for i, err := range errs {
			if err == nil {
				t.Fatalf("round %d, call %d: a rename onto a key the other end holds must "+
					"be refused, not granted", round, i)
			}
			if strings.Contains(err.Error(), "40P01") || strings.Contains(err.Error(), "deadlock") {
				t.Fatalf("round %d, call %d: %v — two opposite renames must not deadlock; "+
					"judgeRename locks the two ends in folded-key order to prevent exactly this",
					round, i, err)
			}
		}
	}
}

// TestARelationTypeIsRenamedTheSameWay pins the twin, end to end.
//
// It is a separate call over a separate table, and the failure this
// repository names most often is a rule established correctly and not
// carried one step along — so the twin gets its own assertions rather
// than a comment claiming symmetry.
func TestARelationTypeIsRenamedTheSameWay(t *testing.T) {
	pool := testutil.NewPool(t)
	hub := realtime.NewHub()
	svc := metamodel.New(pool, hub)
	ctx := context.Background()
	project := newProject(t, pool)

	before, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "available_to", Label: "Available to",
	})
	if err != nil {
		t.Fatalf("seed relation type: %v", err)
	}
	sub := hub.Subscribe(project, "viewer", false)
	defer hub.Unsubscribe(sub)

	after, err := svc.RenameRelationType(ctx, project, metamodel.RenameInput{
		From: "available_to", To: "usable_by", ExpectedVersion: &before.Version,
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("the renamed relation type has id %s, want %s", after.ID, before.ID)
	}
	if after.Key != "usable_by" || after.Version != 2 {
		t.Fatalf("the renamed relation type is (%q, v%d), want (usable_by, v2)",
			after.Key, after.Version)
	}
	if _, err := svc.RelationTypeByKey(ctx, project, "available_to"); !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("reading the old key = %v, want not_found", err)
	}
	if got := receive(t, sub); got.Kind != "relation_type.renamed" {
		t.Fatalf("Kind = %q, want relation_type.renamed", got.Kind)
	}
}

// TestARelationTypeRenameObeysTheSameFourRefusals carries the twin one
// step further than "it works": each refusal is exercised on the
// relation-type side too, because a shared helper called from one of two
// call sites is a rule that holds in one of two places.
func TestARelationTypeRenameObeysTheSameFourRefusals(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)

	for _, key := range []string{"available_to", "requires"} {
		if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
			Key: key, Label: key,
		}); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}

	for name, tc := range map[string]struct {
		in   metamodel.RenameInput
		want error
	}{
		"a taken destination": {
			in:   metamodel.RenameInput{From: "available_to", To: "requires", ExpectedVersion: ptrInt32(1)},
			want: metamodel.ErrInvalidInput,
		},
		"a case-only respelling": {
			in:   metamodel.RenameInput{From: "available_to", To: "Available_To", ExpectedVersion: ptrInt32(1)},
			want: metamodel.ErrInvalidInput,
		},
		"a missing version": {
			in:   metamodel.RenameInput{From: "available_to", To: "usable_by"},
			want: metamodel.ErrInvalidInput,
		},
		"a stale version": {
			in:   metamodel.RenameInput{From: "available_to", To: "usable_by", ExpectedVersion: ptrInt32(9)},
			want: metamodel.ErrVersionConflict,
		},
		"an unknown source": {
			in:   metamodel.RenameInput{From: "rewards", To: "usable_by", ExpectedVersion: ptrInt32(1)},
			want: metamodel.ErrNotFound,
		},
	} {
		if _, err := svc.RenameRelationType(ctx, project, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

// TestARenameIsScopedToItsOwnGame pins the filter every statement in
// this domain carries. Two games declare `quest`; renaming one must not
// touch the other, and — because the rename is addressed by key rather
// than by id — a missing project filter would silently pick whichever
// row the index reached first.
func TestARenameIsScopedToItsOwnGame(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine := newProject(t, pool)
	theirs := newProject(t, pool)
	seedRenameableType(t, svc, mine, "quest")
	seedRenameableType(t, svc, theirs, "quest")

	if err := rename(t, svc, mine, "quest", "mission"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	row, err := svc.EntityTypeByKey(ctx, theirs, "quest")
	if err != nil {
		t.Fatalf("the other game's type was renamed out from under it: %v", err)
	}
	if row.Version != 1 {
		t.Fatalf("the other game's type is at version %d, want 1", row.Version)
	}
}
