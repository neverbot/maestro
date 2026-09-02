package metamodel

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// The bounds on one search. Smaller than a listing's, because a search
// answer is read rather than paged: it is the top of a ranking, and a
// caller wanting a whole set of rows wants ListEntities.
const (
	defaultSearchLimit int32 = 50
	maxSearchLimit     int32 = 200
)

// MaxSearchQuery bounds, in bytes, the query text one search may carry.
//
// It is exported because a bound a caller cannot read is a bound a
// caller trips over: Task 7 states it in the search tool's description
// beside the row bound searchTextLimit sets, so an agent composing a
// query knows both edges of what search can be asked.
//
// 4 KiB is far more than any real query and far less than the size at
// which plainto_tsquery becomes a problem. What made a bound necessary
// is that the cost is *quadratic in the query*, and that it ends in an
// untyped failure. Measured through Search on this project's own
// Postgres, with distinct words so the parse cannot fold them: 126 KiB
// answered in 0.36s, 263 KiB failed with `stack depth limit exceeded`
// (SQLSTATE 54001) after 1.4s, and 536 KiB failed the same way after
// 5.5s — four times the text for fifteen times the work. So a handful of
// concurrent calls carrying text nobody vetted is a denial of service
// the server does to itself, and what a caller gets back for it is an
// internal_error over its own argument.
//
// **This is one of two measurements of the same failure mode, not the
// only one.** `search_test.go`'s
// TestASearchQueryIsBoundedAndReportedAsTheCallersOwnArgument timed the
// identical failure with one word repeated rather than distinct words,
// on a separate run, and got numbers roughly six times larger at the
// same sizes. Distinct runs on different data are not directly
// comparable and neither comment claims to be measuring the other's
// workload; both are recorded because either one alone proves the bound
// is needed, and the gap between them is a caution against reading a
// single measured number as the constant rather than as one sample of a
// quadratic curve.
const MaxSearchQuery = 4 << 10

// Search runs a full-text query over a game's entities and returns the
// best matches first.
//
// **What is searchable.** The `search` column, which UpsertEntity builds
// from the row's name and the text its values carry — text, longtext,
// the chosen option of an enum, and the elements of a list<text>.
// Numbers and booleans are deliberately absent: they are found by
// filtering, not by typing them (searchTextOf records the argument).
// **A row's text is indexed only up to searchTextLimit, 128 KiB of
// flattened values**, so the tail of a very long lore field is stored
// and re-read intact but is not findable here — a search that misses a
// word deep inside a megabyte of prose has found the row's bound, not
// the absence of the word.
//
// **The query has a bound of its own: MaxSearchQuery, 4 KiB**, and it
// must be valid UTF-8 holding no control character. All of it is refused
// as invalid_input at path `query` before the text reaches the database,
// for the reasons checkSearchQuery records. The two bounds are stated
// together because a caller reading only the index one would think the
// query side was unlimited, which is exactly what it used to be.
//
// **What it ranks by.** Two keys, in this order.
//
// First, whether the row's *name* satisfies the query. UpsertEntity
// writes the name under label A and the whole text under label B, and
// SearchEntities leads its ORDER BY with `ts_filter(search, '{a}') @@
// query` — the A half is the name and nothing else, so this asks
// exactly the question the promise is about. **A row the query names
// therefore outranks a row that merely mentions the words in a
// paragraph, however often that paragraph repeats them**, and that is a
// guarantee rather than a tendency.
//
// It has to be a sort key and not a weight, which is review finding M1.
// `ts_rank` saturates towards 1.0 as a lexeme repeats, so with the
// weights alone one word under label A wins comfortably but a
// multi-word query does not: it is a weighted sum of several saturating
// terms, and four repetitions of a two-word phrase in a lore field were
// enough to beat the entity actually named that phrase. An explicit
// weight array only moves the number of repetitions it takes. Task 6
// shipped this column unweighted and said so; 0006_weighted_entity_
// search.sql weighted it and records why its backfill is an exact
// function of the vectors that were already stored.
//
// Second, `ts_rank` over the whole column, which scores by how many of
// the query's lexemes a row matches, how often, and under which weight.
// It orders within each of the two groups, which is the work the
// weights were introduced for. The weights come from `ts_rank`'s
// default array ({D:0.1, C:0.2, B:0.4, A:1.0}); nothing here passes one.
// Ties break by name and then id, so two identical calls answer
// identically.
//
// What this is *not* is a name lookup. A word that appears nowhere but
// in a field is still found — TestSearchStillFindsAWordOnlyAFieldCarries
// pins it — it simply ranks below a row that carries the word in its
// name.
//
// **It matches across entity types**, which is the point: an agent
// looking for "Hogger" does not know whether the game modelled him as a
// quest, a creature or a place. typeKey narrows it to one, and an
// unknown one is a not_found naming the key rather than an empty answer.
//
// **It is not paginated.** The answer is the top `limit` rows in the
// `(name_match, rank)` order above — not by rank alone, which is a
// weaker and narrower claim now that name_match leads the sort — and a
// caller holding exactly `limit` rows cannot tell whether there were
// more. A cursor over that order is not the keyset the listings use —
// neither key is unique, neither is stable under an edit, and neither is
// a position a caller can resume from — and the useful recovery for a
// search that returned too much is a narrower query, not a deeper page.
// **Task 7 states the cap in the tool description**, and puts
// name_match itself on the wire on SearchHit, so an agent knows the
// answer is a top-N and can read the grouping the order is built from
// rather than reconstruct it from rank.
func (s *Service) Search(ctx context.Context, projectID uuid.UUID, query, typeKey string, limit int32) ([]dbq.SearchEntitiesRow, error) {
	if err := checkSearchQuery(query); err != nil {
		return nil, err
	}

	params := dbq.SearchEntitiesParams{
		ProjectID: projectID,
		Query:     query,
		Limit:     pageSize(limit, defaultSearchLimit, maxSearchLimit),
	}
	if typeKey != "" {
		typ, err := s.EntityTypeByKey(ctx, projectID, typeKey)
		if err != nil {
			return nil, err
		}
		params.EntityTypeID = &typ.ID
	}

	rows, err := s.q.SearchEntities(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("search entities: %w", err)
	}
	return rows, nil
}

