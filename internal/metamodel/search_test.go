package metamodel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/testutil"
)

// searchKeys is what these assertions compare: which rows a query found,
// in the order it ranked them.
func searchKeys(rows []dbq.SearchEntitiesRow) []string {
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	return keys
}

// seedSearchableType declares a type whose schema covers every shape
// searchTextOf collects from — a long text, a tagged list and an enum —
// plus a number, which it deliberately does not collect.
func seedSearchableType(t *testing.T, svc *metamodel.Service, project uuid.UUID) {
	t.Helper()
	_, err := svc.UpsertEntityType(context.Background(), project, metamodel.EntityTypeInput{
		Key: "quest", Label: "Quest", LabelPlural: "Quests",
		Schema: metamodel.Schema{
			{Key: "min_level", Type: metamodel.FieldNumber, Required: true},
			{Key: "summary", Type: metamodel.FieldLongText},
			{Key: "tags", Type: metamodel.FieldListText},
			{Key: "faction", Type: metamodel.FieldEnum, Options: []string{"alliance", "horde"}},
		},
	})
	if err != nil {
		t.Fatalf("seed searchable type: %v", err)
	}
}

func TestSearchFindsByNameAndByEveryTextShapeAFieldCanHold(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedSearchableType(t, svc, project)

	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "hogger", Name: "Wanted: Hogger",
		Fields: map[string]any{
			"min_level": float64(10),
			"summary":   "Defeat the gnoll chieftain.",
			"tags":      []any{"elite", "bounty"},
			"faction":   "alliance",
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, tc := range []struct {
		name  string
		query string
		want  []string
	}{
		{"by name", "Hogger", []string{"hogger"}},
		{"by a long text field", "gnoll", []string{"hogger"}},
		{"by an element of a list", "bounty", []string{"hogger"}},
		{"by the chosen option of an enum", "alliance", []string{"hogger"}},
		{"a word nothing carries", "murloc", nil},
		// min_level is 10 and nothing indexes it: numbers are found by
		// filtering, not by typing them. searchTextOf records why.
		{"a number is not indexed", "10", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := svc.Search(ctx, project, tc.query, "", 10)
			if err != nil {
				t.Fatalf("Search(%q): %v", tc.query, err)
			}
			if got := searchKeys(rows); !equalStrings(got, tc.want) {
				t.Fatalf("Search(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// TestSearchMatchesAcrossTypesAndNarrowsToOne pins the decision that a
// search is over a game rather than over a type: an agent looking for a
// name does not know which type the game modelled it as.
func TestSearchMatchesAcrossTypesAndNarrowsToOne(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "zone", Key: "hogger-den", Name: "Hogger's Den",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	all, err := svc.Search(ctx, project, "Hogger", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("an unfiltered search found %v, want the quest and the zone", searchKeys(all))
	}

	zones, err := svc.Search(ctx, project, "Hogger", "zone", 10)
	if err != nil {
		t.Fatalf("Search by type: %v", err)
	}
	if got := searchKeys(zones); !equalStrings(got, []string{"hogger-den"}) {
		t.Fatalf("a type-filtered search found %v, want only the zone", got)
	}
}

// TestSearchNamesAMistypedTypeKey pins that a narrowing filter behaves
// like a listing's: a mistyped key is named, not answered with nothing.
func TestSearchNamesAMistypedTypeKey(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	_, err := svc.Search(ctx, project, "Hogger", "zonne", 10)
	if !errors.Is(err, metamodel.ErrNotFound) {
		t.Fatalf("err = %v, want not_found", err)
	}
	if !strings.Contains(err.Error(), `"zonne"`) {
		t.Fatalf("err = %v, want it to name the key", err)
	}
}

// TestASearchWithNoWordInItIsRefused pins the difference between "this
// game has nothing like that" and "you did not ask for anything".
//
// plainto_tsquery turns a query with no words into an empty tsquery,
// which matches no row, so without this check every one of these returns
// a clean empty answer indistinguishable from a real miss.
func TestASearchWithNoWordInItIsRefused(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	for _, query := range []string{"", "   ", "...", "!?&", "-"} {
		t.Run(query, func(t *testing.T) {
			_, err := svc.Search(ctx, project, query, "", 10)
			if !errors.Is(err, metamodel.ErrInvalidInput) {
				t.Fatalf("Search(%q): err = %v, want invalid_input", query, err)
			}
			var ve *metamodel.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			if len(ve.Fields) != 1 || ve.Fields[0].Path != "query" {
				t.Fatalf("fields = %+v, want one problem at path \"query\"", ve.Fields)
			}
			if !strings.Contains(ve.Fields[0].Message, "no word to search for") {
				t.Fatalf("message = %q, want it to say the query holds no word", ve.Fields[0].Message)
			}
		})
	}

	// A query that does carry a word is not caught by the same check,
	// however much punctuation travels with it.
	if _, err := svc.Search(ctx, project, "Hogger!", "", 10); err != nil {
		t.Fatalf("a query with a word in it was refused: %v", err)
	}
}

// TestSearchRanksTheStrongerMatchFirst pins that the answer is ordered
// by ts_rank and not by name or insertion order.
//
// It also pins what that ranking is *not*: the row that carries the word
// twice wins, and it wins over a row whose own name is the query. Search
// documents that limitation and names the fix (setweight in
// UpsertEntity, plus a rewrite of stored rows); this test is what would
// have to change when Task 7 takes that decision.
func TestSearchRanksTheStrongerMatchFirst(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedSearchableType(t, svc, project)

	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "named", Name: "Gnoll",
		Fields: map[string]any{"min_level": float64(1)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "mentioned", Name: "Wanted: Hogger",
		Fields: map[string]any{
			"min_level": float64(1),
			"summary":   "A gnoll camp led by a gnoll chieftain, gnoll banners everywhere.",
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rows, err := svc.Search(ctx, project, "gnoll", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := searchKeys(rows); !equalStrings(got, []string{"mentioned", "named"}) {
		t.Fatalf("ranking = %v, want the row matching more often first", got)
	}
	if rows[0].Rank <= rows[1].Rank {
		t.Fatalf("ranks are %v and %v, want the first strictly higher", rows[0].Rank, rows[1].Rank)
	}
}

// TestSearchIsScopedToItsGame pins the project filter, which is the only
// thing keeping a query inside one game: the query text names no parent
// whose composite key could scope it.
func TestSearchIsScopedToItsGame(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	mine := newProject(t, pool)
	theirs := newProject(t, pool)
	seedWorld(t, svc, mine)
	seedWorld(t, svc, theirs)

	rows, err := svc.Search(ctx, mine, "Hogger", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("found %d rows, want only my own game's", len(rows))
	}
	if rows[0].ProjectID != mine {
		t.Fatalf("found a row of project %s", rows[0].ProjectID)
	}
}

// TestSearchAnswersAnOverLargeLimitWithMoreThanTheDefault pins the
// distinction relationPageSize's doc comment argues for, at the one
// place a smaller test cannot see it: with fewer rows than the default
// in the game, folding an over-large limit onto the default and clamping
// it to the cap return the same answer, and a test built that way passes
// either way.
//
// So there are sixty rows here, above the default of fifty and below the
// cap of two hundred. Asking for nothing gets fifty; asking for far too
// much gets all sixty, which is what clamping means and what folding
// onto the default could not produce.
func TestSearchAnswersAnOverLargeLimitWithMoreThanTheDefault(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedQuestType(t, svc, project)
	seedQuests(t, svc, project, 60)

	for _, tc := range []struct {
		name  string
		limit int32
		want  int
	}{
		{"nothing asked for", 0, 50},
		{"a negative limit is still no opinion", -1, 50},
		{"an explicit limit below the default", 2, 2},
		{"far above the cap", 1 << 20, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := svc.Search(ctx, project, "Quest", "", tc.limit)
			if err != nil {
				t.Fatalf("Search(limit %d): %v", tc.limit, err)
			}
			if len(rows) != tc.want {
				t.Fatalf("Search(limit %d) found %d rows, want %d", tc.limit, len(rows), tc.want)
			}
		})
	}
}

// TestOnlyTheIndexedHeadOfALongFieldIsSearchable pins the one thing a
// caller of Search has to know that the stored row does not show: a
// value longer than searchTextLimit is stored and re-read whole, and
// findable only by the words in its first 128 KiB.
func TestOnlyTheIndexedHeadOfALongFieldIsSearchable(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedSearchableType(t, svc, project)

	// One word at the front, a great deal of filler, one word past the
	// bound. "padding " is eight bytes, so 20,000 of them clear 128 KiB.
	lore := "gnoll " + strings.Repeat("padding ", 20000) + "murloc"
	row, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "lore", Name: "The Long Story",
		Fields: map[string]any{"min_level": float64(1), "summary": lore},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	found, err := svc.Search(ctx, project, "gnoll", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := searchKeys(found); !equalStrings(got, []string{"lore"}) {
		t.Fatalf("the head of the field found %v, want the row", got)
	}
	missed, err := svc.Search(ctx, project, "murloc", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(missed) != 0 {
		t.Fatalf("a word past the index bound was found: %v", searchKeys(missed))
	}

	// The value itself is untouched: the index gives way, never the
	// content.
	stored, err := svc.EntityByKey(ctx, project, "quest", "lore")
	if err != nil {
		t.Fatalf("EntityByKey: %v", err)
	}
	if !strings.Contains(string(stored.Fields), "murloc") {
		t.Fatal("the stored value lost the text the index dropped")
	}
	if stored.ID != row.ID {
		t.Fatal("read back a different row")
	}
}

// TestASearchQueryIsBoundedAndReportedAsTheCallersOwnArgument pins the
// three ways an unbounded or malformed query reached Postgres and came
// back as something an agent reads as "the server is broken".
//
// Measured on this project's own Postgres before the bound went in,
// through Search:
//
//   - a NUL inside a word — six characters of JSON escape, which an agent
//     produces by accident — reached plainto_tsquery and returned
//     `ERROR: invalid byte sequence for encoding "UTF8"`, untyped, so it
//     surfaced as internal_error. An unpaired surrogate or a lone
//     continuation byte — not a control character, but also not valid
//     UTF-8 — took the identical path to the identical error.
//   - a query of *one word repeated* cost 2.1s of database CPU at
//     146 KiB, then failed with `stack depth limit exceeded`, also
//     untyped; 292 KiB took 8.5s and 585 KiB took 33.5s. This is a
//     separate measurement from MaxSearchQuery's doc comment, which
//     timed *distinct* words and found a shorter query and a smaller
//     multiplier (126 KiB / 0.36s, 263 KiB / 1.4s, 536 KiB / 5.5s) — the
//     two are not the same run and are not meant to be compared word for
//     word; see MaxSearchQuery for why a repeated word is not obviously
//     the cheaper case for `plainto_tsquery` to parse. Either shape
//     alone already makes the point: the growth is quadratic, so a
//     handful of concurrent calls is a self-inflicted denial of service.
//
// All three are the caller's own argument at path `query`, so by this
// package's own rule they are invalid_input, and all three are now
// refused before a byte of them reaches the database.
func TestASearchQueryIsBoundedAndReportedAsTheCallersOwnArgument(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedWorld(t, svc, project)

	for _, tc := range []struct {
		name, query, wants string
	}{
		{"a NUL inside a word", "hogger\x00gnoll", "control character"},
		{"a bare NUL", "\x00", "control character"},
		{"an escape", "hogger\x1bgnoll", "control character"},
		{"a newline", "hogger\ngnoll", "control character"},
		{"an unpaired surrogate", "hello \xed\xa0\x80 world", "valid UTF-8"},
		{"a lone continuation byte", "hello \xff world", "valid UTF-8"},
		{"146 KiB of words", strings.Repeat("gnoll ", 25000), "too long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Search(ctx, project, tc.query, "", 10)
			if !errors.Is(err, metamodel.ErrInvalidInput) {
				t.Fatalf("err = %v, want invalid_input", err)
			}
			var ve *metamodel.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			if len(ve.Fields) != 1 || ve.Fields[0].Path != "query" {
				t.Fatalf("fields = %+v, want one problem at path \"query\"", ve.Fields)
			}
			if !strings.Contains(ve.Fields[0].Message, tc.wants) {
				t.Fatalf("message = %q, want it to name %q", ve.Fields[0].Message, tc.wants)
			}
		})
	}

	// The bound is a bound and not a ban: a query of exactly the bound
	// still searches, and one byte more is refused.
	t.Run("a query at the bound still searches", func(t *testing.T) {
		atBound := strings.Repeat("a", metamodel.MaxSearchQuery)
		if _, err := svc.Search(ctx, project, atBound, "", 10); err != nil {
			t.Fatalf("a query of exactly the bound was refused: %v", err)
		}
		if _, err := svc.Search(ctx, project, atBound+"a", "", 10); !errors.Is(
			err, metamodel.ErrInvalidInput) {
			t.Fatalf("err = %v, want one byte over the bound refused", err)
		}
	})
}
