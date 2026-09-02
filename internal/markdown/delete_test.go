package markdown_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/realtime"
)

func TestDeletingADocumentHidesItFromReadsAndKeepsItsHistory(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	removed, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1), Message: "cut for now",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// The tombstone's version number is the one a caller needs to
	// resurrect the path, and Read cannot supply it: the document is
	// gone from every read. So Delete returns it.
	if removed.CurrentVersion != 2 {
		t.Fatalf("CurrentVersion = %d, want 2 back from Delete", removed.CurrentVersion)
	}
	// pgtype.Timestamptz, not a *time.Time: "is it set" is Valid, and
	// the plan's `!= nil` does not compile against the generated model.
	if !removed.DeletedAt.Valid {
		t.Fatalf("DeletedAt is unset, want the deletion timestamp back from Delete")
	}

	if _, err := svc.Read(ctx, game, "bible"); !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("a deleted document must read as not_found, got %v", err)
	}

	var versions int
	var current int32
	if err := pool.QueryRow(ctx,
		`SELECT d.current_version, (SELECT count(*) FROM document_versions v WHERE v.document_id = d.id)
		   FROM documents d WHERE d.project_id = $1 AND d.path = 'bible'`,
		game).Scan(&current, &versions); err != nil {
		t.Fatalf("read the document: %v", err)
	}
	if current != 2 || versions != 2 {
		t.Fatalf("current_version = %d with %d version rows, want 2 and 2: "+
			"a delete appends a tombstone and the two must never disagree", current, versions)
	}

	// Task 4 asserted the tombstone's own values over SQL because no
	// reader existed; Task 6 reads them back through ReadVersion.
	tombstone, err := svc.ReadVersion(ctx, game, "bible", 2)
	if err != nil {
		t.Fatalf("ReadVersion of the tombstone: %v", err)
	}
	if !tombstone.Deleted || tombstone.BodyMd != "one\n" || tombstone.Message != "cut for now" {
		t.Fatalf("tombstone = (deleted %t, body %q, message %q), want (true, %q, %q)",
			tombstone.Deleted, tombstone.BodyMd, tombstone.Message, "one\n", "cut for now")
	}
}

// The tombstone is the *only* version row carrying deleted = true. Task
// 6's history renders that column, and a live snapshot mislabelled as a
// deletion would tell a designer their document was removed twice.
func TestOnlyTheTombstoneVersionIsMarkedDeleted(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "two\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(2),
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "four\n", ExpectedVersion: ptrInt32(3),
	}); err != nil {
		t.Fatalf("resurrect: %v", err)
	}

	// Read back through History, the public reader Task 6 added: Task 4
	// asserted this over a raw pool.Query because none existed.
	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	got := map[int32]bool{}
	for _, v := range page.Versions {
		got[v.Version] = v.Deleted
	}
	want := map[int32]bool{1: false, 2: false, 3: true, 4: false}
	if len(got) != len(want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
	for version, deleted := range want {
		if got[version] != deleted {
			t.Fatalf("version %d deleted = %t, want %t (all four: %v)",
				version, got[version], deleted, got)
		}
	}
}

func TestWritingToADeletedPathResurrectsItAndContinuesTheNumbering(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", Kind: ptrString("lore"), ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	doc, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "back again\n", ExpectedVersion: ptrInt32(2),
	})
	if err != nil {
		t.Fatalf("resurrect: %v", err)
	}
	if doc.CurrentVersion != 3 {
		t.Fatalf("CurrentVersion = %d, want 3: numbering continues, it does not restart",
			doc.CurrentVersion)
	}
	if doc.DeletedAt.Valid {
		t.Fatalf("DeletedAt = %v, want unset after a resurrection", doc.DeletedAt.Time)
	}
	got, err := svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("Read after resurrection: %v", err)
	}
	if got.BodyMd != "back again\n" {
		t.Fatalf("BodyMd = %q, want %q", got.BodyMd, "back again\n")
	}
	// Resurrection goes through writeWith like any other edit, so
	// WriteInput.Kind's "nil preserves what is stored" rule has to hold
	// across a deletion too — a document brought back on the shelf it
	// was taken from, not on no shelf at all.
	if got.Kind != "lore" {
		t.Fatalf("Kind = %q, want %q: a resurrection is an edit, and an edit that says "+
			"nothing about kind leaves it alone", got.Kind, "lore")
	}
}

