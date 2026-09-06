package markdown_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/neverbot/maestro/internal/markdown"
)

// TestAMovedDocumentKeepsItsHistoryAndItsNumbering is the whole reason
// Move exists. Before it, the only way to fix a path was to write the
// document at the new one and delete the old one, which forked the
// history: the new path began at version 1 holding none of what came
// before. So this asserts the opposite of that fork, in the terms it
// would have shown up in — one document, one id, continuous numbering,
// every past version still readable, and the old path free afterwards.
func TestAMovedDocumentKeepsItsHistoryAndItsNumbering(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	first, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "one\n", Kind: ptrString("lore"),
		ExpectedVersion: ptrInt32(0), Message: "first",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "two\n", ExpectedVersion: ptrInt32(1), Message: "second",
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	moved, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/dusk", To: "zones/duskwood/lore", ExpectedVersion: ptrInt32(2),
		Message: "filed under its zone",
	})
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if moved.ID != first.ID {
		t.Fatalf("the move produced a different document (%s, was %s): it must be the "+
			"same row, or the history forked exactly as writing-and-deleting did",
			moved.ID, first.ID)
	}
	if moved.Path != "zones/duskwood/lore" {
		t.Fatalf("Path = %q, want the destination", moved.Path)
	}
	if moved.CurrentVersion != 3 {
		t.Fatalf("CurrentVersion = %d, want 3: the numbering continues rather than "+
			"restarting at one", moved.CurrentVersion)
	}
	// The two things a fork would have lost: the kind, and the creator.
	if moved.Kind != "lore" {
		t.Fatalf("Kind = %q, want it carried across the move", moved.Kind)
	}

	// Every past version is still there, still readable, still numbered
	// from one.
	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "zones/duskwood/lore"})
	if err != nil {
		t.Fatalf("History at the new path: %v", err)
	}
	if len(page.Versions) != 3 {
		t.Fatalf("history has %d versions, want 3", len(page.Versions))
	}
	v1, err := svc.ReadVersion(ctx, game, "zones/duskwood/lore", 1)
	if err != nil {
		t.Fatalf("ReadVersion 1 through the new path: %v", err)
	}
	if v1.BodyMd != "one\n" || v1.Message != "first" {
		t.Fatalf("version 1 = (%q, %q), want the writing from before the move",
			v1.BodyMd, v1.Message)
	}

	// The old path is free, not tombstoned: a move is not a delete, so
	// nothing was left behind to resurrect.
	if _, err := svc.Read(ctx, game, "lore/dusk"); !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("reading the old path gave %v, want not_found", err)
	}
	written, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "a new one\n", ExpectedVersion: ptrInt32(0),
	})
	if err != nil {
		t.Fatalf("the old path must be free to create at, got %v", err)
	}
	if written.CurrentVersion != 1 || written.ID == moved.ID {
		t.Fatalf("writing at the vacated path continued the moved document instead of "+
			"creating a new one (version %d, id %s)", written.CurrentVersion, written.ID)
	}
}

// TestAHistoryShowsWhereEachVersionWasWritten is the half of the move
// that lives in the record rather than in the row. A history that
// carried only today's path would show three snapshots of
// zones/duskwood/lore, two of which were written while the document was
// called something else, with nothing to say so.
func TestAHistoryShowsWhereEachVersionWasWritten(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/dusk", To: "zones/dusk", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "zones/dusk", Content: "three\n", ExpectedVersion: ptrInt32(2),
	}); err != nil {
		t.Fatalf("write after the move: %v", err)
	}

	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "zones/dusk"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	want := map[int32]string{1: "lore/dusk", 2: "zones/dusk", 3: "zones/dusk"}
	for _, v := range page.Versions {
		if v.Path != want[v.Version] {
			t.Fatalf("version %d was written at %q, want %q", v.Version, v.Path, want[v.Version])
		}
	}
	if len(page.Versions) != 3 {
		t.Fatalf("history has %d versions, want 3", len(page.Versions))
	}
}

