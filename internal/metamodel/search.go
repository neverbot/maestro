package metamodel

import (
	"context"
	"fmt"
	"strings"
	"unicode"

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
// must hold no control character. Both are refused as invalid_input at
// path `query` before the text reaches the database, for the reasons
// checkSearchQuery records. The two bounds are stated together because a
// caller reading only the index one would think the query side was
// unlimited, which is exactly what it used to be.
//
// **What it ranks by.** `ts_rank` over that column, which scores by how
// many of the query's lexemes a row matches and how often. It is
// *unweighted*: the column is one flat vector, so a row whose **name**
// is the query does not outrank a row that merely mentions it in a
// paragraph. That is a real limitation and not a decision this task
// could make on its own — fixing it means `setweight` in UpsertEntity's
// own statement, which is a write path, plus a rewrite of every row
// already stored, since existing vectors carry no weights at all. **Task
// 7 owns that call** when it decides what the search tool promises an
// agent; until then the ranking is honest about being frequency-based,
// and ties break by name and then id so two identical calls answer
// identically.
//
// **It matches across entity types**, which is the point: an agent
// looking for "Hogger" does not know whether the game modelled him as a
// quest, a creature or a place. typeKey narrows it to one, and an
// unknown one is a not_found naming the key rather than an empty answer.
//
// **It is not paginated.** The answer is the top `limit` rows by rank,
// and a caller holding exactly `limit` rows cannot tell whether there
// were more. A cursor over a ranking is not the keyset the listings use —
// rank is not unique, not stable under an edit and not a position a
// caller can resume from — and the useful recovery for a search that
// returned too much is a narrower query, not a deeper page. **Task 7
// states the cap in the tool description** so an agent knows the answer
// is a top-N.
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

// checkSearchQuery refuses a query the database should never be shown,
// and one that cannot match anything, rather than letting either answer
// "nothing found" or "the server is broken".
//
// **All three refusals are invalid_input at path `query`**, which is the
// rule this package applies to every argument a caller supplied: the
// value is the caller's and so is the recovery. Two of them used to be
// neither refused nor typed. A NUL — six characters of JSON escape, so
// an agent writes one by accident — travelled into `plainto_tsquery` and
// came back as `ERROR: invalid byte sequence for encoding "UTF8": 0x00
// (SQLSTATE 22021)`, untyped, reaching the caller as internal_error; and
// an unbounded query burned database CPU before failing the same
// anonymous way (MaxSearchQuery records the measurements). Both now stop
// here, before a byte reaches Postgres.
//
// **A control character is refused, not stripped**, and that includes
// newline and tab: a query is one line of text a human or an agent
// typed, `simple` can make a lexeme of none of these, and silently
// deleting part of a caller's query would answer a question it did not
// ask. NUL is the one Postgres itself rejects; the rest are refused with
// it because a query carrying any of them is a caller assembling text
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