// SearchLimit reports how many rows Search will actually return for a
// requested limit: the default when nothing was asked for, the cap when
// too much was. It is exported so the MCP surface can say whether an
// answer was cut at the limit without duplicating the clamping rule —
// a copy of it here and there is exactly how "asking for 501 returns
// fewer rows than asking for 500" got in the first time.
func SearchLimit(limit int32) int32 {
	return pageSize(limit, defaultSearchLimit, maxSearchLimit)
}

// checkSearchQuery refuses a query the database should never be shown,
// and one that cannot match anything, rather than letting either answer
// "nothing found" or "the server is broken".
//
// **All four refusals are invalid_input at path `query`**, which is the
// rule this package applies to every argument a caller supplied: the
// value is the caller's and so is the recovery. Three of them used to be
// neither refused nor typed. A NUL — six characters of JSON escape, so
// an agent writes one by accident — travelled into `plainto_tsquery` and
// came back as `ERROR: invalid byte sequence for encoding "UTF8": 0x00
// (SQLSTATE 22021)`, untyped, reaching the caller as internal_error; an
// unbounded query burned database CPU before failing the same anonymous
// way (MaxSearchQuery records the measurements); and a query that was
// invalid UTF-8 without containing a NUL — an unpaired surrogate, a lone
// continuation byte — reached Postgres by the identical route and failed
// with the identical SQLSTATE 22021, because `unicode.IsControl` decodes
// an invalid byte as the replacement character U+FFFD, which is not a
// control character, and so let it through. All four now stop here,
// before a byte reaches Postgres.
//
// **The query must be valid UTF-8.** Postgres rejects every invalid
// byte sequence with SQLSTATE 22021, not only a NUL — a NUL is simply
// the one invalid sequence a JSON-encoding agent is likeliest to produce
// by accident, which is why it was the first one this function caught.
// `utf8.ValidString` is checked before the control-character scan below,
// so a query that is both invalid UTF-8 and free of any decodable
// control character is still refused, and refused with a message about
// its actual defect rather than "no word to search for".
//
// **A control character is refused, not stripped**, and that includes
// newline and tab: a query is one line of text a human or an agent
// typed, `simple` can make a lexeme of none of these, and silently
// deleting part of a caller's query would answer a question it did not
// ask. NUL is refused by the same rule as the rest, not a special case:
// a query carrying any control character is a caller assembling text
// wrongly, and telling it so at the first character is more useful than
// searching for whatever survived.
//
// `plainto_tsquery` turns a query with no words into an empty tsquery,
// and an empty tsquery matches no row. So `""`, `"   "` and `"..."` all
// come back as a clean empty answer that reads exactly like "this game
// has no such content" — the wrong answer to a call that never asked a
// question, and one an agent would act on by seeding a duplicate or
// reporting the content missing. The recovery is the caller's, so it is
// an invalid_input at the argument's own path.
//
// The test is "does the query contain a letter or a digit", and that is
// tied to the `simple` text-search configuration the search column and
// these queries both use: `simple` has no stopword list and no stemmer,
// so every token holding a letter or a digit becomes a lexeme. Under a
// configuration with stopwords — `english`, say — a query of nothing but
// stopwords would pass this check and still match nothing, and this
// function would have to ask the database instead. Whoever changes the
// configuration changes this too; they are one decision.
func checkSearchQuery(query string) error {
	if len(query) > MaxSearchQuery {
		return searchQueryProblem(fmt.Sprintf(
			"is too long (%d bytes, the most this search takes is %d): "+
				"search for the words that matter rather than pasting the text",
			len(query), MaxSearchQuery))
	}
	if !utf8.ValidString(query) {
		return searchQueryProblem("is not valid UTF-8: a byte in it does not decode as any " +
			"character, and Postgres refuses that outright")
	}
	if i := strings.IndexFunc(query, unicode.IsControl); i >= 0 {
		return searchQueryProblem(fmt.Sprintf(
			"holds a control character (%U at byte %d): a query is one line of text, "+
				"and no control character can be part of a word to search for",
			[]rune(query[i:])[0], i))
	}
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return nil
		}
	}
	return searchQueryProblem("has no word to search for: give at least one letter or digit, " +
		"since punctuation alone matches nothing and would read as an empty game")
}

// searchQueryProblem is the one shape every refusal of a query takes:
// invalid_input at the argument's own path. The three refusals are one
// rule — the query is the caller's, and so is the recovery — and giving
// them one constructor keeps a future fourth from being reported as a
// server fault.
func searchQueryProblem(message string) error {
	return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: "query", Message: message,
	}}}
}
