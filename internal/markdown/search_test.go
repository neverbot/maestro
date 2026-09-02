package markdown_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
)

func TestADocumentIsFoundByAWordInItsBody(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "scripts/hogger", Content: "# The Confrontation\n\nHOGGER: You no take candle!\n",
		ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	hits, err := svc.SearchDocuments(ctx, game, "candle", "", 0)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) != 1 || hits[0].Path != "scripts/hogger" {
		t.Fatalf("hits = %+v, want the script", hits)
	}
	if hits[0].NameMatch {
		t.Fatal("a word found only in the body must not report a title match")
	}
}

// TestEveryFieldOfASearchHitReadsBack is the read-back this plan's
// header asks of every feature: a value the search path writes and no
// caller can read is a column that could be dropped from the select list
// with nothing going red, which is the write-only defect that survived
// nine review rounds on a relation's fields. Every field of DocumentHit
// is asserted here against a document seeded to give each of them a
// distinguishable value.
//
// Rank is asserted as "greater than zero" rather than as a number: it is
// a ts_rank score whose exact value is Postgres's business, and pinning
// it would pin the version of Postgres. What matters is that the
// projection reaches the caller at all.
func TestEveryFieldOfASearchHitReadsBack(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")

	written, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "scripts/hogger",
		Content: "---\ntitle: \"The Confrontation\"\nsummary: \"Hogger says his line.\"\n---\n" +
			"HOGGER: You no take candle!\n",
		Kind:            ptrString("script"),
		ExpectedVersion: ptrInt32(0),
		Links: &[]markdown.LinkTarget{
			{EntityType: "quest", EntityKey: "wanted-hogger", Role: "script"},
		},
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	// A second version, so Version is read back as something other than
	// the 1 a create would give it whatever the projection did.
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "scripts/hogger",
		Content: "---\ntitle: \"The Confrontation\"\nsummary: \"Hogger says his line.\"\n---\n" +
			"HOGGER: You no take candle!\n\nHe means it.\n",
		Kind:            ptrString("script"),
		ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	hits, err := svc.SearchDocuments(ctx, game, "candle", "", 0)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("%d hits, want 1", len(hits))
	}
	got := hits[0]
	if got.ID != written.ID {
		t.Errorf("ID = %s, want %s", got.ID, written.ID)
	}
	if got.Path != "scripts/hogger" {
		t.Errorf("Path = %q", got.Path)
	}
	if got.Title != "The Confrontation" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Summary != "Hogger says his line." {
		t.Errorf("Summary = %q", got.Summary)
	}
	if got.Kind != "script" {
		t.Errorf("Kind = %q", got.Kind)
	}
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2", got.Version)
	}
	if got.NameMatch {
		t.Errorf("NameMatch = true for a body-only match")
	}
	if got.Rank <= 0 {
		t.Errorf("Rank = %v, want a score the query actually produced", got.Rank)
	}
	want := markdown.EntityLink{
		EntityID: got.LinkedEntities[0].EntityID, EntityTypeKey: "quest",
		EntityKey: "wanted-hogger", EntityName: "Wanted: Hogger", Role: "script",
	}
	if len(got.LinkedEntities) != 1 || got.LinkedEntities[0] != want {
		t.Errorf("LinkedEntities = %+v, want one %+v", got.LinkedEntities, want)
	}
}

// TestASearchHitCarriesNoBodyAtAll pins the absence over every field of
// the type rather than over a field it can name, because what has to
// hold is that no such field exists — the shape
// TestAListingRowCarriesNoBodyAtAll and TestAHistoryRowCarriesNoBodyAtAll
// both take, and the same two checks: a substring match on the lowered
// field name, so a field spelled any way that reads as a body or a
// frontmatter is caught however it is renamed, and a scan of every
// field's rendered value for a body string actually written and read
// back, so a field under some other name that happens to carry prose is
// caught too.
//
// A search is the one call in this domain an agent makes against a whole
// game without knowing what it will get, and prose is the largest
// payload the system holds: fifty bodies would blow a context window on
// the first answer.
//
// The gap this shape shares with its two siblings, stated rather than
// implied: a field carrying a body under a name matching neither
// substring, left at its zero value, is caught by neither check. What
// catches a *populated* one is the value scan; what catches a named one
// is the name check.
func TestASearchHitCarriesNoBodyAtAll(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	const body = "the-body-no-search-hit-may-carry\n"
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "bible", Content: "---\ntitle: T\nsummary: S\n---\n" + body,
		ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	hits, err := svc.SearchDocuments(ctx, game, "carry", "", 0)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("%d hits, want 1", len(hits))
	}
	hit := reflect.ValueOf(hits[0])
	for i := 0; i < hit.NumField(); i++ {
		field := hit.Type().Field(i)
		if name := strings.ToLower(field.Name); strings.Contains(name, "body") ||
			strings.Contains(name, "frontmatter") {
			t.Fatalf("a search hit carries %s: a hit is a summary, never a body", field.Name)
		}
		if rendered := fmt.Sprintf("%v", hit.Field(i).Interface()); strings.Contains(rendered, body) {
			t.Fatalf("field %s of a search hit holds the body %q", field.Name, rendered)
		}
	}
}

