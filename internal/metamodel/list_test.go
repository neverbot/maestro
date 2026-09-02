package metamodel_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// seedQuests writes n quests named "Quest 00" upwards, so a listing has
// a deterministic order to page through: the listing sorts by name, and
// zero-padding keeps the lexicographic order and the numeric one the
// same.
func seedQuests(t *testing.T, svc *metamodel.Service, project uuid.UUID, n int) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest",
			Key:     fmt.Sprintf("quest-%02d", i),
			Name:    fmt.Sprintf("Quest %02d", i),
			Fields:  map[string]any{"min_level": float64(i + 1)},
		}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
}

// keysOf is what most of these assertions compare: a listing is judged by
// which rows it returned and in which order, and a dbq.Entity printed
// whole buries that in audit columns.
func keysOf(page metamodel.EntityPage) []string {
	keys := make([]string, 0, len(page.Entities))
	for _, e := range page.Entities {
		keys = append(keys, e.Key)
	}
	return keys
}

func TestListPaginatesWithACursor(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)
	seedQuests(t, svc, project, 25)

	var seen []string
	filter := metamodel.EntityFilter{TypeKey: "quest", Limit: 10}
	for page := 1; ; page++ {
		got, err := svc.ListEntities(ctx, project, filter)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		seen = append(seen, keysOf(got)...)
		if got.NextCursor == "" {
			if len(got.Entities) == 10 {
				t.Fatalf("page %d is full and carries no cursor", page)
			}
			break
		}
		if len(got.Entities) != 10 {
			t.Fatalf("page %d has %d rows and still carries a cursor", page, len(got.Entities))
		}
		if page > 5 {
			t.Fatal("the listing never ran out of pages")
		}
		filter.Cursor = got.NextCursor
	}

	if len(seen) != 25 {
		t.Fatalf("paged over %d rows, want 25: %v", len(seen), seen)
	}
	for i, key := range seen {
		if want := fmt.Sprintf("quest-%02d", i); key != want {
			t.Fatalf("row %d is %q, want %q (the pages overlapped or skipped)", i, key, want)
		}
	}
}

