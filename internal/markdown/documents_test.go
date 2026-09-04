package markdown_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
)

func TestWritingADocumentCreatesItAtVersionOneAndItReadsBackByteForByte(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	// The title is quoted: `title: Wanted: Hogger` unquoted is not valid
	// YAML (a colon-space inside a plain scalar), which the plan's own
	// fixture missed and SplitContent correctly refuses.
	content := "---\ntitle: \"Wanted: Hogger\"\nera: first\n---\n" +
		"# Ignored\n\nHOGGER: *snarls*\n\n  Indented line kept as written.\n"
	doc, err := svc.Write(ctx, game, markdown.WriteInput{
		Path:            "scripts/wanted-hogger",
		Content:         content,
		Kind:            ptrString("script"),
		Message:         "first draft",
		ExpectedVersion: ptrInt32(0),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if doc.CurrentVersion != 1 {
		t.Fatalf("CurrentVersion = %d, want 1", doc.CurrentVersion)
	}

	// The read-back. This is the step the metamodel skipped for nine
	// review rounds, which is how an edge's field values shipped
	// write-only. Everything this write accepted must come back out.
	got, err := svc.Read(ctx, game, "scripts/wanted-hogger")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	wantBody := "# Ignored\n\nHOGGER: *snarls*\n\n  Indented line kept as written.\n"
	if got.BodyMd != wantBody {
		t.Fatalf("BodyMd = %q, want the body exactly as written (%q)", got.BodyMd, wantBody)
	}
	if got.Path != "scripts/wanted-hogger" {
		t.Fatalf("Path = %q, want the stored spelling", got.Path)
	}
	if got.Title != "Wanted: Hogger" {
		t.Fatalf("Title = %q, want the frontmatter title", got.Title)
	}
	// The summary is derived too, and is stored on the row; nothing but
	// a read-back would notice it going missing.
	if got.Summary != "HOGGER: *snarls*" {
		t.Fatalf("Summary = %q, want the first prose line under the heading", got.Summary)
	}
	if got.Kind != "script" {
		t.Fatalf("Kind = %q, want %q", got.Kind, "script")
	}
	if got.CurrentVersion != 1 {
		t.Fatalf("read-back CurrentVersion = %d, want 1", got.CurrentVersion)
	}
	var frontmatter map[string]any
	if err := json.Unmarshal(got.Frontmatter, &frontmatter); err != nil {
		t.Fatalf("decode frontmatter: %v", err)
	}
	if frontmatter["era"] != "first" {
		t.Fatalf("frontmatter = %v, want it to echo era back untouched", frontmatter)
	}
	if frontmatter["title"] != "Wanted: Hogger" {
		t.Fatalf("frontmatter = %v, want the title key stored as written too", frontmatter)
	}
}

// The audit columns are the other half of the read-back: a write records
// who made it, and Read is the surface that hands it back.
func TestTheActorOfAWriteReadsBackOnTheDocument(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	user := newUser(t, pool, "designer@example.test")
	token := newToken(t, pool, game, user, "seeding agent")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Actor: markdown.Actor{UserID: &user, TokenID: &token},
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	got, err := svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for name, id := range map[string]*uuid.UUID{
		"created_by_user_id":  got.CreatedByUserID,
		"updated_by_user_id":  got.UpdatedByUserID,
		"created_by_token_id": got.CreatedByTokenID,
		"updated_by_token_id": got.UpdatedByTokenID,
	} {
		if id == nil {
			t.Fatalf("%s is null, want the actor of the write", name)
		}
	}
	if *got.CreatedByUserID != user || *got.UpdatedByUserID != user {
		t.Fatalf("user columns = %v/%v, want %v", got.CreatedByUserID, got.UpdatedByUserID, user)
	}
	if *got.CreatedByTokenID != token || *got.UpdatedByTokenID != token {
		t.Fatalf("token columns = %v/%v, want %v", got.CreatedByTokenID, got.UpdatedByTokenID, token)
	}

	// A second write by nobody moves the updated_by columns and leaves
	// the created_by columns where they were: the creator of a document
	// is a fact about its first version.
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "two\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err = svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("Read after the second write: %v", err)
	}
	if got.CreatedByUserID == nil || *got.CreatedByUserID != user {
		t.Fatalf("created_by_user_id = %v, want it unchanged at %v", got.CreatedByUserID, user)
	}
	if got.UpdatedByUserID != nil || got.UpdatedByTokenID != nil {
		t.Fatalf("updated_by = %v/%v, want both null: the second write named no actor",
			got.UpdatedByUserID, got.UpdatedByTokenID)
	}
}

func TestTheFirstWriteAlsoWritesVersionOneWithItsAuthorAndMessage(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	user := newUser(t, pool, "designer@example.test")
	token := newToken(t, pool, game, user, "seeding agent")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "---\nera: first\n---\nThe world is called Azeroth.\n",
		Message: "the first note", ExpectedVersion: ptrInt32(0),
		Actor: markdown.Actor{UserID: &user, TokenID: &token},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// **This read-back goes through the public surface.** Task 3 landed
	// it as a raw pool.QueryRow, because History and ReadVersion did not
	// exist yet and this plan's rule is that no query lands ahead of its
	// caller — so a version's message, author, title, summary,
	// frontmatter and deleted flag were written and readable by nothing.
	// Task 6 pays that debt: every assertion below is now made through
	// the two readers a real caller uses, and the SQL is gone.
	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(page.Versions) != 1 {
		t.Fatalf("%d versions, want 1", len(page.Versions))
	}
	meta := page.Versions[0]
	if meta.Version != 1 || meta.Message != "the first note" {
		t.Fatalf("version row = (%d, %q), want (1, %q)",
			meta.Version, meta.Message, "the first note")
	}
	if meta.Title != "bible" || meta.Summary != "The world is called Azeroth." {
		t.Fatalf("version row derived values = (%q, %q), want the document's own",
			meta.Title, meta.Summary)
	}
	if meta.AuthorUserID == nil || *meta.AuthorUserID != user ||
		meta.AuthorTokenID == nil || *meta.AuthorTokenID != token {
		t.Fatalf("author = %v/%v, want %v/%v",
			meta.AuthorUserID, meta.AuthorTokenID, user, token)
	}
	if meta.Deleted {
		t.Fatal("deleted = true on an ordinary write: only a tombstone carries it")
	}
	if !meta.CreatedAt.Valid {
		t.Fatal("a version records when it was written")
	}
	// document_versions.project_id was written by InsertDocumentVersion
	// and asserted by nothing at all until this read (Task 3's
	// correction 15 recorded it for Task 6's isolation tests).
	if meta.ProjectID != game {
		t.Fatalf("ProjectID = %v, want the game the write was made in (%v)", meta.ProjectID, game)
	}

	// The body and the frontmatter are the half a history row
	// deliberately does not carry, so they come back through ReadVersion.
	full, err := svc.ReadVersion(ctx, game, "bible", 1)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	if full.BodyMd != "The world is called Azeroth.\n" {
		t.Fatalf("BodyMd = %q, want the body as written", full.BodyMd)
	}
	if string(full.Frontmatter) != `{"era": "first"}` {
		t.Fatalf("version frontmatter = %s, want the frontmatter of the write", full.Frontmatter)
	}
	if full.ProjectID != game {
		t.Fatalf("ProjectID = %v, want %v", full.ProjectID, game)
	}
}

func TestASecondWriteNeedsTheCurrentVersionAndAdvancesIt(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	doc, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "two\n", ExpectedVersion: ptrInt32(1),
	})
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if doc.CurrentVersion != 2 {
		t.Fatalf("CurrentVersion = %d, want 2", doc.CurrentVersion)
	}
	got, err := svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.BodyMd != "two\n" {
		t.Fatalf("BodyMd = %q, want the second write's body", got.BodyMd)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM document_versions v JOIN documents d ON d.id = v.document_id
		  WHERE d.project_id = $1 AND d.path = 'bible'`, game).Scan(&count); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if count != 2 {
		t.Fatalf("%d version rows, want 2: every save is a snapshot", count)
	}
}

func TestAStaleVersionIsAConflictThatCarriesTheCurrentBody(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "---\ntitle: The Bible\n---\ntwo\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "three\n", ExpectedVersion: ptrInt32(1),
		IncludeCurrent: true,
	})
	if !errors.Is(err, markdown.ErrVersionConflict) {
		t.Fatalf("want version_conflict, got %v", err)
	}
	var conflict *markdown.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want a *markdown.ConflictError, got %#v", err)
	}
	if conflict.Current != 2 {
		t.Fatalf("Current = %d, want 2", conflict.Current)
	}
	// The whole point of the shape: an agent merging prose needs the
	// text, and a second round trip for it costs a turn and a context
	// window.
	if conflict.BodyMD != "two\n" {
		t.Fatalf("BodyMD = %q, want the current body %q", conflict.BodyMD, "two\n")
	}
	details := conflict.Details()
	if got := details["current_body"]; got != "two\n" {
		t.Fatalf("Details()[current_body] = %v, want the current body", got)
	}
	if got := details["current_title"]; got != "The Bible" {
		t.Fatalf("Details()[current_title] = %v, want the current title", got)
	}
	raw, ok := details["current_frontmatter"].(json.RawMessage)
	if !ok || string(raw) != `{"title": "The Bible"}` {
		t.Fatalf("Details()[current_frontmatter] = %v, want the stored frontmatter", details["current_frontmatter"])
	}
	// The refused write left nothing behind.
	got, err := svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.CurrentVersion != 2 || got.BodyMd != "two\n" {
		t.Fatalf("after the refusal the document is at version %d with body %q, want 2 and %q",
			got.CurrentVersion, got.BodyMd, "two\n")
	}
}

func TestIncludeCurrentFalseWithholdsTheBodyAndKeepsTheVersion(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: strings.Repeat("a", 4096), ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "small\n", ExpectedVersion: ptrInt32(0),
		IncludeCurrent: false,
	})
	var conflict *markdown.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want a *markdown.ConflictError, got %#v", err)
	}
	details := conflict.Details()
	if details["current_version"] != int32(1) {
		t.Fatalf("current_version = %v, want 1", details["current_version"])
	}
	if _, present := details["current_body"]; present {
		t.Fatal("include_current false must withhold the body")
	}
}

func TestAMissingExpectedVersionIsInvalidInputRatherThanAGuess(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.Write(ctx, game, markdown.WriteInput{Path: "bible", Content: "one\n"})
	requireFieldError(t, err, "expected_version",
		"is required: pass 0 to create a document")

	// And nothing was written: a refused write is a refused write.
	if _, err := svc.Read(ctx, game, "bible"); !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("the refused write created something, got %v", err)
	}
}

func TestExpectingAVersionOfADocumentThatDoesNotExistIsNotFound(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(3),
	})
	// not_found and not version_conflict: there is no version to merge
	// onto, so telling a caller to merge would send it round a loop that
	// cannot terminate.
	if !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("want not_found, got %v", err)
	}
	if errors.Is(err, markdown.ErrVersionConflict) {
		t.Fatalf("want not_found and not version_conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), `"bible"`) {
		t.Fatalf("the message must name the path, got %q", err.Error())
	}
	var missing *markdown.MissingError
	if !errors.As(err, &missing) || missing.Path != "path" {
		t.Fatalf("want a *markdown.MissingError at path `path`, got %#v", err)
	}
}

func TestARespelledPathIsRefusedNamingBothSpellings(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/duskwood", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "Lore/Duskwood", Content: "two\n", ExpectedVersion: ptrInt32(1),
	})
	requireFieldError(t, err, "path", `already exists here spelled "lore/duskwood"`)

	// The document is untouched, under its own spelling.
	got, err := svc.Read(ctx, game, "lore/duskwood")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.CurrentVersion != 1 || got.BodyMd != "one\n" {
		t.Fatalf("the refused respelling changed the document: version %d, body %q",
			got.CurrentVersion, got.BodyMd)
	}
}

// A path that differs only by case addresses the document that is there,
// which is the other half of the case-folding rule: Read finds it.
func TestAPathIsMatchedWithoutRegardToCaseOnRead(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/duskwood", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := svc.Read(ctx, game, "LORE/Duskwood")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Path != "lore/duskwood" {
		t.Fatalf("Path = %q, want the stored spelling back", got.Path)
	}
}

func TestReadRefusesABadPathBeforeItReachesTheDatabase(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.Read(ctx, game, "lore//duskwood")
	requireFieldError(t, err, "path", "has an empty segment")
}

func TestTwoConcurrentWritesLeaveOneWinnerAndNoGapInTheVersions(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = svc.Write(ctx, game, markdown.WriteInput{
				Path: "bible", Content: "rewrite\n", ExpectedVersion: ptrInt32(1),
			})
		}(i)
	}
	wg.Wait()

	var won, conflicted int
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, markdown.ErrVersionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if won != 1 || conflicted != 1 {
		t.Fatalf("got %d winners and %d conflicts, want exactly one of each", won, conflicted)
	}

	var current int32
	var versions int
	if err := pool.QueryRow(ctx,
		`SELECT d.current_version, (SELECT count(*) FROM document_versions v WHERE v.document_id = d.id)
		   FROM documents d WHERE d.project_id = $1 AND d.path = 'bible'`,
		game).Scan(&current, &versions); err != nil {
		t.Fatalf("read the document: %v", err)
	}
	if current != 2 || versions != 2 {
		t.Fatalf("current_version = %d with %d version rows, want 2 and 2: "+
			"no gap and no duplicate in the sequence", current, versions)
	}
}

// Two creations of the same path race with nothing to lock, so the
// guarded upsert is the only thing that can refuse one of them.
//
// **It is staged rather than run as two goroutines, and that is the
// point.** The goroutine version of this test (and of the one above)
// almost never produces a real overlap — each writer acquires a
// connection and opens its own transaction first — so with two
// unsynchronised goroutines the second writer's locked read simply finds
// the committed row and the Go check refuses it, leaving the SQL guard
// unexercised: changing `=` to `>=` in UpsertDocument, or deleting the
// guard outright, left the whole suite green. Holding an uncommitted
// insert open is what forces the writer down the path where nothing but
// the guard stands between two creations.
func TestTheGuardedUpsertIsWhatRefusesACreationThatRacedAnother(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	// Another writer's creation, begun and not yet committed. Our writer
	// cannot see it, cannot lock it, and will meet it at the unique
	// index.
	other, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = other.Rollback(ctx) }()
	if _, err := other.Exec(ctx,
		`INSERT INTO documents (project_id, path, body_md) VALUES ($1, 'bible', 'theirs' || chr(10))`,
		game); err != nil {
		t.Fatalf("the other writer's insert: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: "bible", Content: "ours\n", ExpectedVersion: ptrInt32(0),
			IncludeCurrent: true,
		})
		done <- err
	}()

	waitForABlockedStatement(t, pool)
	if err := other.Commit(ctx); err != nil {
		t.Fatalf("commit the other writer: %v", err)
	}

	err = <-done
	var conflict *markdown.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want a *markdown.ConflictError from the losing creation, got %#v", err)
	}
	if conflict.Current != 1 {
		t.Fatalf("Current = %d, want 1: the path was created while this write was in flight",
			conflict.Current)
	}
	if conflict.BodyMD != "theirs\n" {
		t.Fatalf("BodyMD = %q, want the winner's body", conflict.BodyMD)
	}

	got, err := svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.CurrentVersion != 1 || got.BodyMd != "theirs\n" {
		t.Fatalf("document = (%d, %q), want the winner's row untouched at version 1",
			got.CurrentVersion, got.BodyMd)
	}
	var versions int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM document_versions WHERE project_id = $1`, game).Scan(&versions); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versions != 0 {
		t.Fatalf("%d version rows, want 0: the refused write must leave no snapshot", versions)
	}
}

