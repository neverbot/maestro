package markdown_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/neverbot/maestro/internal/markdown"
)

// TestTheKindCatalogueListsWhatTheGameUses is the discovery half of the
// free-text kind. Both filters compare kinds and nothing told a caller
// which kinds exist; this is that answer, in the shape the game summary
// already uses for entity types.
func TestTheKindCatalogueListsWhatTheGameUses(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	seedKinded(t, svc, game, "lore/a", "lore")
	seedKinded(t, svc, game, "lore/b", "lore")
	seedKinded(t, svc, game, "scripts/a", "script")
	seedKinded(t, svc, game, "notes/a", "")

	catalogue, err := svc.Kinds(ctx, game)
	if err != nil {
		t.Fatalf("Kinds: %v", err)
	}
	want := []markdown.KindCount{
		{Kind: "lore", DocumentCount: 2},
		{Kind: "script", DocumentCount: 1},
	}
	if fmt.Sprint(catalogue.Kinds) != fmt.Sprint(want) {
		t.Fatalf("Kinds = %v, want %v (sorted, and the kind-less document is not a kind)",
			catalogue.Kinds, want)
	}
	if catalogue.Totals.Documents != 4 || catalogue.Totals.Unkinded != 1 {
		t.Fatalf("Totals = %+v, want 4 documents of which 1 unkinded", catalogue.Totals)
	}

	// Every kind the catalogue names must select exactly the documents
	// it counted, through the filter it exists to feed. A catalogue that
	// listed a value docs.list answers nothing for would be worse than
	// no catalogue.
	for _, k := range catalogue.Kinds {
		page, err := svc.List(ctx, game, markdown.ListFilter{Kind: k.Kind})
		if err != nil {
			t.Fatalf("List by kind %q: %v", k.Kind, err)
		}
		if int64(len(page.Documents)) != k.DocumentCount {
			t.Fatalf("kind %q counts %d documents and lists %d",
				k.Kind, k.DocumentCount, len(page.Documents))
		}
	}
}

// TestAnEmptyGamesKindCatalogueIsAnEmptyListAndNotNil. A game with no
// prose is the first state anyone sees, and a client that has to tell []
// from null before it can loop has been handed two spellings of one
// fact — the rule DocumentPage and the game summary both follow.
func TestAnEmptyGamesKindCatalogueIsAnEmptyListAndNotNil(t *testing.T) {
	svc, _, _, pool := newService(t)
	game := newGame(t, pool, "azeroth")

	catalogue, err := svc.Kinds(context.Background(), game)
	if err != nil {
		t.Fatalf("Kinds: %v", err)
	}
	if catalogue.Kinds == nil {
		t.Fatal("Kinds is nil, want an empty slice")
	}
	encoded, err := json.Marshal(catalogue.Kinds)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("an empty catalogue marshals as %s, want []", encoded)
	}
	if catalogue.Totals.Documents != 0 || catalogue.Totals.Unkinded != 0 {
		t.Fatalf("Totals = %+v, want zeroes", catalogue.Totals)
	}
}

// TestTwoSpellingsOfOneKindAreOneCatalogueRow is the folding decision.
// Both filters match kinds case-insensitively, so "Lore" and "lore"
// select one set of documents and must be one row counting all of them.
// Two rows would each carry a count belonging to neither filter result.
func TestTwoSpellingsOfOneKindAreOneCatalogueRow(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	seedKinded(t, svc, game, "a", "Lore")
	seedKinded(t, svc, game, "b", "lore")
	seedKinded(t, svc, game, "c", "LORE")

	catalogue, err := svc.Kinds(ctx, game)
	if err != nil {
		t.Fatalf("Kinds: %v", err)
	}
	if len(catalogue.Kinds) != 1 {
		t.Fatalf("Kinds = %v, want one row: three spellings of a kind are one kind",
			catalogue.Kinds)
	}
	if catalogue.Kinds[0].Kind != "lore" {
		t.Fatalf("Kind = %q, want the folded spelling, which is what the filter matches on",
			catalogue.Kinds[0].Kind)
	}
	if catalogue.Kinds[0].DocumentCount != 3 {
		t.Fatalf("DocumentCount = %d, want 3", catalogue.Kinds[0].DocumentCount)
	}
	// And the spelling it reports selects all three, which is the whole
	// contract of a catalogue value.
	page, err := svc.List(ctx, game, markdown.ListFilter{Kind: catalogue.Kinds[0].Kind})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Documents) != 3 {
		t.Fatalf("filtering by the catalogue's own spelling found %d documents, want 3",
			len(page.Documents))
	}
}

