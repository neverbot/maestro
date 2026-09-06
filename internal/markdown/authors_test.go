package markdown_test

import (
	"context"
	"testing"

	"github.com/neverbot/maestro/internal/markdown"
)

// TestARevokedTokenStillNamesWhatItWrote is where the decision lives.
//
// The question a batch of audit columns forces is what a caller is told
// about a token that has since been revoked, and the answer is: its
// label, the same as a live token's. Revoking a token changes what it
// may do next, not who wrote the prose, and the tokens listing already
// includes revoked rows on purpose because it is the audit trail for
// what happened. The alternative — falling back to a category, the way
// the reading view says "a former member" — would hide the one fact a
// designer reading a suspect version needs.
func TestARevokedTokenStillNamesWhatItWrote(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	user := newUser(t, pool, "designer@example.test")
	token := newToken(t, pool, game, user, "the lore agent")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/duskwood", Content: "# Duskwood\n", ExpectedVersion: ptrInt32(0),
		Actor: markdown.Actor{TokenID: &token},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE api_tokens SET revoked_at = now() WHERE id = $1`, token); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "lore/duskwood"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got := page.Versions[0].Author; got.Kind != "token" || got.Label != "the lore agent" {
		t.Fatalf("author = %+v, want the revoked token still named", got)
	}
	listing, err := svc.List(ctx, game, markdown.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := listing.Documents[0].UpdatedBy; got.Label != "the lore agent" {
		t.Fatalf("updated_by = %+v, want the revoked token still named", got)
	}
}

// TestAnAuthorThatIsGoneIsNoAuthorRatherThanAnEmptyOne pins the case the
// zero Author is for: both audit columns are ON DELETE SET NULL, so a
// user who is really deleted leaves the row naming nobody, and the
// answer is no kind and no id rather than a kind naming nothing.
func TestAnAuthorThatIsGoneIsNoAuthorRatherThanAnEmptyOne(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	user := newUser(t, pool, "leaver@example.test")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/duskwood", Content: "# Duskwood\n", ExpectedVersion: ptrInt32(0),
		Actor: markdown.Actor{UserID: &user},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	page, err := svc.History(ctx, game, markdown.HistoryFilter{Path: "lore/duskwood"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got := page.Versions[0].Author; got != (markdown.Author{}) {
		t.Fatalf("author = %+v, want no author at all", got)
	}
	listing, err := svc.List(ctx, game, markdown.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	row := listing.Documents[0]
	if row.CreatedBy != (markdown.Author{}) || row.UpdatedBy != (markdown.Author{}) {
		t.Fatalf("listing row = %+v, want both authors gone", row)
	}
	// The timestamps outlive the author: *when* it changed is still
	// answerable when *who* changed it is not.
	if row.UpdatedAt.IsZero() {
		t.Fatal("a document whose author is gone still knows when it changed")
	}
}

// TestATokenIsResolvedInsideItsOwnGameOnly pins the project filter on
// the token half of the resolver. api_tokens rows are project-scoped,
// and one game reading another's labels would be a cross-game read on
// the one call that turns an id into a name.
//
// The document write path cannot reach this — 0007_documents.sql's
// composite keys refuse another game's token outright — so the resolver
// is driven directly, which is also what internal/web does for a
// single document's audit pairs.
func TestATokenIsResolvedInsideItsOwnGameOnly(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	user := newUser(t, pool, "designer@example.test")
	foreign := newToken(t, pool, outland, user, "outland's agent")

	authors, err := svc.Authors(ctx, azeroth, []markdown.Actor{{TokenID: &foreign}})
	if err != nil {
		t.Fatalf("Authors: %v", err)
	}
	if authors[0].Kind != "token" || authors[0].ID == nil || *authors[0].ID != foreign {
		t.Fatalf("author = %+v, want the id echoed back unresolved", authors[0])
	}
	if authors[0].Label != "" {
		t.Fatalf("label = %q: another game's token label must not be readable here",
			authors[0].Label)
	}
	// And inside its own game it resolves, so the empty answer above is
	// the filter and not a resolver that never finds anything.
	authors, err = svc.Authors(ctx, outland, []markdown.Actor{{TokenID: &foreign}})
	if err != nil {
		t.Fatalf("Authors: %v", err)
	}
	if authors[0].Label != "outland's agent" {
		t.Fatalf("label = %q, want the token's own inside its own game", authors[0].Label)
	}
}

// TestAConflictNamesWhoWroteTheVersionToMergeOnto is the third of the
// three findings, end to end through Write: a caller told to merge onto
// version 2 is told who wrote version 2 and when, which is what decides
// whether it merges or asks.
func TestAConflictNamesWhoWroteTheVersionToMergeOnto(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	user := newUser(t, pool, "ana@example.test")
	agent := newToken(t, pool, game, user, "the lore agent")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/duskwood", Content: "# Duskwood\n", ExpectedVersion: ptrInt32(0),
		Actor: markdown.Actor{TokenID: &agent},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/duskwood", Content: "# Duskwood\n\nRewritten by hand.\n",
		ExpectedVersion: ptrInt32(1), Actor: markdown.Actor{UserID: &user},
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	_, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/duskwood", Content: "stale\n", ExpectedVersion: ptrInt32(1),
	})
	conflict := requireConflict(t, err)
	if conflict.Current != 2 {
		t.Fatalf("current = %d, want 2", conflict.Current)
	}
	// The *last* writer, not the first: the version being merged onto is
	// the one that is there, and it was the designer's.
	if conflict.Author.Kind != "user" || conflict.Author.ID == nil ||
		*conflict.Author.ID != user {
		t.Fatalf("author = %+v, want the designer %v", conflict.Author, user)
	}
	if conflict.Author.Label != "ana@example.test" {
		t.Fatalf("author label = %q, want the designer's display name", conflict.Author.Label)
	}
	if conflict.UpdatedAt.IsZero() {
		t.Fatal("a conflict says when the version it names was written")
	}
	// And it is on the wire, not merely in the Go value.
	details := conflict.Details()
	if details["current_author_label"] != "ana@example.test" {
		t.Fatalf("details = %#v, want the author's label published", details)
	}

	// A conflict raised on a delete carries it too — deleteRefusal is a
	// second call site and this is the rule that was not carried one step
	// along in the last three sub-projects.
	_, err = svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "lore/duskwood", ExpectedVersion: ptrInt32(1),
	})
	if err == nil {
		t.Fatal("deleting with a stale version must be refused")
	}
	if got := requireConflict(t, err).Author.Label; got != "ana@example.test" {
		t.Fatalf("a refused delete names its author %q, want the designer's", got)
	}
}
