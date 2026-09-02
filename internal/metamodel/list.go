package metamodel

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
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

// pageSize turns a caller's requested limit into the one a listing will
// use. relationPageSize's doc comment carries the argument for why a
// limit above the cap is clamped rather than folded onto the default;
// this is that rule, shared, so the two listings cannot drift apart.
func pageSize(limit, def, max int32) int32 {
	switch {
	case limit <= 0:
		return def
	case limit > max:
		return max
	default:
		return limit
	}
}

// cursor is the keyset position of the last row of a page, together with
// a fingerprint of the listing it was issued for.
//
// **The contract is EntityPage's**, where a caller can read it: what a
// position buys and does not buy, why a cursor cannot be carried between
// listings, and why nothing signs it. This comment is only the shape.
//
// Sort is the leading half of the sort key, spelled as text: an entity
// listing's is the row's name, and ListRelations', whose rows are
// ordered by creation, is its created_at in RFC 3339. Text and not a
// typed union because a cursor is an opaque token whose only job is to
// come back unchanged, and the listing that issued it — pinned by the
// fingerprint — is the only code that has to know how to read it.
//
// Fingerprint is fingerprintOf over the *resolved* listing: the project
// id first, then which listing it is, then the filter — the entity type
// id, the invalid flag, and a traversal's relation type, anchor and
// direction. Resolved and not as spelled, so two spellings of one key
// give one fingerprint. The project id is first because without it two
// games' unfiltered listings shared a fingerprint and one game's cursor
// paged the other's rows from a position that meant nothing there:
// TestACursorFromAnotherGameIsRefused pins each of the three listings.
type cursor struct {
	Sort        string    `json:"n"`
	ID          uuid.UUID `json:"i"`
	Fingerprint string    `json:"f"`
}

func encodeCursor(c cursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		// Unreachable: every field is a string or a uuid.UUID, whose
		// MarshalJSON cannot fail. Returning no cursor rather than
		// panicking keeps a listing answerable if that ever stops being
		// true — a page without a cursor ends the listing, which is a
		// smaller lie than a page with an unreadable one.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// malformedCursor is what every unreadable cursor is reported as.
//
// invalid_input, not a bare error: the cursor is the caller's own
// argument, at a path, and the recovery is the caller's — page from a
// cursor a previous call returned, or omit it. Left untyped it reaches
// an agent as internal_error and reads as "the server is broken" over a
// value the agent itself supplied.
func malformedCursor(why string) error {
	return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: "cursor",
		Message: fmt.Sprintf(
			"is malformed (%s): page from the cursor a previous call returned, or omit it to start",
			why),
	}}}
}

// decodeCursor reads a page position back and checks it belongs to the
// listing it was handed to.
//
// The order of the two refusals matters: a cursor that is not readable at
// all is malformed, and only a readable one can be judged against the
// fingerprint. Reversed, a truncated cursor would be reported as
// belonging to another listing, which sends a caller looking at its
// filter instead of at the value it passed.
func decodeCursor(s, fingerprint string) (cursor, error) {
	if s == "" {
		return cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, malformedCursor("it is not the encoding this listing issues")
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return cursor{}, malformedCursor("it does not decode to a page position")
	}
	if c.ID == uuid.Nil {
		return cursor{}, malformedCursor("it carries no row position")
	}
	if c.Fingerprint != fingerprint {
		return cursor{}, &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
			Path: "cursor",
			Message: "was issued for a different listing: page with the filter the cursor came " +
				"from, or omit the cursor to start this listing over",
		}}}
	}
	return c, nil
}

// fingerprintOf digests the resolved filter a cursor was issued under.
//
// The parts are joined length-prefixed, for the reason foldedIdentity
// records: without it a filter whose parts run together spells the same
// string as a different filter whose parts divide elsewhere, and two
// listings would share one fingerprint. It hashes rather than storing the
// parts so the cursor does not grow with the filter, and truncates to 96
// bits because this is a consistency check against a caller's own
// mistake and not a signature — cursor's doc comment says why it cannot
// be one.
func fingerprintOf(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&b, "%d:%s", len(p), p)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

// invalidFilterPart spells the tri-state invalid flag for a fingerprint,
// keeping "no opinion" distinct from both of the opinions.
func invalidFilterPart(invalid *bool) string {
	if invalid == nil {
		return "any"
	}
	return fmt.Sprintf("%t", *invalid)
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
	var typeID *uuid.UUID
	typePart := ""
	if f.TypeKey != "" {
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