// TestAFullFinalPageCarriesACursorToAnEmptyOne pins the one cost of
// deciding a cursor by "the page came back full": a listing whose row
// count is an exact multiple of the limit spends one extra call finding
// out it is over.
//
// The alternative — reading limit+1 rows and dropping the last — buys
// nothing here and costs a row on every page of every listing, so the
// empty page stands. It is pinned rather than merely tolerated because a
// caller looping on NextCursor must know an empty page is reachable and
// is not an error.
func TestAFullFinalPageCarriesACursorToAnEmptyOne(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)
	seedQuests(t, svc, project, 10)

	full, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{TypeKey: "quest", Limit: 10})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(full.Entities) != 10 || full.NextCursor == "" {
		t.Fatalf("first page has %d rows, cursor %q", len(full.Entities), full.NextCursor)
	}

	empty, err := svc.ListEntities(ctx, project,
		metamodel.EntityFilter{TypeKey: "quest", Limit: 10, Cursor: full.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(empty.Entities) != 0 {
		t.Fatalf("second page has %d rows, want 0", len(empty.Entities))
	}
	if empty.NextCursor != "" {
		t.Fatal("the empty page must end the listing")
	}
}

// TestAPageIsNotShiftedByAConcurrentWrite pins what a keyset cursor buys
// over an offset, which is the whole reason it is a keyset.
//
// Between the two pages this test deletes a row of the first page and
// inserts one before the cursor's position. An OFFSET would have slid the
// window by the net change and skipped or repeated a row; a keyset asks
// for "the rows after this name and id", so the second page is exactly
// the rows it would have been. The inserted row sorts before the position
// and is therefore never seen — correct, and stated in ListEntities'
// doc comment, because a caller paging a game that is being edited under
// it needs to know a page is a position and not a snapshot.
func TestAPageIsNotShiftedByAConcurrentWrite(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)
	seedQuests(t, svc, project, 12)

	first, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{TypeKey: "quest", Limit: 5})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}

	// A deletion inside the page already read, and an insertion that sorts
	// before the cursor's position.
	if err := svc.RemoveEntity(ctx, project, first.Entities[1].ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "quest-00-extra", Name: "Quest 00 extra",
		Fields: map[string]any{"min_level": float64(1)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	second, err := svc.ListEntities(ctx, project,
		metamodel.EntityFilter{TypeKey: "quest", Limit: 5, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	want := []string{"quest-05", "quest-06", "quest-07", "quest-08", "quest-09"}
	if got := keysOf(second); !equalStrings(got, want) {
		t.Fatalf("second page = %v, want %v", got, want)
	}
}

// TestACursorSurvivesTheRowItNamesBeingDeleted pins that a cursor is a
// position and not a reference: nothing re-reads the row it was issued
// from, so deleting that row between two pages does not break the
// listing.
func TestACursorSurvivesTheRowItNamesBeingDeleted(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)
	seedQuests(t, svc, project, 6)

	first, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{TypeKey: "quest", Limit: 3})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if err := svc.RemoveEntity(ctx, project, first.Entities[2].ID); err != nil {
		t.Fatalf("remove the row the cursor names: %v", err)
	}

	second, err := svc.ListEntities(ctx, project,
		metamodel.EntityFilter{TypeKey: "quest", Limit: 3, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	want := []string{"quest-03", "quest-04", "quest-05"}
	if got := keysOf(second); !equalStrings(got, want) {
		t.Fatalf("second page = %v, want %v", got, want)
	}
}

// TestAMalformedCursorIsInvalidInput pins the code, the path and the
// message of every way a cursor can be unreadable.
//
// It is invalid_input and not a bare error because the cursor is the
// caller's own argument and the recovery is the caller's: page from a
// cursor a previous call handed it, or omit it. Left untyped this reaches
// an agent as internal_error over a value the agent itself supplied.
func TestAMalformedCursorIsInvalidInput(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)
	seedQuests(t, svc, project, 2)

	cases := []struct {
		name   string
		cursor string
	}{
		{"not base64", "not a cursor!"},
		{"base64 of something that is not JSON", base64.RawURLEncoding.EncodeToString([]byte("{{{"))},
		{"JSON of the wrong shape", base64.RawURLEncoding.EncodeToString([]byte(`["quest-00"]`))},
		{"a position with no id", base64.RawURLEncoding.EncodeToString([]byte(`{"n":"Quest 00"}`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.ListEntities(ctx, project,
				metamodel.EntityFilter{TypeKey: "quest", Limit: 2, Cursor: tc.cursor})
			if !errors.Is(err, metamodel.ErrInvalidInput) {
				t.Fatalf("err = %v, want invalid_input", err)
			}
			var ve *metamodel.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			if len(ve.Fields) != 1 || ve.Fields[0].Path != "cursor" {
				t.Fatalf("fields = %+v, want one problem at path \"cursor\"", ve.Fields)
			}
			if !strings.Contains(ve.Fields[0].Message, "is malformed") {
				t.Fatalf("message = %q, want it to say the cursor is malformed", ve.Fields[0].Message)
			}
		})
	}
}

// TestACursorIssuedForAnotherListingIsRefused pins the one thing a
// keyset cursor cannot do on its own: tell a caller it has been carried
// across to a different filter.
//
// A cursor holds a position in a sort order, and every filter of this
// listing shares that order, so a cursor from the quest listing pages
// perfectly well into the zone listing and returns zones — a silently
// wrong answer to a call nobody meant to make. The cursor therefore
// carries a fingerprint of the filter it was issued for and a mismatch
// is refused at path "cursor".
func TestACursorIssuedForAnotherListingIsRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	quests, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{TypeKey: "quest", Limit: 1})
	if err != nil {
		t.Fatalf("quest page: %v", err)
	}
	if quests.NextCursor == "" {
		t.Fatal("the quest page must carry a cursor for this test to mean anything")
	}

	for _, tc := range []struct {
		name   string
		filter metamodel.EntityFilter
	}{
		{"another type", metamodel.EntityFilter{TypeKey: "zone", Limit: 1}},
		{"no type at all", metamodel.EntityFilter{Limit: 1}},
		{"the same type with another flag", metamodel.EntityFilter{
			TypeKey: "quest", Invalid: ptrBool(false), Limit: 1,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.filter
			f.Cursor = quests.NextCursor
			_, err := svc.ListEntities(ctx, project, f)
			if !errors.Is(err, metamodel.ErrInvalidInput) {
				t.Fatalf("err = %v, want invalid_input", err)
			}
			var ve *metamodel.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			if len(ve.Fields) != 1 || ve.Fields[0].Path != "cursor" {
				t.Fatalf("fields = %+v, want one problem at path \"cursor\"", ve.Fields)
			}
			if !strings.Contains(ve.Fields[0].Message, "different listing") {
				t.Fatalf("message = %q, want it to name the mismatch", ve.Fields[0].Message)
			}
		})
	}

	// The same cursor against the listing it came from still works, so the
	// fingerprint refuses a mismatch and nothing else.
	if _, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{
		TypeKey: "quest", Limit: 1, Cursor: quests.NextCursor,
	}); err != nil {
		t.Fatalf("the cursor's own listing: %v", err)
	}
}

