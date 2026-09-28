package metamodel

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// storedFields is one row a sweep judges: the values it holds and the id
// to flag. Nothing else is read, which is why both listings behind this
// select two columns rather than whole rows — a type can hold thousands
// of instances and every schema edit sweeps all of them.
type storedFields struct {
	ID     uuid.UUID
	Fields []byte
}

// sweep is one re-validation pass, in the terms the rule needs and no
// others: the schema to judge against, where the rows come from, where
// the verdicts go, and what to call the rows when something fails.
type sweep struct {
	// fieldSchema is the type's raw declaration, parsed here rather than
	// by the caller so that a malformed one is refused before either
	// query runs.
	fieldSchema []byte
	// subject is the plural noun the two error messages use: "entities"
	// or "relations".
	subject string
	list    func(context.Context) ([]storedFields, error)
	mark    func(ctx context.Context, ids []uuid.UUID, invalid bool) error
}

// revalidate is the schema-evolution rule, written once.
func revalidate(ctx context.Context, sw sweep) error {
	schema, err := ParseSchema(sw.fieldSchema)
	if err != nil {
		return err
	}

	rows, err := sw.list(ctx)
	if err != nil {
		return fmt.Errorf("list %s: %w", sw.subject, err)
	}

	var invalid, valid []uuid.UUID
	for _, row := range rows {
		values, err := decodeFields(row.Fields)
		if err != nil {
			invalid = append(invalid, row.ID)
			continue
		}
		if err := schema.CheckValues(values); err != nil {
			invalid = append(invalid, row.ID)
			continue
		}
		valid = append(valid, row.ID)
	}

	for _, batch := range []struct {
		ids  []uuid.UUID
		flag bool
	}{{invalid, true}, {valid, false}} {
		if len(batch.ids) == 0 {
			continue
		}
		if err := sw.mark(ctx, batch.ids, batch.flag); err != nil {
			return fmt.Errorf("flag %s: %w", sw.subject, err)
		}
	}
	return nil
}

// storedFieldsOf adapts a generated listing's rows onto the shape a sweep
// judges. sqlc names a row type per statement, so the two listings behind
// revalidate have two structurally identical Go types and no common one;
// this is the one line that costs.
func storedFieldsOf[R any](rows []R, of func(R) storedFields) []storedFields {
	out := make([]storedFields, 0, len(rows))
	for _, row := range rows {
		out = append(out, of(row))
	}
	return out
}
