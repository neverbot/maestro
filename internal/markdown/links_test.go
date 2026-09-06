package markdown_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
)

// newEntityOfType declares an entity type in the game and seeds one
// entity of it. It goes through the metamodel service rather than a raw
// INSERT because the entity must be a real row with a real search
// vector — a fixture written by hand bypasses the application-computed
// column and Task 9's search test would silently find nothing.
//
// A repeated type declaration comes back as a version conflict (the
// metamodel refuses an upsert against an existing row that carries no
// expected version) and is ignored: the helper's job is "make sure this
// type is there", and every call after the first has already done it.
func newEntityOfType(t *testing.T, entities *metamodel.Service, game uuid.UUID,
	typeKey, label, key, name string,
) {
	t.Helper()
	ctx := context.Background()
	if _, err := entities.UpsertEntityType(ctx, game, metamodel.EntityTypeInput{
		Key: typeKey, Label: label, LabelPlural: label + "s",
	}); err != nil && !errors.Is(err, metamodel.ErrVersionConflict) {
		t.Fatalf("declare %s type: %v", typeKey, err)
	}
	if _, err := entities.UpsertEntity(ctx, game, metamodel.EntityInput{
		TypeKey: typeKey, Key: key, Name: name,
	}); err != nil {
		t.Fatalf("seed %s %s: %v", typeKey, key, err)
	}
}

// newQuest is newEntityOfType for the type every test here uses.
func newQuest(t *testing.T, entities *metamodel.Service, game uuid.UUID, key, name string) {
	t.Helper()
	newEntityOfType(t, entities, game, "quest", "Quest", key, name)
}

// seedDoc writes one document at version 1, for the tests whose subject
// is the link rather than the write.
func seedDoc(t *testing.T, svc *markdown.Service, game uuid.UUID, path string) {
	t.Helper()
	if _, err := svc.Write(context.Background(), game, markdown.WriteInput{
		Path: path, Content: "x\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestAResurrectedDocumentKeepsItsIdAndItsLinks pins the *asymmetry*
// between this domain and internal/metamodel, from the side that has to
// stay different.
//
// internal/metamodel refuses a version claim against a row that is not
// there (metamodel.RemovedError), because its removals are hard: the row
// that comes back carries a new id and every relation, position,
// saved-view reference and attachment that named the old one goes on
// naming nothing. This domain deliberately does **not** adopt that rule
// for a deleted path, and the reason is exactly what this test asserts:
// the tombstone is the same row, so a write to the path continues the
// document — same id, same links, history unbroken — and nothing a
// caller would mourn is lost.
//
// **It is here to stop the two being harmonised.** A reader who notices
// that markdown accepts a claim the metamodel refuses will look for the
// reason, and the reason is a behaviour, not a paragraph: turning this
// test red is what a "consistency" fix would do first.
//
// It also pins the half the two domains *do* share: a claim against a
// path this game has never had is refused here too
// (TestExpectingAVersionOfADocumentThatDoesNotExistIsNotFound), so the
// two agree wherever the row is gone in every sense.
func TestAResurrectedDocumentKeepsItsIdAndItsLinks(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")

	before, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "scripts/wanted-hogger", Content: "one\n", ExpectedVersion: ptrInt32(0),
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "scripts/wanted-hogger", EntityType: "quest", EntityKey: "wanted-hogger",
		Role: "script",
	}); err != nil {
		t.Fatalf("LinkAdd: %v", err)
	}
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "scripts/wanted-hogger", ExpectedVersion: &before.CurrentVersion,
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// The claim the metamodel refuses: a version stated for a path whose
	// document was deleted. Here it is the resurrection, by design.
	after, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "scripts/wanted-hogger", Content: "back again\n", ExpectedVersion: ptrInt32(2),
	})
	if err != nil {
		t.Fatalf("a write to a deleted path must bring the document back: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("the resurrected document has id %s, want the id it already had (%s): "+
			"the whole reason this domain accepts the claim the metamodel refuses is that "+
			"the row survives its own deletion", after.ID, before.ID)
	}
	links, err := docLinks(svc, ctx, game, "scripts/wanted-hogger")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(links) != 1 || links[0].EntityKey != "wanted-hogger" {
		t.Fatalf("links after the resurrection = %+v, want the attachment still there: an "+
			"association the metamodel's own resurrection would have lost", links)
	}
}

