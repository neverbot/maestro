package markdown

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// The bounds on one document search.
//
// **They are metamodel's numbers, deliberately, and this package exports
// none of its own.** The two answers are merged into one ranked list
// (internal/web/mcp_search.go), so there is one limit governing both
// sides — a caller asking for fifty hits must not get fifty documents
// and two hundred entities — and a second exported pair here would be a
// second number for a tool description to quote and a second thing to
// keep in step. The search tool's description interpolates
// metamodel.DefaultSearchLimit and metamodel.MaxSearchLimit; these two
// unexported constants exist only so this function can clamp without
// importing a bound it would then have to re-export.
//
// TestASearchLimitDefaultsAndIsHonoured pins that a default applies and
// that a requested limit is passed through. It does *not* pin the cap:
// separating "clamped at the cap" from "no cap at all" needs more than
// maxSearchLimit matching documents in one fixture, and the clamping
// policy itself is pinned against paging.Size in
// internal/paging/cursor_test.go's
// TestSizeClampsRatherThanFoldingOntoTheDefault.
const (
	defaultSearchLimit int32 = 50
	maxSearchLimit     int32 = 200
)

// MaxIndexedChars is how much of each of a document's title, summary and
// body reaches the search vector: 0007_documents.sql wraps all three in
// `left(…, 131072)`, in *characters*, and the tail of anything longer is
// stored and re-read intact but is not findable by search.
//
// It is exported because a bound a caller cannot read is a bound a
// caller trips over — the same argument metamodel.MaxSearchQuery makes
// for its own. The search tool's description interpolates it, the way
// that tool already discloses the metamodel's 128 KiB row bound.
//
// **This constant mirrors a literal in a migration and nothing in
// Postgres reads it.** What keeps the two in step is
// TestAWordPastTheIndexBoundIsStoredButNotFindable, which writes a body
// with a distinctive word on each side of exactly this offset and
// asserts that search finds the earlier one and not the later one — so
// changing the migration's literal without changing this constant fails
// there rather than quietly making the tool description a lie.
//
// It is far below markdown.MaxBodyBytes (1 MiB, in *bytes*), which is
// the looser of the two at every encoding: this truncation is what binds
// first for any body longer than it, and it happens silently.
const MaxIndexedChars = 131072

// DocumentHit is one search result: a document, whether its *title*
// satisfied the query, the rank it matched at, and the entities it is
// attached to.
//
// NameMatch is on the wire and not only in the sort, for the reason
// metamodel's SearchHit gives: the order is `(name_match, rank)`, and a
// caller that re-sorts by rank alone — or simply reasons that a higher
// rank must come first — reconstructs the wrong order, because a hit
// with name_match false can carry a higher rank than one with it true
// and still sort after it. TestADocumentTheQueryNamesOutranksOneThatOnlyMentionsIt
// asserts exactly that inversion in its fixture.
//
// LinkedEntities is what makes a document hit actionable. A word that
// appears only in a quest's script produces a *document* hit and not a
// quest hit (spec §8) — entity search deliberately does not reach into
// attached prose, which is what keeps the entity ranking honest — so
// the hit has to carry the way back to the quest, or the designer is
// left holding a path and no context. It is never nil, so a hit
// attached to nothing marshals as [] and not null.
//
// **There is no body here and there must not be one.** A search is the
// one call an agent makes against a whole game without knowing what it
// will get back, and fifty bodies would blow a context window on the
// first answer. TestASearchHitCarriesNoBodyAtAll pins the absence over
// every field of this struct rather than over a field it can name.
type DocumentHit struct {
	ID             uuid.UUID
	Path           string
	Title          string
	Summary        string
	Kind           string
	Version        int32
	NameMatch      bool
	Rank           float32
	LinkedEntities []EntityLink
}

