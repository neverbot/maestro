package markdown_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/realtime"
)

// writeVersions writes n successive versions of one document, whose
// bodies are "v1\n", "v2\n" and so on.
func writeVersions(t *testing.T, svc *markdown.Service, game uuid.UUID, path string, n int32) {
	t.Helper()
	ctx := context.Background()
	for v := int32(0); v < n; v++ {
		if _, err := svc.Write(ctx, game, markdown.WriteInput{
			Path:            path,
			Content:         fmt.Sprintf("v%d\n", v+1),
			Message:         fmt.Sprintf("edit %d", v+1),
			ExpectedVersion: ptrInt32(v),
		}); err != nil {
			t.Fatalf("write version %d: %v", v+1, err)
		}
	}
}

func TestHistoryIsNewestFirstAndCarriesNoBodies(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 3)

	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Versions) != 3 {
		t.Fatalf("%d versions, want 3", len(page.Versions))
	}
	if page.Versions[0].Version != 3 || page.Versions[1].Version != 2 || page.Versions[2].Version != 1 {
		t.Fatalf("versions came back %d, %d, %d, want newest first",
			page.Versions[0].Version, page.Versions[1].Version, page.Versions[2].Version)
	}
	if page.Versions[0].Message != "edit 3" {
		t.Fatalf("Message = %q, want %q", page.Versions[0].Message, "edit 3")
	}
	if page.NextCursor != "" {
		t.Fatalf("NextCursor = %q, want empty: the page was not full", page.NextCursor)
	}
	if page.Versions[0].Title == "" {
		t.Fatal("a history row carries the title it was written with")
	}
}

// TestAHistoryRowCarriesNoBodyAtAll is the assertion the test above
// cannot make from a field it can name: what has to be pinned is the
// *absence* of a field, so it is checked over every field the generated
// row type has. Adding body_md or frontmatter to ListDocumentVersions'
// SELECT list — the whole document back, once per version — is what
// turns this red.
func TestAHistoryRowCarriesNoBodyAtAll(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	const body = "the-body-no-history-row-may-carry\n"
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "---\ntitle: T\nsummary: S\n---\n" + body,
		ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	row := reflect.ValueOf(page.Versions[0])
	for i := 0; i < row.NumField(); i++ {
		field := row.Type().Field(i)
		if name := strings.ToLower(field.Name); strings.Contains(name, "body") ||
			strings.Contains(name, "frontmatter") {
			t.Fatalf("a history row carries %s: bodies belong to ReadVersion alone", field.Name)
		}
		if rendered := fmt.Sprintf("%v", row.Field(i).Interface()); strings.Contains(rendered, body) {
			t.Fatalf("field %s of a history row holds the body %q", field.Name, rendered)
		}
	}
}

func TestHistoryPagesWithACursorBelongingToItsOwnDocument(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 5)
	writeVersions(t, svc, game, "tone", 5)

	first, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible", Limit: 2})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(first.Versions) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %d versions with cursor %q, want 2 and a cursor",
			len(first.Versions), first.NextCursor)
	}
	second, err := svc.History(ctx, game, markdown.HistoryFilter{
		Path: "bible", Limit: 2, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Versions) != 2 || second.Versions[0].Version != 3 {
		t.Fatalf("second page = %d versions starting at %d, want 2 starting at 3",
			len(second.Versions), second.Versions[0].Version)
	}

	// A cursor belongs to the listing that issued it and to no other.
	_, err = svc.History(ctx, game, markdown.HistoryFilter{
		Path: "tone", Limit: 2, Cursor: first.NextCursor,
	})
	requireFieldError(t, err, "cursor", "was issued for a different listing")
}