// TestAForgedCursorCannotReachAnotherGamesRows pins what stops a cursor
// being an escape from a game.
//
// A cursor is an unsigned, unencrypted position, so a caller can write
// one naming any name and any id it likes, including a row of another
// game. It buys nothing: the cursor only ever becomes a `>` comparison
// inside a statement already filtered by this game's project id, so the
// worst a forged position does is skip the caller's own rows. This test
// hands one listing a cursor built from another game's row, under this
// game's own fingerprint, and pins that the answer is still this game's
// rows.
func TestAForgedCursorCannotReachAnotherGamesRows(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine := newProject(t, pool)
	theirs := newProject(t, pool)
	seedQuestType(t, svc, mine)
	seedQuestType(t, svc, theirs)
	seedQuests(t, svc, mine, 4)
	seedQuests(t, svc, theirs, 4)

	// A cursor of my own, to lift the fingerprint out of.
	page, err := svc.ListEntities(ctx, mine, metamodel.EntityFilter{TypeKey: "quest", Limit: 1})
	if err != nil {
		t.Fatalf("my first page: %v", err)
	}
	theirRows, err := svc.ListEntities(ctx, theirs, metamodel.EntityFilter{TypeKey: "quest", Limit: 4})
	if err != nil {
		t.Fatalf("their listing: %v", err)
	}

	forged := reissueCursor(t, page.NextCursor, theirRows.Entities[0].Name, theirRows.Entities[0].ID)
	got, err := svc.ListEntities(ctx, mine,
		metamodel.EntityFilter{TypeKey: "quest", Limit: 10, Cursor: forged})
	if err != nil {
		t.Fatalf("forged page: %v", err)
	}
	for _, row := range got.Entities {
		if row.ProjectID != mine {
			t.Fatalf("a forged cursor returned a row of project %s", row.ProjectID)
		}
	}
	// Their first row sorts at the same name as mine, so the forgery lands
	// mid-listing and returns the rest of my own rows.
	if len(got.Entities) == 0 {
		t.Fatal("the forged cursor returned nothing at all; it must page my own rows")
	}
}

// reissueCursor rewrites the position inside a cursor this package
// issued, keeping its fingerprint. It is the forger a caller could
// trivially be: the encoding is base64 of JSON and nothing signs it.
func reissueCursor(t *testing.T, encoded, name string, id uuid.UUID) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal cursor: %v", err)
	}
	fields["n"] = name
	fields["i"] = id.String()
	out, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal cursor: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(out)
}

