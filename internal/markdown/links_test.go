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

	"github.com/neverbot/maestro/internal/assert"
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

func TestLinksArea(t *testing.T) {
	t.Parallel()
	a := newArea(t)

	// TestLinksArea's "a resurrected document keeps its id and its links" case
	// pins the *asymmetry* between this domain and internal/metamodel, from
	// the side that has to stay different.
	t.Run("a resurrected document keeps its id and its links", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")
		newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")

		before, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: "scripts/wanted-hogger", Content: "one\n", ExpectedVersion: ptrInt32(0),
		})
		assert.Must(t, err == nil, "write: %v", err)
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
		assert.Must(t, err == nil, "a write to a deleted path must bring the document back: %v", err)
		assert.Must(t, after.ID == before.ID, "the resurrected document has id %s, want the id it already had (%s): "+
			"the whole reason this domain accepts the claim the metamodel refuses is that "+
			"the row survives its own deletion", after.ID, before.ID)
		links, err := docLinks(svc, ctx, game, "scripts/wanted-hogger")
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(links) == 1 && links[0].EntityKey == "wanted-hogger", "links after the resurrection = %+v, want the attachment still there: an "+
			"association the metamodel's own resurrection would have lost", links)
	})

	t.Run("a link attaches a document to an entity and reads back from both sides", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(fromDoc) == 1 && fromDoc[0].EntityKey == "wanted-hogger" && fromDoc[0].Role == "script", "links from the document = %+v, want one script link to wanted-hogger", fromDoc)
		if fromDoc[0].EntityName != "Wanted: Hogger" || fromDoc[0].EntityTypeKey != "quest" {
			t.Fatalf("document-side link = %+v, want the entity's type key and name so a UI need not fetch them",
				fromDoc[0])
		}
		assert.Must(t, fromDoc[0].EntityID != uuid.Nil, "EntityID = zero, want the entity's own id")

		// From the entity. This is the read-back that matters most: it is
		// how the UI builds a quest page and how an agent finds the script
		// from the quest instead of guessing a path.
		fromEntity, err := entityLinks(svc, ctx, game, "quest", "wanted-hogger")
		assert.Must(t, err == nil, "LinksByEntity: %v", err)
		assert.Must(t, len(fromEntity) == 1 && fromEntity[0].Path == "scripts/wanted-hogger", "links from the entity = %+v, want the script", fromEntity)
		if fromEntity[0].Title != "scripts/wanted-hogger" || fromEntity[0].Role != "script" {
			t.Fatalf("entity-side link = %+v, want the document's title and the role", fromEntity[0])
		}
		if fromEntity[0].Kind != "script" || fromEntity[0].DocumentID == uuid.Nil {
			t.Fatalf("entity-side link = %+v, want the document's kind and id", fromEntity[0])
		}
	})

	// TestLinksArea's "a document is attached to several entities and listed
	// in a stable order" case is the case the many-to-many table was chosen
	// for — one piece of lore that describes a faction and a city — plus the
	// ordering the listing promises, which no single-link test can see.
	t.Run("a document is attached to several entities and listed in a stable order", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		// Type key, then entity key: faction before quest, and inside quest
		// the-defias before wanted-hogger.
		var got []string
		for _, link := range links {
			got = append(got, link.EntityTypeKey+"/"+link.EntityKey)
		}
		want := []string{"faction/defias", "quest/the-defias", "quest/wanted-hogger"}
		assert.Must(t, strings.Join(got, " ") == strings.Join(want, " "), "links = %v, want %v", got, want)
	})

	// TestLinksArea's "a document with no attachments is an ordinary document"
	// case is the other end of the same decision: a game bible is attached to
	// nothing, and the listing answers an empty slice rather than nil, so the
	// wire carries [] and not null.
	t.Run("a document with no attachments is an ordinary document", func(t *testing.T) {
		svc, _, _, pool := a.service(t)
		ctx := context.Background()
		game := newGame(t, pool, "azeroth")
		seedDoc(t, svc, game, "bible")

		links, err := docLinks(svc, ctx, game, "bible")
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, links != nil && len(links) == 0, "links = %#v, want an empty non-nil slice", links)
		encoded, err := json.Marshal(links)
		assert.Must(t, err == nil, "marshal: %v", err)
		assert.Must(t, string(encoded) == "[]", "encoded = %s, want []", encoded)
	})

	t.Run("re adding a link updates its role rather than duplicating it", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(links) == 1 && links[0].Role == "dialogue", "links = %+v, want exactly one, with the latest role", links)
	})

	// TestLinksArea's "a link is addressed by its own document" case is what
	// pins ListDocumentLinksByDocument's document_id filter. Two documents
	// inside one game: the project filter separates nothing here, which is
	// exactly why it is the right fixture — see the query's own comment for
	// why no call this package offers can make that filter matter.
	t.Run("a link is addressed by its own document", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(links) == 1 && links[0].EntityKey == "wanted-hogger", "links of one = %+v, want only wanted-hogger", links)

		// The reverse direction is addressed by its own entity for the same
		// reason: the other document's quest must not appear here.
		fromEntity, err := entityLinks(svc, ctx, game, "quest", "wanted-hogger")
		assert.Must(t, err == nil, "LinksByEntity: %v", err)
		assert.Must(t, len(fromEntity) == 1 && fromEntity[0].Path == "one", "documents of wanted-hogger = %+v, want only one", fromEntity)
	})

	t.Run("a link to an entity in another game is refused", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
		ctx := context.Background()
		azeroth := newGame(t, pool, "azeroth")
		outland := newGame(t, pool, "outland")
		newQuest(t, entities, outland, "wanted-hogger", "Wanted: Hogger")
		seedDoc(t, svc, azeroth, "s")

		err := svc.LinkAdd(ctx, azeroth, markdown.LinkInput{
			Path: "s", EntityType: "quest", EntityKey: "wanted-hogger",
		})
		requireMissing(t, err, "entity_type", `no entity type "quest"`)
	})

	// TestLinksArea's "a link to an entity from another games type of the same
	// name is refused at the key" case is the half the test above cannot
	// reach: with both games declaring `quest`, the type resolves and only
	// GetEntityIDByKey's own project filter stands between this caller and
	// another game's entity.
	t.Run("a link to an entity from another games type of the same name is refused at the key", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
	})

	t.Run("a mistyped entity type and a mistyped entity key are told apart", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, errors.Is(err, markdown.ErrNotFound), "want not_found, got %v", err)
	})

	// TestLinksArea's "an entity address is bounded before postgres sees it"
	// case closes the one way a caller could turn its own typo into an
	// internal_error: an entity key that is not valid UTF-8 reaches Postgres
	// as a byte sequence the server refuses outright (SQLSTATE 22021), and an
	// unmapped 22021 is a server fault reported over a value the caller
	// supplied.
	t.Run("an entity address is bounded before postgres sees it", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
	})

	t.Run("removing a link leaves the document and the entity", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(links) == 0, "links = %+v, want none", links)
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
	})

	t.Run("deleting an entity drops its links and leaves the document", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(links) == 0, "links = %+v, want the cascade to have dropped them", links)
	})

	// TestLinksArea's "an entity stops listing a document that was deleted"
	// case pins ListDocumentLinksByEntity's deleted_at filter: an entity page
	// listing prose nobody can read is a dead link on every quest it was
	// attached to. The plan's own mutation table predicted no test would go
	// red for this and named the gap as the finding; this is it.
	t.Run("an entity stops listing a document that was deleted", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, err == nil, "LinksByEntity: %v", err)
		assert.Must(t, len(links) == 0, "the entity still lists %+v, want a deleted document hidden", links)

		// Nothing cascades on a soft delete: the row is still there, and
		// resurrecting the document brings the attachment back with its
		// role, without anyone re-attaching it.
		if _, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: "s", Content: "back\n", ExpectedVersion: ptrInt32(2),
		}); err != nil {
			t.Fatalf("resurrect: %v", err)
		}
		links, err = entityLinks(svc, ctx, game, "quest", "wanted-hogger")
		assert.Must(t, err == nil, "LinksByEntity: %v", err)
		assert.Must(t, len(links) == 1 && links[0].Role == "script", "links = %+v, want the surviving script link back", links)
	})

	// TestLinksArea's "a deleted document is not an address for links" case is
	// the document side of the same rule: attaching prose nobody can read
	// would put a dead entry on an entity page, so the address is not found
	// rather than accepted.
	t.Run("a deleted document is not an address for links", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
	})

	t.Run("a links array on a write replaces the set and omitting it preserves it", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, len(links) == 1 && links[0].EntityKey == "wanted-hogger" && links[0].Role == "script", "links = %+v after an edit with no links array, want the set preserved with its role", links)

		// A links array replaces.
		if _, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: "s", Content: "three\n", ExpectedVersion: ptrInt32(2),
			Links: &[]markdown.LinkTarget{{EntityType: "quest", EntityKey: "the-defias", Role: "lore"}},
		}); err != nil {
			t.Fatalf("write replacing links: %v", err)
		}
		links, _ = docLinks(svc, ctx, game, "s")
		assert.Must(t, len(links) == 1 && links[0].EntityKey == "the-defias", "links = %+v, want only the-defias", links)

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
	})

	// TestLinksArea's "omitting links and sending an empty array are different
	// on the wire" case is the half of that distinction Go's type system alone
	// does not carry. It also pins a third shape, `"links":null`, decided the
	// same way an omitted field is: both preserve.
	t.Run("omitting links and sending an empty array are different on the wire", func(t *testing.T) {
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
		assert.Must(t, omitted.Links == nil, "an omitted links array decoded to %#v, want nil — nil is what preserves the set",
			omitted.Links)
		assert.Must(t, empty.Links != nil && len(*empty.Links) == 0, "an empty links array decoded to %#v, want a pointer to an empty slice — "+
			"that is the only way a caller can say \"detach everything\"", empty.Links)
		assert.Must(t, explicitNull.Links == nil, "an explicit \"links\":null decoded to %#v, want nil — null preserves, "+
			"the same as omitting the field", explicitNull.Links)

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
			assert.Must(t, err == nil, "encode %s: %v", tc.name, err)
			assert.Must(t, string(encoded) == tc.want, "%s encoded as %s, want %s", tc.name, encoded, tc.want)
		}
	})

	t.Run("a bad link in a write rolls the whole write back", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, err == nil, "Read: %v", err)
		assert.Must(t, doc.BodyMd == "one\n" && doc.CurrentVersion == 1, "document = (%q, v%d), want the write rolled back", doc.BodyMd, doc.CurrentVersion)
		// Nor did the good half of the array land.
		links, err := docLinks(svc, ctx, game, "s")
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(links) == 0, "links = %+v, want the whole array rolled back", links)
	})

	// TestLinksArea's "a links array naming one entity twice is refused" case
	// pins the decision duplicateLinkTargets' doc comment argues: a links
	// array that addresses one entity under two spellings is refused whole, up
	// front, rather than silently upserting twice and landing whichever role
	// ran last. Two spellings of one key ("wanted-hogger", "WANTED-HOGGER")
	// are used deliberately, folding case the way the entity key's own unique
	// index does — the metamodel settled the identical question for a
	// case-differing pair in a bulk write's own duplicate check.
	t.Run("a links array naming one entity twice is refused", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, lerr == nil, "LinksByDocument: %v", lerr)
		assert.Must(t, len(links) == 0, "links = %+v, want none — a refused write attaches nothing", links)
	})

	t.Run("a link role is bounded as the callers own argument", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(links) == 1 && links[0].Role == role, "links = %+v, want the role stored exactly as written", links)
	})

	t.Run("a write carrying too many attachments is refused at links", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
	})

	// TestLinksArea's "a link row cannot claim a game its document does not
	// belong to" case is the database-level half of the isolation invariant,
	// forced from the package that writes the column: UpsertDocumentLink sets
	// project_id from the caller's own resolved id, and 0007_documents.sql's
	// two composite keys are the only thing that would refuse a mismatch.
	t.Run("a link row cannot claim a game its document does not belong to", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
		ctx := context.Background()
		azeroth := newGame(t, pool, "azeroth")
		outland := newGame(t, pool, "outland")
		newQuest(t, entities, outland, "wanted-hogger", "Wanted: Hogger")
		seedDoc(t, svc, azeroth, "s")

		doc, err := svc.Read(ctx, azeroth, "s")
		assert.Must(t, err == nil, "Read: %v", err)
		entity, err := entities.EntityByKey(ctx, outland, "quest", "wanted-hogger")
		assert.Must(t, err == nil, "EntityByKey: %v", err)
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
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(links) == 0, "links = %+v, want the refusal to have left nothing behind", links)
	})

	t.Run("linking is announced", func(t *testing.T) {
		svc, entities, hub, pool := a.service(t)
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
		assert.Must(t, ev.Kind == "document.linked", "Kind = %q, want %q", ev.Kind, "document.linked")
		payload, ok := ev.Payload.(markdown.DocumentEvent)
		assert.Must(t, ok && payload.Path == "s" && payload.Version == 1, "payload = %#v, want s at version 1 — a link does not move the version", ev.Payload)

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
	})

	// TestLinksArea's "a write carrying links announces both the write and the
	// link" case pins the order events.go states: a client watching only
	// document.written would be watching for one of the two things that
	// changed.
	t.Run("a write carrying links announces both the write and the link", func(t *testing.T) {
		svc, entities, hub, pool := a.service(t)
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
	})

	t.Run("no link is announced when the attachment is refused", func(t *testing.T) {
		svc, entities, hub, pool := a.service(t)
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
	})

	// TestLinksArea's "upsert document links conflict path cannot write
	// another games link" case drives the statement directly, for the reason
	// its twin in internal/metamodel (TestRelationsArea's "upsert relations
	// conflict path cannot write another games edge" case) records.
	t.Run("upsert document links conflict path cannot write another games link", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, errors.Is(err, pgx.ErrNoRows), "UpsertDocumentLink returned %+v (err = %v) for another game's link, "+
			"want no rows", row, err)

		// The positive control, both ways: the role is what its own game
		// wrote, and that game can still rewrite it.
		links, err := docLinks(svc, ctx, mine, "scripts/wanted-hogger")
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(links) == 1 && links[0].Role == "script", "the link reads back as %+v, want one link with role \"script\": another "+
			"game must not rewrite it", links)
		if _, err := q.UpsertDocumentLink(ctx, dbq.UpsertDocumentLinkParams{
			ProjectID: mine, DocumentID: documentID, EntityID: entityID, Role: "notes",
		}); err != nil {
			t.Fatalf("the owning game cannot rewrite its own link: %v", err)
		}
	})

	// TestLinksArea's "a documents attachments page and the cursor belongs to
	// its own side" case walks one document's attachments two at a time and
	// pins the three things a page has to get right: every row is seen exactly
	// once and in the listing's order, a full final page carries a cursor to
	// an empty one, and the cursor cannot be carried anywhere else.
	t.Run("a documents attachments page and the cursor belongs to its own side", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
			assert.Must(t, err == nil, "LinksByDocument page %d: %v", pages, err)
			pages++
			for _, link := range page.Links {
				seen = append(seen, link.EntityKey)
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
			assert.Must(t, pages <= 10, "the walk did not terminate")
		}
		assert.Must(t, strings.Join(seen, " ") == strings.Join(keys, " "), "walked %v, want %v exactly once each in order", seen, keys)
		// Five rows two at a time is three pages of 2, 2, 1: the last one is
		// short, so it carries no cursor and the walk ends on it.
		assert.Must(t, pages == 3, "%d pages over five rows at two a page, want 3", pages)

		// A cursor from the document side, offered to the entity side.
		first, err := svc.LinksByDocument(ctx, game, "lore/westfall", markdown.LinksFilter{Limit: 2})
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, first.NextCursor != "", "a full page must carry a cursor, or the rest of this test proves nothing")
		_, err = svc.LinksByEntity(ctx, game, "quest", "alpha",
			markdown.LinksFilter{Cursor: first.NextCursor})
		requireFieldError(t, err, "cursor", "was issued for a different listing")
	})

	// TestLinksArea's "an entitys documents page and its cursor belongs to its
	// own entity" case is the other direction: an entity that a dozen scripts
	// hang off is ordinary, and its listing is a page like any other.
	t.Run("an entitys documents page and its cursor belongs to its own entity", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
			assert.Must(t, err == nil, "LinksByEntity: %v", err)
			for _, link := range page.Links {
				seen = append(seen, link.Path)
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
			assert.Must(t, len(seen) <= 10, "the walk did not terminate")
		}
		assert.Must(t, strings.Join(seen, " ") == strings.Join(paths, " "), "walked %v, want %v in path order", seen, paths)

		// The cursor names its own entity: a page of one quest's scripts
		// cannot be continued against another quest's.
		first, err := svc.LinksByEntity(ctx, game, "quest", "wanted-hogger",
			markdown.LinksFilter{Limit: 2})
		assert.Must(t, err == nil, "LinksByEntity: %v", err)
		assert.Must(t, first.NextCursor != "", "a full page must carry a cursor")
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
	})

	// TestLinksArea's "a full final page of attachments carries a cursor to an
	// empty one" case pins the contract EntityLinkPage states: a listing whose
	// length is an exact multiple of the limit ends on an empty page rather
	// than on a short one, and a caller looping until the cursor is empty must
	// expect that rather than treat it as an error.
	t.Run("a full final page of attachments carries a cursor to an empty one", func(t *testing.T) {
		svc, entities, _, pool := a.service(t)
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
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(page.Links) == 2 && page.NextCursor != "", "page = %+v, want two rows and a cursor", page)
		last, err := svc.LinksByDocument(ctx, game, "s",
			markdown.LinksFilter{Limit: 2, Cursor: page.NextCursor})
		assert.Must(t, err == nil, "LinksByDocument: %v", err)
		assert.Must(t, len(last.Links) == 0 && last.NextCursor == "", "final page = %+v, want an empty one with no cursor", last)
	})
}

// TestNoLinkIsAnnouncedWhenTheAttachmentCannotCommit is the placement
// TestLinksArea's "no link is announced when the attachment is refused"
// case cannot reach, and this package has now found the same gap four times
// — Task 3's correction 4 for Write, Task 4's for Delete, Task 6's for
// Revert, and here. A publish sitting as the last statement *inside*
// withTx's callback differs from the correct placement only by the commit
// that follows, and every other failure LinkAdd can produce returns from
// the callback before that statement runs.
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
	assert.Must(t, err != nil, "want the commit to fail")
	assert.Must(t, strings.Contains(err.Error(), "commit"), "err = %v, want the commit to be what failed", err)
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
