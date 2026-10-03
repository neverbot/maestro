package metamodel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// The orders an entity listing can be read in, and the whole vocabulary
// a caller may spell.
const (
	OrderByName    = "name"
	OrderByKey     = "key"
	OrderByUpdated = "updated"
	// OrderByFieldPrefix leads a field order: "field:level".
	OrderByFieldPrefix = "field:"
)

// entityOrders is the parse table and the error message's own source, so
// a spelling this package accepts and a spelling it names in a refusal
// cannot drift apart.
var entityOrders = []string{OrderByName, OrderByKey, OrderByUpdated}

// entityOrder is a parsed order: which column, which way, and — for a
// field order — which field.
type entityOrder struct {
	By         string
	Field      string
	Descending bool
}

// byField reports whether this is an order by a declared field, which is
// the one shape that needs a type, a schema check and a statement of its
// own.
func (o entityOrder) byField() bool { return o.Field != "" }

// String is the canonical spelling, which is what the cursor's
// fingerprint is taken over. It is derived rather than the caller's own
// text so that a listing and the cursor it issued agree on the order
// even if a caller re-spells it.
func (o entityOrder) String() string {
	spelling := o.By
	if o.byField() {
		spelling = OrderByFieldPrefix + o.Field
	}
	if o.Descending {
		return "-" + spelling
	}
	return spelling
}

// parseEntityOrder reads the caller's spelling.
func parseEntityOrder(spelling string) (entityOrder, error) {
	if spelling == "" {
		return entityOrder{By: OrderByName}, nil
	}
	order := entityOrder{By: strings.TrimPrefix(spelling, "-"), Descending: strings.HasPrefix(spelling, "-")}
	if after, found := strings.CutPrefix(order.By, OrderByFieldPrefix); found {
		// The field key is bounded here, by the same rule a key is
		// checked against before it is ever a row: it reaches SQL as a
		// jsonb path and an invalid UTF-8 byte in it would surface as
		// SQLSTATE 22021 over a value the caller supplied.
		if problems := rowKeyProblems("order", after); len(problems) > 0 {
			return entityOrder{}, &ValidationError{Code: codeInvalidInput, Fields: problems}
		}
		order.By = OrderByFieldPrefix
		order.Field = after
		return order, nil
	}
	for _, known := range entityOrders {
		if order.By == known {
			return order, nil
		}
	}
	return entityOrder{}, &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: "order",
		Message: fmt.Sprintf("must be one of %s, or %q naming a field declared by the type "+
			"this listing is filtered to; each optionally with a leading %q for the reverse "+
			"(%q), or omitted for %s",
			strings.Join(quoted(entityOrders), ", "), OrderByFieldPrefix+"<key>", "-",
			"-"+OrderByUpdated, OrderByName),
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
	if o.byField() {
		// The jsonb value as it is stored, or the empty string for a row
		// that has no such field — which is the signal the two keyset
		// arms in the SQL are chosen by. No jsonb value serialises to an
		// empty string, so absent and present cannot be confused.
		return fieldValueText(row.Fields, o.Field)
	}
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
func (s *Service) listEntitiesPage(ctx context.Context, o entityOrder, base listingParams, after cursor) ([]dbq.Entity, error) {
	switch {
	case o.byField():
		params := dbq.ListEntitiesPageByFieldParams{
			ProjectID: base.ProjectID, EntityTypeID: base.EntityTypeID, Invalid: base.Invalid,
			Prefix: base.Prefix, Contains: base.Contains, Field: o.Field, Limit: base.Limit,
		}
		if after.ID != uuid.Nil {
			params.AfterID = &after.ID
			if after.Sort != "" {
				params.AfterValue = []byte(after.Sort)
			}
		}
		if !o.Descending {
			return s.q.ListEntitiesPageByField(ctx, params)
		}
		return s.q.ListEntitiesPageByFieldDesc(ctx, dbq.ListEntitiesPageByFieldDescParams(params))
	case o.By == OrderByKey && !o.Descending:
		params := dbq.ListEntitiesPageByKeyParams{
			ProjectID: base.ProjectID, EntityTypeID: base.EntityTypeID, Invalid: base.Invalid,
			Prefix: base.Prefix, Contains: base.Contains, Limit: base.Limit,
		}
		if after.ID != uuid.Nil {
			params.AfterID = &after.ID
			params.AfterKey = &after.Sort
		}
		return s.q.ListEntitiesPageByKey(ctx, params)
	case o.By == OrderByKey:
		params := dbq.ListEntitiesPageByKeyDescParams{
			ProjectID: base.ProjectID, EntityTypeID: base.EntityTypeID, Invalid: base.Invalid,
			Prefix: base.Prefix, Contains: base.Contains, Limit: base.Limit,
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
			Prefix: base.Prefix, Contains: base.Contains, Limit: base.Limit,
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
			Prefix: base.Prefix, Contains: base.Contains, Limit: base.Limit,
		}
		if after.ID != uuid.Nil {
			params.AfterID = &after.ID
			params.AfterUpdated = at
		}
		return s.q.ListEntitiesPageByUpdatedDesc(ctx, params)
	case o.Descending:
		params := dbq.ListEntitiesPageNameDescParams{
			ProjectID: base.ProjectID, EntityTypeID: base.EntityTypeID, Invalid: base.Invalid,
			Prefix: base.Prefix, Contains: base.Contains, Limit: base.Limit,
		}
		if after.ID != uuid.Nil {
			params.AfterID = &after.ID
			params.AfterName = &after.Sort
		}
		return s.q.ListEntitiesPageNameDesc(ctx, params)
	default:
		params := dbq.ListEntitiesPageParams{
			ProjectID: base.ProjectID, EntityTypeID: base.EntityTypeID, Invalid: base.Invalid,
			Prefix: base.Prefix, Contains: base.Contains, Limit: base.Limit,
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
	Contains     *string
	Limit        int32
}

// sortTime reads a recency cursor's position back.
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

// fieldValueText is the stored jsonb of one field, as text, or "" when
// the row does not carry that field at all.
func fieldValueText(raw []byte, key string) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ""
	}
	value, ok := fields[key]
	if !ok {
		return ""
	}
	return string(value)
}
