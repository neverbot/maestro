package markdown

import (
	"context"
	"fmt"
	"strings"
	"time"

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
//
// **Kind's zero value, "", means "no filter", and there is deliberately
// no spelling of "documents with no kind" today.** Kinds reports how
// many there are (KindTotals.Unkinded) without offering a filter that
// would select them, which is the honest half of this gap: a caller can
// see that thirty documents are unfiled, and filing them is the only
// thing it can then do about it. A document's kind is
// optional (Write's own comment), so some rows in a real game are
// expected to have none, and this filter cannot select them: Kind: ""
// is indistinguishable from Kind unset. Recorded here as a decision Task
// 10 (the docs.list tool surface) must either accept, stating the gap in
// the tool description, or close by giving this filter a third state --
// the same TriState problem paging.TriState solves for a caller-supplied
// boolean, applied here to a caller-supplied string.
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
// that no such field exists: a substring match on the lowered field
// name, plus a scan of every field's rendered value for a body string
// actually written and read back, the same two checks
// TestAHistoryRowCarriesNoBodyAtAll runs for a version row. What
// actually catches a populated field this reflection missed is
// TestAListingIsSummariesInPathOrderAndEveryFieldReadsBack's struct
// equality, which reads every field that is here back through List.
type DocumentSummary struct {
	ID             uuid.UUID
	Path           string
	Kind           string
	Title          string
	Summary        string
	CurrentVersion int32
	Deleted        bool

	// CreatedAt, UpdatedAt, CreatedBy and UpdatedBy are what make "what
	// changed lately" answerable from a listing.
	//
	// **They were selected by this query and dropped on the floor before
	// this**: 0007_documents.sql has carried both timestamps and both
	// audit pairs since Task 1, ListDocumentsPage has selected all six
	// since Task 8, and none of them reached a caller — so answering the
	// first question a designer opens a game bible to ask cost one
	// history call per document, fifty for a page of fifty. A column the
	// database keeps and the surface withholds is a write-only column,
	// which this project has now shipped twice.
	//
	// **Both pairs, not only the recent one.** "Who last touched this"
	// is the question a listing is usually read for, and "who wrote this
	// in the first place" is the one asked about a document nobody has
	// edited since — where the two answers are the same row and
	// carrying only one of them would say nothing about which. They cost
	// one query between them for the whole page (Service.Authors), so
	// the choice is which facts are true rather than which are cheap.
	CreatedAt time.Time
	UpdatedAt time.Time
	CreatedBy Author
	UpdatedBy Author
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
// **A document path is mutable, and Move is the one call that moves
// it.** That sentence used to read "mutable in principle and immutable
// in practice", on the grounds that UpsertDocument never writes the path
// column; Move does, so the hazard paging.Cursor documents is real here
// now and not hypothetical. A document moved to a path that sorts before
// a walk's position is not seen again by that walk, and one moved to a
// path that sorts after it is seen twice. Both are the ordinary keyset
// behaviour over a mutable sort key rather than anything this listing
// can fix -- the alternative is a snapshot, which paging.Cursor's own
// contract refuses to be -- and both are announced: Move publishes
// document.moved carrying the old path and the new, so a client walking
// this listing has the one fact it needs to reconcile its page.
// A creation at a path that sorts earlier stays what it always was, the
// "a row inserted before the position is never seen" behaviour.
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
// of known kinds to have missed. **That stands, and Kinds is what makes
// it liveable**: an unrecognised kind is still an empty page rather than
// a refusal, because it is still not a knowable state, but a caller no
// longer has to guess the vocabulary to filter by it -- Kinds lists what
// this game actually uses, with counts, the way the game summary lists
// the entity types it declared. A prefix naming nothing is the other
// half of the same rule; a prefix names a subtree, and a subtree
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
		// calls do. entityAddressKeyProblems, not entityAddressProblems:
		// List has no role argument, and running the whole of
		// entityAddressProblems here would silently apply the role rule
		// to a field this filter does not have (see
		// entityAddressKeyProblems' doc comment). An entity key holding
		// an invalid UTF-8 byte reaches Postgres as a byte sequence it
		// refuses outright, which would land on the default arm as
		// internal_error over a value the caller supplied.
		// TestAnEntityFilterIsBoundedBeforePostgresSeesIt pins it.
		problems = append(problems, entityAddressKeyProblems("", f.EntityType, f.EntityKey)...)
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
	// Two audit pairs per row, resolved to labels in one round trip for
	// the whole page rather than one per row: see Service.Authors. The
	// order is (created, updated) per row and is unpacked the same way
	// below, which is the one thing this pairing has to get right.
	actors := make([]Actor, 0, 2*len(rows))
	for _, row := range rows {
		actors = append(actors,
			Actor{UserID: row.CreatedByUserID, TokenID: row.CreatedByTokenID},
			Actor{UserID: row.UpdatedByUserID, TokenID: row.UpdatedByTokenID})
	}
	authors, err := s.authorsWith(ctx, s.q, projectID, actors)
	if err != nil {
		return DocumentPage{}, err
	}
	for i, row := range rows {
		page.Documents = append(page.Documents, DocumentSummary{
			ID: row.ID, Path: row.Path, Kind: row.Kind, Title: row.Title,
			Summary: row.Summary, CurrentVersion: row.CurrentVersion,
			// pgtype.Timestamptz, not a pointer: a nil test does not
			// compile against the generated model (Task 4's correction
			// 2).
			Deleted:   row.DeletedAt.Valid,
			CreatedAt: row.CreatedAt.Time,
			UpdatedAt: row.UpdatedAt.Time,
			CreatedBy: authors[2*i],
			UpdatedBy: authors[2*i+1],
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
//
// **The two strings.ToLower calls are pinned only by
// TestTheDocumentListingFingerprintLeadsWithTheProjectId, the
// composition test, and not by any behavioural test in this package.**
// What would pin them behaviourally is a cursor issued from a listing
// filtered on one casing of a prefix or a kind, carried across to the
// same listing re-spelled with a different casing, and accepted -- the
// entity part already has exactly that proof, the "QUEST"/"Wanted-Hogger"
// case in TestACursorFromAnEntityFilteredListingIsRefusedElsewhere. No
// such case exists for the prefix or the kind. The failure mode is a
// false refusal -- a caller re-spelling its own filter's case gets
// "cursor belongs to a different listing" where the two filters answer
// the same rows -- rather than a wrong answer, so this is recorded as a
// known gap rather than fixed here.
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