func TestALinkAttachesADocumentToAnEntityAndReadsBackFromBothSides(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "scripts/wanted-hogger", Content: "HOGGER: *snarls*\n", ExpectedVersion: ptrInt32(0),
		Kind: ptrString("script"),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "scripts/wanted-hogger", EntityType: "quest", EntityKey: "wanted-hogger",
		Role: "script",
	}); err != nil {
		t.Fatalf("LinkAdd: %v", err)
	}

	// From the document.
	fromDoc, err := docLinks(svc, ctx, game, "scripts/wanted-hogger")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(fromDoc) != 1 || fromDoc[0].EntityKey != "wanted-hogger" || fromDoc[0].Role != "script" {
		t.Fatalf("links from the document = %+v, want one script link to wanted-hogger", fromDoc)
	}
	if fromDoc[0].EntityName != "Wanted: Hogger" || fromDoc[0].EntityTypeKey != "quest" {
		t.Fatalf("document-side link = %+v, want the entity's type key and name so a UI need not fetch them",
			fromDoc[0])
	}
	if fromDoc[0].EntityID == uuid.Nil {
		t.Fatal("EntityID = zero, want the entity's own id")
	}

	// From the entity. This is the read-back that matters most: it is
	// how the UI builds a quest page and how an agent finds the script
	// from the quest instead of guessing a path.
	fromEntity, err := entityLinks(svc, ctx, game, "quest", "wanted-hogger")
	if err != nil {
		t.Fatalf("LinksByEntity: %v", err)
	}
	if len(fromEntity) != 1 || fromEntity[0].Path != "scripts/wanted-hogger" {
		t.Fatalf("links from the entity = %+v, want the script", fromEntity)
	}
	if fromEntity[0].Title != "scripts/wanted-hogger" || fromEntity[0].Role != "script" {
		t.Fatalf("entity-side link = %+v, want the document's title and the role", fromEntity[0])
	}
	if fromEntity[0].Kind != "script" || fromEntity[0].DocumentID == uuid.Nil {
		t.Fatalf("entity-side link = %+v, want the document's kind and id", fromEntity[0])
	}
}

// TestADocumentIsAttachedToSeveralEntitiesAndListedInAStableOrder is the
// case the many-to-many table was chosen for — one piece of lore that
// describes a faction and a city — plus the ordering the listing
// promises, which no single-link test can see.
func TestADocumentIsAttachedToSeveralEntitiesAndListedInAStableOrder(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	newQuest(t, entities, game, "the-defias", "The Defias Brotherhood")
	newEntityOfType(t, entities, game, "faction", "Faction", "defias", "Defias Brotherhood")

	seedDoc(t, svc, game, "lore/westfall")
	for _, target := range []markdown.LinkTarget{
		{EntityType: "quest", EntityKey: "wanted-hogger", Role: "background"},
		{EntityType: "faction", EntityKey: "defias", Role: "lore"},
		{EntityType: "quest", EntityKey: "the-defias", Role: "background"},
	} {
		if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
			Path: "lore/westfall", EntityType: target.EntityType,
			EntityKey: target.EntityKey, Role: target.Role,
		}); err != nil {
			t.Fatalf("LinkAdd %s: %v", target.EntityKey, err)
		}
	}

	links, err := docLinks(svc, ctx, game, "lore/westfall")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	// Type key, then entity key: faction before quest, and inside quest
	// the-defias before wanted-hogger.
	var got []string
	for _, link := range links {
		got = append(got, link.EntityTypeKey+"/"+link.EntityKey)
	}
	want := []string{"faction/defias", "quest/the-defias", "quest/wanted-hogger"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("links = %v, want %v", got, want)
	}
}

// TestADocumentWithNoAttachmentsIsAnOrdinaryDocument is the other end of
// the same decision: a game bible is attached to nothing, and the
// listing answers an empty slice rather than nil, so the wire carries []
// and not null.
func TestADocumentWithNoAttachmentsIsAnOrdinaryDocument(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	seedDoc(t, svc, game, "bible")

	links, err := docLinks(svc, ctx, game, "bible")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if links == nil || len(links) != 0 {
		t.Fatalf("links = %#v, want an empty non-nil slice", links)
	}
	encoded, err := json.Marshal(links)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("encoded = %s, want []", encoded)
	}
}

func TestReAddingALinkUpdatesItsRoleRatherThanDuplicatingIt(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")

	for _, role := range []string{"script", "dialogue"} {
		if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
			Path: "s", EntityType: "quest", EntityKey: "wanted-hogger", Role: role,
		}); err != nil {
			t.Fatalf("LinkAdd %s: %v", role, err)
		}
	}
	links, err := docLinks(svc, ctx, game, "s")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(links) != 1 || links[0].Role != "dialogue" {
		t.Fatalf("links = %+v, want exactly one, with the latest role", links)
	}
}

// TestALinkIsAddressedByItsOwnDocument is what pins
// ListDocumentLinksByDocument's document_id filter. Two documents inside
// one game: the project filter separates nothing here, which is exactly
// why it is the right fixture — see the query's own comment for why no
// call this package offers can make that filter matter.
func TestALinkIsAddressedByItsOwnDocument(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	newQuest(t, entities, game, "the-defias", "The Defias Brotherhood")
	seedDoc(t, svc, game, "one")
	seedDoc(t, svc, game, "two")

	if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "one", EntityType: "quest", EntityKey: "wanted-hogger",
	}); err != nil {
		t.Fatalf("LinkAdd: %v", err)
	}
	if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "two", EntityType: "quest", EntityKey: "the-defias",
	}); err != nil {
		t.Fatalf("LinkAdd: %v", err)
	}

	links, err := docLinks(svc, ctx, game, "one")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(links) != 1 || links[0].EntityKey != "wanted-hogger" {
		t.Fatalf("links of one = %+v, want only wanted-hogger", links)
	}

	// The reverse direction is addressed by its own entity for the same
	// reason: the other document's quest must not appear here.
	fromEntity, err := entityLinks(svc, ctx, game, "quest", "wanted-hogger")
	if err != nil {
		t.Fatalf("LinksByEntity: %v", err)
	}
	if len(fromEntity) != 1 || fromEntity[0].Path != "one" {
		t.Fatalf("documents of wanted-hogger = %+v, want only one", fromEntity)
	}
}