// SearchDocuments runs a full-text query over a game's current documents
// and returns the best matches first.
//
// **What is searchable**: the generated `search` column, which is the
// title under weight A, the summary under B and the first 131072
// *characters* of the body under C (0007_documents.sql). The tail of a
// longer body is stored and re-read intact but is not findable here — a
// search that misses a word deep inside a megabyte of prose has found
// the document's index bound, not the absence of the word. MaxBodyBytes
// is the looser of the two limits at every encoding, so this truncation
// is what binds first. `kind` and `path` are not in the vector at all:
// `kind` is a filter here, not a term.
//
// **What it ranks by**, in this order: whether the document's *title*
// satisfies the query, then ts_rank over the whole vector. The first has
// to be a sort key and not a weight, because ts_rank saturates towards
// 1.0 as a lexeme repeats, so a body repeating a two-word phrase beats
// the weights alone. SearchDocuments' own SQL comment works it through;
// TestADocumentTheQueryNamesOutranksOneThatOnlyMentionsIt pins it,
// against a fixture where the mentioning document carries the higher
// rank.
//
// **The query is bounded and validated by metamodel.CheckSearchQuery**,
// shared rather than copied: the rule about UTF-8, control characters,
// the 4 KiB cap and "a query with no word in it is refused rather than
// answered empty" is one rule, and it is tied to the `simple` text
// search configuration both indexes use. A copy here would be a second
// place that has to change the day the configuration does.
// `kind` goes through this package's own checkShortText, the same bound
// List applies to the same argument.
// TestASearchQueryIsBoundedAndReportedAsTheCallersOwnArgument and
// TestASearchKindIsBoundedAsTheCallersOwnArgument pin the two.
//
// **Deleted documents are absent, and so is history.** Only the current
// version of a live document is indexed
// (TestADeletedDocumentIsNotSearchable,
// TestOnlyTheCurrentVersionIsSearchable).
//
// **It is not paginated.** The answer is the top `limit` hits, and a
// caller holding exactly `limit` of them cannot tell whether there were
// more. A cursor over this order is not a keyset — neither key is unique
// and neither is stable under an edit — and the useful recovery for a
// search that returned too much is a narrower query, not a deeper page.
func (s *Service) SearchDocuments(ctx context.Context, projectID uuid.UUID, query, kind string, limit int32) ([]DocumentHit, error) {
	problems := checkShortText("kind", kind, MaxKindLen)
	if len(problems) > 0 {
		return nil, invalidInputProblems(problems)
	}
	if err := metamodel.CheckSearchQuery(query); err != nil {
		return nil, err
	}

	rows, err := s.q.SearchDocuments(ctx, dbq.SearchDocumentsParams{
		ProjectID: projectID,
		Query:     query,
		Kind:      kind,
		Limit:     paging.Size(limit, defaultSearchLimit, maxSearchLimit),
	})
	if err != nil {
		return nil, fmt.Errorf("search documents: %w", err)
	}

	hits := make([]DocumentHit, 0, len(rows))
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, DocumentHit{
			ID: row.ID, Path: row.Path, Title: row.Title, Summary: row.Summary,
			Kind: row.Kind, Version: row.CurrentVersion,
			NameMatch: row.NameMatch, Rank: row.Rank,
			LinkedEntities: []EntityLink{},
		})
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		return hits, nil
	}

	links, err := s.q.ListLinksForDocuments(ctx, dbq.ListLinksForDocumentsParams{
		ProjectID: projectID, DocumentIds: ids,
	})
	if err != nil {
		return nil, fmt.Errorf("list links for search hits: %w", err)
	}
	byDocument := make(map[uuid.UUID][]EntityLink, len(ids))
	for _, link := range links {
		byDocument[link.DocumentID] = append(byDocument[link.DocumentID], EntityLink{
			EntityID: link.EntityID, EntityTypeKey: link.EntityTypeKey,
			EntityKey: link.EntityKey, EntityName: link.EntityName, Role: link.Role,
		})
	}
	for i := range hits {
		if attached, ok := byDocument[hits[i].ID]; ok {
			hits[i].LinkedEntities = attached
		}
	}
	return hits, nil
}

// There is deliberately no exported SearchLimit here. metamodel's is the
// one the surface calls, because the surface clamps once for both sides;
// a twin would be an exported accessor with no caller, which is how a
// shape gets pre-committed sight unseen.
