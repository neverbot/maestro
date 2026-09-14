package metamodel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// The orders an entity listing can be read in, and the whole vocabulary
// a caller may spell.
//
// A descending order is the ascending one with a leading minus, which is
// the spelling every listing API that has ever had to grow a second
// direction converges on and which keeps one parameter where two would
// otherwise be — an `order` and a `direction` that can disagree, and a
// cursor that then has to carry both.
//
// **There is no order by a declared field here, and that absence is the
// open half of this feature.** A catalogue column holding `level` sorts
// numerically only if the ordering knows the field's declared type, and
// jsonb ordering by a typed field is a query per type, not a fourth
// constant. What ships here is the three columns every entity has.
const (
	OrderByName    = "name"
	OrderByKey     = "key"
	OrderByUpdated = "updated"
)

// entityOrders is the parse table and the error message's own source, so
// a spelling this package accepts and a spelling it names in a refusal
// cannot drift apart.
var entityOrders = []string{OrderByName, OrderByKey, OrderByUpdated}

// entityOrder is a parsed order: which column, and which way.
type entityOrder struct {
	By         string
	Descending bool
}

// String is the canonical spelling, which is what the cursor's
// fingerprint is taken over. It is derived rather than the caller's own
// text so that a listing and the cursor it issued agree on the order
// even if a caller re-spells it.
func (o entityOrder) String() string {
	if o.Descending {
		return "-" + o.By
	}
	return o.By
}

// parseEntityOrder reads the caller's spelling.
//
// **An unrecognised order is refused, not defaulted**, for the reason
// listRelated refuses an unrecognised direction: a listing that quietly
// ordered by name when asked for `-lvl` would answer a question nobody
// asked and look like it worked. The message names every spelling there
// is, because the caller cannot see this table.
func parseEntityOrder(spelling string) (entityOrder, error) {
	if spelling == "" {
		return entityOrder{By: OrderByName}, nil
	}
	order := entityOrder{By: strings.TrimPrefix(spelling, "-"), Descending: strings.HasPrefix(spelling, "-")}
	for _, known := range entityOrders {
		if order.By == known {
			return order, nil
		}
	}
	return entityOrder{}, &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: "order",
		Message: fmt.Sprintf("must be one of %s, each optionally with a leading %q for the "+
			"reverse (%q), or omitted for %s",
			strings.Join(quoted(entityOrders), ", "), "-", "-"+OrderByUpdated, OrderByName),
	}}}
}

func quoted(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, fmt.Sprintf("%q", value))
	}
	return out
}

// sortValue is the cursor position for one row under one order, and it
// is the counterpart of applyAfter below: the two must read and write
// the same column, because a keyset whose position comes from one column
// and whose comparison is made on another pages nowhere.
func (o entityOrder) sortValue(row dbq.Entity) string {
	switch o.By {
	case OrderByKey:
		return row.Key
	case OrderByUpdated:
		// RFC 3339 with nanoseconds, as the relation listing's own
		// created_at cursor is: two entities written in the same second
		// are ordinary in a bulk write, and a second-resolution position
		// would put a page boundary inside a tie.
		return row.UpdatedAt.Time.Format(time.RFC3339Nano)
	default:
		return row.Name
	}
}

