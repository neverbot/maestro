package markdown_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/markdown"
)

// seedDocuments writes n documents under a prefix, so a listing has
// enough rows to page. Sixty, when the default page is fifty: a fixture
// smaller than the default cannot tell "the default applies" from "no
// bound applies", and one smaller than the page a test asks for cannot
// tell a clamp from a fold onto the default. See
// TestTheLimitIsClampedRatherThanFoldedOntoTheDefault, which is the
// reason the number is what it is.
func seedDocuments(t *testing.T, svc *markdown.Service, game uuid.UUID, prefix string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if _, err := svc.Write(ctx, game, markdown.WriteInput{
			Path:            fmt.Sprintf("%s/doc-%03d", prefix, i),
			Content:         fmt.Sprintf("body %d\n", i),
			Kind:            ptrString("lore"),
			ExpectedVersion: ptrInt32(0),
		}); err != nil {
			t.Fatalf("seed %s/doc-%03d: %v", prefix, i, err)
		}
	}
}

// TestAListingIsSummariesInPathOrderAndEveryFieldReadsBack reads every
// field of DocumentSummary back through List, which is this domain's
// public reader for a listing. The rule this plan's header states — a
// value written by one path and read by none is how a relation's fields
// stayed write-only through nine review rounds — applies to a projection
// as much as to a column: a field of this type that no test reads is a
// field that can be dropped from the select list without anything going
// red.
func TestAListingIsSummariesInPathOrderAndEveryFieldReadsBack(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	designer := newUser(t, pool, "designer@example.test")

	// Written in reverse path order on purpose: with the two writes in
	// path order, an ORDER BY that sorted by creation time instead would
	// answer identically and this test could not tell the two apart.
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path:            "scripts/hogger",
		Content:         "Hogger says hello.\n",
		Kind:            ptrString("script"),
		ExpectedVersion: ptrInt32(0),
		Actor:           markdown.Actor{UserID: &designer},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	written, err := svc.Write(ctx, game, markdown.WriteInput{
		Path:            "lore/duskwood",
		Content:         "# Duskwood\n\nA haunted forest.\n",
		Kind:            ptrString("lore"),
		ExpectedVersion: ptrInt32(0),
		Actor:           markdown.Actor{UserID: &designer},
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	page, err := svc.List(ctx, game, markdown.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Documents) != 2 {
		t.Fatalf("%d documents, want 2", len(page.Documents))
	}
	if page.Documents[0].Path != "lore/duskwood" || page.Documents[1].Path != "scripts/hogger" {
		t.Fatalf("paths = %q, %q, want path order",
			page.Documents[0].Path, page.Documents[1].Path)
	}
	got := page.Documents[0]
	// The two timestamps and the two authors are compared against the
	// row the write itself answered with, not against "not zero": a
	// listing that invented a plausible time would pass the weaker check,
	// and these four are exactly the fields that were selected by this
	// query and dropped on the floor before this run.
	//
	// The two authors come out of the struct comparison first, because
	// Author carries a *uuid.UUID and == on a pointer compares addresses
	// rather than ids: two authors naming the same user would fail an
	// equality that reads correct in the failure message, which is worse
	// than no test. They are asserted by value immediately below.
	gotCreatedBy, gotUpdatedBy := got.CreatedBy, got.UpdatedBy
	got.CreatedBy, got.UpdatedBy = markdown.Author{}, markdown.Author{}
	want := markdown.DocumentSummary{
		ID:             written.ID,
		Path:           "lore/duskwood",
		Kind:           "lore",
		Title:          "Duskwood",
		Summary:        "A haunted forest.",
		CurrentVersion: 1,
		Deleted:        false,
		CreatedAt:      written.CreatedAt.Time,
		UpdatedAt:      written.UpdatedAt.Time,
	}
	if got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
	for name, author := range map[string]markdown.Author{
		"created_by": gotCreatedBy, "updated_by": gotUpdatedBy,
	} {
		if author.Kind != "user" || author.ID == nil || *author.ID != designer {
			t.Fatalf("%s = %+v, want the designer %v", name, author, designer)
		}
		if author.Label != "designer@example.test" {
			t.Fatalf("%s label = %q, want the designer's display name", name, author.Label)
		}
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("a listing row that says nothing about when the document changed " +
			"makes \"what moved this week\" a call per document")
	}
}

// TestAListingRowCarriesNoBodyAtAll pins the absence over every field of
// the type rather than over a field it can name, because what has to
// hold is that no such field exists — the same shape
// TestAHistoryRowCarriesNoBodyAtAll takes for a version row, and the
// same two checks: a substring match on the lowered field name, so a
// field spelled any way that reads as a body or a frontmatter survives
// a rename, and a scan of every field's rendered value for a body
// string actually written and read back, so a field under some other
// name that happens to carry prose is caught too. A listing is the one
// call in this domain an agent makes against a whole game, and a body
// on it would blow a context window on the first call.
//
// A version of this test that instead matched a hardcoded set of field
// names shipped in Task 8 and was proved too weak by a later review:
// adding a populated `Markdown string` field to DocumentSummary left it
// green. This shape was proved red against that same mutation, both
// empty and populated with the seeded body text.
func TestAListingRowCarriesNoBodyAtAll(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	const body = "the-body-no-listing-row-may-carry\n"
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "---\ntitle: T\nsummary: S\n---\n" + body,
		ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	page, err := svc.List(ctx, game, markdown.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Documents) != 1 {
		t.Fatalf("%d documents, want 1", len(page.Documents))
	}
	row := reflect.ValueOf(page.Documents[0])
	for i := 0; i < row.NumField(); i++ {
		field := row.Type().Field(i)
		if name := strings.ToLower(field.Name); strings.Contains(name, "body") ||
			strings.Contains(name, "frontmatter") {
			t.Fatalf("a listing row carries %s: a listing returns summaries, never bodies",
				field.Name)
		}
		if rendered := fmt.Sprintf("%v", row.Field(i).Interface()); strings.Contains(rendered, body) {
			t.Fatalf("field %s of a listing row holds the body %q", field.Name, rendered)
		}
	}
}

func TestAListingPagesAndItsCursorBelongsToItsFilter(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	seedDocuments(t, svc, game, "lore", 60)
	seedDocuments(t, svc, game, "scripts", 5)

	first, err := svc.List(ctx, game, markdown.ListFilter{Limit: 25})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(first.Documents) != 25 || first.NextCursor == "" {
		t.Fatalf("first page = %d docs, cursor %q", len(first.Documents), first.NextCursor)
	}
	second, err := svc.List(ctx, game, markdown.ListFilter{Limit: 25, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if second.Documents[0].Path != "lore/doc-025" {
		t.Fatalf("second page starts at %q, want lore/doc-025", second.Documents[0].Path)
	}

	// The same cursor against a different filter is refused. One case per
	// part of the fingerprint that a caller can move, because a cursor
	// that pages into a different question answers it perfectly and says
	// nothing.
	for _, other := range []struct {
		name   string
		filter markdown.ListFilter
	}{
		{"a path prefix", markdown.ListFilter{Limit: 25, PathPrefix: "lore/"}},
		{"a kind", markdown.ListFilter{Limit: 25, Kind: "lore"}},
		{"deleted documents too", markdown.ListFilter{Limit: 25, IncludeDeleted: true}},
	} {
		t.Run(other.name, func(t *testing.T) {
			filter := other.filter
			filter.Cursor = first.NextCursor
			_, err := svc.List(ctx, game, filter)
			requireFieldError(t, err, "cursor", "different listing")
		})
	}
}

// TestACursorFromAnEntityFilteredListingIsRefusedElsewhere is the entity
// half of the fingerprint, which the neighbouring test cannot reach: an
// entity filter needs an entity, and the cursor has to come *from* the
// filtered listing to prove the part is in the digest at all.
func TestACursorFromAnEntityFilteredListingIsRefusedElsewhere(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	newQuest(t, entities, game, "the-defias", "The Defias Brotherhood")

	for i := 0; i < 4; i++ {
		if _, err := svc.Write(ctx, game, markdown.WriteInput{
			Path:            fmt.Sprintf("scripts/act-%d", i),
			Content:         "x\n",
			ExpectedVersion: ptrInt32(0),
			Links: &[]markdown.LinkTarget{
				{EntityType: "quest", EntityKey: "wanted-hogger"},
			},
		}); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	filtered, err := svc.List(ctx, game, markdown.ListFilter{
		Limit: 2, EntityType: "quest", EntityKey: "wanted-hogger",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(filtered.Documents) != 2 || filtered.NextCursor == "" {
		t.Fatalf("filtered page = %d docs, cursor %q",
			len(filtered.Documents), filtered.NextCursor)
	}

	// The same position, carried to the unfiltered listing and to a
	// listing filtered on another entity. Both would page perfectly.
	_, err = svc.List(ctx, game, markdown.ListFilter{Limit: 2, Cursor: filtered.NextCursor})
	requireFieldError(t, err, "cursor", "different listing")

	_, err = svc.List(ctx, game, markdown.ListFilter{
		Limit: 2, Cursor: filtered.NextCursor,
		EntityType: "quest", EntityKey: "the-defias",
	})
	requireFieldError(t, err, "cursor", "different listing")

	// And two spellings of one entity key are one listing, because the
	// fingerprint carries the *resolved* id rather than what was typed.
	same, err := svc.List(ctx, game, markdown.ListFilter{
		Limit: 2, Cursor: filtered.NextCursor,
		EntityType: "QUEST", EntityKey: "Wanted-Hogger",
	})
	if err != nil {
		t.Fatalf("a cursor must survive a respelling of its own filter: %v", err)
	}
	if len(same.Documents) != 2 || same.Documents[0].Path != "scripts/act-2" {
		t.Fatalf("second filtered page = %+v, want scripts/act-2 first", same.Documents)
	}
}

func TestACursorFromAnotherGamesListingIsRefused(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	seedDocuments(t, svc, azeroth, "lore", 30)
	seedDocuments(t, svc, outland, "lore", 30)

	first, err := svc.List(ctx, azeroth, markdown.ListFilter{Limit: 25})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	_, err = svc.List(ctx, outland, markdown.ListFilter{Limit: 25, Cursor: first.NextCursor})
	requireFieldError(t, err, "cursor", "different listing")
}

func TestAFullFinalPageCarriesACursorToAnEmptyOne(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	seedDocuments(t, svc, game, "lore", 4)

	page, err := svc.List(ctx, game, markdown.ListFilter{Limit: 4})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("a full page must carry a cursor, even when it is the last one")
	}
	empty, err := svc.List(ctx, game, markdown.ListFilter{Limit: 4, Cursor: page.NextCursor})
	if err != nil {
		t.Fatalf("final page: %v", err)
	}
	if len(empty.Documents) != 0 || empty.NextCursor != "" {
		t.Fatalf("final page = %d docs with cursor %q, want empty and no cursor",
			len(empty.Documents), empty.NextCursor)
	}
	// A page that came back short carries no cursor at all, which is the
	// other half of the rule and the one a caller loops on.
	short, err := svc.List(ctx, game, markdown.ListFilter{Limit: 3, Cursor: ""})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if short.NextCursor == "" {
		t.Fatal("a full page of three must carry a cursor")
	}
	rest, err := svc.List(ctx, game, markdown.ListFilter{Limit: 3, Cursor: short.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(rest.Documents) != 1 || rest.NextCursor != "" {
		t.Fatalf("short page = %d docs with cursor %q, want 1 and no cursor",
			len(rest.Documents), rest.NextCursor)
	}
}

// TestAnEmptyListingIsAnEmptyPageAndNotNil pins the shape a game with no
// prose answers with: [] and not null, the same decision
// TestADocumentWithNoAttachmentsIsAnOrdinaryDocument makes for links,
// and asserted through encoding/json for the same reason — non-nilness
// is not the claim, what a client receives is.
func TestAnEmptyListingIsAnEmptyPageAndNotNil(t *testing.T) {
	svc, _, _, pool := newService(t)
	game := newGame(t, pool, "azeroth")

	page, err := svc.List(context.Background(), game, markdown.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	encoded, err := json.Marshal(page.Documents)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("an empty listing marshals as %s, want []", encoded)
	}
}

func TestAPathPrefixFilterDoesNotTreatUnderscoreAsAWildcard(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	for _, path := range []string{"lore_x/one", "loreax/two"} {
		if _, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: path, Content: "x\n", ExpectedVersion: ptrInt32(0),
		}); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	page, err := svc.List(ctx, game, markdown.ListFilter{PathPrefix: "lore_x"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Documents) != 1 || page.Documents[0].Path != "lore_x/one" {
		t.Fatalf("documents = %+v, want only lore_x/one: `_` is a path character, not a wildcard",
			page.Documents)
	}
	// And the prefix folds case, like every other path comparison in
	// this domain: a subtree is not two subtrees because someone
	// capitalised it.
	folded, err := svc.List(ctx, game, markdown.ListFilter{PathPrefix: "LORE_X/"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(folded.Documents) != 1 || folded.Documents[0].Path != "lore_x/one" {
		t.Fatalf("documents = %+v, want the prefix matched without regard to case",
			folded.Documents)
	}
}

func TestAListingFiltersByKindAndByEntity(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "scripts/hogger", Content: "x\n", Kind: ptrString("script"),
		ExpectedVersion: ptrInt32(0),
		Links:           &[]markdown.LinkTarget{{EntityType: "quest", EntityKey: "wanted-hogger"}},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/duskwood", Content: "y\n", Kind: ptrString("lore"),
		ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	byKind, err := svc.List(ctx, game, markdown.ListFilter{Kind: "SCRIPT"})
	if err != nil {
		t.Fatalf("List by kind: %v", err)
	}
	if len(byKind.Documents) != 1 || byKind.Documents[0].Path != "scripts/hogger" {
		t.Fatalf("by kind = %+v, want the script (kinds fold case)", byKind.Documents)
	}

	byEntity, err := svc.List(ctx, game, markdown.ListFilter{
		EntityType: "quest", EntityKey: "wanted-hogger",
	})
	if err != nil {
		t.Fatalf("List by entity: %v", err)
	}
	if len(byEntity.Documents) != 1 || byEntity.Documents[0].Path != "scripts/hogger" {
		t.Fatalf("by entity = %+v, want the script", byEntity.Documents)
	}
}

// TestADocumentAttachedToTwoEntitiesAppearsOnceInAFilteredListing is why
// the entity filter is an EXISTS and not a join: with a join, a document
// attached to two entities comes back twice on a listing filtered by one
// of them, and the page's row count stops being a count of documents.
// A caller paging that listing would see the same path twice and its
// page of fifty would hold fewer than fifty documents.
func TestADocumentAttachedToTwoEntitiesAppearsOnceInAFilteredListing(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	newEntityOfType(t, entities, game, "zone", "Zone", "elwynn", "Elwynn Forest")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "scripts/hogger", Content: "x\n", ExpectedVersion: ptrInt32(0),
		Links: &[]markdown.LinkTarget{
			{EntityType: "quest", EntityKey: "wanted-hogger"},
			{EntityType: "zone", EntityKey: "elwynn"},
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	page, err := svc.List(ctx, game, markdown.ListFilter{
		EntityType: "quest", EntityKey: "wanted-hogger",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Documents) != 1 {
		t.Fatalf("%d rows for one document attached to two entities, want 1: %+v",
			len(page.Documents), page.Documents)
	}
}

func TestAnUnknownFilterKeyIsNotFoundRatherThanAnEmptyPage(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")

	// A caller that mistyped `quesst` must hear about `quesst`, not be
	// told this game has no documents and go off to seed a second copy.
	_, err := svc.List(ctx, game, markdown.ListFilter{
		EntityType: "quesst", EntityKey: "wanted-hogger",
	})
	requireMissing(t, err, "entity_type", `this game has no entity type "quesst"`)

	// And the two misses are told apart, exactly as they are on a link:
	// a mistyped type and a mistyped key are two different mistakes at
	// two different arguments.
	_, err = svc.List(ctx, game, markdown.ListFilter{
		EntityType: "quest", EntityKey: "wanted-hoggerr",
	})
	requireMissing(t, err, "entity_key", `this game has no quest named "wanted-hoggerr"`)
}

// TestAnEntityFilterIsBoundedBeforePostgresSeesIt is Task 7's correction
// 3 applied to this listing: an entity key holding an invalid UTF-8 byte
// reaches Postgres as a byte sequence it refuses outright (SQLSTATE
// 22021), which would land on the default arm as internal_error over a
// value the caller itself supplied.
func TestAnEntityFilterIsBoundedBeforePostgresSeesIt(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	// The type has to be there, or the bad key never gets as far as the
	// entity lookup and the test would be red for the missing type
	// instead of for the byte Postgres refuses.
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")

	_, err := svc.List(ctx, game, markdown.ListFilter{
		EntityType: "quest", EntityKey: "hogger\x80",
	})
	requireFieldError(t, err, "entity_key",
		"must be letters, digits, underscores or hyphens, starting with a letter or a digit")

	_, err = svc.List(ctx, game, markdown.ListFilter{
		EntityType: "que st", EntityKey: "wanted-hogger",
	})
	requireFieldError(t, err, "entity_type",
		"must be letters, digits, underscores or hyphens, starting with a letter or a digit")
}

// TestAPathPrefixAndAKindAreBoundedAsTheCallersOwnArguments covers the
// other two filter strings, which reach the query as text and would
// otherwise reach Postgres unbounded for the same 22021.
func TestAPathPrefixAndAKindAreBoundedAsTheCallersOwnArguments(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.List(ctx, game, markdown.ListFilter{PathPrefix: "lore/\x80"})
	requireFieldError(t, err, "path_prefix", "is not valid UTF-8")

	_, err = svc.List(ctx, game, markdown.ListFilter{PathPrefix: "lore/\n"})
	requireFieldError(t, err, "path_prefix", "holds a control character")

	long := ""
	for i := 0; i <= markdown.MaxPathLen; i++ {
		long += "a"
	}
	_, err = svc.List(ctx, game, markdown.ListFilter{PathPrefix: long})
	requireFieldError(t, err, "path_prefix", "must be at most 200 bytes")

	_, err = svc.List(ctx, game, markdown.ListFilter{Kind: "lo\tre"})
	requireFieldError(t, err, "kind", "holds a control character")

	// A trailing slash is the most obvious thing a caller types and is
	// accepted, though CheckPath refuses it on a path: a prefix is not a
	// path, and refusing "lore/" would be refusing the subtree filter
	// this argument exists for.
	if _, err := svc.List(ctx, game, markdown.ListFilter{PathPrefix: "lore/"}); err != nil {
		t.Fatalf(`a prefix ending in a slash is a subtree, not a bad path: %v`, err)
	}
}

// TestEveryProblemWithOneListingIsReportedInOnePass keeps this call to
// the rule Write, Delete and LinkRemove already follow: a caller whose
// prefix and whose kind are both wrong hears about both, instead of
// fixing one, calling again and learning about the other.
func TestEveryProblemWithOneListingIsReportedInOnePass(t *testing.T) {
	svc, _, _, pool := newService(t)
	game := newGame(t, pool, "azeroth")

	_, err := svc.List(context.Background(), game, markdown.ListFilter{
		PathPrefix: "lore/\n", Kind: "lo\tre",
	})
	requireFieldError(t, err, "path_prefix", "holds a control character")
	requireFieldError(t, err, "kind", "holds a control character")
}

func TestNamingOnlyOneHalfOfTheEntityFilterIsInvalidInput(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.List(ctx, game, markdown.ListFilter{EntityType: "quest"})
	requireFieldError(t, err, "entity_key", "must be given together")

	_, err = svc.List(ctx, game, markdown.ListFilter{EntityKey: "wanted-hogger"})
	requireFieldError(t, err, "entity_type", "must be given together")
}

func TestDeletedDocumentsAreAbsentUnlessAskedFor(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	seedDocuments(t, svc, game, "lore", 2)
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "lore/doc-000", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	page, err := svc.List(ctx, game, markdown.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Documents) != 1 {
		t.Fatalf("%d documents, want 1", len(page.Documents))
	}
	withDeleted, err := svc.List(ctx, game, markdown.ListFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("List including deleted: %v", err)
	}
	if len(withDeleted.Documents) != 2 {
		t.Fatalf("%d documents, want 2", len(withDeleted.Documents))
	}
	var seen bool
	for _, doc := range withDeleted.Documents {
		if doc.Path != "lore/doc-000" {
			continue
		}
		seen = true
		if !doc.Deleted {
			t.Fatal("a deleted document in the listing must say it is deleted")
		}
	}
	if !seen {
		t.Fatal("the deleted document is not in the listing that asked for it")
	}
	for _, doc := range page.Documents {
		if doc.Deleted {
			t.Fatalf("%q is listed as deleted in a listing that excludes them", doc.Path)
		}
	}
}

func TestAListingNeverCrossesGames(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	seedDocuments(t, svc, azeroth, "lore", 3)

	page, err := svc.List(ctx, outland, markdown.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Documents) != 0 {
		t.Fatalf("outland sees %d of azeroth's documents, want none", len(page.Documents))
	}
}

// TestTheLimitIsClampedRatherThanFoldedOntoTheDefault needs a fixture
// larger than the default page, or clamping to the cap and folding onto
// the default return the same rows and the test proves nothing: sixty
// documents against a default of fifty is what separates them. The cap
// itself is not reachable from here — asking for the cap and asking for
// no bound at all both return the sixty rows that exist — and that
// policy is pinned against paging.Size directly, in
// internal/paging/cursor_test.go's
// TestSizeClampsRatherThanFoldingOntoTheDefault.
func TestTheLimitIsClampedRatherThanFoldedOntoTheDefault(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	seedDocuments(t, svc, game, "lore", 60)

	atCap, err := svc.List(ctx, game, markdown.ListFilter{Limit: markdown.MaxDocumentPage})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	overCap, err := svc.List(ctx, game, markdown.ListFilter{Limit: markdown.MaxDocumentPage + 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(atCap.Documents) != 60 {
		t.Fatalf("asking for the cap returned %d of 60 documents", len(atCap.Documents))
	}
	if len(atCap.Documents) != len(overCap.Documents) {
		t.Fatalf("asking for one more than the cap returned %d rather than %d: "+
			"a limit over the cap is clamped, never folded onto the default",
			len(overCap.Documents), len(atCap.Documents))
	}
	none, err := svc.List(ctx, game, markdown.ListFilter{Limit: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(none.Documents) != int(markdown.DefaultDocumentPage) {
		t.Fatalf("%d documents with no limit, want the default %d",
			len(none.Documents), markdown.DefaultDocumentPage)
	}
	negative, err := svc.List(ctx, game, markdown.ListFilter{Limit: -1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(negative.Documents) != int(markdown.DefaultDocumentPage) {
		t.Fatalf("%d documents for a negative limit, want the default %d",
			len(negative.Documents), markdown.DefaultDocumentPage)
	}
}

// TestACapExceedingFixtureIsActuallyCappedAtTheCap is what
// TestTheLimitIsClampedRatherThanFoldedOntoTheDefault's sixty-row
// fixture cannot prove: with sixty rows against a cap of two hundred,
// "clamped at the cap" and "no bound at all" answer identically, since
// there is nothing past the cap to lose. This fixture has more rows
// than MaxDocumentPage, so the two only agree if the cap is genuinely
// applied.
func TestACapExceedingFixtureIsActuallyCappedAtTheCap(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	seedDocuments(t, svc, game, "lore", int(markdown.MaxDocumentPage)+1)

	page, err := svc.List(ctx, game, markdown.ListFilter{Limit: markdown.MaxDocumentPage + 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Documents) != int(markdown.MaxDocumentPage) {
		t.Fatalf("%d documents, want the cap of %d: a limit over the cap must still be capped, "+
			"not treated as no bound at all", len(page.Documents), markdown.MaxDocumentPage)
	}
	if page.NextCursor == "" {
		t.Fatal("a page at the cap with rows still behind it must carry a cursor")
	}
}

// TestAListingsCursorIsRefusedWhenItIsNotOne keeps this listing's
// malformed-cursor answer the one every listing in Maestro gives: the
// caller's own argument, at path cursor, with the recovery in the
// message — not an internal_error over a value the caller supplied.
func TestAListingsCursorIsRefusedWhenItIsNotOne(t *testing.T) {
	svc, _, _, pool := newService(t)
	game := newGame(t, pool, "azeroth")

	_, err := svc.List(context.Background(), game, markdown.ListFilter{Cursor: "not-base64!!"})
	requireFieldError(t, err, "cursor",
		"is malformed (it is not the encoding this listing issues): "+
			"page from the cursor a previous call returned, or omit it to start")
}
