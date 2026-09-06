package metamodel

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/paging"
)

// RelatedFilter is the single hop of traversal this sub-project offers.
// Transitive walks belong to the views query engine.
//
// **Direction is relative to the anchor**, which is the entity named by
// EntityTypeKey and EntityKey: "outgoing" returns the entities the
// anchor points at along this relation type, "incoming" returns the ones
// that point at it. Both are required to be spelled exactly; see
// ListEntities for why an unrecognised spelling is refused rather than
// defaulted.
//
// **There is deliberately no "both".** An entity joined to the anchor by
// two edges of one type, one each way, would appear twice in a listing
// whose rows are entities, and collapsing the pair would throw away the
// one thing the caller asked about — which way the edge runs. A caller
// that wants the whole neighbourhood makes two calls and knows which
// half each row came from. The views sub-project, whose rows are edges
// rather than entities, is where an undirected walk belongs.
//
// **A self-loop appears once, and it is the anchor itself.** An edge
// from an entity to itself satisfies exactly one arm of the query's join
// in either direction, so the anchor comes back in its own neighbour
// list, once, under both "outgoing" and "incoming". That is the honest
// answer: the edge exists and it does point at that entity. Nothing here
// filters the anchor out, because a caller asking "what does this
// unlock" is owed the loop it declared, and the analysis sub-project is
// where self-references are reported as a modelling problem.
type RelatedFilter struct {
	RelationTypeKey string
	EntityTypeKey   string
	EntityKey       string
	Direction       string // "outgoing" or "incoming", relative to the anchor
}

// The two directions a hop can be taken in. They are compared against
// the caller's spelling here and passed to SQL as text; the query has an
// arm for each and nothing else, so a value that reached it unchecked
// would match no arm and return an empty page.
const (
	DirectionOutgoing = "outgoing"
	DirectionIncoming = "incoming"
)

// EntityFilter narrows an entity listing.
//
// TypeKey and Invalid apply to both shapes of listing — a plain one and a
// traversal — so neither is ever silently dropped: "which zones does
// Elwynn connect to" and "which of them are invalid" are one question
// with two clauses, and a filter that only worked on one path would
// answer the other question without saying so.
//
// **Invalid is the row's own flag, and on a traversal it stays the row's
// own flag**: it narrows the entities the hop reached, and it says
// nothing about the edge each hop crossed. Since 0009 an edge carries the
// same flag, so the question is worth answering rather than leaving
// implicit — a traversal follows every edge of its relation type,
// flagged or not, exactly as this listing returns every entity unless
// asked otherwise. That is deliberately *not* what internal/views does,
// and the two are not inconsistent: a view is a picture a designer will
// trust, so it excludes flagged rows and flagged edges by default and
// takes `include_invalid` to opt back in; a listing is a query, and its
// default is "no opinion" on both tables. An agent that wants the edges
// a schema edit broke asks ListRelations for them by name.
//
// Cursor is the NextCursor of a previous call. It belongs to the game
// and the filter it was issued for and to no other, and it is a position
// rather than a snapshot; EntityPage carries the whole contract, and it
// is worth reading before paging a game that is being edited.
type EntityFilter struct {
	TypeKey   string
	Invalid   *bool
	RelatedTo *RelatedFilter
	Cursor    string
	Limit     int32
}