func TestListFiltersByInvalid(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "ok", Name: "Fine", Fields: map[string]any{"min_level": float64(1)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{
			{Key: "min_level", Type: metamodel.FieldNumber, Required: true},
			{Key: "faction", Type: metamodel.FieldText, Required: true},
		},
		ExpectedVersion: ptrInt32(1),
	}); err != nil {
		t.Fatalf("evolve: %v", err)
	}

	for _, tc := range []struct {
		name  string
		flag  *bool
		want  []string
		total int
	}{
		{"only the invalid rows", ptrBool(true), []string{"ok"}, 1},
		{"only the valid rows", ptrBool(false), nil, 0},
		{"no opinion", nil, []string{"ok"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := svc.ListEntities(ctx, project,
				metamodel.EntityFilter{TypeKey: "quest", Invalid: tc.flag, Limit: 50})
			if err != nil {
				t.Fatalf("ListEntities: %v", err)
			}
			if got := keysOf(page); !equalStrings(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAnUnknownTypeKeyInAListingNamesTheKey pins that a mistyped filter
// is a not_found naming what was mistyped, and not an empty listing: a
// caller told "no rows" reads it as "this game has no quests" and goes
// off to seed a second copy of them under the wrong key.
func TestAnUnknownTypeKeyInAListingNamesTheKey(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)

	_, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{TypeKey: "quesst", Limit: 10})
	if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want not_found", err)
	}
	if !strings.Contains(err.Error(), `"quesst"`) {
		t.Fatalf("err = %v, want it to name the key that was not found", err)
	}
}

// TestAListingIsScopedToItsGame pins the filter that does the isolating.
func TestAListingIsScopedToItsGame(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine := newProject(t, pool)
	theirs := newProject(t, pool)
	seedQuestType(t, svc, mine)
	seedQuestType(t, svc, theirs)
	seedQuests(t, svc, theirs, 3)

	page, err := svc.ListEntities(ctx, mine, metamodel.EntityFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(page.Entities) != 0 {
		t.Fatalf("my empty game listed %d rows of somebody else's", len(page.Entities))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ptrBool(v bool) *bool { return &v }

// seedTakesPlaceIn declares the relation type the traversal tests hop
// along and returns nothing: every test that uses it addresses the type
// by its key, as a caller would.
func seedTakesPlaceIn(t *testing.T, svc *metamodel.Service, project uuid.UUID) {
	t.Helper()
	if _, err := svc.UpsertRelationType(context.Background(), project, metamodel.RelationTypeInput{
		Key: "takes_place_in", Label: "takes place in", SemanticRole: "spatial",
	}); err != nil {
		t.Fatalf("seed relation type: %v", err)
	}
}

func relate(t *testing.T, svc *metamodel.Service, project uuid.UUID, typeKey string, src, dst metamodel.Ref) {
	t.Helper()
	if _, err := svc.UpsertRelation(context.Background(), project, metamodel.RelationInput{
		TypeKey: typeKey, Source: src, Target: dst,
	}); err != nil {
		t.Fatalf("relate %s/%s -> %s/%s: %v", src.TypeKey, src.Key, dst.TypeKey, dst.Key, err)
	}
}

// TestOneHopTraversalAnswersInBothDirections pins the only thing
// direction means here: which end of the edge the anchor sits at, and
// therefore which end comes back.
func TestOneHopTraversalAnswersInBothDirections(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedTakesPlaceIn(t, svc, project)

	relate(t, svc, project, "takes_place_in",
		metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"})
	relate(t, svc, project, "takes_place_in",
		metamodel.Ref{TypeKey: "quest", Key: "kobold-camp"},
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"})

	// Which quests happen in Elwynn Forest? The anchor is the zone and
	// the edges point at it, so the hop is incoming.
	incoming, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{
		RelatedTo: &metamodel.RelatedFilter{
			RelationTypeKey: "takes_place_in", EntityTypeKey: "zone", EntityKey: "elwynn",
			Direction: metamodel.DirectionIncoming,
		},
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("incoming: %v", err)
	}
	// Ordered by name, not by key: "Kobold Camp" sorts before "Wanted:
	// Hogger". Every listing in this file shares that order, which is
	// what the cursor is a position in.
	if got := keysOf(incoming); !equalStrings(got, []string{"kobold-camp", "hogger"}) {
		t.Fatalf("incoming = %v, want the two quests", got)
	}

	// Where does Hogger happen? The anchor is the quest and its edge
	// points away, so the hop is outgoing.
	outgoing, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{
		RelatedTo: &metamodel.RelatedFilter{
			RelationTypeKey: "takes_place_in", EntityTypeKey: "quest", EntityKey: "hogger",
			Direction: metamodel.DirectionOutgoing,
		},
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("outgoing: %v", err)
	}
	if got := keysOf(outgoing); !equalStrings(got, []string{"elwynn"}) {
		t.Fatalf("outgoing = %v, want the zone", got)
	}

	// The same anchor read the other way round has no neighbours, and
	// says so rather than falling back to the direction that does.
	empty, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{
		RelatedTo: &metamodel.RelatedFilter{
			RelationTypeKey: "takes_place_in", EntityTypeKey: "quest", EntityKey: "hogger",
			Direction: metamodel.DirectionIncoming,
		},
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("the empty direction: %v", err)
	}
	if len(empty.Entities) != 0 {
		t.Fatalf("incoming from a source entity returned %v", keysOf(empty))
	}
}

// TestAnUnknownDirectionIsRefused pins that a misspelled direction is an
// invalid_input at its own path, not an empty page and not a silent fall
// back to outgoing. Both of those answer a question the caller did not
// ask, and a caller cannot tell either from "this anchor has no
// neighbours".
func TestAnUnknownDirectionIsRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedTakesPlaceIn(t, svc, project)
	relate(t, svc, project, "takes_place_in",
		metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"})

	for _, direction := range []string{"", "in", "Outgoing", "both"} {
		t.Run(fmt.Sprintf("%q", direction), func(t *testing.T) {
			_, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{
				RelatedTo: &metamodel.RelatedFilter{
					RelationTypeKey: "takes_place_in", EntityTypeKey: "quest", EntityKey: "hogger",
					Direction: direction,
				},
				Limit: 50,
			})
			if !errors.Is(err, metamodel.ErrInvalidInput) {
				t.Fatalf("err = %v, want invalid_input", err)
			}
			var ve *metamodel.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			if len(ve.Fields) != 1 || ve.Fields[0].Path != "related_to.direction" {
				t.Fatalf("fields = %+v, want one problem at path \"related_to.direction\"", ve.Fields)
			}
			for _, want := range []string{`"outgoing"`, `"incoming"`} {
				if !strings.Contains(ve.Fields[0].Message, want) {
					t.Fatalf("message = %q, want it to name %s", ve.Fields[0].Message, want)
				}
			}
		})
	}
}

// TestASelfLoopAppearsOnceInItsOwnNeighbourList pins the decision
// RelatedFilter records: an edge from an entity to itself puts the
// anchor in its own answer, once, under either direction. Twice would be
// the join matching both arms; never would be this listing hiding an edge
// the game declared.
func TestASelfLoopAppearsOnceInItsOwnNeighbourList(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "unlocks", Label: "unlocks", SemanticRole: "unlock",
	}); err != nil {
		t.Fatalf("seed relation type: %v", err)
	}
	relate(t, svc, project, "unlocks",
		metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		metamodel.Ref{TypeKey: "quest", Key: "hogger"})

	for _, direction := range []string{metamodel.DirectionOutgoing, metamodel.DirectionIncoming} {
		t.Run(direction, func(t *testing.T) {
			page, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{
				RelatedTo: &metamodel.RelatedFilter{
					RelationTypeKey: "unlocks", EntityTypeKey: "quest", EntityKey: "hogger",
					Direction: direction,
				},
				Limit: 50,
			})
			if err != nil {
				t.Fatalf("ListEntities: %v", err)
			}
			if got := keysOf(page); !equalStrings(got, []string{"hogger"}) {
				t.Fatalf("got %v, want the anchor exactly once", got)
			}
		})
	}
}

// TestATraversalNamesAMistypedKey pins that each of the three keys a hop
// carries is resolved before the listing runs, and that a miss names the
// key rather than answering with an empty neighbourhood.
func TestATraversalNamesAMistypedKey(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedTakesPlaceIn(t, svc, project)

	cases := []struct {
		name string
		rel  metamodel.RelatedFilter
		want string
	}{
		{"the relation type", metamodel.RelatedFilter{
			RelationTypeKey: "takes_place_at", EntityTypeKey: "zone", EntityKey: "elwynn",
			Direction: metamodel.DirectionIncoming,
		}, `"takes_place_at"`},
		{"the anchor's type", metamodel.RelatedFilter{
			RelationTypeKey: "takes_place_in", EntityTypeKey: "zonne", EntityKey: "elwynn",
			Direction: metamodel.DirectionIncoming,
		}, `"zonne"`},
		{"the anchor itself", metamodel.RelatedFilter{
			RelationTypeKey: "takes_place_in", EntityTypeKey: "zone", EntityKey: "elwyn",
			Direction: metamodel.DirectionIncoming,
		}, `"elwyn"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rel := tc.rel
			_, err := svc.ListEntities(ctx, project,
				metamodel.EntityFilter{RelatedTo: &rel, Limit: 50})
			if !errors.Is(err, metamodel.ErrNotFound) {
				t.Fatalf("err = %v, want not_found", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to name %s", err, tc.want)
			}
		})
	}
}

// TestATraversalHonoursTheEntityTypeFilter pins that a filter carried
// alongside RelatedTo is applied and not dropped. A hop that ignored
// TypeKey would answer "which quests are here" with the zones and
// classes too, and nothing in the answer would say the clause had been
// discarded.
func TestATraversalHonoursTheEntityTypeFilter(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	if _, err := svc.UpsertRelationType(ctx, project, metamodel.RelationTypeInput{
		Key: "available_in", Label: "available in", SemanticRole: "availability",
	}); err != nil {
		t.Fatalf("seed relation type: %v", err)
	}
	relate(t, svc, project, "available_in",
		metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"})
	relate(t, svc, project, "available_in",
		metamodel.Ref{TypeKey: "class", Key: "mage"},
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"})

	all, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{
		RelatedTo: &metamodel.RelatedFilter{
			RelationTypeKey: "available_in", EntityTypeKey: "zone", EntityKey: "elwynn",
			Direction: metamodel.DirectionIncoming,
		},
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("unfiltered: %v", err)
	}
	if got := keysOf(all); !equalStrings(got, []string{"mage", "hogger"}) {
		t.Fatalf("unfiltered = %v, want both neighbours", got)
	}

	quests, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{
		TypeKey: "quest",
		RelatedTo: &metamodel.RelatedFilter{
			RelationTypeKey: "available_in", EntityTypeKey: "zone", EntityKey: "elwynn",
			Direction: metamodel.DirectionIncoming,
		},
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("filtered: %v", err)
	}
	if got := keysOf(quests); !equalStrings(got, []string{"hogger"}) {
		t.Fatalf("filtered = %v, want only the quest", got)
	}
}

// TestATraversalPagesLikeEveryOtherListing pins that a hop is not
// silently truncated at its limit. The plan for this task cut the
// neighbour set in Go and returned no cursor, which made every neighbour
// past the limit unreachable with nothing in the answer to say so; the
// keyset is on the query instead.
func TestATraversalPagesLikeEveryOtherListing(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)
	seedQuests(t, svc, project, 7)
	if _, err := svc.UpsertEntityType(ctx, project, metamodel.EntityTypeInput{
		Key: "zone", Label: "Zone", LabelPlural: "Zones",
	}); err != nil {
		t.Fatalf("seed zone type: %v", err)
	}
	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "zone", Key: "elwynn", Name: "Elwynn Forest",
	}); err != nil {
		t.Fatalf("seed zone: %v", err)
	}
	seedTakesPlaceIn(t, svc, project)
	for i := range 7 {
		relate(t, svc, project, "takes_place_in",
			metamodel.Ref{TypeKey: "quest", Key: fmt.Sprintf("quest-%02d", i)},
			metamodel.Ref{TypeKey: "zone", Key: "elwynn"})
	}

	rel := metamodel.RelatedFilter{
		RelationTypeKey: "takes_place_in", EntityTypeKey: "zone", EntityKey: "elwynn",
		Direction: metamodel.DirectionIncoming,
	}
	var seen []string
	filter := metamodel.EntityFilter{RelatedTo: &rel, Limit: 3}
	for page := 1; page <= 5; page++ {
		got, err := svc.ListEntities(ctx, project, filter)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		seen = append(seen, keysOf(got)...)
		if got.NextCursor == "" {
			break
		}
		filter.Cursor = got.NextCursor
	}
	if len(seen) != 7 {
		t.Fatalf("paged over %d neighbours, want 7: %v", len(seen), seen)
	}
	for i, key := range seen {
		if want := fmt.Sprintf("quest-%02d", i); key != want {
			t.Fatalf("neighbour %d is %q, want %q", i, key, want)
		}
	}

	// A traversal's cursor belongs to that traversal, so it cannot be
	// carried over to the plain listing of the same entity type.
	first, err := svc.ListEntities(ctx, project, metamodel.EntityFilter{RelatedTo: &rel, Limit: 3})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	_, err = svc.ListEntities(ctx, project,
		metamodel.EntityFilter{TypeKey: "quest", Limit: 3, Cursor: first.NextCursor})
	if !errors.Is(err, metamodel.ErrInvalidInput) {
		t.Fatalf("err = %v, want a traversal's cursor refused by the plain listing", err)
	}
}

// TestATraversalIsScopedToItsGame pins that two games sharing every key
// do not share a neighbourhood. The anchor and the relation type are
// resolved inside the project, which is the mechanism; the query's own
// project filters are defence in depth, as the SQL comment records.
func TestATraversalIsScopedToItsGame(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine := newProject(t, pool)
	theirs := newProject(t, pool)
	for _, project := range []uuid.UUID{mine, theirs} {
		seedWorld(t, svc, project)
		seedTakesPlaceIn(t, svc, project)
	}
	relate(t, svc, theirs, "takes_place_in",
		metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"})

	page, err := svc.ListEntities(ctx, mine, metamodel.EntityFilter{
		RelatedTo: &metamodel.RelatedFilter{
			RelationTypeKey: "takes_place_in", EntityTypeKey: "zone", EntityKey: "elwynn",
			Direction: metamodel.DirectionIncoming,
		},
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(page.Entities) != 0 {
		t.Fatalf("my Elwynn has %v as neighbours, all of them somebody else's", keysOf(page))
	}
}

// TestAnEdgeCannotOutliveItsRelationType pins the answer to "what does a
// traversal do with an edge whose type was deleted": there is no such
// edge to have an opinion about.
//
// relations.relation_type_id is ON DELETE RESTRICT, so a type with edges
// cannot be dropped, and RemoveRelationType(cascade) deletes the edges
// first — so the two ways a relation type goes away either refuse or take
// the edges with them. This traversal therefore never sees a dangling
// edge, and nothing in the query filters for one.
func TestAnEdgeCannotOutliveItsRelationType(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)
	seedTakesPlaceIn(t, svc, project)
	relate(t, svc, project, "takes_place_in",
		metamodel.Ref{TypeKey: "quest", Key: "hogger"},
		metamodel.Ref{TypeKey: "zone", Key: "elwynn"})

	relType, err := svc.RelationTypeByKey(ctx, project, "takes_place_in")
	if err != nil {
		t.Fatalf("RelationTypeByKey: %v", err)
	}
	if err := svc.RemoveRelationType(ctx, project, relType.ID, false); !errors.Is(err, metamodel.ErrInUse) {
		t.Fatalf("removing a type with edges: err = %v, want in_use", err)
	}
	if err := svc.RemoveRelationType(ctx, project, relType.ID, true); err != nil {
		t.Fatalf("cascade: %v", err)
	}

	var edges int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM relations WHERE project_id = $1`, project).Scan(&edges); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if edges != 0 {
		t.Fatalf("%d edges outlived their relation type", edges)
	}
	// And the traversal that named it now names the key, rather than
	// answering over edges that are no longer classified.
	_, err = svc.ListEntities(ctx, project, metamodel.EntityFilter{
		RelatedTo: &metamodel.RelatedFilter{
			RelationTypeKey: "takes_place_in", EntityTypeKey: "zone", EntityKey: "elwynn",
			Direction: metamodel.DirectionIncoming,
		},
		Limit: 50,
	})
	if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want not_found naming the removed relation type", err)
	}
}