func TestALinkToAnEntityInAnotherGameIsRefused(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	newQuest(t, entities, outland, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, azeroth, "s")

	err := svc.LinkAdd(ctx, azeroth, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
	})
	requireMissing(t, err, "entity_type", `no entity type "quest"`)
}

// TestALinkToAnEntityFromAnotherGamesTypeOfTheSameNameIsRefusedAtTheKey
// is the half the test above cannot reach: with both games declaring
// `quest`, the type resolves and only GetEntityIDByKey's own project
// filter stands between this caller and another game's entity.
func TestALinkToAnEntityFromAnotherGamesTypeOfTheSameNameIsRefusedAtTheKey(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	newQuest(t, entities, azeroth, "the-defias", "The Defias Brotherhood")
	newQuest(t, entities, outland, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, azeroth, "s")

	err := svc.LinkAdd(ctx, azeroth, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
	})
	requireMissing(t, err, "entity_key", `no quest named "wanted-hogger"`)
}

func TestAMistypedEntityTypeAndAMistypedEntityKeyAreToldApart(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")

	err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quesst", EntityKey: "wanted-hogger",
	})
	requireMissing(t, err, "entity_type", `no entity type "quesst"`)

	err = svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hoggerr",
	})
	requireMissing(t, err, "entity_key", `no quest named "wanted-hoggerr"`)

	// And the document's own path is a third address, named as itself.
	err = svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "nope", EntityType: "quest", EntityKey: "wanted-hogger",
	})
	requireMissing(t, err, "path", `no document at "nope"`)

	// All three are not_found on the wire — the discrimination is the
	// path, which is data, not prose an agent has to parse. This is why
	// the spec's proposed `entity_not_found` code does not ship.
	if !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("want not_found, got %v", err)
	}
}

// TestAnEntityAddressIsBoundedBeforePostgresSeesIt closes the one way a
// caller could turn its own typo into an internal_error: an entity key
// that is not valid UTF-8 reaches Postgres as a byte sequence the server
// refuses outright (SQLSTATE 22021), and an unmapped 22021 is a server
// fault reported over a value the caller supplied.
func TestAnEntityAddressIsBoundedBeforePostgresSeesIt(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")

	err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted\x80hogger",
	})
	requireFieldError(t, err, "entity_key", "letters, digits, underscores or hyphens")

	err = svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "", EntityKey: "wanted-hogger",
	})
	requireFieldError(t, err, "entity_type", "is required")

	// Inside a write's array the same refusal names the element.
	_, err = svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "two\n", ExpectedVersion: ptrInt32(1),
		Links: &[]markdown.LinkTarget{
			{EntityType: "quest", EntityKey: "wanted-hogger"},
			{EntityType: "quest", EntityKey: "wanted\x80hogger"},
		},
	})
	requireFieldError(t, err, "links[1].entity_key", "letters, digits, underscores or hyphens")
}

func TestRemovingALinkLeavesTheDocumentAndTheEntity(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")
	if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
	}); err != nil {
		t.Fatalf("LinkAdd: %v", err)
	}
	if err := svc.LinkRemove(ctx, game, markdown.UnlinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
	}); err != nil {
		t.Fatalf("LinkRemove: %v", err)
	}
	links, err := docLinks(svc, ctx, game, "s")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("links = %+v, want none", links)
	}
	if _, err := svc.Read(ctx, game, "s"); err != nil {
		t.Fatalf("the document must survive: %v", err)
	}
	if _, err := entities.EntityByKey(ctx, game, "quest", "wanted-hogger"); err != nil {
		t.Fatalf("the entity must survive: %v", err)
	}

	// Removing a link that is not there is not_found, not silence: an
	// agent that removed the wrong one needs to be told.
	err = svc.LinkRemove(ctx, game, markdown.UnlinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
	})
	requireMissing(t, err, "entity_key", "is not attached to the quest")
}

func TestDeletingAnEntityDropsItsLinksAndLeavesTheDocument(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")
	if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
	}); err != nil {
		t.Fatalf("LinkAdd: %v", err)
	}

	if err := entities.RemoveEntity(ctx, game, "quest", "wanted-hogger"); err != nil {
		t.Fatalf("RemoveEntity: %v", err)
	}

	// A quest is cut; its lore survives for the next quest.
	if _, err := svc.Read(ctx, game, "s"); err != nil {
		t.Fatalf("the document must survive the entity: %v", err)
	}
	links, err := docLinks(svc, ctx, game, "s")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("links = %+v, want the cascade to have dropped them", links)
	}
}

// TestAnEntityStopsListingADocumentThatWasDeleted pins
// ListDocumentLinksByEntity's deleted_at filter: an entity page listing
// prose nobody can read is a dead link on every quest it was attached
// to. The plan's own mutation table predicted no test would go red for
// this and named the gap as the finding; this is it.
func TestAnEntityStopsListingADocumentThatWasDeleted(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")
	if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger", Role: "script",
	}); err != nil {
		t.Fatalf("LinkAdd: %v", err)
	}
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "s", ExpectedVersion: ptrInt32(1), Message: "cut",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	links, err := entityLinks(svc, ctx, game, "quest", "wanted-hogger")
	if err != nil {
		t.Fatalf("LinksByEntity: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("the entity still lists %+v, want a deleted document hidden", links)
	}

	// Nothing cascades on a soft delete: the row is still there, and
	// resurrecting the document brings the attachment back with its
	// role, without anyone re-attaching it.
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "back\n", ExpectedVersion: ptrInt32(2),
	}); err != nil {
		t.Fatalf("resurrect: %v", err)
	}
	links, err = entityLinks(svc, ctx, game, "quest", "wanted-hogger")
	if err != nil {
		t.Fatalf("LinksByEntity: %v", err)
	}
	if len(links) != 1 || links[0].Role != "script" {
		t.Fatalf("links = %+v, want the surviving script link back", links)
	}
}