// A creation that loses to a racing creation *under a different
// spelling* is told the spelling, not a version conflict: the version is
// not the caller's problem here and merging onto it would not help.
//
// This is the path the plan expected the post-write `row.Path != in.Path`
// check in writeWith to cover. It does not — see writeWith's comment for
// why that check was unreachable — conflictAfterFailedUpsert's own
// re-read is what covers it, and this is the test that pins it.
func TestACreationLosingToADifferentlySpelledPathIsToldTheSpelling(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	other, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = other.Rollback(ctx) }()
	if _, err := other.Exec(ctx,
		`INSERT INTO documents (project_id, path, body_md) VALUES ($1, 'lore/duskwood', 'theirs')`,
		game); err != nil {
		t.Fatalf("the other writer's insert: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: "Lore/Duskwood", Content: "ours\n", ExpectedVersion: ptrInt32(0),
		})
		done <- err
	}()

	waitForABlockedStatement(t, pool)
	if err := other.Commit(ctx); err != nil {
		t.Fatalf("commit the other writer: %v", err)
	}

	requireFieldError(t, <-done, "path", `already exists here spelled "lore/duskwood"`)
}

// A creation that races a path being both created *and* soft-deleted by
// someone else is told a version_conflict naming the tombstone, not an
// internal_error.
//
// The creating writer's locked read finds nothing at a free path, so it
// locks nothing, and there is nothing to force the other writer to wait
// on. What is staged here instead is the guarded upsert itself: the
// other writer's uncommitted INSERT blocks our writer's own INSERT ...
// ON CONFLICT the same way TestTheGuardedUpsertIsWhatRefusesACreation
// ThatRacedAnother stages it, and by the time the other writer commits,
// its transaction has both created the row and soft-deleted it, landing
// current_version at 2 with deleted_at set before our writer's guarded
// upsert ever runs. The guard fails on the version mismatch exactly as
// it does for an ordinary racing creation, and conflictAfterFailedUpsert
// must re-read the tombstone -- not a live row -- to report it, which is
// what IncludeDeleted being true on that re-read is for.
func TestACreationRacingACreateAndDeleteIsToldTheTombstone(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	other, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = other.Rollback(ctx) }()
	var docID string
	if err := other.QueryRow(ctx,
		`INSERT INTO documents (project_id, path, body_md) VALUES ($1, 'bible', 'theirs' || chr(10))
		 RETURNING id`, game).Scan(&docID); err != nil {
		t.Fatalf("the other writer's insert: %v", err)
	}
	if _, err := other.Exec(ctx,
		`INSERT INTO document_versions (project_id, document_id, version, body_md)
		 VALUES ($1, $2, 1, 'theirs' || chr(10))`, game, docID); err != nil {
		t.Fatalf("the other writer's version 1: %v", err)
	}
	if _, err := other.Exec(ctx,
		`UPDATE documents SET current_version = 2, deleted_at = now() WHERE id = $1`, docID); err != nil {
		t.Fatalf("the other writer's soft delete: %v", err)
	}
	if _, err := other.Exec(ctx,
		`INSERT INTO document_versions (project_id, document_id, version, body_md, deleted)
		 VALUES ($1, $2, 2, 'theirs' || chr(10), true)`, game, docID); err != nil {
		t.Fatalf("the other writer's tombstone version: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: "bible", Content: "ours\n", ExpectedVersion: ptrInt32(0),
			IncludeCurrent: true,
		})
		done <- err
	}()

	waitForABlockedStatement(t, pool)
	if err := other.Commit(ctx); err != nil {
		t.Fatalf("commit the other writer: %v", err)
	}

	err = <-done
	var conflict *markdown.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want a *markdown.ConflictError from the losing creation, got %#v", err)
	}
	if conflict.Current != 2 {
		t.Fatalf("Current = %d, want 2: the tombstone left by the create-and-delete",
			conflict.Current)
	}
	if !conflict.Deleted {
		t.Fatal("Deleted = false, want true: the path this write raced onto was deleted, not merely edited")
	}
}

