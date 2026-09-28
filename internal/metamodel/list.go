package metamodel

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/paging"
)

// RelatedFilter is the single hop of traversal this sub-project offers.
// Transitive walks belong to the views query engine.
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
type EntityFilter struct {
	TypeKey string
	Invalid *bool
	// Prefix narrows a listing to the entities whose name starts with
	// it, case-insensitively. It is the one filter a person can compose
	// without a query language, and it costs nothing to page: the
	// listing is already ordered by name, so a prefix is a contiguous
	// stretch of that order rather than a scatter through it.
	Prefix string
	// Order is which way the listing is read: "name" (the default),
	// "key" or "updated", each with a leading "-" for the reverse. It is
	// part of the cursor's fingerprint, because a position in one order
	// means nothing in another — carried across, it would page
	// perfectly and return a stretch of rows nobody asked for.
	Order     string
	RelatedTo *RelatedFilter
	Cursor    string
	Limit     int32
}

// EntityPage is one page of results plus the cursor for the next. It is
// where every cursor in this package comes from, so the contract a
// caller has to know is written here — RelationPage's cursor obeys the
// same one, and so does the one a traversal issues.
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
type cursor = paging.Cursor

func pageSize(limit, def, max int32) int32 { return paging.Size(limit, def, max) }

func encodeCursor(c cursor) string { return paging.Encode(c) }

func fingerprintOf(parts ...string) string { return paging.Fingerprint(parts...) }

func invalidFilterPart(invalid *bool) string { return paging.TriState(invalid) }

// refuseCursor turns paging's message into this package's own error, at
// the argument's own path.
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
func (s *Service) ListEntities(ctx context.Context, projectID uuid.UUID, f EntityFilter) (EntityPage, error) {
	limit := pageSize(f.Limit, defaultEntityPage, maxEntityPage)

	// The type filter is resolved for both shapes of listing, so a
	// traversal narrowed by entity type answers the question it was
	// asked instead of dropping the clause.
	var typeID *uuid.UUID
	typePart := ""
	var typeSchema []byte
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
		typeSchema = typ.FieldSchema
	}

	order, err := parseEntityOrder(f.Order)
	if err != nil {
		return EntityPage{}, err
	}
	// **A field order is checked against the schema, not answered from
	// the rows.** An undeclared key is present in no row, so the listing
	// would come back in id order with every row's position absent —
	// sorted, plausible, and about nothing. The same rule the rest of
	// this package follows for a mistyped type key: a caller who wrote
	// `levl` hears about `levl`.
	if order.byField() {
		if err := checkFieldOrder(order.Field, f.TypeKey, typeSchema); err != nil {
			return EntityPage{}, err
		}
	}

	if f.RelatedTo != nil {
		if f.Order != "" {
			return EntityPage{}, &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
				Path:    "order",
				Message: "a listing narrowed by related_to is read in name order and cannot be reordered",
			}}}
		}
		return s.listRelated(ctx, projectID, *f.RelatedTo, typeID, typePart, f, limit)
	}

	// The prefix and the order are part of the fingerprint for the reason
	// the type and the invalid flag are: a cursor is a position *in one
	// listing*, and carrying it into a differently filtered one would
	// skip or repeat rows with nothing anywhere saying so. For the order
	// it is sharper still — a position in the name order carried into the
	// recency order compares a name against a timestamp, which is not an
	// error anywhere, just a page of rows that answers nothing. The
	// canonical spelling is what is fingerprinted, not the caller's, so
	// "name" and "" are one listing.
	fingerprint := fingerprintOf(projectID.String(), "entities", typePart,
		invalidFilterPart(f.Invalid), f.Prefix, order.String())
	after, err := decodeCursor(f.Cursor, fingerprint)
	if err != nil {
		return EntityPage{}, err
	}

	base := listingParams{
		ProjectID:    projectID,
		EntityTypeID: typeID,
		Invalid:      f.Invalid,
		Limit:        limit,
	}
	if f.Prefix != "" {
		prefix := f.Prefix
		base.Prefix = &prefix
	}

	rows, err := s.listEntitiesPage(ctx, order, base, after)
	if err != nil {
		return EntityPage{}, listingError(err)
	}
	return pageOf(rows, limit, fingerprint, order), nil
}

// listRelated resolves the one-hop filter and pages its answer.
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
	return pageOf(rows, limit, fingerprint, entityOrder{By: OrderByName}), nil
}

// pageOf wraps the rows a listing read, issuing a cursor when the page
// came back full. Both listings share it so that neither can end up
// issuing a cursor the other's decoder would refuse.
func pageOf(rows []dbq.Entity, limit int32, fingerprint string, order entityOrder) EntityPage {
	page := EntityPage{Entities: rows}
	// pageSize never returns a limit below one, so a full page is never
	// an empty one and there is no separate emptiness check to make.
	if len(rows) == int(limit) {
		last := rows[len(rows)-1]
		page.NextCursor = encodeCursor(cursor{
			// The position is the row's value in *this* order, which is
			// the half of a keyset a listing with more than one order
			// gets wrong first: a cursor carrying the name while the
			// statement compares keys pages from a position that is not
			// in the order being walked.
			Sort: order.sortValue(last), ID: last.ID, Fingerprint: fingerprint,
		})
	}
	return page
}

// The bounds this package's callers are allowed to state out loud.
const (
	// DefaultEntityPage and MaxEntityPage bound one page of ListEntities, in
	// both of its shapes: a one-hop traversal is paged by the same pageSize,
	// the same cursor and the same pageOf as the plain listing (see
	// listRelated), so these two numbers describe a page there too and not a
	// cap on the neighbour set. An earlier comment here, and the tool
	// description built from it, said the traversal returned up to
	// MaxEntityPage neighbours and stopped; review finding H1 caught it, and
	// TestListArea's "a traversal pages like every other listing" case had
	// already been pinning the truth.
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

// checkFieldOrder refuses the two ways an order by a declared field can
// be asked for and not answerable: without a type to declare it, and
// naming a field that type does not have.
func checkFieldOrder(field, typeKey string, schema []byte) error {
	if typeKey == "" {
		return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
			Path: "order",
			Message: "ordering by a field needs a type_key: a field belongs to one type's schema, " +
				"and the same name in two types is two different fields",
		}}}
	}
	parsed, err := ParseSchema(schema)
	if err != nil {
		return fmt.Errorf("read the type's schema: %w", err)
	}
	for _, declared := range parsed {
		if declared.Key == field {
			return nil
		}
	}
	return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path:    "order",
		Message: fmt.Sprintf("type %q declares no field %q", typeKey, field),
	}}}
}

// listingError keeps a refusal a refusal. The order dispatch reads a
// recency cursor's position back before the statement runs, so the one
// error that comes out of it that is not Postgres's is this package's
// own malformed-cursor refusal; wrapping that in "list entities: %w"
// would turn a caller's own bad argument into an internal_error.
func listingError(err error) error {
	var invalid *ValidationError
	if errors.As(err, &invalid) {
		return invalid
	}
	return fmt.Errorf("list entities: %w", err)
}