// TestACursorFromAnotherGamesHistoryIsRefused is this domain's
// equivalent of internal/metamodel's TestACursorFromAnotherGameIsRefused,
// which paging.Fingerprint's own comment instructs every new caller to
// write: without the project id leading the fingerprint, two games'
// histories of one path share it and one game's cursor pages the
// other's versions from a position that means nothing there.
func TestACursorFromAnotherGamesHistoryIsRefused(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	writeVersions(t, svc, azeroth, "bible", 5)
	writeVersions(t, svc, outland, "bible", 5)

	first, err := svc.History(ctx, azeroth, markdown.HistoryFilter{Path: "bible", Limit: 2})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	_, err = svc.History(ctx, outland, markdown.HistoryFilter{
		Path: "bible", Limit: 2, Cursor: first.NextCursor,
	})
	requireFieldError(t, err, "cursor", "was issued for a different listing")
}

func TestAMalformedHistoryCursorIsInvalidInputAtItsOwnPath(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 2)

	_, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible", Cursor: "not base64!!"})
	requireFieldError(t, err, "cursor",
		"is malformed (it is not the encoding this listing issues): "+
			"page from the cursor a previous call returned, or omit it to start")
}

// TestAHistoryPageAsksForTooMuchAndGetsTheCap does not pin the clamp
// policy itself -- with only two versions in the fixture, clamping to
// MaxHistoryPage, folding onto DefaultHistoryPage and applying no bound
// at all all return the same two rows, so no assertion here can tell
// them apart. That policy is paging.Size's and is pinned directly, in
// internal/paging/cursor_test.go's
// TestSizeClampsRatherThanFoldingOntoTheDefault. What this test does
// pin: History does not error or truncate below a real page's worth of
// rows when asked for more than the cap.
func TestAHistoryPageAsksForTooMuchAndGetsTheCap(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 2)

	page, err := svc.History(ctx, game, markdown.HistoryFilter{
		Path: "bible", Limit: markdown.MaxHistoryPage + 1,
	})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Versions) != 2 {
		t.Fatalf("%d versions, want 2", len(page.Versions))
	}
	if page.NextCursor != "" {
		t.Fatalf("NextCursor = %q: two rows do not fill a page of %d",
			page.NextCursor, markdown.MaxHistoryPage)
	}
}

func TestHistoryOfADeletedDocumentIsStillReadable(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 1)
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1), Message: "cut",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History of a deleted document: %v", err)
	}
	if len(page.Versions) != 2 || !page.Versions[0].Deleted {
		t.Fatalf("history = %+v, want two versions with the newest marked deleted", page.Versions)
	}
	if page.Versions[1].Deleted {
		t.Fatal("the live version must not be marked deleted")
	}
	if page.Versions[0].Message != "cut" {
		t.Fatalf("the tombstone's message = %q, want %q", page.Versions[0].Message, "cut")
	}
}

func TestReadVersionReturnsTheBodyAsItStood(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 3)

	v2, err := svc.ReadVersion(ctx, game, "bible", 2)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	if v2.BodyMd != "v2\n" {
		t.Fatalf("BodyMd = %q, want %q", v2.BodyMd, "v2\n")
	}
	// The current document is untouched by a read.
	current, err := svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if current.BodyMd != "v3\n" {
		t.Fatalf("BodyMd = %q, want %q", current.BodyMd, "v3\n")
	}
}

func TestReadingAVersionThatDoesNotExistNamesTheArgument(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 2)

	_, err := svc.ReadVersion(ctx, game, "bible", 9)
	if !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("want not_found, got %v", err)
	}
	// At `version` and not at `path`: a caller that named the document
	// correctly and the version wrongly must not go looking for the
	// document.
	requireMissing(t, err, "version", `the document at "bible" has no version 9`)
}

func TestReadingAnotherGamesVersionIsNotFound(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	writeVersions(t, svc, azeroth, "bible", 2)

	_, err := svc.ReadVersion(ctx, outland, "bible", 1)
	if !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("a token for outland must not read azeroth's version, got %v", err)
	}
	requireMissing(t, err, "path", `this game has no document at "bible"`)

	_, err = svc.History(ctx, outland, markdown.HistoryFilter{Path: "bible"})
	if !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("nor its history, got %v", err)
	}
	requireMissing(t, err, "path", `this game has no document at "bible"`)
}