func TestAStaleVersionCannotSilentlyResurrectADocument(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// The agent still holds version 1, from before the delete. This is
	// the whole reason the delete bumps current_version.
	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "did not notice\n", ExpectedVersion: ptrInt32(1),
	})
	if !errors.Is(err, markdown.ErrVersionConflict) {
		t.Fatalf("want version_conflict, got %v", err)
	}
	var conflict *markdown.ConflictError
	if !errors.As(err, &conflict) || conflict.Current != 2 {
		t.Fatalf("want a conflict at version 2, got %#v", err)
	}
	// And it is told *why* the ground moved. Without this, the recovery
	// the message prescribes — re-read and merge — sends the agent to a
	// Read that answers not_found, with nothing connecting the two.
	if !conflict.Deleted {
		t.Fatalf("Deleted = false, want true: the version this caller must merge onto "+
			"is a tombstone, and %q says nothing about that", conflict.Error())
	}
	details := conflict.Details()
	if details["deleted"] != true {
		t.Fatalf("Details()[%q] = %v, want true", "deleted", details["deleted"])
	}
}

// The other half: an ordinary conflict must not claim a deletion.
func TestAnOrdinaryConflictDoesNotClaimTheDocumentWasDeleted(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "two\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "three\n", ExpectedVersion: ptrInt32(1),
	})
	var conflict *markdown.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want a *markdown.ConflictError, got %#v", err)
	}
	if conflict.Deleted {
		t.Fatalf("Deleted = true on a live document: %q", conflict.Error())
	}
	if _, ok := conflict.Details()["deleted"]; ok {
		t.Fatalf("Details() = %v, want no deleted key at all when nothing was deleted",
			conflict.Details())
	}
}