func TestADocumentTheQueryNamesOutranksOneThatOnlyMentionsIt(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	// The mentioner repeats the phrase four times, which is what beats
	// weights alone once ts_rank saturates. name_match leading the sort
	// is what makes this a guarantee.
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/mentions", Content: "# Elwynn notes\n\n" +
			strings.Repeat("the gnoll pack roams here. ", 4) + "\n",
		ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/the-pack", Content: "# The Gnoll Pack\n\nA short note.\n",
		ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	hits, err := svc.SearchDocuments(ctx, game, "gnoll pack", "", 0)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("%d hits, want 2", len(hits))
	}
	if hits[0].Path != "lore/the-pack" || !hits[0].NameMatch {
		t.Fatalf("hits = %+v, want the document the query names first, flagged", hits)
	}
	// The half a title-first assertion alone cannot see: the mentioner
	// carries the *higher* ts_rank here, so the order is not explained by
	// rank and a merge that sorted on rank alone would invert it.
	if hits[1].Rank <= hits[0].Rank {
		t.Fatalf("ranks = [%v %v]: this fixture no longer proves that name_match, "+
			"and not rank, is what puts the named document first",
			hits[0].Rank, hits[1].Rank)
	}
}

func TestASearchHitNamesTheEntitiesTheDocumentIsAttachedTo(t *testing.T) {
	svc, entities, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	newQuest(t, entities, game, "wanted-hogger", "Wanted: Hogger")
	newQuest(t, entities, game, "the-defias", "The Defias Brotherhood")

	// Two documents, one attached and one not: a single-hit fixture
	// cannot tell "the links were joined to their own document" from
	// "every hit got every link".
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "scripts/hogger", Content: "You no take candle!\n", ExpectedVersion: ptrInt32(0),
		Links: &[]markdown.LinkTarget{{EntityType: "quest", EntityKey: "wanted-hogger", Role: "script"}},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/candles", Content: "Candle-making in Elwynn.\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write the unattached document: %v", err)
	}
	hits, err := svc.SearchDocuments(ctx, game, "candle", "", 0)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("%d hits, want 2", len(hits))
	}
	byPath := map[string][]markdown.EntityLink{}
	for _, hit := range hits {
		byPath[hit.Path] = hit.LinkedEntities
	}
	attached := byPath["scripts/hogger"]
	if len(attached) != 1 || attached[0].EntityKey != "wanted-hogger" ||
		attached[0].EntityTypeKey != "quest" || attached[0].EntityName != "Wanted: Hogger" ||
		attached[0].Role != "script" {
		t.Fatalf("scripts/hogger linked to %+v, want the quest it is a script for", attached)
	}
	// Never nil, so a hit with no attachments marshals as [] rather than
	// null: "attached to nothing" and "the server said nothing about
	// attachments" are different statements.
	if unattached := byPath["lore/candles"]; unattached == nil || len(unattached) != 0 {
		t.Fatalf("lore/candles linked to %#v, want an empty non-nil slice", unattached)
	}
}

func TestADeletedDocumentIsNotSearchable(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "candle\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "s", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	hits, err := svc.SearchDocuments(ctx, game, "candle", "", 0)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("hits = %+v, want none: a result nobody can open is worse than no result", hits)
	}
}

func TestOnlyTheCurrentVersionIsSearchable(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "candle\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "s", Content: "lantern\n", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	gone, err := svc.SearchDocuments(ctx, game, "candle", "", 0)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(gone) != 0 {
		t.Fatalf("hits = %+v, want none: history is not indexed", gone)
	}
	current, err := svc.SearchDocuments(ctx, game, "lantern", "", 0)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(current) != 1 {
		t.Fatalf("the current version must be findable, got %+v", current)
	}
}

