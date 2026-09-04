package views

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// seedViews saves n views under keys view0…viewN-1, all sharing one
// name so the (name, id) keyset is the only thing that orders them.
//
// **One name for every row is the point.** With distinct names a keyset
// that compared name alone, or that sorted by name alone, pages
// perfectly: the defect only shows where rows tie, and duplicate names
// are ordinary in game content ("Elwynn" as a map and as a quest list).
// internal/metamodel found exactly this — dropping `id` from its own
// ORDER BY left its whole suite green until a fixture shared one name.
func (g *game) seedViews(t *testing.T, n int, name, renderer string) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		in := ViewInput{
			Key: keyOf(i), Name: name, Query: []byte(questsOnly), Renderer: renderer,
		}
		if renderer == RendererLayered {
			in.Query = []byte(questsToZones)
		}
		row, err := g.views.UpsertView(context.Background(), g.projectID, in)
		if err != nil {
			t.Fatalf("seed view %d: %v", i, err)
		}
		ids = append(ids, row.ID)
	}
	return ids
}

func keyOf(i int) string { return "view" + string(rune('a'+i)) }

// TestAViewListingWalksEveryRowOnceInItsSortOrder pages a listing whose
// rows all share one name, three at a time, and asserts the walk sees
// each row exactly once and in the order the statement sorts by.
//
// Seven rows and a page of three, rather than two rows and a page of
// one: a two-row fixture cannot distinguish any paging policy at all —
// "the second page starts after the first row" is true of a keyset, of
// an offset, and of an implementation that returns the tail every time.
func TestAViewListingWalksEveryRowOnceInItsSortOrder(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	ids := g.seedViews(t, 7, "One name", RendererGraph)

	seen := make([]uuid.UUID, 0, len(ids))
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("the listing did not terminate: a cursor that never empties is a loop")
		}
		page, err := g.views.ListViews(ctx, g.projectID, ViewFilter{Cursor: cursor, Limit: 3})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, row := range page.Views {
			seen = append(seen, row.ID)
		}
		if page.NextCursor == "" {
			if len(page.Views) == 3 {
				t.Fatal("a full page must carry a cursor, even when the next one is empty")
			}
			break
		}
		cursor = page.NextCursor
	}

	if len(seen) != len(ids) {
		t.Fatalf("the walk returned %d rows, want %d: %v", len(seen), len(ids), seen)
	}
	// Every row exactly once, and in the id order the tie-break imposes,
	// since every name is the same.
	sorted := append([]uuid.UUID(nil), ids...)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].String() < sorted[i].String() {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	for i := range sorted {
		if seen[i] != sorted[i] {
			t.Fatalf("row %d = %s, want %s: the walk must follow (name, id)", i, seen[i], sorted[i])
		}
	}
}

// TestTheRendererFilterNarrowsTheListingAndTheCursorGoesWithIt.
//
// The second half is the one worth having: every filter of a listing
// shares one sort order, so a cursor carried from the unfiltered listing
// into the filtered one would page perfectly and answer a different
// question.
func TestTheRendererFilterNarrowsTheListingAndTheCursorGoesWithIt(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	g.seedViews(t, 3, "Graphs", RendererGraph)
	// A second renderer over a query that can feed it.
	in := ViewInput{Key: "layers", Name: "Layers", Query: []byte(questsToZones),
		Renderer: RendererLayered}
	if _, err := g.views.UpsertView(ctx, g.projectID, in); err != nil {
		t.Fatalf("seed the layered view: %v", err)
	}

	all, err := g.views.ListViews(ctx, g.projectID, ViewFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all.Views) != 4 {
		t.Fatalf("%d views unfiltered, want 4", len(all.Views))
	}
	only, err := g.views.ListViews(ctx, g.projectID, ViewFilter{Renderer: RendererLayered})
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(only.Views) != 1 || only.Views[0].Key != "layers" {
		t.Fatalf("filtered listing = %+v, want the one layered view", only.Views)
	}

	// A cursor from the unfiltered listing does not belong to the
	// filtered one.
	first, err := g.views.ListViews(ctx, g.projectID, ViewFilter{Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("a full page must carry a cursor")
	}
	_, err = g.views.ListViews(ctx, g.projectID,
		ViewFilter{Renderer: RendererLayered, Cursor: first.NextCursor, Limit: 1})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input: a cursor belongs to the listing that "+
			"issued it", err)
	}
}