func TestDeletingTwiceIsNotFoundRatherThanASecondTombstone(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	_, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(2),
	})
	if !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("want not_found on a second delete, got %v", err)
	}
	// "Already gone" and "never here" are both not_found, and they have
	// different recoveries: one is nothing to do, the other is a typo'd
	// path. The message is the only thing that separates them.
	requireMissing(t, err, "path",
		`the document at "bible" was already deleted; write to the path to bring it back`)

	var versions int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM document_versions WHERE project_id = $1`, game).Scan(&versions); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versions != 2 {
		t.Fatalf("%d version rows, want 2: a refused second delete writes no tombstone", versions)
	}
}

func TestDeletingADocumentThatWasNeverThereIsNotFoundNamingThePath(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "lore/nowhere", ExpectedVersion: ptrInt32(1),
	})
	if !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("want not_found, got %v", err)
	}
	requireMissing(t, err, "path", `this game has no document at "lore/nowhere"`)
}

func TestDeletingWithAStaleVersionIsAConflict(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "two\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	_, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1),
	})
	var conflict *markdown.ConflictError
	if !errors.As(err, &conflict) || conflict.Current != 2 {
		t.Fatalf("want a conflict at version 2, got %#v", err)
	}
	// A caller asking to remove a document is not merging prose, so the
	// body it asked to be rid of is not echoed back at it.
	if conflict.Include {
		t.Fatalf("Include = true: a delete's conflict must not carry the body")
	}
	if _, ok := conflict.Details()["current_body"]; ok {
		t.Fatalf("Details() = %v, want current_version alone", conflict.Details())
	}

	// The refused delete changed nothing.
	got, err := svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.CurrentVersion != 2 || got.DeletedAt.Valid {
		t.Fatalf("document = (version %d, deleted_at valid %t), want it untouched at version 2",
			got.CurrentVersion, got.DeletedAt.Valid)
	}
}

func TestDeletingAnotherGamesDocumentIsNotFound(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")

	if _, err := svc.Write(ctx, azeroth, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := svc.Delete(ctx, outland, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1),
	})
	if !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("a token for outland must not delete azeroth's prose, got %v", err)
	}
	requireMissing(t, err, "path", `this game has no document at "bible"`)
	got, err := svc.Read(ctx, azeroth, "bible")
	if err != nil {
		t.Fatalf("azeroth's document must still be there: %v", err)
	}
	if got.DeletedAt.Valid {
		t.Fatalf("DeletedAt = %v, want azeroth's row untouched", got.DeletedAt.Time)
	}
}

func TestAPathIsMatchedWithoutRegardToCaseOnDelete(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/duskwood", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	removed, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "Lore/Duskwood", ExpectedVersion: ptrInt32(1),
	})
	if err != nil {
		t.Fatalf("Delete under another casing: %v", err)
	}
	// The stored spelling stands, on the row and therefore in the event
	// payload: a path in a payload names an identity every reader shares.
	if removed.Path != "lore/duskwood" {
		t.Fatalf("Path = %q, want the stored spelling %q", removed.Path, "lore/duskwood")
	}
	if _, err := svc.Read(ctx, game, "lore/duskwood"); !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("want the document gone, got %v", err)
	}
}

func TestEveryProblemWithOneDeleteIsReportedInOnePass(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "/bad//path", Message: "why\nnot",
	})
	requireFieldError(t, err, "path", "must not begin or end with a slash")
	requireFieldError(t, err, "message", "holds a control character")
	requireFieldError(t, err, "expected_version", "is required")
}

func TestADeletionIsAnnounced(t *testing.T) {
	svc, _, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var ev realtime.Event
	select {
	case ev = <-sub.C:
	case <-time.After(5 * time.Second):
		t.Fatal("the committed delete announced nothing within 5s")
	}
	if ev.Kind != "document.deleted" {
		t.Fatalf("Kind = %q, want %q", ev.Kind, "document.deleted")
	}
	payload, ok := ev.Payload.(markdown.DocumentEvent)
	if !ok || payload.Path != "bible" || payload.Version != 2 {
		t.Fatalf("payload = %#v, want bible at version 2", ev.Payload)
	}
}

// TestNoDeletionIsAnnouncedWhenTheTombstoneCannotBeWritten is what
// TestADeletionIsAnnounced cannot reach, and Task 3's correction 4 says
// so for Write: moving the publish inside withTx's callback leaves the
// announcement test green, because every failure a refusal produces
// never reaches a publish wherever the call sits. This one fails
// *after* the document row has been updated — the tombstone insert hits
// the unique index on (document_id, version) — so a publish inside the
// transaction would announce a deletion that then rolled back.
func TestNoDeletionIsAnnouncedWhenTheTombstoneCannotBeWritten(t *testing.T) {
	svc, _, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A version row already standing where the tombstone will be
	// written. The document is at version 1, so the delete will try to
	// insert version 2.
	if _, err := pool.Exec(ctx,
		`INSERT INTO document_versions (project_id, document_id, version, body_md, message)
		 SELECT $1, d.id, 2, 'squatter', 'in the way'
		   FROM documents d WHERE d.project_id = $1 AND d.path = 'bible'`, game); err != nil {
		t.Fatalf("seed the colliding version row: %v", err)
	}

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1),
	}); err == nil {
		t.Fatal("want the tombstone insert to fail")
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("a delete that rolled back announced %v", ev)
	default:
	}
	// And the rollback is real: the document is still readable.
	got, err := svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("the document must survive a delete that could not commit: %v", err)
	}
	if got.CurrentVersion != 1 {
		t.Fatalf("CurrentVersion = %d, want 1: the whole delete rolled back", got.CurrentVersion)
	}
}

// TestNoDeletionIsAnnouncedWhenTheDeleteCannotCommit is the placement
// neither of the two tests above can reach, and Task 3's correction 4
// records the same gap for Write: a publish sitting as the last
// statement *inside* withTx's callback differs from the correct
// placement only by the commit that follows, and every other failure a
// delete can produce returns from the callback before that statement
// runs. Verified: with the publish moved there, both
// TestADeletionIsAnnounced and
// TestNoDeletionIsAnnouncedWhenTheTombstoneCannotBeWritten stay green.
//
// A deferred foreign key from document_versions.id to projects.id is
// satisfied by nothing — a version's id is not a project id — but being
// DEFERRABLE INITIALLY DEFERRED it is checked at COMMIT and not before,
// so the tombstone insert succeeds and only the commit fails. It hangs
// off document_versions rather than documents because Delete's only
// INSERT is the tombstone; an UPDATE that does not touch documents.id
// would never fire a key on that column. NOT VALID is what lets it be
// added at all: version 1 already stands and would fail the check on
// the spot, and NOT VALID skips the existing rows while still checking
// every new one.
func TestNoDeletionIsAnnouncedWhenTheDeleteCannotCommit(t *testing.T) {
	svc, _, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`ALTER TABLE document_versions ADD CONSTRAINT versions_commit_must_fail
		   FOREIGN KEY (id) REFERENCES projects (id) DEFERRABLE INITIALLY DEFERRED NOT VALID`); err != nil {
		t.Fatalf("install the deferred constraint: %v", err)
	}

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	_, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1),
	})
	if err == nil {
		t.Fatal("want the commit to fail")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Fatalf("err = %v, want the commit to be what failed", err)
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("a delete whose commit failed announced %v", ev)
	default:
	}
}

// The delete is a compare-and-set in SQL and not a read-then-write, and
// this is the test that pins it. Task 3's correction 1 recorded that
// goroutine pairs measurably do not overlap in this suite, so the race
// is staged: another transaction advances the row and is held open, the
// delete is started and blocks on that row's lock, the other
// transaction commits, and the SQL guard is the only thing left that can
// refuse.
func TestTheGuardedUpdateIsWhatRefusesADeleteThatRacedAnEdit(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

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
		_, err := svc.Delete(ctx, game, markdown.DeleteInput{
			Path: "bible", ExpectedVersion: ptrInt32(1),
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
		t.Fatalf("want a *markdown.ConflictError from the losing delete, got %#v", err)
	}
	if conflict.Current != 2 {
		t.Fatalf("Current = %d, want 2: the row moved while this delete was in flight",
			conflict.Current)
	}
	got, err := svc.Read(ctx, game, "bible")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.DeletedAt.Valid || got.CurrentVersion != 2 {
		t.Fatalf("document = (version %d, deleted_at valid %t), want the editor's row alive at 2",
			got.CurrentVersion, got.DeletedAt.Valid)
	}
}

// The delete's audit columns, read back through the public surface —
// which for a deleted document means through the resurrection, since
// Read hides it while it is gone. Nothing else in this package can show
// a caller who removed a document until Task 6's history lands.
func TestTheActorOfADeletionIsRecorded(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	author := newUser(t, pool, "author@example.test")
	remover := newUser(t, pool, "remover@example.test")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Actor: markdown.Actor{UserID: &author},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	removed, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1), Message: "cut",
		Actor: markdown.Actor{UserID: &remover},
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if removed.UpdatedByUserID == nil || *removed.UpdatedByUserID != remover {
		t.Fatalf("UpdatedByUserID = %v, want the remover %v", removed.UpdatedByUserID, remover)
	}
	if removed.CreatedByUserID == nil || *removed.CreatedByUserID != author {
		t.Fatalf("CreatedByUserID = %v, want the author %v: a deletion does not rewrite "+
			"who created the document", removed.CreatedByUserID, author)
	}

	// The tombstone version carries its own author, read back through
	// History rather than off the row as Task 4 had to.
	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "bible"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if page.Versions[0].Version != 2 {
		t.Fatalf("newest version = %d, want the tombstone at 2", page.Versions[0].Version)
	}
	if got := page.Versions[0].AuthorUserID; got == nil || *got != remover {
		t.Fatalf("tombstone author_user_id = %v, want the remover %v", got, remover)
	}
	if got := page.Versions[1].AuthorUserID; got == nil || *got != author {
		t.Fatalf("version 1's author = %v, want it unchanged at %v", got, author)
	}
}

func TestATombstonesBodyIsStillReadableAsAVersion(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible",
		Content: "---\ntitle: The Bible\n---\n" +
			"the world is called Azeroth\n",
		ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "bible", ExpectedVersion: ptrInt32(1), Message: "cut",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Task 4 landed this as a raw pool.QueryRow because the tombstone
	// had no public reader; Task 6 replaces it with the reader, which is
	// what makes "nothing is ever lost" true rather than claimed. A
	// placeholder nobody replaces is how a feature ships write-only.
	v2, err := svc.ReadVersion(ctx, game, "bible", 2)
	if err != nil {
		t.Fatalf("ReadVersion of the tombstone: %v", err)
	}
	if v2.BodyMd != "the world is called Azeroth\n" {
		t.Fatalf("BodyMd = %q, want the body as it stood at deletion", v2.BodyMd)
	}
	if !v2.Deleted {
		t.Fatal("the tombstone version must say it is one")
	}
	// A tombstone is a full snapshot, not a body with the rest blanked:
	// everything version 1 carried is carried here too.
	if v2.Title != "The Bible" {
		t.Fatalf("Title = %q, want the title as it stood at deletion", v2.Title)
	}
	if v2.Summary != "the world is called Azeroth" {
		t.Fatalf("Summary = %q, want the summary as it stood at deletion", v2.Summary)
	}
	if string(v2.Frontmatter) != `{"title": "The Bible"}` {
		t.Fatalf("Frontmatter = %s, want the frontmatter as it stood at deletion", v2.Frontmatter)
	}
	if v2.Message != "cut" {
		t.Fatalf("Message = %q, want the reason the document was cut", v2.Message)
	}
}