// listEntitiesPage runs the statement this order is served by.
//
// One statement per order, chosen here, rather than one statement taking
// the order as a parameter: the SQL file's own comment says why, and it
// is not style — an order chosen at run time inside the statement is an
// order Postgres cannot serve from an index, and the whole point of
// these indexes is that a page is sought to rather than sorted for.
//
// The switch is exhaustive over a vocabulary parseEntityOrder has
// already bounded, so the default arm is the ascending name listing
// every caller that never asks for an order gets.
func (s *Service) listEntitiesPage(ctx context.Context, o entityOrder, base listingParams, after cursor) ([]dbq.Entity, error) {
	switch {
	case o.By == OrderByKey && !o.Descending:
		params := dbq.ListEntitiesPageByKeyParams{
			ProjectID: base.ProjectID, EntityTypeID: base.EntityTypeID, Invalid: base.Invalid,
			Prefix: base.Prefix, Limit: base.Limit,
		}
		if after.ID != uuid.Nil {
			params.AfterID = &after.ID
			params.AfterKey = &after.Sort
		}
		return s.q.ListEntitiesPageByKey(ctx, params)
	case o.By == OrderByKey:
		params := dbq.ListEntitiesPageByKeyDescParams{
			ProjectID: base.ProjectID, EntityTypeID: base.EntityTypeID, Invalid: base.Invalid,
			Prefix: base.Prefix, Limit: base.Limit,
		}
		if after.ID != uuid.Nil {
			params.AfterID = &after.ID
			params.AfterKey = &after.Sort
		}
		return s.q.ListEntitiesPageByKeyDesc(ctx, params)
	case o.By == OrderByUpdated && !o.Descending:
		at, err := sortTime(after)
		if err != nil {
			return nil, err
		}
		params := dbq.ListEntitiesPageByUpdatedParams{
			ProjectID: base.ProjectID, EntityTypeID: base.EntityTypeID, Invalid: base.Invalid,
			Prefix: base.Prefix, Limit: base.Limit,
		}
		if after.ID != uuid.Nil {
			params.AfterID = &after.ID
			params.AfterUpdated = at
		}
		return s.q.ListEntitiesPageByUpdated(ctx, params)
	case o.By == OrderByUpdated:
		at, err := sortTime(after)
		if err != nil {
			return nil, err
		}
		params := dbq.ListEntitiesPageByUpdatedDescParams{
			ProjectID: base.ProjectID, EntityTypeID: base.EntityTypeID, Invalid: base.Invalid,
			Prefix: base.Prefix, Limit: base.Limit,
		}
		if after.ID != uuid.Nil {
			params.AfterID = &after.ID
			params.AfterUpdated = at
		}
		return s.q.ListEntitiesPageByUpdatedDesc(ctx, params)
	case o.Descending:
		params := dbq.ListEntitiesPageNameDescParams{
			ProjectID: base.ProjectID, EntityTypeID: base.EntityTypeID, Invalid: base.Invalid,
			Prefix: base.Prefix, Limit: base.Limit,
		}
		if after.ID != uuid.Nil {
			params.AfterID = &after.ID
			params.AfterName = &after.Sort
		}
		return s.q.ListEntitiesPageNameDesc(ctx, params)
	default:
		params := dbq.ListEntitiesPageParams{
			ProjectID: base.ProjectID, EntityTypeID: base.EntityTypeID, Invalid: base.Invalid,
			Prefix: base.Prefix, Limit: base.Limit,
		}
		if after.ID != uuid.Nil {
			params.AfterID = &after.ID
			params.AfterName = &after.Sort
		}
		return s.q.ListEntitiesPage(ctx, params)
	}
}

// listingParams is the half of a listing that no order changes: the
// game, the filters and the page size. It exists so the six calls above
// state only what differs between them.
type listingParams struct {
	ProjectID    uuid.UUID
	EntityTypeID *uuid.UUID
	Invalid      *bool
	Prefix       *string
	Limit        int32
}

// sortTime reads a recency cursor's position back.
//
// Only a hand-edited cursor reaches the refusal: the fingerprint has
// already agreed, and this package writes the format it reads. It is
// still the caller's own argument, so it is answered as one — the same
// arm, and the same sentence, ListRelations has.
func sortTime(after cursor) (pgtype.Timestamptz, error) {
	if after.ID == uuid.Nil {
		return pgtype.Timestamptz{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, after.Sort)
	if err != nil {
		return pgtype.Timestamptz{}, malformedCursor("it carries no time")
	}
	return pgtype.Timestamptz{Time: at, Valid: true}, nil
}