// The number a conflicted caller is told to merge onto is the one its own
// write would have met, not the one that was current when it started.
// Told a stale number, a caller retries into the same refusal forever.
//
// This is the test GetDocumentByPathForUpdate's comment points at. It
// passes with FOR UPDATE and without it — see that comment for why both
// mechanisms deliver a fresh number — so what it pins is the guarantee,
// not the clause.
func TestTheReportedCurrentVersionIsTheOneTheWriteWouldHaveMet(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Another writer holds the row and is about to advance it.
	other, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = other.Rollback(ctx) }()
	if _, err := other.Exec(ctx,
		`UPDATE documents SET current_version = 2, body_md = 'theirs' || chr(10)
		  WHERE project_id = $1 AND path = 'bible'`, game); err != nil {
		t.Fatalf("the other writer's update: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: "bible", Content: "ours\n", ExpectedVersion: ptrInt32(1),
			IncludeCurrent: true,
		})
		done <- err
	}()

	waitForABlockedStatement(t, pool)
	if err := other.Commit(ctx); err != nil {
		t.Fatalf("commit the other writer: %v", err)
	}

	err = <-done
	var conflict *markdown.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want a *markdown.ConflictError, got %#v", err)
	}
	if conflict.Current != 2 {
		t.Fatalf("Current = %d, want 2: told 1, this caller would merge onto the version "+
			"it already holds and retry into the same refusal forever", conflict.Current)
	}
	if conflict.BodyMD != "theirs\n" {
		t.Fatalf("BodyMD = %q, want the body the retry has to merge onto", conflict.BodyMD)
	}
}