// TestTwoGamesSharingOnePathKeepSeparateHistories is the isolation the
// test above cannot show, because a miss looks the same whatever the
// reason: with the path taken in both games, each history must answer
// with its own rows and its own project id.
func TestTwoGamesSharingOnePathKeepSeparateHistories(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	writeVersions(t, svc, azeroth, "bible", 3)
	writeVersions(t, svc, outland, "bible", 1)

	page, err := svc.History(ctx, outland, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Versions) != 1 {
		t.Fatalf("%d versions in outland, want 1: azeroth's three are not its own", len(page.Versions))
	}
	// The project id itself is not on VersionSummary — see that type —
	// and what this test pins is the filter that uses it: outland's one
	// version and not azeroth's three.
	if _, err := svc.ReadVersion(ctx, outland, "bible", 3); !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("outland's document has no version 3, got %v", err)
	}
}

// TestAVersionRowCannotClaimAGameItsDocumentDoesNotBelongTo is the
// database-level half of the same invariant, and it is here rather than
// only in internal/db because this is the package that writes the
// column: InsertDocumentVersion sets project_id from the caller's own
// resolved id, and 0007_documents.sql's composite key is the only thing
// that would refuse a mismatch.
func TestAVersionRowCannotClaimAGameItsDocumentDoesNotBelongTo(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	writeVersions(t, svc, azeroth, "bible", 1)

	doc, err := svc.Read(ctx, azeroth, "bible")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	_, err = pool.Exec(ctx,
		`INSERT INTO document_versions (project_id, document_id, version, body_md, message)
		 VALUES ($1, $2, 99, 'smuggled', 'from another game')`, outland, doc.ID)
	if err == nil {
		t.Fatal("a version row naming another game's document must be refused")
	}
	// And the refusal leaves nothing behind for History to serve.
	page, err := svc.History(ctx, azeroth, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Versions) != 1 {
		t.Fatalf("%d versions, want 1", len(page.Versions))
	}
}

func TestReadingAVersionOfAPathThatIsNotOneIsInvalidInput(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.ReadVersion(ctx, game, "/leading", 1); err == nil {
		t.Fatal("want a refusal for a bad path")
	} else {
		requireFieldError(t, err, "path", "")
	}
	_, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "/leading"})
	requireFieldError(t, err, "path", "")
}

func TestRevertWritesForwardRatherThanRewritingHistory(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 3)

	doc, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 1, ExpectedVersion: ptrInt32(3),
	})
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if doc.CurrentVersion != 4 {
		t.Fatalf("CurrentVersion = %d, want 4: a revert appends", doc.CurrentVersion)
	}
	if doc.BodyMd != "v1\n" {
		t.Fatalf("BodyMd = %q, want %q", doc.BodyMd, "v1\n")
	}

	// There is no state in which version 3 exists and version 2 does not.
	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Versions) != 4 {
		t.Fatalf("%d versions, want 4", len(page.Versions))
	}
	if page.Versions[0].Message != "reverted to version 1" {
		t.Fatalf("Message = %q, want the automatic message", page.Versions[0].Message)
	}
	if page.Versions[0].Deleted {
		t.Fatal("a revert writes a live version, not a tombstone")
	}
	// The restored version itself is still there, unchanged.
	v1, err := svc.ReadVersion(ctx, game, "bible", 1)
	if err != nil {
		t.Fatalf("ReadVersion(1): %v", err)
	}
	if v1.BodyMd != "v1\n" || v1.Message != "edit 1" {
		t.Fatalf("version 1 = (%q, %q), want it untouched by the revert", v1.BodyMd, v1.Message)
	}
}