// TestADeletedDocumentIsNotAnAddressForLinks is the document side of the
// same rule: attaching prose nobody can read would put a dead entry on
// an entity page, so the address is not found rather than accepted.
func TestADeletedDocumentIsNotAnAddressForLinks(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "s", ExpectedVersion: ptrInt32(1), Message: "cut",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
	})
	requireMissing(t, err, "path", `no document at "s"`)

	_, err = docLinks(svc, ctx, game, "s")
	requireMissing(t, err, "path", `no document at "s"`)
}

func TestALinksArrayOnAWriteReplacesTheSetAndOmittingItPreservesIt(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	newQuest(t, entities, game, "the-defias", "The Defias Brotherhood")

	// Created with one link, in one call.
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Links: &[]markdown.LinkTarget{{EntityType: "quest", EntityKey: "wanted-hogger", Role: "script"}},
	}); err != nil {
		t.Fatalf("write with links: %v", err)
	}
	if links, _ := docLinks(svc, ctx, game, "s"); len(links) != 1 {
		t.Fatalf("links = %+v, want one", links)
	}

	// An ordinary edit, no links array: the set is untouched. This is
	// the case that would silently detach everything if "omitted" meant
	// "empty".
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "two\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	links, _ := docLinks(svc, ctx, game, "s")
	if len(links) != 1 || links[0].EntityKey != "wanted-hogger" || links[0].Role != "script" {
		t.Fatalf("links = %+v after an edit with no links array, want the set preserved with its role", links)
	}

	// A links array replaces.
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "three\n", ExpectedVersion: ptrInt32(2),
		Links: &[]markdown.LinkTarget{{EntityType: "quest", EntityKey: "the-defias", Role: "lore"}},
	}); err != nil {
		t.Fatalf("write replacing links: %v", err)
	}
	links, _ = docLinks(svc, ctx, game, "s")
	if len(links) != 1 || links[0].EntityKey != "the-defias" {
		t.Fatalf("links = %+v, want only the-defias", links)
	}

	// An empty array detaches everything, which is the one way to say so.
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "four\n", ExpectedVersion: ptrInt32(3),
		Links: &[]markdown.LinkTarget{},
	}); err != nil {
		t.Fatalf("write detaching links: %v", err)
	}
	if links, _ := docLinks(svc, ctx, game, "s"); len(links) != 0 {
		t.Fatalf("links = %+v, want none", links)
	}
}