// TestTheVersionAMoveAppendsCarriesTheNewPathAndTheOldContent pins what
// makes that history row legible as a move rather than as an edit: its
// content equals its predecessor's and its path does not.
func TestTheVersionAMoveAppendsCarriesTheNewPathAndTheOldContent(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "# Duskwood\n\nA wood.\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/dusk", To: "zones/dusk", ExpectedVersion: ptrInt32(1),
		Message: "wrong shelf",
	}); err != nil {
		t.Fatalf("Move: %v", err)
	}

	before, err := svc.ReadVersion(ctx, game, "zones/dusk", 1)
	if err != nil {
		t.Fatalf("ReadVersion 1: %v", err)
	}
	after, err := svc.ReadVersion(ctx, game, "zones/dusk", 2)
	if err != nil {
		t.Fatalf("ReadVersion 2: %v", err)
	}
	if after.BodyMd != before.BodyMd || after.Title != before.Title ||
		after.Summary != before.Summary || string(after.Frontmatter) != string(before.Frontmatter) {
		t.Fatalf("the move's snapshot changed the writing: body %q vs %q, title %q vs %q",
			after.BodyMd, before.BodyMd, after.Title, before.Title)
	}
	if before.Path != "lore/dusk" || after.Path != "zones/dusk" {
		t.Fatalf("paths = %q then %q, want lore/dusk then zones/dusk: the path is the only "+
			"thing a move's version row changes", before.Path, after.Path)
	}
	if after.Message != "wrong shelf" {
		t.Fatalf("Message = %q, want the caller's own", after.Message)
	}
	if after.Deleted {
		t.Fatal("a move's snapshot is marked as a tombstone")
	}
}

// TestAMovedDocumentKeepsItsLinks is the payoff of links keying on
// document_id rather than on a path: nothing about the attachments has
// to be maintained by Move, so this test is what proves that claim
// rather than repeating it.
func TestAMovedDocumentKeepsItsLinks(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "one\n", ExpectedVersion: ptrInt32(0),
		Links: &[]markdown.LinkTarget{{EntityType: "quest", EntityKey: "wanted-hogger"}},
	}); err != nil {
		t.Fatalf("write with a link: %v", err)
	}
	if _, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/dusk", To: "zones/dusk", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("Move: %v", err)
	}

	// From the document's side.
	links, err := svc.LinksByDocument(ctx, game, "zones/dusk", markdown.LinksFilter{})
	if err != nil {
		t.Fatalf("LinksByDocument at the new path: %v", err)
	}
	if len(links.Links) != 1 || links.Links[0].EntityKey != "wanted-hogger" {
		t.Fatalf("the move lost the attachment: %+v", links.Links)
	}
	// And from the entity's, where the document's *new* path must show.
	docs, err := svc.LinksByEntity(ctx, game, "quest", "wanted-hogger", markdown.LinksFilter{})
	if err != nil {
		t.Fatalf("LinksByEntity: %v", err)
	}
	if len(docs.Links) != 1 || docs.Links[0].Path != "zones/dusk" {
		t.Fatalf("the quest still lists %+v, want one document at zones/dusk", docs.Links)
	}
}

// TestACaseOnlyMoveIsRefusedAsARespelling is the case-folding decision.
// documents_path_key folds case, so the two spellings are one address;
// this repository refuses a case-only respelling of a row key and of a
// document path on a write, and a move is refused for the same reason.
func TestACaseOnlyMoveIsRefusedAsARespelling(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/duskwood", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/duskwood", To: "lore/Duskwood", ExpectedVersion: ptrInt32(1),
	})
	requireRefusal(t, err)
	requireFieldError(t, err, "to", "differs from \"lore/duskwood\" only in capitalisation")

	// And the document is untouched: not moved, not versioned, not
	// respelled behind the refusal.
	row, err := svc.Read(ctx, game, "lore/duskwood")
	if err != nil {
		t.Fatalf("Read after the refusal: %v", err)
	}
	if row.Path != "lore/duskwood" || row.CurrentVersion != 1 {
		t.Fatalf("the refused move left the document at %q version %d",
			row.Path, row.CurrentVersion)
	}

	// A move to the byte-identical path is the same family of refusal
	// with its own wording, because "you asked for nothing" and "you
	// asked for a respelling" are two different mistakes.
	_, err = svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/duskwood", To: "lore/duskwood", ExpectedVersion: ptrInt32(1),
	})
	requireRefusal(t, err)
	requireFieldError(t, err, "to", "is the path the document is already at")
}