func TestARevertTakesAMessageOfItsOwnWhenOneIsGiven(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 2)

	if _, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 1, ExpectedVersion: ptrInt32(2),
		Message: "the rewrite lost the tone",
	}); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if page.Versions[0].Message != "the rewrite lost the tone" {
		t.Fatalf("Message = %q, want the caller's own", page.Versions[0].Message)
	}
}

func TestRevertKeepsTheStoredTitleRatherThanDerivingItAgain(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "---\ntitle: The Old Name\n---\nbody\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "# A New Name\n\nbody\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	doc, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 1, ExpectedVersion: ptrInt32(2),
	})
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	// Restoring means restoring what was stored, not re-running today's
	// derivation rules over yesterday's body.
	if doc.Title != "The Old Name" {
		t.Fatalf("Title = %q, want the stored title of version 1", doc.Title)
	}
	if string(doc.Frontmatter) != `{"title": "The Old Name"}` {
		t.Fatalf("Frontmatter = %s, want version 1's own", doc.Frontmatter)
	}
	if doc.BodyMd != "body\n" {
		t.Fatalf("BodyMd = %q, want version 1's own", doc.BodyMd)
	}
}

// TestRevertingLeavesTheDocumentsKindAlone pins RevertInput's own
// comment: kind is a property of the document and is not versioned, so
// restoring prose must not move the shelf the document sits on.
func TestRevertingLeavesTheDocumentsKindAlone(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", Kind: ptrString("lore"), ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "two\n", Kind: ptrString("script"), ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	doc, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 1, ExpectedVersion: ptrInt32(2),
	})
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if doc.Kind != "script" {
		t.Fatalf("Kind = %q, want %q: a revert restores prose, not the shelf", doc.Kind, "script")
	}
}

// TestRevertingToATombstoneRestoresItsBodyAndLeavesTheDocumentAlive
// pins Revert's own comment. The alternative — re-deleting — would mean
// one call doing two things, with the delete's own expected_version
// never checked.
func TestRevertingToATombstoneRestoresItsBodyAndLeavesTheDocumentAlive(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 1)
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1), Message: "cut",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "back\n", ExpectedVersion: ptrInt32(2),
	}); err != nil {
		t.Fatalf("resurrect: %v", err)
	}

	doc, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 2, ExpectedVersion: ptrInt32(3),
	})
	if err != nil {
		t.Fatalf("Revert to the tombstone: %v", err)
	}
	if doc.DeletedAt.Valid {
		t.Fatal("reverting to a tombstone must not re-delete the document")
	}
	if doc.BodyMd != "v1\n" {
		t.Fatalf("BodyMd = %q, want the tombstone's own body", doc.BodyMd)
	}
	if _, err := svc.Read(ctx, game, "bible"); err != nil {
		t.Fatalf("the document must still read: %v", err)
	}
	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if page.Versions[0].Deleted {
		t.Fatal("the version a revert writes is live even when its source was a tombstone")
	}
}

// TestRevertingADeletedDocumentResurrectsIt pins the case Revert's own
// doc comment names as following from writeWith rather than being
// chosen here: the document itself is currently deleted (not merely the
// version being restored), and a revert brings it back live, the same
// way a Write to a deleted path does. Delete at version 4 (tombstone
// lands at 5), then revert to version 1 -- the document comes back live
// at version 6, its content equal to version 1's, deleted_at cleared.
func TestRevertingADeletedDocumentResurrectsIt(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 4)
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(4), Message: "cut",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	doc, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 1, ExpectedVersion: ptrInt32(5),
	})
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if doc.DeletedAt.Valid {
		t.Fatal("reverting a deleted document must resurrect it, deleted_at cleared")
	}
	if doc.CurrentVersion != 6 {
		t.Fatalf("CurrentVersion = %d, want 6 (the tombstone was version 5)", doc.CurrentVersion)
	}
	if doc.BodyMd != "v1\n" {
		t.Fatalf("BodyMd = %q, want version 1's own body", doc.BodyMd)
	}
	if _, err := svc.Read(ctx, game, "bible"); err != nil {
		t.Fatalf("the document must read live after the revert: %v", err)
	}
}