// TestOmittingLinksAndSendingAnEmptyArrayAreDifferentOnTheWire is the
// half of that distinction Go's type system alone does not carry. It
// also pins a third shape, `"links":null`, decided the same way an
// omitted field is: both preserve.
//
// The domain says "nil preserves, empty replaces with nothing"; a field
// tagged as a plain slice would collapse both into one value on the way
// in, and a plain slice with omitempty would collapse them on the way
// out — the exact failure Task 3's review found for `kind`, which had to
// be chased from the service onto the wire after the fact. Pointer plus
// omitempty is the shape that survives both directions, and this test
// pins it here, in the domain that defines the meaning, rather than
// waiting for Task 10 to define it again.
//
// The struct below deliberately mirrors Task 10's DocsWriteInput field:
// Links is a `*[]DocsLinkInput`, tagged `json:"links,omitempty"`. When
// that type lands, this test stays: it is the statement of what the tag
// has to be.
//
// **This pins a stand-in, not the real thing.** `wireWrite` is declared
// right here, in this file, because this package cannot import
// internal/web. Nothing here forces the real type to keep this shape — a
// `Links` declared as a plain `[]DocsLinkInput`, or without `omitempty`,
// would leave this test green while the wire behaviour it documents is
// gone, which is `kind`'s defect one layer up. **The claim is re-pinned
// against the real type by
// TestOmittingLinksAndSendingAnEmptyArrayAreDifferentOnThisType
// (internal/web/mcp_docs_internal_test.go)**, which asserts
// DocsWriteInput.Links is exactly `*[]DocsLinkInput` tagged
// `json:"links,omitempty"` and decodes the same three shapes through
// it. Neither test replaces the other: this one owns the meaning, that
// one owns the declaration.
func TestOmittingLinksAndSendingAnEmptyArrayAreDifferentOnTheWire(t *testing.T) {
	type wireWrite struct {
		Path  string                 `json:"path"`
		Links *[]markdown.LinkTarget `json:"links,omitempty"`
	}

	var omitted, empty, explicitNull wireWrite
	if err := json.Unmarshal([]byte(`{"path":"s"}`), &omitted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"path":"s","links":[]}`), &empty); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// "links": null is decided here, not left to whichever caller hits it
	// first: it means the same thing an omitted field means, preserve,
	// not the same thing an empty array means, detach everything. That is
	// the safe side — the alternative reads a caller's "I said nothing
	// about links" as "detach everything" — but safe is not the same as
	// obvious, and an agent that sends `null` meaning "detach" gets the
	// opposite of what it asked for with no error to notice by. Decided
	// and documented at WriteInput.Links; pinned here because it is the
	// same wire distinction the rest of this test pins.
	if err := json.Unmarshal([]byte(`{"path":"s","links":null}`), &explicitNull); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if omitted.Links != nil {
		t.Fatalf("an omitted links array decoded to %#v, want nil — nil is what preserves the set",
			omitted.Links)
	}
	if empty.Links == nil || len(*empty.Links) != 0 {
		t.Fatalf("an empty links array decoded to %#v, want a pointer to an empty slice — "+
			"that is the only way a caller can say \"detach everything\"", empty.Links)
	}
	if explicitNull.Links != nil {
		t.Fatalf("an explicit \"links\":null decoded to %#v, want nil — null preserves, "+
			"the same as omitting the field", explicitNull.Links)
	}

	// And back out again: a request built in Go must not lose the
	// distinction on the way to the server either.
	for _, tc := range []struct {
		name  string
		value wireWrite
		want  string
	}{
		{"omitted", wireWrite{Path: "s"}, `{"path":"s"}`},
		{"empty", wireWrite{Path: "s", Links: &[]markdown.LinkTarget{}}, `{"path":"s","links":[]}`},
	} {
		encoded, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatalf("encode %s: %v", tc.name, err)
		}
		if string(encoded) != tc.want {
			t.Fatalf("%s encoded as %s, want %s", tc.name, encoded, tc.want)
		}
	}
}

func TestABadLinkInAWriteRollsTheWholeWriteBack(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "two\n", ExpectedVersion: ptrInt32(1),
		Links: &[]markdown.LinkTarget{
			{EntityType: "quest", EntityKey: "wanted-hogger"},
			{EntityType: "quest", EntityKey: "does-not-exist"},
		},
	})
	requireMissing(t, err, "links[1].entity_key", `no quest named "does-not-exist"`)

	// The body did not move either: a write and its links are one change.
	doc, err := svc.Read(ctx, game, "s")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if doc.BodyMd != "one\n" || doc.CurrentVersion != 1 {
		t.Fatalf("document = (%q, v%d), want the write rolled back", doc.BodyMd, doc.CurrentVersion)
	}
	// Nor did the good half of the array land.
	links, err := docLinks(svc, ctx, game, "s")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("links = %+v, want the whole array rolled back", links)
	}
}

// TestALinksArrayNamingOneEntityTwiceIsRefused pins the decision
// duplicateLinkTargets' doc comment argues: a links array that
// addresses one entity under two spellings is refused whole, up front,
// rather than silently upserting twice and landing whichever role ran
// last. Two spellings of one key ("wanted-hogger", "WANTED-HOGGER") are
// used deliberately, folding case the way the entity key's own unique
// index does — the metamodel settled the identical question for a
// case-differing pair in a bulk write's own duplicate check.
func TestALinksArrayNamingOneEntityTwiceIsRefused(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")

	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "two\n", ExpectedVersion: ptrInt32(1),
		Links: &[]markdown.LinkTarget{
			{EntityType: "quest", EntityKey: "wanted-hogger", Role: "script"},
			{EntityType: "quest", EntityKey: "WANTED-HOGGER", Role: "lore"},
		},
	})
	requireFieldError(t, err, "links[1].entity_key", "is already addressed by links[0]")

	// Refused whole: nothing landed, not even the first, unambiguous
	// element.
	links, lerr := docLinks(svc, ctx, game, "s")
	if lerr != nil {
		t.Fatalf("LinksByDocument: %v", lerr)
	}
	if len(links) != 0 {
		t.Fatalf("links = %+v, want none — a refused write attaches nothing", links)
	}
}

func TestALinkRoleIsBoundedAsTheCallersOwnArgument(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")

	err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
		Role: strings.Repeat("r", markdown.MaxRoleLen+1),
	})
	requireFieldError(t, err, "role", "must be at most")

	// One line of text: a role is rendered in a listing row.
	err = svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger", Role: "two\nlines",
	})
	requireFieldError(t, err, "role", "holds a control character")

	// Inside a write's array the refusal names the element.
	_, err = svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "two\n", ExpectedVersion: ptrInt32(1),
		Links: &[]markdown.LinkTarget{{
			EntityType: "quest", EntityKey: "wanted-hogger",
			Role: strings.Repeat("r", markdown.MaxRoleLen+1),
		}},
	})
	requireFieldError(t, err, "links[0].role", "must be at most")

	// A role of exactly the bound is accepted, and stored as written.
	role := strings.Repeat("r", markdown.MaxRoleLen)
	if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger", Role: role,
	}); err != nil {
		t.Fatalf("a role of exactly MaxRoleLen must be accepted: %v", err)
	}
	links, err := docLinks(svc, ctx, game, "s")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(links) != 1 || links[0].Role != role {
		t.Fatalf("links = %+v, want the role stored exactly as written", links)
	}
}

func TestAWriteCarryingTooManyAttachmentsIsRefusedAtLinks(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")

	targets := make([]markdown.LinkTarget, markdown.MaxLinksPerWrite+1)
	for i := range targets {
		targets[i] = markdown.LinkTarget{EntityType: "quest", EntityKey: "wanted-hogger"}
	}
	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "one\n", ExpectedVersion: ptrInt32(0), Links: &targets,
	})
	requireFieldError(t, err, "links", "the most one write takes is")

	// Refused before anything was written, like every other argument
	// problem: the document does not exist afterwards.
	if _, err := svc.Read(ctx, game, "s"); !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("want the write refused before it landed, got %v", err)
	}
}

// TestALinkRowCannotClaimAGameItsDocumentDoesNotBelongTo is the
// database-level half of the isolation invariant, forced from the
// package that writes the column: UpsertDocumentLink sets project_id
// from the caller's own resolved id, and 0007_documents.sql's two
// composite keys are the only thing that would refuse a mismatch.
func TestALinkRowCannotClaimAGameItsDocumentDoesNotBelongTo(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	newQuest(t, entities, outland, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, azeroth, "s")

	doc, err := svc.Read(ctx, azeroth, "s")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	entity, err := entities.EntityByKey(ctx, outland, "quest", "wanted-hogger")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	// Neither project id can satisfy both keys at once, which is the
	// point: whichever is written, one of the two composite keys refuses.
	for _, game := range []uuid.UUID{azeroth, outland} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO document_links (project_id, document_id, entity_id, role)
			 VALUES ($1, $2, $3, 'smuggled')`, game, doc.ID, entity.ID); err == nil {
			t.Fatalf("a link row across two games must be refused (project %s)", game)
		}
	}
	links, err := docLinks(svc, ctx, azeroth, "s")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("links = %+v, want the refusal to have left nothing behind", links)
	}
}

