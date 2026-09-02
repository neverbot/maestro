package markdown

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// The bounds on one page of documents.
//
// Exported for the rule the metamodel established for MaxSearchQuery: a
// bound a caller cannot read is a bound a caller trips over, and Task
// 10's docs.list description is built with these values interpolated
// rather than typed out, where a number would go on promising a cap that
// moved.
//
// The clamp policy itself -- that a limit above the cap is clamped to
// the cap rather than folded onto the default -- is paging.Size's, and
// it is pinned there, by internal/paging/cursor_test.go's
// TestSizeClampsRatherThanFoldingOntoTheDefault. What this package's own
// TestTheLimitIsClampedRatherThanFoldedOntoTheDefault pins is that this
// listing applies that policy and not another: its fixture is sixty
// documents against a default of fifty, so clamping and folding return
// different answers here, which is the trap
// TestAHistoryPageAsksForTooMuchAndGetsTheCap fell into with two rows.
const (
	// DefaultDocumentPage and MaxDocumentPage bound one page of List.
	DefaultDocumentPage int32 = 50
	MaxDocumentPage     int32 = 200
)

// ListFilter narrows a document listing.
//
// EntityType and EntityKey are given together or not at all: half of an
// address is not a filter, and reading one half as "no filter" would
// answer a different question with a full page.
// TestNamingOnlyOneHalfOfTheEntityFilterIsInvalidInput pins both halves.
//
// Cursor is the NextCursor of a previous call. It belongs to the game
// and to the exact filter it was issued for and to no other;
// DocumentPage carries the contract.
type ListFilter struct {
	PathPrefix     string
	Kind           string
	EntityType     string
	EntityKey      string
	IncludeDeleted bool
	Cursor         string
	Limit          int32
}

// DocumentSummary is one row of a listing.
//
// **It has no body and no frontmatter field**, which is what makes "a
// listing never returns a body" a fact about the type rather than a
// promise about the query -- the shape HistoryPage.Versions already
// takes for a version row. A body is returned by Read, ReadVersion and
// Diff and by nothing else (spec §7): prose is the largest payload in
// the system, agents are its main consumer, and a listing carrying
// bodies would blow a context window on the first call against a real
// game. TestAListingRowCarriesNoBodyAtAll pins the absence over every
// field rather than over a field it can name, since what has to hold is
// that no such field exists, and
// TestAListingIsSummariesInPathOrderAndEveryFieldReadsBack reads every
// field that is here back through List.
type DocumentSummary struct {
	ID             uuid.UUID
	Path           string
	Kind           string
	Title          string
	Summary        string
	CurrentVersion int32
	Deleted        bool
}

// DocumentPage is one page of summaries plus the cursor for the next.
//
// **NextCursor is set when the page came back full**, and empty
// otherwise. That is one call more than strictly necessary on a listing
// whose length is an exact multiple of the limit -- the last full page
// carries a cursor to an empty one -- and the alternative, reading
// limit+1 rows and dropping the extra, costs a row on every page of
// every listing to save that one call. A caller looping until
// NextCursor is empty is correct and must expect an empty final page
// rather than treating one as an error.
// TestAFullFinalPageCarriesACursorToAnEmptyOne pins both halves.
//
// The rest of the cursor contract is paging.Cursor's: a position and not
// a snapshot, refused against any other listing, unsigned and not a
// capability. Cursor.Sort, for this listing, is the row's path;
// HistoryPage's is the version number and metamodel.EntityPage's the
// row's name.
//
// **A document path is mutable in principle and immutable in practice**
// -- UpsertDocument never writes the path column, so the only way a
// row's sort key moves is a caller creating a document at a path that
// sorts earlier, which a paged walk will simply not see. That is the
// ordinary "a row inserted before the position is never seen" behaviour
// paging.Cursor documents, not a special case.
//
// Documents is never nil: an empty listing marshals as [] and not null,
// the same decision LinksByDocument makes for a document with no
// attachments (TestAnEmptyListingIsAnEmptyPageAndNotNil, asserted
// through encoding/json for the same reason).
type DocumentPage struct {
	Documents  []DocumentSummary
	NextCursor string
}