func TestRevertingToAMissingVersionNamesTheArgument(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 2)

	_, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 9, ExpectedVersion: ptrInt32(2),
	})
	requireMissing(t, err, "to_version", `the document at "bible" has no version 9`)

	// Nothing was written.
	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Versions) != 2 {
		t.Fatalf("%d versions after a refused revert, want 2", len(page.Versions))
	}
	doc, err := svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if doc.CurrentVersion != 2 {
		t.Fatalf("CurrentVersion = %d, want 2: a refused revert changes nothing", doc.CurrentVersion)
	}
}

func TestRevertingWithAStaleVersionIsAConflict(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 3)

	_, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 1, ExpectedVersion: ptrInt32(2), IncludeCurrent: true,
	})
	var conflict *markdown.ConflictError
	if !errors.As(err, &conflict) || conflict.Current != 3 || conflict.BodyMD != "v3\n" {
		t.Fatalf("want a conflict at version 3 carrying v3, got %#v", err)
	}
	if conflict.Deleted {
		t.Fatal("a live document's conflict must not claim it was deleted")
	}
}

func TestEveryProblemWithOneRevertIsReportedInOnePass(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "/bad", ToVersion: 0, Message: "line\nbreak",
	})
	requireFieldError(t, err, "path", "")
	requireFieldError(t, err, "to_version", "must be 1 or greater")
	requireFieldError(t, err, "message", "holds a control character")
	requireFieldError(t, err, "expected_version", "is required")
}

func TestRevertingADocumentThatWasNeverThereIsNotFound(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 1, ExpectedVersion: ptrInt32(1),
	})
	requireMissing(t, err, "path", `this game has no document at "bible"`)
}

func TestRevertIsAnnouncedWithTheVersionItRestored(t *testing.T) {
	svc, _, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 2)

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	if _, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 1, ExpectedVersion: ptrInt32(2),
	}); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	var ev realtime.Event
	select {
	case ev = <-sub.C:
	case <-time.After(5 * time.Second):
		t.Fatal("the committed revert announced nothing within 5s")
	}
	if ev.Kind != "document.reverted" {
		t.Fatalf("Kind = %q, want %q", ev.Kind, "document.reverted")
	}
	payload, ok := ev.Payload.(markdown.RevertEvent)
	if !ok || payload.Version != 3 || payload.FromVersion != 1 || payload.Path != "bible" {
		t.Fatalf("payload = %#v, want bible at version 3 restored from 1", ev.Payload)
	}
}

// TestNoRevertIsAnnouncedWhenTheRevertIsRefused is what the test above
// cannot reach, and it is here because the plan predicted the
// announcement test would stay green against a publish moved inside
// withTx — it does, since every refusal returns before the publish
// wherever the call sits. This one asserts the other direction: a revert
// whose to_version does not exist announces nothing at all, so a
// subscriber never hears about a restore that did not happen.
func TestNoRevertIsAnnouncedWhenTheRevertIsRefused(t *testing.T) {
	svc, _, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 2)

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	if _, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 9, ExpectedVersion: ptrInt32(2),
	}); err == nil {
		t.Fatal("want the revert refused")
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("a refused revert announced %v", ev)
	default:
	}
}