// waitForABlockedStatement waits until some backend on this database is
// waiting on a lock, which is how the two staged tests above know their
// writer has reached the row rather than sleeping for a guessed number of
// milliseconds.
func waitForABlockedStatement(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var blocked int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&blocked); err != nil {
			t.Fatalf("inspect pg_stat_activity: %v", err)
		}
		if blocked > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no statement blocked on a lock within 10s: the race this test stages did not happen")
}

func TestReadingAnotherGamesDocumentIsNotFound(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")

	if _, err := svc.Write(ctx, azeroth, markdown.WriteInput{
		Path: "bible", Content: "secret\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Read(ctx, outland, "bible"); !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("a token for outland must not read azeroth's prose, got %v", err)
	}
}

// The write path is scoped too: creating "bible" in a second game must
// create a second document rather than meeting the first one's version.
func TestWritingAPathAnotherGameUsesCreatesASecondDocument(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")

	if _, err := svc.Write(ctx, azeroth, markdown.WriteInput{
		Path: "bible", Content: "azeroth\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write to azeroth: %v", err)
	}
	if _, err := svc.Write(ctx, outland, markdown.WriteInput{
		Path: "bible", Content: "outland\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write to outland: %v", err)
	}
	first, err := svc.Read(ctx, azeroth, "bible")
	if err != nil {
		t.Fatalf("read azeroth: %v", err)
	}
	if first.BodyMd != "azeroth\n" || first.CurrentVersion != 1 {
		t.Fatalf("azeroth's document = (%q, %d), want (%q, 1): the second game's write "+
			"reached it", first.BodyMd, first.CurrentVersion, "azeroth\n")
	}
}

func TestAWriteIsAnnouncedOnlyAfterItCommits(t *testing.T) {
	svc, _, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	// A refused write announces nothing.
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(9),
	}); err == nil {
		t.Fatal("want a refusal")
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("a refused write announced %v", ev)
	default:
	}

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var ev realtime.Event
	select {
	case ev = <-sub.C:
	case <-time.After(5 * time.Second):
		t.Fatal("the committed write announced nothing within 5s")
	}
	if ev.Kind != "document.written" {
		t.Fatalf("Kind = %q, want %q", ev.Kind, "document.written")
	}
	payload, ok := ev.Payload.(markdown.DocumentEvent)
	if !ok {
		t.Fatalf("Payload = %#v, want a markdown.DocumentEvent", ev.Payload)
	}
	if payload.Path != "bible" || payload.Version != 1 {
		t.Fatalf("payload = %+v, want path bible at version 1", payload)
	}
	if payload.ID == uuid.Nil {
		t.Fatalf("payload = %+v, want the document's id", payload)
	}
}