// TestMovingOntoALiveDocumentIsRefusedAtTheDestination: a move never
// merges two documents, and the refusal has to name the end that is in
// the way rather than reporting a bare conflict.
func TestMovingOntoALiveDocumentIsRefusedAtTheDestination(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	for _, path := range []string{"lore/dusk", "zones/dusk"} {
		if _, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: path, Content: path + "\n", ExpectedVersion: ptrInt32(0),
		}); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	_, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/dusk", To: "zones/dusk", ExpectedVersion: ptrInt32(1),
	})
	requireRefusal(t, err)
	requireFieldError(t, err, "to", "already has a document at")

	// Neither end moved and neither end was versioned.
	for _, path := range []string{"lore/dusk", "zones/dusk"} {
		row, err := svc.Read(ctx, game, path)
		if err != nil {
			t.Fatalf("Read %s: %v", path, err)
		}
		if row.CurrentVersion != 1 || row.BodyMd != path+"\n" {
			t.Fatalf("%s = version %d body %q after a refused move", path, row.CurrentVersion, row.BodyMd)
		}
	}

	// A destination taken under another casing is the same refusal and
	// says which spelling is there, because the caller cannot see it.
	_, err = svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/dusk", To: "zones/Dusk", ExpectedVersion: ptrInt32(1),
	})
	requireRefusal(t, err)
	requireFieldError(t, err, "to", `spelled "zones/dusk"`)
}

// TestMovingOntoADeletedPathSaysItsHistoryIsStillThere: the unique index
// covers soft-deleted rows, so a deleted path is still taken. A caller
// told only "that path exists" would go looking for a document no read
// answers for.
func TestMovingOntoADeletedPathSaysItsHistoryIsStillThere(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "zones/dusk", Content: "cut\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "zones/dusk", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "live\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write the mover: %v", err)
	}

	_, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/dusk", To: "zones/dusk", ExpectedVersion: ptrInt32(1),
	})
	requireRefusal(t, err)
	requireFieldError(t, err, "to", "was deleted at")
	requireFieldError(t, err, "to", "its history is still there")
}

// TestMovingADeletedDocumentSaysToBringItBackFirst. The recovery is a
// write, and the refusal has to say so: a bare not_found would read as
// "no such document" for one whose whole history is still on file.
func TestMovingADeletedDocumentSaysToBringItBackFirst(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "lore/dusk", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/dusk", To: "zones/dusk", ExpectedVersion: ptrInt32(2),
	})
	requireRefusal(t, err)
	requireMissing(t, err, "from", "was deleted; write to the path to bring it back")

	// And a path that was never here is the other not_found, at the same
	// argument, with a different message.
	_, err = svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/nowhere", To: "zones/dusk", ExpectedVersion: ptrInt32(1),
	})
	requireRefusal(t, err)
	requireMissing(t, err, "from", `this game has no document at "lore/nowhere"`)
}

// TestMovingWithAStaleVersionIsAConflict pins the guard on
// MoveDocument. A move advances the version, so an unguarded one would
// land on top of an edit the caller never read.
func TestMovingWithAStaleVersionIsAConflict(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "two\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	_, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/dusk", To: "zones/dusk", ExpectedVersion: ptrInt32(1),
	})
	requireRefusal(t, err)
	conflict := requireConflict(t, err)
	if conflict.Current != 2 {
		t.Fatalf("Current = %d, want 2", conflict.Current)
	}
	// A caller changing an address is not merging prose, so the body is
	// not echoed — the same choice deleteRefusal makes.
	if conflict.Include {
		t.Fatal("a refused move echoed the document's body")
	}
	if _, err := svc.Read(ctx, game, "zones/dusk"); !errors.Is(err, markdown.ErrNotFound) {
		t.Fatalf("the refused move landed anyway: %v", err)
	}
}

// TestEveryProblemWithOneMoveIsReportedAtItsOwnEnd. Two bad paths in one
// call are two field errors under two names, so a caller can tell which
// end it typo'd — the discrimination pathProblemsAt exists for.
func TestEveryProblemWithOneMoveIsReportedAtItsOwnEnd(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	_, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "/leading", To: "trailing/", Message: strings.Repeat("m", 501),
	})
	requireRefusal(t, err)
	requireFieldError(t, err, "from", "must not begin or end with a slash")
	requireFieldError(t, err, "to", "must not begin or end with a slash")
	requireFieldError(t, err, "message", "must be at most 500 bytes")
	requireFieldError(t, err, "expected_version", "is required")
}

// TestAMoveIsAnnouncedWithBothEnds. A move is the one change in this
// domain a subscriber cannot discover by re-reading what it holds: the
// path it knows answers not_found, indistinguishably from a deletion.
// So the payload carries both ends.
func TestAMoveIsAnnouncedWithBothEnds(t *testing.T) {
	svc, _, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	moved, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/dusk", To: "zones/dusk", ExpectedVersion: ptrInt32(1),
	})
	if err != nil {
		t.Fatalf("Move: %v", err)
	}

	ev := nextEvent(t, sub)
	if ev.Kind != "document.moved" {
		t.Fatalf("Kind = %q, want document.moved: a move is not a write, and a client "+
			"treating it as one would re-fetch a path that is no longer there", ev.Kind)
	}
	payload, ok := ev.Payload.(markdown.MoveEvent)
	if !ok {
		t.Fatalf("payload = %#v, want a markdown.MoveEvent", ev.Payload)
	}
	if payload.From != "lore/dusk" || payload.To != "zones/dusk" {
		t.Fatalf("payload = (%q -> %q), want lore/dusk -> zones/dusk", payload.From, payload.To)
	}
	if payload.ID != moved.ID || payload.Version != moved.CurrentVersion {
		t.Fatalf("payload = (%s, v%d), want (%s, v%d)",
			payload.ID, payload.Version, moved.ID, moved.CurrentVersion)
	}
}