// List returns one page of a game's documents, in path order.
//
// **An unknown key in a filter is a not_found naming the key**, not an
// empty page. A caller that mistyped `quesst` hears about `quesst`
// instead of being told this game has no documents and going off to seed
// a second copy of them, and the two halves of an entity address are
// told apart the way a link's are -- a mistyped type and a mistyped key
// are two different mistakes at two different arguments (see
// resolveEntity, which this call shares with the link tools).
// TestAnUnknownFilterKeyIsNotFoundRatherThanAnEmptyPage pins both.
//
// A `kind` or a `path_prefix` naming nothing is deliberately *not* the
// same case, and answers with an empty page: neither is a key of a row
// that has to exist. `kind` is free text a project attaches to a
// document and Maestro ships no vocabulary of them, so there is no set
// of known kinds to have missed; a prefix names a subtree, and a subtree
// with nothing in it is an ordinary answer -- "no scripts yet" is what a
// listing of `scripts/` in a new game means, not a mistake to report. An
// entity is the only filter part that addresses a row, so it is the only
// one that can be wrong rather than empty.
func (s *Service) List(ctx context.Context, projectID uuid.UUID, f ListFilter) (DocumentPage, error) {
	problems := checkPathPrefix(f.PathPrefix)
	problems = append(problems, checkShortText("kind", f.Kind, MaxKindLen)...)
	switch {
	case f.EntityType != "" && f.EntityKey == "":
		problems = append(problems, metamodel.FieldError{
			Path:    "entity_key",
			Message: "and entity_type must be given together: half an address is not a filter",
		})
	case f.EntityKey != "" && f.EntityType == "":
		problems = append(problems, metamodel.FieldError{
			Path:    "entity_type",
			Message: "and entity_key must be given together: half an address is not a filter",
		})
	case f.EntityType != "":
		// Bounded before either lookup runs, through the metamodel's own
		// key rule rather than a second copy of it, exactly as the link
		// calls do (entityAddressProblems). An entity key holding an
		// invalid UTF-8 byte reaches Postgres as a byte sequence it
		// refuses outright, which would land on the default arm as
		// internal_error over a value the caller supplied.
		// TestAnEntityFilterIsBoundedBeforePostgresSeesIt pins it.
		problems = append(problems, entityAddressProblems("", LinkTarget{
			EntityType: f.EntityType, EntityKey: f.EntityKey,
		})...)
	}
	if len(problems) > 0 {
		return DocumentPage{}, invalidInputProblems(problems)
	}

	params := dbq.ListDocumentsPageParams{
		ProjectID:      projectID,
		IncludeDeleted: f.IncludeDeleted,
		Limit:          paging.Size(f.Limit, DefaultDocumentPage, MaxDocumentPage),
	}
	entityPart := ""
	if f.EntityType != "" {
		entityID, err := s.resolveEntity(ctx, s.q, projectID, "", LinkTarget{
			EntityType: f.EntityType, EntityKey: f.EntityKey,
		})
		if err != nil {
			return DocumentPage{}, err
		}
		params.EntityID = &entityID
		entityPart = entityID.String()
	}
	if f.PathPrefix != "" {
		prefix := f.PathPrefix
		params.PathPrefix = &prefix
	}
	if f.Kind != "" {
		kind := f.Kind
		params.Kind = &kind
	}

	fingerprint := documentListingFingerprint(projectID, f, entityPart)
	after, err := paging.Decode(f.Cursor, fingerprint, refuseCursor)
	if err != nil {
		return DocumentPage{}, err
	}
	if after.ID != uuid.Nil {
		id := after.ID
		sort := after.Sort
		params.AfterID = &id
		params.AfterPath = &sort
	}

	rows, err := s.q.ListDocumentsPage(ctx, params)
	if err != nil {
		return DocumentPage{}, fmt.Errorf("list documents: %w", err)
	}

	page := DocumentPage{Documents: make([]DocumentSummary, 0, len(rows))}
	for _, row := range rows {
		page.Documents = append(page.Documents, DocumentSummary{
			ID: row.ID, Path: row.Path, Kind: row.Kind, Title: row.Title,
			Summary: row.Summary, CurrentVersion: row.CurrentVersion,
			// pgtype.Timestamptz, not a pointer: a nil test does not
			// compile against the generated model (Task 4's correction
			// 2).
			Deleted: row.DeletedAt.Valid,
		})
	}
	// paging.Size never returns a limit below one, so a full page is
	// never an empty one and there is no separate emptiness check.
	if len(rows) == int(params.Limit) {
		last := rows[len(rows)-1]
		page.NextCursor = paging.Encode(paging.Cursor{
			Sort: last.Path, ID: last.ID, Fingerprint: fingerprint,
		})
	}
	return page, nil
}