// TestNoEventIsPublishedWhenTheWriteCannotCommit is the placement
// TestAWriteIsAnnouncedOnlyAfterItCommits cannot reach: a publish
// sitting as the last statement *inside* withTx's callback, which
// differs from the correct placement only by the commit that follows.
// Proved necessary by mutation — moving the publish inside the callback,
// guarded on a nil error, left the rest of this suite green, because
// every other failure this package can produce is a refusal that never
// reaches a publish at all.
//
// It is reachable because testutil.NewPool hands every test its own
// throwaway database, so this test may install a constraint no other
// test sees. A deferred foreign key from documents.id to projects.id is
// satisfied by nothing — a document's id is not a project id — but being
// DEFERRABLE INITIALLY DEFERRED it is checked at COMMIT and not before,
// so every statement inside the transaction succeeds and only the commit
// fails. The same technique as internal/metamodel's
// TestNoEventIsPublishedWhenTheCommitFails.
func TestNoEventIsPublishedWhenTheWriteCannotCommit(t *testing.T) {
	svc, _, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := pool.Exec(ctx,
		`ALTER TABLE documents ADD CONSTRAINT documents_commit_must_fail
		   FOREIGN KEY (id) REFERENCES projects (id) DEFERRABLE INITIALLY DEFERRED`); err != nil {
		t.Fatalf("install the deferred constraint: %v", err)
	}

	sub := hub.Subscribe(game, "owner", false)
	defer hub.Unsubscribe(sub)

	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	})
	if err == nil {
		t.Fatal("want the commit to fail")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Fatalf("err = %v, want the commit to be what failed", err)
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("a write whose commit failed announced %v", ev)
	default:
	}
}