// TestNoMoveIsAnnouncedWhenTheMoveIsRefused. publish runs after the
// transaction, so a refused move must announce nothing at all — the case
// TestAMoveIsAnnouncedWithBothEnds cannot reach on its own.
func TestNoMoveIsAnnouncedWhenTheMoveIsRefused(t *testing.T) {
	svc, _, hub, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/dusk", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	sub := hub.Subscribe(game, "viewer", false)
	defer hub.Unsubscribe(sub)

	if _, err := svc.Move(ctx, game, markdown.MoveInput{
		From: "lore/dusk", To: "zones/dusk", ExpectedVersion: ptrInt32(99),
	}); err == nil {
		t.Fatal("a move with a stale version must be refused")
	}

	select {
	case ev := <-sub.C:
		t.Fatalf("a refused move announced %v", ev)
	default:
	}
}

// TestTwoOppositeMovesDoNotDeadlock drives the lock ordering in
// lockBothEnds. Two transactions moving a to b and b to a would, if each
// locked its own source first, hold the lock the other needs; Postgres
// would break the cycle with SQLSTATE 40P01 and one caller would meet a
// deadlock over two writes that have a perfectly good serial order.
//
// It is a real race and not a staged one, because staging it means
// holding a lock across the two goroutines, which is what the ordering
// removes. Run repeatedly (-count=20) it reddens reliably with the
// ordering removed and stays green with it: what is asserted is that
// neither caller is ever answered with a deadlock, whichever of them
// wins.
func TestTwoOppositeMovesDoNotDeadlock(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	for _, path := range []string{"a", "b"} {
		if _, err := svc.Write(ctx, game, markdown.WriteInput{
			Path: path, Content: path + "\n", ExpectedVersion: ptrInt32(0),
		}); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, ends := range [][2]string{{"a", "b"}, {"b", "a"}} {
		wg.Add(1)
		go func(i int, from, to string) {
			defer wg.Done()
			_, errs[i] = svc.Move(ctx, game, markdown.MoveInput{
				From: from, To: to, ExpectedVersion: ptrInt32(1),
			})
		}(i, ends[0], ends[1])
	}
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			continue
		}
		if strings.Contains(err.Error(), "40P01") || strings.Contains(err.Error(), "deadlock") {
			t.Fatalf("mover %d met a deadlock: %v", i, err)
		}
		// Every other refusal is legitimate: whichever move loses finds
		// its destination occupied, and the answer names it.
		requireFieldError(t, err, "to", "")
	}
}

// requireRefusal fails the test when a call this package expected to
// refuse succeeded instead. requireFieldError and requireMissing both
// report "want a refusal, got nil" on their own, but a *conflict*
// assertion does not — errors.As against a nil error simply returns
// false and reports a type mismatch — so the check is stated once, here,
// and made before every one of them rather than before the one that
// needs it.
func requireRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want a refusal, got none")
	}
}

// TestAVersionRecordsTheStoredSpellingAndNotTheCallers. writeWith
// records row.Path and not in.Path, because a write addressed under
// another casing reaches the existing document and UpsertDocument leaves
// the path column alone — so the caller's spelling is not the version's
// address, and recording it would put a spelling in the history that no
// reader of the document ever sees.
func TestAVersionRecordsTheStoredSpellingAndNotTheCallers(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/duskwood", Content: "one\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A delete addressed under another casing reaches the same row
	// (TestAPathIsMatchedWithoutRegardToCaseOnDelete) and appends the
	// tombstone, so it is the write path in this package that can reach
	// InsertDocumentVersion under a spelling the row does not carry.
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "lore/DUSKWOOD", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("delete under another casing: %v", err)
	}

	tombstone, err := svc.ReadVersion(ctx, game, "lore/duskwood", 2)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	if tombstone.Path != "lore/duskwood" {
		t.Fatalf("the tombstone records %q, want the stored spelling %q",
			tombstone.Path, "lore/duskwood")
	}
}