// EntityPage is one page of results plus the cursor for the next. It is
// where every cursor in this package comes from, so the contract a
// caller has to know is written here — RelationPage's cursor obeys the
// same one, and so does the one a traversal issues.
//
// **NextCursor is set when the page came back full**, and empty
// otherwise. That is one call more than strictly necessary on a listing
// whose length is an exact multiple of the limit: the last full page
// carries a cursor to an empty one. The alternative — reading limit+1
// rows and dropping the extra — costs a row on every page of every
// listing to save that one call, so the empty page stands and
// TestAFullFinalPageCarriesACursorToAnEmptyOne pins it. A caller looping
// until NextCursor is empty is therefore correct, and must expect to be
// handed an empty final page rather than treating one as an error.
//
// **A cursor is a position, not a snapshot, and this is the part that
// bites.** It is the keyset position of the page's last row — (name, id)
// for an entity listing, (created_at, id) for a relation one — so the
// next page is "the rows after this position", never "skip this many
// rows". What that buys is stability under concurrent editing: a row
// deleted or inserted before the position does not slide the window, and
// the row the cursor names need not still exist, because nothing
// re-reads it.
//
// What it does not buy is a consistent view of the whole listing. The
// sort key is mutable, so between two pages:
//
//   - a row renamed to sort *after* the position can be seen twice;
//   - a row renamed to sort *before* it is never seen again by that
//     listing, however many pages are still to come — it has moved
//     behind the reader;
//   - a row inserted before the position is likewise never seen.
//
// None of these is a bug and none of them is reported, so a caller that
// needs a consistent whole re-reads the listing from no cursor rather
// than trusting a paged walk taken while the game was being written.
// ListRelations is the one listing this does not apply to, because
// nothing edits created_at.
//
// **A cursor belongs to the listing that issued it**, and to no other:
// the game, the filter and, for a traversal, the anchor and direction.
// Every filter of a listing shares one sort order, so a cursor carried
// across to another one would page perfectly and answer a different
// question — the quest listing's position walking the zone listing and
// returning zones, or one game's position walking another game's rows.
// It therefore carries a fingerprint of the listing it came from and a
// mismatch is refused as invalid_input at path `cursor`. Pass a cursor
// back only to the call that produced it, with the same filter.
//
// **It is not a capability and it is not signed.** It is base64 of JSON;
// a caller can decode it, rewrite the position and recompute the
// fingerprint from values it already holds. That buys nothing, because
// the position only ever becomes a `>` comparison inside a statement
// already filtered by the caller's own project id — the worst a forged
// position does is skip the caller's own rows, which
// TestAForgedCursorCannotReachAnotherGamesRows pins. The fingerprint is
// a consistency check against a caller's own mistake, and it must not be
// relied on as a security boundary. For the same reason it leaks
// nothing: the position is the sort value and id of a row this same call
// just returned to this same caller, and the fingerprint is a digest of
// the filter that caller supplied.
//
// Cursor.Sort, for this listing, is the row's name — paging.Cursor
// generalises Sort away from any one listing's sort key, and this is
// the fact that generalisation abstracts over here. RelationPage's is
// its row's created_at in RFC 3339; see that type.
type EntityPage struct {
	Entities   []dbq.Entity
	NextCursor string
}

// The bounds on one entity listing, in the shape relations.go's already
// are: asking for nothing is no opinion and gets the default, asking for
// too much is an opinion and gets the cap.
const (
	defaultEntityPage int32 = 50
	maxEntityPage     int32 = 500
)

// The keyset cursor, its fingerprint and the page clamp live in
// internal/paging. Today only this package imports it; the markdown
// domain has no listing, no cursor and no import of paging yet, and
// arrives with one in Task 8. Extracting the code now, rather than when
// Task 8 needs it, is what keeps that task from copying it: two
// listings in two packages paging with two copies of this code is how
// the fingerprint that omitted the project id — one game's cursor
// paging another game's rows — would have been fixed in one copy and
// left standing in the other.
//
// What stays here is this package's own spelling of that API: six
// one-line delegations, which the three listings and this package's
// in-package tests (list_internal_test.go, relations_internal_test.go)
// both go through. Keeping the names is what proves the extraction did
// not move the shared code out from under the tests that pin it —
// dropping the project id from a fingerprintOf call still reddens
// TestACursorFromAnotherGameIsRefused, and folding paging.Size's
// over-cap arm onto the default still reddens
// TestAnEntityPageAsksForTooMuchAndGetsTheCap and
// TestARelationPageAsksForTooMuchAndGetsTheCap — and it is why nothing
// in internal/metamodel's tests changed for this extraction.
//
// **The contract a caller has to know is EntityPage's**, where a caller
// can read it, and paging.Cursor's, which states it once for both
// domains: what a position buys and does not buy, why a cursor cannot
// be carried between listings, and why nothing signs it.
//
// A fingerprint here is over the *resolved* listing: the project id
// first, then which listing it is, then the filter — the entity type
// id, the invalid flag, and a traversal's relation type, anchor and
// direction. Resolved and not as spelled, so two spellings of one key
// give one fingerprint. The project id is first because without it two
// games' unfiltered listings shared a fingerprint and one game's cursor
// paged the other's rows from a position that meant nothing there:
// TestACursorFromAnotherGameIsRefused pins each of the three listings.
type cursor = paging.Cursor

func pageSize(limit, def, max int32) int32 { return paging.Size(limit, def, max) }

func encodeCursor(c cursor) string { return paging.Encode(c) }

func fingerprintOf(parts ...string) string { return paging.Fingerprint(parts...) }

func invalidFilterPart(invalid *bool) string { return paging.TriState(invalid) }

