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

// TestSearchRanksTheNameMatchFirst pins the ranking Task 7 settled: a
// row whose **name** is the query outranks a row that merely mentions
// the words in a field, however often it mentions them.
//
// This is the test Task 6 said would have to change, renamed from its
// previous name. It used to assert the opposite order — "mentioned"
// first, because the vector was unweighted and the row carrying the
// word three times simply matched more often.
//
// **It covered only the single-word case, and the promise was false for
// every other one — review finding M1.** Weights alone do not deliver
// it: `ts_rank` saturates towards 1.0 as a lexeme repeats, so one word
// under the A weight wins comfortably, but a multi-word query is a
// weighted sum of several saturating terms and the frequency side
// overtakes the name side. Four repetitions of a two-word phrase in a
// lore field were enough to rank that row above the entity actually
// named the phrase. SearchEntities now leads its ORDER BY with a
// name-match predicate over the A-weighted half of the vector, which
// makes the promise a guarantee instead of a tendency, and rank still
// orders within each group. The SQL comment argues the choice.
//
// The mentioned row carries the query far more often than the named row
// does in every case below, so no assertion here can pass on frequency.
func TestSearchRanksTheNameMatchFirst(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()

	for _, tc := range []struct {
		name, entityName, query string
		repeats                 int
	}{
		{"one word", "Gnoll", "gnoll", 3},
		// The reviewer's case, and then far past where it broke: the
		// old ranking lost this at four repetitions.
		{"two words", "Gnoll Pack", "gnoll pack", 4},
		{"two words, repeated until the field dwarfs the name", "Gnoll Pack", "gnoll pack", 500},
		{"three words", "Riverpaw Gnoll Pack", "riverpaw gnoll pack", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := newProject(t, pool)
			seedSearchableType(t, svc, project)

			if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
				TypeKey: "quest", Key: "named", Name: tc.entityName,
				Fields: map[string]any{"min_level": float64(1)},
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
				TypeKey: "quest", Key: "mentioned", Name: "Wanted: Hogger",
				Fields: map[string]any{
					"min_level": float64(1),
					"summary": strings.TrimSpace(strings.Repeat(
						"A "+tc.entityName+" camp led by a "+tc.entityName+" chieftain. ",
						tc.repeats)),
				},
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}

			rows, err := svc.Search(ctx, project, tc.query, "", 10)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if got := searchKeys(rows); !equalStrings(got, []string{"named", "mentioned"}) {
				t.Fatalf("ranking = %v, want the row the query names first", got)
			}
		})
	}
}

