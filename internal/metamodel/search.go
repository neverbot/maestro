package metamodel

import (
	"context"
	"encoding/json"
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
const MaxSearchQuery = 4 << 10

// Search runs a full-text query over a game's entities and returns the
// best matches first.
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

// CheckSearchQuery is checkSearchQuery, exported so the markdown domain
// applies the identical rule to the identical index configuration
// instead of growing a copy.
func CheckSearchQuery(query string) error { return checkSearchQuery(query) }

// checkSearchQuery refuses a query the database should never be shown,
// and one that cannot match anything, rather than letting either answer
// "nothing found" or "the server is broken".
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

// SearchFilter is one page of a search: the question, what it is
// narrowed to, and where the previous page stopped.
type SearchFilter struct {
	Query   string
	TypeKey string
	Cursor  string
	Limit   int32
}

// SearchPage is Search with a cursor, and it is what the interface uses.
func (s *Service) SearchPage(ctx context.Context, projectID uuid.UUID, f SearchFilter) (SearchResultPage, error) {
	if err := checkSearchQuery(f.Query); err != nil {
		return SearchResultPage{}, err
	}
	limit := pageSize(f.Limit, defaultSearchLimit, maxSearchLimit)

	params := dbq.SearchEntitiesPageParams{
		ProjectID: projectID,
		Query:     f.Query,
		Limit:     limit,
	}
	typePart := ""
	if f.TypeKey != "" {
		typ, err := s.EntityTypeByKey(ctx, projectID, f.TypeKey)
		if err != nil {
			return SearchResultPage{}, err
		}
		params.EntityTypeID = &typ.ID
		typePart = typ.ID.String()
	}

	fingerprint := fingerprintOf(projectID.String(), "search", typePart, f.Query)
	after, err := decodeCursor(f.Cursor, fingerprint)
	if err != nil {
		return SearchResultPage{}, err
	}
	if after.ID != uuid.Nil {
		position, err := decodeSearchPosition(after.Sort)
		if err != nil {
			return SearchResultPage{}, err
		}
		params.AfterID = &after.ID
		params.AfterNameMatch = &position.NameMatch
		params.AfterRank = &position.Rank
		params.AfterName = &position.Name
	}

	rows, err := s.q.SearchEntitiesPage(ctx, params)
	if err != nil {
		return SearchResultPage{}, fmt.Errorf("search entities: %w", err)
	}

	page := SearchResultPage{Entities: rows}
	if len(rows) == int(limit) {
		last := rows[len(rows)-1]
		page.NextCursor = encodeCursor(cursor{
			Sort: encodeSearchPosition(searchPosition{
				NameMatch: last.NameMatch, Rank: last.Rank, Name: last.Name,
			}),
			ID:          last.ID,
			Fingerprint: fingerprint,
		})
	}
	return page, nil
}

// SearchResultPage is one page of SearchPage's answer.
type SearchResultPage struct {
	Entities   []dbq.SearchEntitiesPageRow
	NextCursor string
}

// searchPosition is the three-part half of the position that is not the
// id. It travels inside the cursor's Sort field as JSON because
// paging.Cursor's position is one string and this order's is three
// values — and because a separator-joined string would have to escape a
// name, which can hold anything a game writes.
type searchPosition struct {
	NameMatch bool    `json:"m"`
	Rank      float32 `json:"r"`
	Name      string  `json:"n"`
}

func encodeSearchPosition(p searchPosition) string {
	raw, err := json.Marshal(p)
	if err != nil {
		// A bool, a float32 and a string always marshal. Returning ""
		// here would issue a cursor that decodes to nothing, so the
		// impossible case is loud rather than quietly wrong.
		panic("marshal search position: " + err.Error())
	}
	return string(raw)
}

func decodeSearchPosition(raw string) (searchPosition, error) {
	var p searchPosition
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		// Only a hand-edited cursor reaches this: the fingerprint has
		// already agreed, and this package writes what it reads. It is
		// the caller's own argument, so it is answered as one, with
		// paging's shared sentence rather than a second one.
		return searchPosition{}, malformedCursor("it carries no search position")
	}
	return p, nil
}