// The gating decided in events.go, asserted rather than described: a
// token subscriber at the lowest role hears a document change, because
// it may read every document on demand anyway and is the subscriber with
// the most to lose from not being told its base moved.
func TestADocumentEventReachesAViewerAndATokenAlike(t *testing.T) {
	svc, _, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	viewer := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(viewer)
	agent := hub.Subscribe(game, "viewer", true)
	defer hub.Unsubscribe(agent)

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	for name, events := range map[string]chan realtime.Event{
		"the viewer": viewer.C,
		"the agent":  agent.C,
	} {
		select {
		case ev := <-events:
			if ev.Kind != "document.written" {
				t.Fatalf("%s got %q, want document.written", name, ev.Kind)
			}
		default:
			t.Fatalf("%s heard nothing: the gating decided in events.go says both hear this", name)
		}
	}
}

func TestAKindAndAMessageAreBoundedAsTheCallersOwnArguments(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Kind: ptrString(strings.Repeat("k", markdown.MaxKindLen+1)),
	})
	requireFieldError(t, err, "kind", "must be at most")

	_, err = svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Message: strings.Repeat("m", markdown.MaxMessageLen+1),
	})
	requireFieldError(t, err, "message", "must be at most")

	_, err = svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Kind: ptrString("scr\npt"),
	})
	requireFieldError(t, err, "kind", "control character")

	_, err = svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Message: "wrote it\tin a hurry",
	})
	requireFieldError(t, err, "message", "control character")

	// A byte that decodes as no character at all, refused before the
	// control scan and before Postgres sees it: CheckText's own ordering,
	// shared with the metamodel rather than copied.
	_, err = svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Kind: ptrString("scr\x80pt"),
	})
	requireFieldError(t, err, "kind", "is not valid UTF-8")

	// A kind of exactly the bound is accepted, and reads back whole.
	atTheBound := strings.Repeat("k", markdown.MaxKindLen)
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0), Kind: ptrString(atTheBound),
	}); err != nil {
		t.Fatalf("a kind of exactly %d bytes must be accepted: %v", markdown.MaxKindLen, err)
	}
	got, err := svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != atTheBound {
		t.Fatalf("Kind = %q, want the %d-byte kind back whole", got.Kind, markdown.MaxKindLen)
	}
}