// TestSearchStillFindsAWordOnlyAFieldCarries pins that weighting the
// name did not turn search into a name lookup: a word that appears
// nowhere but in a field is still found, and still found alone.
func TestSearchStillFindsAWordOnlyAFieldCarries(t *testing.T) {
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

	rows, err := svc.Search(ctx, project, "chieftain", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := searchKeys(rows); !equalStrings(got, []string{"mentioned"}) {
		t.Fatalf("found %v, want only the row whose field carries the word", got)
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
//
// **The bound is pinned from both sides, not just the "too far" one** —
// the same gap markdown.MaxIndexedChars' own test had (Task 9's review):
// a word at offset zero and one past the bound proves the constant is
// not too large, but says nothing about whether it is smaller than the
// code that enforces it claims. `boundary`'s last byte sits at index
// searchTextLimit-1, the tightest position "just inside the bound" can
// mean, and it must still be findable.
func TestOnlyTheIndexedHeadOfALongFieldIsSearchable(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedSearchableType(t, svc, project)

	// One word at the front, a great deal of filler, a boundary word
	// ending exactly at the bound, then one word past it. "padding " is
	// eight bytes, so 20,000 of them clear 128 KiB on their own; fillerN
	// below trims that back to leave exact room for `boundary`.
	const prefix, boundary, tail = "gnoll ", "borderland", " murloc"
	filler := strings.Repeat("padding ", 20000)
	// Reserve one byte for the separator space before boundary, for the
	// same reason markdown's version of this fixture does: filler can
	// end mid-word with no trailing space, and without a separator that
	// fuses onto boundary's own first character.
	fillerN := metamodel.MaxIndexedText - len(prefix) - len(boundary) - 1
	if fillerN < 0 || fillerN > len(filler) {
		t.Fatalf("fixture arithmetic is out of range: fillerN=%d", fillerN)
	}
	lore := prefix + filler[:fillerN] + " " + boundary + tail
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
	edge, err := svc.Search(ctx, project, "borderland", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := searchKeys(edge); !equalStrings(got, []string{"lore"}) {
		t.Fatalf("a word ending exactly at searchTextLimit-1 found %v, want the row: the "+
			"constant claims more headroom than the code that truncates gives", got)
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

// TestSearchFindsARowByItsKey pins what 0010_entity_key_search.sql
// added: the handle every other tool on this surface addresses a row by
// is a handle search can find.
//
// Task 9's seeding run measured the hole this closes — `search
// ("circuit-000")` answered with nothing, so a designer typing the string
// they see in every error message and every listing had to know to reach
// for entities.get instead — and pinned it as a passing limitation to be
// deleted when it was fixed.
//
// **The fixture is chosen so the key is the only thing that can match.**
// The row is named "Silverpine Straight" and its summary talks about
// kerbs; nothing but the key carries the lexeme `circuit-000`. A vector
// that had merely grown a copy of the name would leave this red.
func TestSearchFindsARowByItsKey(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedSearchableType(t, svc, project)

	if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
		TypeKey: "quest", Key: "circuit-000", Name: "Silverpine Straight",
		Fields: map[string]any{
			"min_level": float64(1),
			"summary":   "A long left-hander onto the kerbs.",
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, tc := range []struct {
		name  string
		query string
		want  []string
	}{
		{"the whole key", "circuit-000", []string{"circuit-000"}},
		// Postgres's parser splits a hyphenated token into the whole and
		// its parts, so the halves are lexemes of their own. This is a
		// consequence of indexing the key rather than a promise about
		// key syntax, and it is written down here so a parser change
		// that removed it is seen rather than silently absorbed.
		{"a part of a hyphenated key", "circuit", []string{"circuit-000"}},
		{"a key nothing carries", "circuit-001", nil},
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

// TestAKeyMatchDoesNotClaimToBeANameMatch is the other half of 0010, and
// the one that pins *where* in the vector the key went.
//
// `name_match` is `ts_filter(search, '{a}') @@ query`, and SearchEntities
// leads its ORDER BY with it so that a row the query names outranks a row
// that merely mentions the words, however often. 0010 put the key under
// label C rather than A precisely so that predicate keeps asking about
// the name alone. Without that decision, two hundred rows keyed
// `race-000`…`race-199` would every one of them answer the word "race" as
// a name match, ahead of the row actually named Race — which is this
// fixture, in miniature and with the inversion made visible.
func TestAKeyMatchDoesNotClaimToBeANameMatch(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := metamodel.New(pool, nil)
	ctx := context.Background()
	project := newProject(t, pool)
	seedSearchableType(t, svc, project)

	// The row the word names, and three rows that merely carry it in
	// their handle. The named row is deliberately seeded *last*, so a
	// ranking that fell back to insertion or key order would put it
	// fourth and be caught.
	for _, seed := range []struct{ key, name string }{
		{"race-000", "Silverpine Straight"},
		{"race-001", "Redridge Sweep"},
		{"race-002", "Duskwood Chicane"},
		{"opening", "Race"},
	} {
		if _, err := svc.UpsertEntity(ctx, project, metamodel.EntityInput{
			TypeKey: "quest", Key: seed.key, Name: seed.name,
			Fields: map[string]any{"min_level": float64(1)},
		}); err != nil {
			t.Fatalf("seed %s: %v", seed.key, err)
		}
	}

	rows, err := svc.Search(ctx, project, "race", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("Search(\"race\") found %v, want all four rows", searchKeys(rows))
	}
	if rows[0].Key != "opening" || !rows[0].NameMatch {
		t.Fatalf("the top hit is %q (name_match %v), want the row actually named Race",
			rows[0].Key, rows[0].NameMatch)
	}
	for _, row := range rows[1:] {
		if row.NameMatch {
			t.Fatalf("row %q was found by its key and reported name_match true", row.Key)
		}
	}
}