// TestACursorFromAnotherGameIsRefusedByTheViewListing is the behavioural
// half of the fingerprint's project-id-first rule.
//
// It is deliberately taken with the *same* filter in both games, because
// that is the only shape in which the project id is what discriminates:
// with different filters the renderer part of the fingerprint would
// refuse the cursor on its own, and the test would pass with the project
// id gone. Which is exactly how the original defect in internal/paging
// survived its first test — hence the compositional test below, which
// this one cannot replace.
func TestACursorFromAnotherGameIsRefusedByTheViewListing(t *testing.T) {
	azeroth, outland := newGame(t)
	ctx := context.Background()
	azeroth.seedViews(t, 3, "Shared name", RendererGraph)
	outland.seedViews(t, 3, "Shared name", RendererGraph)

	page, err := azeroth.views.ListViews(ctx, azeroth.projectID, ViewFilter{Limit: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("a full page must carry a cursor")
	}
	// The positive control: the cursor pages the listing it came from.
	if _, err := azeroth.views.ListViews(ctx, azeroth.projectID,
		ViewFilter{Cursor: page.NextCursor, Limit: 2}); err != nil {
		t.Fatalf("the cursor must page its own listing: %v", err)
	}
	_, err = outland.views.ListViews(ctx, outland.projectID,
		ViewFilter{Cursor: page.NextCursor, Limit: 2})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input: one game's position means nothing in "+
			"another game's listing", err)
	}
}

// TestTheViewListingFingerprintIsProjectIdFirstAndCarriesItsDomain is
// the compositional half, asserting the exact ordered argument list.
//
// **The behavioural test can pass without the project id** whenever
// another part discriminates — here the renderer filter does — and that
// is precisely how the original defect survived its first test.
func TestTheViewListingFingerprintIsProjectIdFirstAndCarriesItsDomain(t *testing.T) {
	projectID := uuid.New()
	got := viewListingFingerprint(projectID, ViewFilter{Renderer: "graph"})
	want := paging.Fingerprint(projectID.String(), "views", "graph")
	if got != want {
		t.Fatalf("the fingerprint must be project id first, then the domain discriminator, "+
			"then the filter: got %q want %q", got, want)
	}
	// A different game must not produce the same fingerprint even with an
	// identical filter — which is what the project-id-first rule buys and
	// what a behavioural test cannot isolate.
	if viewListingFingerprint(uuid.New(), ViewFilter{Renderer: "graph"}) == got {
		t.Fatal("two games share a fingerprint")
	}
	// And the domain discriminator: without it, a views cursor and an
	// entities cursor over the same game and an empty filter would be
	// interchangeable.
	if got == paging.Fingerprint(projectID.String(), "graph") {
		t.Fatal("the fingerprint carries no domain discriminator")
	}
}

// TestAViewListingIsScopedToItsOwnGame is the isolation half of the
// listing: two games hold views under the same keys and the same names,
// and each listing answers with its own.
func TestAViewListingIsScopedToItsOwnGame(t *testing.T) {
	azeroth, outland := newGame(t)
	ctx := context.Background()
	azeroth.seedViews(t, 3, "Shared name", RendererGraph)
	outland.seedViews(t, 2, "Shared name", RendererGraph)

	mine, err := azeroth.views.ListViews(ctx, azeroth.projectID, ViewFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	theirs, err := outland.views.ListViews(ctx, outland.projectID, ViewFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(mine.Views) != 3 || len(theirs.Views) != 2 {
		t.Fatalf("listings = %d and %d rows, want 3 and 2: neither game may see the "+
			"other's views", len(mine.Views), len(theirs.Views))
	}
	for _, row := range mine.Views {
		if row.ProjectID != azeroth.projectID {
			t.Fatalf("a row of another game reached this listing: %s", row.ID)
		}
	}
}

// TestAMalformedViewCursorIsRefusedAsTheCallersOwnArgument: the sentence
// is internal/paging's, shared, and the type and the path are this
// domain's, so an agent that garbled a cursor is not told the server is
// broken.
func TestAMalformedViewCursorIsRefusedAsTheCallersOwnArgument(t *testing.T) {
	g, _ := newGame(t)
	_, err := g.views.ListViews(context.Background(), g.projectID,
		ViewFilter{Cursor: "not-a-cursor"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input", err)
	}
	var ve *metamodel.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Path != "/cursor" {
		t.Fatalf("err = %#v, want one problem at the cursor's own path", err)
	}
}