func TestASearchNeverCrossesGames(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")
	if _, err := svc.Write(ctx, azeroth, markdown.WriteInput{
		Path: "s", Content: "candle\n", ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	hits, err := svc.SearchDocuments(ctx, outland, "candle", "", 0)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("outland found %d of azeroth's documents", len(hits))
	}
}

// TestASearchNarrowsToOneKind pins the kind filter in both directions:
// the matching kind is kept and the other is dropped. A one-document
// fixture would be answered identically by a filter that did nothing.
//
// The spelling is folded, the way every other kind comparison in this
// domain folds it (ListDocumentsPage's own filter, documents_path_key).
func TestASearchNarrowsToOneKind(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "scripts/hogger", Content: "candle\n",
		Kind: ptrString("script"), ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "lore/candles", Content: "candle\n",
		Kind: ptrString("lore"), ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	both, err := svc.SearchDocuments(ctx, game, "candle", "", 0)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("%d hits with no kind filter, want 2", len(both))
	}
	for _, kind := range []string{"script", "SCRIPT"} {
		t.Run(kind, func(t *testing.T) {
			hits, err := svc.SearchDocuments(ctx, game, "candle", kind, 0)
			if err != nil {
				t.Fatalf("SearchDocuments: %v", err)
			}
			if len(hits) != 1 || hits[0].Path != "scripts/hogger" {
				t.Fatalf("hits = %+v, want only the script", hits)
			}
		})
	}
}

// TestASearchKindIsBoundedAsTheCallersOwnArgument keeps the kind filter
// from reaching Postgres unbounded, the hole Task 7's correction 3 and
// Task 8's correction 1 each found one step further along: an invalid
// UTF-8 byte reaches the server as a byte sequence it refuses outright
// (SQLSTATE 22021), which would land on the default arm as
// internal_error over a value the caller itself supplied.
func TestASearchKindIsBoundedAsTheCallersOwnArgument(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	for _, tc := range []struct{ name, kind, message string }{
		{"too long", strings.Repeat("a", markdown.MaxKindLen+1), "must be at most"},
		{"a control character", "scr\x00ipt", "control character"},
		{"invalid utf-8", "scr\x80ipt", "valid UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SearchDocuments(ctx, game, "candle", tc.kind, 0)
			if !errors.Is(err, markdown.ErrInvalidInput) {
				t.Fatalf("want invalid_input, got %v", err)
			}
			requireFieldError(t, err, "kind", tc.message)
		})
	}
}

func TestASearchQueryIsBoundedAndReportedAsTheCallersOwnArgument(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	for _, tc := range []struct{ name, query, message string }{
		{"too long", strings.Repeat("a", metamodel.MaxSearchQuery+1), "is too long"},
		{"nul", "cand\x00le", "control character"},
		{"invalid utf-8", "cand\x80le", "valid UTF-8"},
		{"no word", "...", "no word to search for"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SearchDocuments(ctx, game, tc.query, "", 0)
			if !errors.Is(err, markdown.ErrInvalidInput) {
				t.Fatalf("want invalid_input, got %v", err)
			}
			requireFieldError(t, err, "query", tc.message)
		})
	}
}

// TestASearchLimitDefaultsAndIsHonoured pins that a limit applies at all
// — sixty matching documents against a default of fifty, since a fixture
// smaller than the default cannot tell "the default applies" from "no
// bound applies" — and that a limit the caller asks for is passed
// through.
//
// What sixty documents cannot separate is "clamped at the cap" from "no
// cap at all", which would need more than metamodel.MaxSearchLimit rows;
// that policy is pinned against paging.Size itself, in
// internal/paging/cursor_test.go's
// TestSizeClampsRatherThanFoldingOntoTheDefault, and this test does not
// claim to cover it.
func TestASearchLimitDefaultsAndIsHonoured(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")
	seedDocuments(t, svc, game, "lore", 60)

	for _, tc := range []struct {
		name  string
		limit int32
		want  int
	}{
		{"omitted", 0, int(metamodel.DefaultSearchLimit)},
		{"negative", -1, int(metamodel.DefaultSearchLimit)},
		{"asked for", 10, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := svc.SearchDocuments(ctx, game, "body", "", tc.limit)
			if err != nil {
				t.Fatalf("SearchDocuments: %v", err)
			}
			if len(hits) != tc.want {
				t.Fatalf("%d hits, want %d", len(hits), tc.want)
			}
		})
	}
}