// documentListingFingerprint is the resolved listing one document cursor
// belongs to: the game, then this domain's listing, then every filter
// that changes which rows a page holds.
//
// **The project id is first, always**, which is paging.Fingerprint's
// standing rule and the one that was learned the hard way -- a
// fingerprint that omitted it let one game's cursor page another game's
// rows and answer with two of its ten. Here the rule is load-bearing
// rather than redundant: unlike the history listing, whose document id
// tells two games apart on its own (Task 6's correction 4), an
// unfiltered document listing's other parts are the same strings in
// every game, so TestACursorFromAnotherGamesListingIsRefused does go red
// without it. TestTheDocumentListingFingerprintLeadsWithTheProjectId
// pins the composition itself anyway, because "the behaviour test
// happens to reach it" is a property of today's filters and not of the
// rule: the day this listing grows a filter whose resolved form differs
// per game, the behaviour test goes green with the project id gone.
//
// "documents" is a domain discriminator, which paging.Fingerprint
// mandates for nobody and this package supplies anyway:
// internal/metamodel spells its own listings "entities", "related" and
// "relations", this package's other one "document_versions", and two
// domains reaching for one word is a collision nothing would catch.
//
// The entity filter is spelled as the *resolved* id rather than as the
// caller's keys, so two spellings of one key give one fingerprint and a
// cursor survives a respelling of its own filter; the prefix and the
// kind are folded for the same reason, since the query folds them too.
// An absent entity filter is the empty string, which no uuid spells, so
// "no opinion" stays distinct from every opinion -- the point
// paging.TriState makes for an optional boolean.
// TestACursorFromAnEntityFilteredListingIsRefusedElsewhere covers the
// entity part in both directions, and
// TestAListingPagesAndItsCursorBelongsToItsFilter the other three.
func documentListingFingerprint(projectID uuid.UUID, f ListFilter, entityID string) string {
	return paging.Fingerprint(projectID.String(), "documents",
		strings.ToLower(f.PathPrefix), strings.ToLower(f.Kind), entityID,
		fmt.Sprintf("%t", f.IncludeDeleted))
}

// checkPathPrefix bounds a prefix filter.
//
// It is deliberately looser than CheckPath: "lore/" is a legitimate
// prefix and CheckPath refuses a trailing slash, so running the full
// grammar here would refuse the most obvious thing a caller would type.
// What it keeps is the part that protects the database -- the length
// bound, valid UTF-8, no control characters -- and the part that
// protects the caller from a silent wrong answer is in the query, where
// starts_with rather than LIKE means `_` is not a wildcard.
// TestAPathPrefixAndAKindAreBoundedAsTheCallersOwnArguments pins the
// three refusals and the accepted trailing slash.
func checkPathPrefix(prefix string) []metamodel.FieldError {
	if prefix == "" {
		return nil
	}
	return checkShortText("path_prefix", prefix, MaxPathLen)
}