func TestLinkingIsAnnounced(t *testing.T) {
	svc, entities, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
	}); err != nil {
		t.Fatalf("LinkAdd: %v", err)
	}
	ev := nextEvent(t, sub)
	if ev.Kind != "document.linked" {
		t.Fatalf("Kind = %q, want %q", ev.Kind, "document.linked")
	}
	payload, ok := ev.Payload.(markdown.DocumentEvent)
	if !ok || payload.Path != "s" || payload.Version != 1 {
		t.Fatalf("payload = %#v, want s at version 1 — a link does not move the version", ev.Payload)
	}

	// Detaching announces the same kind: the payload says nothing about
	// the link set, so there is no sentence a client could write from
	// one that it could not write from the other.
	if err := svc.LinkRemove(ctx, game, markdown.UnlinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
	}); err != nil {
		t.Fatalf("LinkRemove: %v", err)
	}
	if ev := nextEvent(t, sub); ev.Kind != "document.linked" {
		t.Fatalf("Kind = %q, want %q for a detach too", ev.Kind, "document.linked")
	}
}

// TestAWriteCarryingLinksAnnouncesBothTheWriteAndTheLink pins the order
// events.go states: a client watching only document.written would be
// watching for one of the two things that changed.
func TestAWriteCarryingLinksAnnouncesBothTheWriteAndTheLink(t *testing.T) {
	svc, entities, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Links: &[]markdown.LinkTarget{{EntityType: "quest", EntityKey: "wanted-hogger"}},
	}); err != nil {
		t.Fatalf("write with links: %v", err)
	}
	if ev := nextEvent(t, sub); ev.Kind != "document.written" {
		t.Fatalf("first event = %q, want document.written", ev.Kind)
	}
	if ev := nextEvent(t, sub); ev.Kind != "document.linked" {
		t.Fatalf("second event = %q, want document.linked", ev.Kind)
	}

	// A write with no links array announces the write alone: a client
	// must not have to re-read the links on every ordinary edit.
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "two\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if ev := nextEvent(t, sub); ev.Kind != "document.written" {
		t.Fatalf("first event = %q, want document.written", ev.Kind)
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("an edit with no links array announced %v as well", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNoLinkIsAnnouncedWhenTheAttachmentIsRefused(t *testing.T) {
	svc, entities, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "does-not-exist",
	}); err == nil {
		t.Fatal("want the attachment refused")
	}
	if err := svc.LinkRemove(ctx, game, markdown.UnlinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
	}); err == nil {
		t.Fatal("want the detachment refused")
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("a refused link operation announced %v", ev)
	default:
	}
}

// TestNoLinkIsAnnouncedWhenTheAttachmentCannotCommit is the placement
// TestNoLinkIsAnnouncedWhenTheAttachmentIsRefused cannot reach, and this
// package has now found the same gap four times — Task 3's correction 4
// for Write, Task 4's for Delete, Task 6's for Revert, and here. A
// publish sitting as the last statement *inside* withTx's callback
// differs from the correct placement only by the commit that follows,
// and every other failure LinkAdd can produce returns from the callback
// before that statement runs.
//
// The technique is TestNoDeletionIsAnnouncedWhenTheDeleteCannotCommit's,
// unchanged but for the table: a deferred foreign key from
// document_links.id to projects.id is satisfied by nothing — a link's id
// is not a project id — but being DEFERRABLE INITIALLY DEFERRED it is
// checked at COMMIT and not before, so the INSERT succeeds and only the
// commit fails. It hangs off document_links because that is LinkAdd's
// only INSERT.
func TestNoLinkIsAnnouncedWhenTheAttachmentCannotCommit(t *testing.T) {
	svc, entities, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, game, "s")

	if _, err := pool.Exec(ctx,
		`ALTER TABLE document_links ADD CONSTRAINT links_commit_must_fail
		   FOREIGN KEY (id) REFERENCES projects (id) DEFERRABLE INITIALLY DEFERRED NOT VALID`); err != nil {
		t.Fatalf("install the deferred constraint: %v", err)
	}

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	err := svc.LinkAdd(ctx, game, markdown.LinkInput{
		Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
	})
	if err == nil {
		t.Fatal("want the commit to fail")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Fatalf("err = %v, want the commit to be what failed", err)
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("an attachment whose commit failed announced %v", ev)
	default:
	}
}