// TestAnEditThatOmitsKindLeavesItUnchanged pins review finding 1 on Task
// 3: `kind` is a property of the document, not of any one edit, so a
// caller that does not mention it must not erase it. A nil Kind
// preserves whatever is stored; a Kind pointing at "" is a caller
// explicitly clearing the label, and that must go through too.
func TestAnEditThatOmitsKindLeavesItUnchanged(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	doc, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Kind: ptrString("lore"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// A body-only edit, Kind omitted (nil): the label survives.
	doc, err = svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/bible", Content: "two\n", ExpectedVersion: ptrInt32(doc.CurrentVersion),
	})
	if err != nil {
		t.Fatalf("edit without kind: %v", err)
	}
	if doc.Kind != "lore" {
		t.Fatalf("Kind = %q after an edit that did not mention it, want it preserved as %q",
			doc.Kind, "lore")
	}
	got, err := svc.Read(ctx, game, "lore/bible")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != "lore" {
		t.Fatalf("Read Kind = %q, want the kind preserved across a body-only edit", got.Kind)
	}

	// An edit that explicitly clears it, Kind pointing at "": that takes
	// effect, distinguishing "did not say" from "said empty".
	doc, err = svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/bible", Content: "three\n", ExpectedVersion: ptrInt32(doc.CurrentVersion),
		Kind: ptrString(""),
	})
	if err != nil {
		t.Fatalf("edit clearing kind: %v", err)
	}
	if doc.Kind != "" {
		t.Fatalf("Kind = %q after explicitly clearing it, want empty", doc.Kind)
	}
}

