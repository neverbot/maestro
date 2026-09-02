package metamodel

import (
	"context"
	"fmt"
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

// checkSearchQuery refuses a query that cannot match anything, rather
// than letting it answer "nothing found".
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
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return nil
		}
	}
	return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: "query",
		Message: "has no word to search for: give at least one letter or digit, " +
			"since punctuation alone matches nothing and would read as an empty game",
	}}}
}