// TestADeletedDocumentsKindLeavesTheCatalogue. A kind kept alive by
// nothing but a tombstone is a filter value whose answer, under the
// default listing and under search, is an empty page — so the catalogue
// must not offer it. And because deletion is soft, the kind has to come
// back when the document does.
func TestADeletedDocumentsKindLeavesTheCatalogue(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	seedKinded(t, svc, game, "notes/only", "pitch")
	if _, err := svc.Delete(ctx, game, markdown.DeleteInput{
		Path: "notes/only", ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	catalogue, err := svc.Kinds(ctx, game)
	if err != nil {
		t.Fatalf("Kinds: %v", err)
	}
	if len(catalogue.Kinds) != 0 || catalogue.Totals.Documents != 0 {
		t.Fatalf("catalogue = %+v, want nothing: its only document is deleted", catalogue)
	}

	if _, err := svc.Write(ctx, game, markdown.WriteInput{
		Path: "notes/only", Content: "back\n", ExpectedVersion: ptrInt32(2),
	}); err != nil {
		t.Fatalf("resurrect: %v", err)
	}
	catalogue, err = svc.Kinds(ctx, game)
	if err != nil {
		t.Fatalf("Kinds after resurrection: %v", err)
	}
	if len(catalogue.Kinds) != 1 || catalogue.Kinds[0].Kind != "pitch" {
		t.Fatalf("catalogue = %+v, want the kind back with the document", catalogue)
	}
}

// TestAKindCatalogueIsPerGame. The counting query is one grouped scan
// and the project filter is the only thing keeping it inside one game.
func TestAKindCatalogueIsPerGame(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	azeroth := newGame(t, pool, "azeroth")
	outland := newGame(t, pool, "outland")

	seedKinded(t, svc, azeroth, "a", "lore")
	seedKinded(t, svc, outland, "a", "script")

	catalogue, err := svc.Kinds(ctx, azeroth)
	if err != nil {
		t.Fatalf("Kinds: %v", err)
	}
	if len(catalogue.Kinds) != 1 || catalogue.Kinds[0].Kind != "lore" ||
		catalogue.Totals.Documents != 1 {
		t.Fatalf("catalogue = %+v, want only this game's own prose", catalogue)
	}
}

// TestTheKindCatalogueDoesNotGrowWithTheDocumentsItCounts is the
// property that makes this safe on a page load and in an agent's
// context, and it is asserted the way the game summary's own test
// asserts it: seed many rows under few kinds and check that no row's
// path appears in the answer.
func TestTheKindCatalogueDoesNotGrowWithTheDocumentsItCounts(t *testing.T) {
	svc, _, _, pool := newService(t)
	ctx := context.Background()
	game := newGame(t, pool, "azeroth")

	for i := range 60 {
		seedKinded(t, svc, game, fmt.Sprintf("lore/entry-%d", i), "lore")
	}

	catalogue, err := svc.Kinds(ctx, game)
	if err != nil {
		t.Fatalf("Kinds: %v", err)
	}
	if len(catalogue.Kinds) != 1 || catalogue.Kinds[0].DocumentCount != 60 {
		t.Fatalf("catalogue = %+v, want one kind counting 60", catalogue)
	}
	encoded, err := json.Marshal(catalogue)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, path := range []string{"lore/entry-42", "entry-0"} {
		if strings.Contains(string(encoded), path) {
			t.Fatalf("the catalogue names %q: it counts documents and must not list them:\n%s",
				path, encoded)
		}
	}
}

func seedKinded(t *testing.T, svc *markdown.Service, game uuid.UUID, path, kind string) {
	t.Helper()
	if _, err := svc.Write(context.Background(), game, markdown.WriteInput{
		Path: path, Content: "x\n", Kind: &kind, ExpectedVersion: ptrInt32(0),
	}); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