// refuseCursor turns paging's message into this package's own error, at
// the argument's own path.
//
// invalid_input, not a bare error: the cursor is the caller's own
// argument, at a path, and the recovery is the caller's — page from a
// cursor a previous call returned, or omit it. Left untyped it reaches
// an agent as internal_error and reads as "the server is broken" over a
// value the agent itself supplied.
//
// The messages live in internal/paging so that this domain and the
// markdown domain — once Task 8 gives it a listing of its own — cannot
// tell a caller two different things about one bad cursor; the *type*
// is this domain's, because that is what internal/web's existing
// invalid_input arm matches on.
func refuseCursor(message string) error {
	return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: "cursor", Message: message,
	}}}
}

// malformedCursor is what every unreadable cursor is reported as. Its
// sentence is paging.Malformed's, shared, because ListRelations finds a
// cursor unreadable one step past paging.Decode — it parses the
// position back into a timestamp — and a second sentence for the same
// fault is the drift this extraction exists to prevent.
func malformedCursor(why string) error { return paging.Malformed(why, refuseCursor) }

// decodeCursor reads a page position back and checks it belongs to the
// listing it was handed to. The order of paging.Decode's two refusals is
// pinned from this side by
// TestAMalformedCursorIsRefusedBeforeItsFingerprintIsJudged.
func decodeCursor(s, fingerprint string) (cursor, error) {
	return paging.Decode(s, fingerprint, refuseCursor)
}

// ListEntities returns one page of a game's entities, either the plain
// listing or the single hop RelatedTo asks for.
//
// **An unknown key in a filter is a not_found naming the key**, not an
// empty page. Every key this filter carries — the entity type, and a
// traversal's relation type and anchor — is resolved before the listing
// runs, so a caller that mistyped `quesst` hears about `quesst` instead
// of being told this game has no quests and going off to seed a second
// copy of them.
//
// **A page is a position, not a snapshot.** Pages are keyset-ordered by
// (name, id); EntityPage records what that means while the game is being
// written underneath the caller, what it does not mean, and why a cursor
// cannot be carried to another listing or another game.
func (s *Service) ListEntities(ctx context.Context, projectID uuid.UUID, f EntityFilter) (EntityPage, error) {
	limit := pageSize(f.Limit, defaultEntityPage, maxEntityPage)

	// The type filter is resolved for both shapes of listing, so a
	// traversal narrowed by entity type answers the question it was
	// asked instead of dropping the clause.
	//
	// Bounded before the lookup runs, through rowKeyProblems -- the same
	// rule UpsertEntityType checks a caller's key against before it is
	// ever a row -- so a type_key holding an invalid UTF-8 byte is a
	// named invalid_input rather than a bare "invalid byte sequence for
	// encoding \"UTF8\"" (SQLSTATE 22021) surfacing as internal_error
	// over a value the caller itself supplied.
	// TestATypeKeyFilterIsBoundedBeforePostgresSeesIt pins it.
	var typeID *uuid.UUID
	typePart := ""
	if f.TypeKey != "" {
		if problems := rowKeyProblems("type_key", f.TypeKey); len(problems) > 0 {
			return EntityPage{}, &ValidationError{Code: codeInvalidInput, Fields: problems}
		}
		typ, err := s.EntityTypeByKey(ctx, projectID, f.TypeKey)
		if err != nil {
			return EntityPage{}, err
		}
		typeID = &typ.ID
		typePart = typ.ID.String()
	}

	if f.RelatedTo != nil {
		return s.listRelated(ctx, projectID, *f.RelatedTo, typeID, typePart, f, limit)
	}

	fingerprint := fingerprintOf(projectID.String(), "entities", typePart,
		invalidFilterPart(f.Invalid))
	after, err := decodeCursor(f.Cursor, fingerprint)
	if err != nil {
		return EntityPage{}, err
	}

	params := dbq.ListEntitiesPageParams{
		ProjectID:    projectID,
		EntityTypeID: typeID,
		Invalid:      f.Invalid,
		Limit:        limit,
	}
	if after.ID != uuid.Nil {
		params.AfterID = &after.ID
		params.AfterName = &after.Sort
	}

	rows, err := s.q.ListEntitiesPage(ctx, params)
	if err != nil {
		return EntityPage{}, fmt.Errorf("list entities: %w", err)
	}
	return pageOf(rows, limit, fingerprint), nil
}