func TestEveryProblemWithOneWriteIsReportedInOnePass(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore//duskwood", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Kind: ptrString(strings.Repeat("k", markdown.MaxKindLen+1)),
	})
	requireFieldError(t, err, "path", "has an empty segment")
	requireFieldError(t, err, "kind", "must be at most")

	var v *metamodel.ValidationError
	if !errors.As(err, &v) || len(v.Fields) != 2 {
		t.Fatalf("want both problems in one answer, got %#v", err)
	}
}

func newUser(t *testing.T, pool *pgxpool.Pool, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, display_name, password_hash) VALUES ($1, $1, 'x') RETURNING id`,
		email).Scan(&id); err != nil {
		t.Fatalf("insert user %s: %v", email, err)
	}
	return id
}

func newToken(t *testing.T, pool *pgxpool.Pool, game, user uuid.UUID, label string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO api_tokens (token_hash, project_id, user_id, label, token_hint)
		 VALUES (convert_to(gen_random_uuid()::text, 'UTF8'), $1, $2, $3, 'abcd') RETURNING id`,
		game, user, label).Scan(&id); err != nil {
		t.Fatalf("insert api token %s: %v", label, err)
	}
	return id
}

// TestADocumentWrittenWithAnotherGamesTokenIsRefusedAsSuch closes a gap
// Task 14 recorded rather than fixed.
//
// 0007_documents.sql gives both documents and document_versions the
// composite key into api_tokens that every audit column in this product
// carries, so the database already refuses this write; what was missing
// was the *sentence*. Without the mapping the refusal reaches a server
// log as SQLSTATE 23503 over
// document_versions_author_token_id_project_id_fkey, which says nothing
// about a credential bound to the wrong game — the exact defect
// metamodel.ActorConstraintViolation exists to remove, and which was
// mapped in the metamodel and in views and in neither of the two write
// paths here.
//
// The wire code stays internal_error and that is deliberate: the actor
// is resolved by the transport from the credential the call arrived
// with and is never caller-supplied, so there is nothing an agent can
// change. errors.go states that argument where the sentinel is aliased.
func TestADocumentWrittenWithAnotherGamesTokenIsRefusedAsSuch(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	user := newUser(t, pool, "designer@example.test")
	foreign := newToken(t, pool, outland, user, "another game's agent")

	_, err := svc.Write(ctx, azeroth, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Actor: markdown.Actor{TokenID: &foreign},
	})
	if !errors.Is(err, markdown.ErrActorNotInGame) {
		t.Fatalf("a foreign token must be refused as such, got %v", err)
	}

	// The positive control: a token of this game writes the same
	// document, so the refusal above is about the token's scope and not
	// about the write being impossible.
	own := newToken(t, pool, azeroth, user, "this game's agent")
	if _, err := svc.Write(ctx, azeroth, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Actor: markdown.Actor{TokenID: &own},
	}); err != nil {
		t.Fatalf("this game's own token must be able to write: %v", err)
	}

	// The tombstone's author travels the same column, and Delete is the
	// other write path that fills it: the mapping has to be on both, so
	// a soft delete under a foreign token is asserted too.
	if _, err := svc.Delete(ctx, azeroth, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1),
		Actor: markdown.Actor{TokenID: &foreign},
	}); !errors.Is(err, markdown.ErrActorNotInGame) {
		t.Fatalf("a foreign token must be refused on a delete too, got %v", err)
	}
}