// TestNoRevertIsAnnouncedWhenTheRevertCannotCommit is the placement
// TestNoRevertIsAnnouncedWhenTheRevertIsRefused cannot reach, and this
// package has now found the same gap three times — Task 3's correction 4
// for Write, Task 4's correction 4 for Delete, and here. A publish
// sitting as the last statement *inside* withTx's callback differs from
// the correct placement only by the commit that follows, and every other
// failure a revert can produce returns from the callback before that
// statement runs: verified, with the publish moved there both
// TestRevertIsAnnouncedWithTheVersionItRestored and
// TestNoRevertIsAnnouncedWhenTheRevertIsRefused stay green.
//
// The technique is TestNoDeletionIsAnnouncedWhenTheDeleteCannotCommit's,
// unchanged: a deferred foreign key from document_versions.id to
// projects.id is satisfied by nothing, but being DEFERRABLE INITIALLY
// DEFERRED it is checked at COMMIT, so every statement succeeds and only
// the commit fails. NOT VALID is what lets it be added while earlier
// versions already stand.
func TestNoRevertIsAnnouncedWhenTheRevertCannotCommit(t *testing.T) {
	svc, _, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	writeVersions(t, svc, game, "bible", 2)

	if _, err := pool.Exec(ctx,
		`ALTER TABLE document_versions ADD CONSTRAINT versions_commit_must_fail
		   FOREIGN KEY (id) REFERENCES projects (id) DEFERRABLE INITIALLY DEFERRED NOT VALID`); err != nil {
		t.Fatalf("install the deferred constraint: %v", err)
	}

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	_, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 1, ExpectedVersion: ptrInt32(2),
	})
	if err == nil {
		t.Fatal("want the commit to fail")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Fatalf("err = %v, want the commit to be what failed", err)
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("a revert whose commit failed announced %v", ev)
	default:
	}
}

// TestAVersionIsAddressedByItsOwnDocument is what the cross-game tests
// cannot show: with two documents inside *one* game, the project filter
// separates nothing and only the document filter on the two version
// queries answers the right rows. Dropping either query's document_id
// filter is what turns this red.
func TestAVersionIsAddressedByItsOwnDocument(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	for _, doc := range []struct{ path, body string }{
		{"bible", "the bible\n"},
		{"tone", "the tone\n"},
	} {
		if _, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: doc.path, Content: doc.body, ExpectedVersion: ptrInt32(0),
		}); err != nil {
			t.Fatalf("write %s: %v", doc.path, err)
		}
	}

	got, err := svc.ReadVersion(ctx, game, "tone", 1)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	if got.BodyMd != "the tone\n" {
		t.Fatalf("BodyMd = %q, want tone's own version 1", got.BodyMd)
	}
	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "tone"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Versions) != 1 {
		t.Fatalf("%d versions in tone's history, want 1: the bible's are not its own",
			len(page.Versions))
	}
	if page.Versions[0].Title != "tone" {
		t.Fatalf("Title = %q, want tone's own", page.Versions[0].Title)
	}
}

// TestARevertRecordsWhoMadeIt pins that the audit columns of the version
// a revert writes name the reverter, not the author of the version whose
// content was restored: the restore is a new edit and someone made it.
func TestARevertRecordsWhoMadeIt(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	author := newUser(t, pool, "author@example.test")
	reverter := newUser(t, pool, "reverter@example.test")

	for v := int32(0); v < 2; v++ {
		if _, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: "bible", Content: fmt.Sprintf("v%d\n", v+1), ExpectedVersion: ptrInt32(v),
			Actor: markdown.Actor{UserID: &author},
		}); err != nil {
			t.Fatalf("write %d: %v", v+1, err)
		}
	}
	if _, err := svc.Revert(ctx, game, markdown.RevertInput{
		Path: "bible", ToVersion: 1, ExpectedVersion: ptrInt32(2),
		Actor: markdown.Actor{UserID: &reverter},
	}); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	got := page.Versions[0]
	if got.Author.Kind != "user" || got.Author.ID == nil || *got.Author.ID != reverter {
		t.Fatalf("author = %+v, want the reverter %v", got.Author, reverter)
	}
	if first := page.Versions[2].Author; first.ID == nil || *first.ID != author {
		t.Fatalf("version 1's author moved: %+v", first)
	}
}