// listRelated resolves the one-hop filter and pages its answer.
//
// The relation type and the anchor are resolved by key first, so a
// mistyped one is a named not_found. That is also what makes the
// listing's own project filter enough: an anchor id and a relation type
// id that came out of this project cannot select another game's edges,
// and the SQL comment records that the filters in the query itself are
// defence in depth rather than the mechanism.
func (s *Service) listRelated(ctx context.Context, projectID uuid.UUID, rel RelatedFilter,
	typeID *uuid.UUID, typePart string, f EntityFilter, limit int32,
) (EntityPage, error) {
	// An unrecognised direction is refused rather than defaulted. The
	// query has an arm for each of the two spellings and none for
	// anything else, so a value passed through unchecked would return an
	// empty page — a caller that wrote "in" would be told the anchor has
	// no neighbours, which is a wrong answer rather than a refusal, and
	// the class of defect this whole read surface is most exposed to.
	// Defaulting to outgoing is the same fault with a fuller page.
	if rel.Direction != DirectionOutgoing && rel.Direction != DirectionIncoming {
		return EntityPage{}, &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
			Path: "related_to.direction",
			Message: fmt.Sprintf("must be %q or %q, relative to the anchor entity",
				DirectionOutgoing, DirectionIncoming),
		}}}
	}

	relType, err := s.RelationTypeByKey(ctx, projectID, rel.RelationTypeKey)
	if err != nil {
		return EntityPage{}, err
	}
	anchor, err := s.EntityByKey(ctx, projectID, rel.EntityTypeKey, rel.EntityKey)
	if err != nil {
		return EntityPage{}, err
	}

	fingerprint := fingerprintOf(projectID.String(), "related", relType.ID.String(),
		anchor.ID.String(), rel.Direction, typePart, invalidFilterPart(f.Invalid))
	after, err := decodeCursor(f.Cursor, fingerprint)
	if err != nil {
		return EntityPage{}, err
	}

	params := dbq.ListEntitiesRelatedToParams{
		ProjectID:      projectID,
		RelationTypeID: relType.ID,
		AnchorID:       anchor.ID,
		Direction:      rel.Direction,
		EntityTypeID:   typeID,
		Invalid:        f.Invalid,
		Limit:          limit,
	}
	if after.ID != uuid.Nil {
		params.AfterID = &after.ID
		params.AfterName = &after.Sort
	}

	rows, err := s.q.ListEntitiesRelatedTo(ctx, params)
	if err != nil {
		return EntityPage{}, fmt.Errorf("list related entities: %w", err)
	}
	return pageOf(rows, limit, fingerprint), nil
}

// pageOf wraps the rows a listing read, issuing a cursor when the page
// came back full. Both listings share it so that neither can end up
// issuing a cursor the other's decoder would refuse.
func pageOf(rows []dbq.Entity, limit int32, fingerprint string) EntityPage {
	page := EntityPage{Entities: rows}
	// pageSize never returns a limit below one, so a full page is never
	// an empty one and there is no separate emptiness check to make.
	if len(rows) == int(limit) {
		last := rows[len(rows)-1]
		page.NextCursor = encodeCursor(cursor{
			Sort: last.Name, ID: last.ID, Fingerprint: fingerprint,
		})
	}
	return page
}

// The bounds this package's callers are allowed to state out loud.
//
// Every one of these mirrors an unexported constant a few lines from
// where it is used. They are exported for one reason, and it is the rule
// correction 24 established for MaxSearchQuery: **a bound a caller
// cannot read is a bound a caller trips over.** The MCP tool
// descriptions (internal/web/mcp_metamodel.go) are built with these
// values interpolated rather than typed out, so a description cannot go
// on promising a cap that moved.
//
// They are aliases and not the constants themselves because the
// unexported names are what the code reads, and each of them lives
// beside the listing whose bound it is; renaming them into an exported
// block would move six constants away from the six arguments that
// justify them.
const (
	// DefaultEntityPage and MaxEntityPage bound one page of
	// ListEntities, in both of its shapes: a one-hop traversal is paged
	// by the same pageSize, the same cursor and the same pageOf as the
	// plain listing (see listRelated), so these two numbers describe a
	// page there too and not a cap on the neighbour set. An earlier
	// comment here, and the tool description built from it, said the
	// traversal returned up to MaxEntityPage neighbours and stopped;
	// review finding H1 caught it, and TestATraversalPagesLikeEveryOther
	// Listing had already been pinning the truth.
	DefaultEntityPage = defaultEntityPage
	MaxEntityPage     = maxEntityPage

	// DefaultRelationPage and MaxRelationPage bound one page of
	// ListRelations.
	DefaultRelationPage = defaultRelationPage
	MaxRelationPage     = maxRelationPage

	// DefaultSearchLimit and MaxSearchLimit bound one answer from
	// Search, which is a top-N by rank and not a page; see Search.
	DefaultSearchLimit = defaultSearchLimit
	MaxSearchLimit     = maxSearchLimit

	// MaxIndexedText is how much of one entity's flattened text is fed
	// to the search vector. The tail of a longer value is stored and
	// re-read intact but is not findable; see Search.
	MaxIndexedText = searchTextLimit
)