// nextEvent reads one event or fails, rather than blocking the suite
// until the test binary is killed: a mutation that hangs a suite is
// worth no less than one that reddens it, but it should not need a
// SIGKILL to report (Task 3's correction 10).
func nextEvent(t *testing.T, sub *realtime.Subscription) realtime.Event {
	t.Helper()
	select {
	case ev := <-sub.C:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was announced within 5s")
		return realtime.Event{}
	}
}

// TestUpsertDocumentLinksConflictPathCannotWriteAnotherGamesLink drives
// the statement directly, for the reason its twin in internal/metamodel
// (TestUpsertRelationsConflictPathCannotWriteAnotherGamesEdge) records.
//
// 0007_documents.sql's two composite foreign keys make a cross-game link
// impossible to *insert*: the row's project_id must agree with the
// document's and with the entity's. They check nothing on the conflict
// path. The target is (document_id, entity_id), which names no project;
// project_id is not in the SET list, so the stored row keeps its own and
// every key stays satisfied. Measured before the guard existed: one game
// rewrote the role on another game's link and was handed that game's row
// back.
//
// No caller here can reach it — both call sites resolve the document and
// the entity by key inside the project first — which is the same status
// the position write's guard has in internal/views, and the same
// decision: the guard stays, and it is asserted by driving the statement.
func TestUpsertDocumentLinksConflictPathCannotWriteAnotherGamesLink(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	mine := newGame(t, pool, "azeroth")
	theirs := newGame(t, pool, "outland")
	newQuest(t, entities, mine, "wanted-hogger", "Wanted: Hogger")
	seedDoc(t, svc, mine, "scripts/wanted-hogger")
	if err := svc.LinkAdd(ctx, mine, markdown.LinkInput{
		Path: "scripts/wanted-hogger", EntityType: "quest", EntityKey: "wanted-hogger",
		Role: "script",
	}); err != nil {
		t.Fatalf("LinkAdd: %v", err)
	}

	var documentID, entityID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT document_id, entity_id FROM document_links WHERE project_id = $1`,
		mine).Scan(&documentID, &entityID); err != nil {
		t.Fatalf("read the stored link: %v", err)
	}
	q := dbq.New(pool)

	row, err := q.UpsertDocumentLink(ctx, dbq.UpsertDocumentLinkParams{
		ProjectID: theirs, DocumentID: documentID, EntityID: entityID, Role: "theirs",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("UpsertDocumentLink returned %+v (err = %v) for another game's link, "+
			"want no rows", row, err)
	}

	// The positive control, both ways: the role is what its own game
	// wrote, and that game can still rewrite it.
	links, err := docLinks(svc, ctx, mine, "scripts/wanted-hogger")
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(links) != 1 || links[0].Role != "script" {
		t.Fatalf("the link reads back as %+v, want one link with role \"script\": another "+
			"game must not rewrite it", links)
	}
	if _, err := q.UpsertDocumentLink(ctx, dbq.UpsertDocumentLinkParams{
		ProjectID: mine, DocumentID: documentID, EntityID: entityID, Role: "notes",
	}); err != nil {
		t.Fatalf("the owning game cannot rewrite its own link: %v", err)
	}
}

// docLinks and entityLinks are the first page of each side of the join,
// for the many tests here that are about *what* a document is attached
// to rather than about paging. The paging tests below call the service
// directly, with a filter, and are the only ones that need to.
func docLinks(svc *markdown.Service, ctx context.Context, game uuid.UUID,
	path string,
) ([]markdown.EntityLink, error) {
	page, err := svc.LinksByDocument(ctx, game, path, markdown.LinksFilter{})
	return page.Links, err
}

func entityLinks(svc *markdown.Service, ctx context.Context, game uuid.UUID,
	entityType, entityKey string,
) ([]markdown.DocumentLink, error) {
	page, err := svc.LinksByEntity(ctx, game, entityType, entityKey, markdown.LinksFilter{})
	return page.Links, err
}

// TestADocumentsAttachmentsPageAndTheCursorBelongsToItsOwnSide walks one
// document's attachments two at a time and pins the three things a page
// has to get right: every row is seen exactly once and in the listing's
// order, a full final page carries a cursor to an empty one, and the
// cursor cannot be carried anywhere else.
//
// **The last part is this listing's own hazard rather than an inherited
// one.** The two sides of the join answer the same question from two
// ends, take the same LinksFilter, and sort on two different columns —
// an entity key one way, a document path the other — so a cursor carried
// across would compare a path against a key and answer nonsense. Nothing
// but the fingerprint stops it.
func TestADocumentsAttachmentsPageAndTheCursorBelongsToItsOwnSide(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	keys := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for _, key := range keys {
		newQuest(t, entities, game, key, strings.ToUpper(key))
	}
	seedDoc(t, svc, game, "lore/westfall")
	for _, key := range keys {
		if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
			Path: "lore/westfall", EntityType: "quest", EntityKey: key,
		}); err != nil {
			t.Fatalf("LinkAdd %s: %v", key, err)
		}
	}

	var (
		seen   []string
		cursor string
		pages  int
	)
	for {
		page, err := svc.LinksByDocument(ctx, game, "lore/westfall",
			markdown.LinksFilter{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatalf("LinksByDocument page %d: %v", pages, err)
		}
		pages++
		for _, link := range page.Links {
			seen = append(seen, link.EntityKey)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("the walk did not terminate")
		}
	}
	if strings.Join(seen, " ") != strings.Join(keys, " ") {
		t.Fatalf("walked %v, want %v exactly once each in order", seen, keys)
	}
	// Five rows two at a time is three pages of 2, 2, 1: the last one is
	// short, so it carries no cursor and the walk ends on it.
	if pages != 3 {
		t.Fatalf("%d pages over five rows at two a page, want 3", pages)
	}

	// A cursor from the document side, offered to the entity side.
	first, err := svc.LinksByDocument(ctx, game, "lore/westfall", markdown.LinksFilter{Limit: 2})
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("a full page must carry a cursor, or the rest of this test proves nothing")
	}
	_, err = svc.LinksByEntity(ctx, game, "quest", "alpha",
		markdown.LinksFilter{Cursor: first.NextCursor})
	requireFieldError(t, err, "cursor", "was issued for a different listing")
}

// TestAnEntitysDocumentsPageAndItsCursorBelongsToItsOwnEntity is the
// other direction: an entity that a dozen scripts hang off is ordinary,
// and its listing is a page like any other.
func TestAnEntitysDocumentsPageAndItsCursorBelongsToItsOwnEntity(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	newQuest(t, entities, game, "the-defias", "The Defias Brotherhood")
	paths := []string{"scripts/a", "scripts/b", "scripts/c", "scripts/d", "scripts/e"}
	for _, path := range paths {
		seedDoc(t, svc, game, path)
		if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
			Path: path, EntityType: "quest", EntityKey: "wanted-hogger",
		}); err != nil {
			t.Fatalf("LinkAdd %s: %v", path, err)
		}
	}

	var (
		seen   []string
		cursor string
	)
	for {
		page, err := svc.LinksByEntity(ctx, game, "quest", "wanted-hogger",
			markdown.LinksFilter{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatalf("LinksByEntity: %v", err)
		}
		for _, link := range page.Links {
			seen = append(seen, link.Path)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if len(seen) > 10 {
			t.Fatal("the walk did not terminate")
		}
	}
	if strings.Join(seen, " ") != strings.Join(paths, " ") {
		t.Fatalf("walked %v, want %v in path order", seen, paths)
	}

	// The cursor names its own entity: a page of one quest's scripts
	// cannot be continued against another quest's.
	first, err := svc.LinksByEntity(ctx, game, "quest", "wanted-hogger",
		markdown.LinksFilter{Limit: 2})
	if err != nil {
		t.Fatalf("LinksByEntity: %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("a full page must carry a cursor")
	}
	_, err = svc.LinksByEntity(ctx, game, "quest", "the-defias",
		markdown.LinksFilter{Cursor: first.NextCursor})
	requireFieldError(t, err, "cursor", "was issued for a different listing")
	// And it survives a respelling of its own address, because the
	// fingerprint is built from the resolved entity id and not from the
	// keys as the caller typed them.
	if _, err := svc.LinksByEntity(ctx, game, "QUEST", "Wanted-Hogger",
		markdown.LinksFilter{Cursor: first.NextCursor}); err != nil {
		t.Fatalf("a cursor must survive a respelling of its own filter: %v", err)
	}
}

// TestAFullFinalPageOfAttachmentsCarriesACursorToAnEmptyOne pins the
// contract EntityLinkPage states: a listing whose length is an exact
// multiple of the limit ends on an empty page rather than on a short
// one, and a caller looping until the cursor is empty must expect that
// rather than treat it as an error.
func TestAFullFinalPageOfAttachmentsCarriesACursorToAnEmptyOne(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	for _, key := range []string{"alpha", "bravo"} {
		newQuest(t, entities, game, key, key)
	}
	seedDoc(t, svc, game, "s")
	for _, key := range []string{"alpha", "bravo"} {
		if err := svc.LinkAdd(ctx, game, markdown.LinkInput{
			Path: "s", EntityType: "quest", EntityKey: key,
		}); err != nil {
			t.Fatalf("LinkAdd: %v", err)
		}
	}
	page, err := svc.LinksByDocument(ctx, game, "s", markdown.LinksFilter{Limit: 2})
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(page.Links) != 2 || page.NextCursor == "" {
		t.Fatalf("page = %+v, want two rows and a cursor", page)
	}
	last, err := svc.LinksByDocument(ctx, game, "s",
		markdown.LinksFilter{Limit: 2, Cursor: page.NextCursor})
	if err != nil {
		t.Fatalf("LinksByDocument: %v", err)
	}
	if len(last.Links) != 0 || last.NextCursor != "" {
		t.Fatalf("final page = %+v, want an empty one with no cursor", last)
	}
}
