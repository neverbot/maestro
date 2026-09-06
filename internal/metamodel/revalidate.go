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
//
// It is a struct of closures rather than an interface because the two
// implementations are two generated query methods bound to one
// transaction handle, and an interface would need a type per call site to
// carry that handle.
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
//
// **The rule.** A type's field schema may be edited at any time, and the
// rows already stored against it are neither rejected nor back-filled:
// they are re-checked, and the ones that no longer fit are flagged. The
// designer decides what a newly required field should hold, and a
// validation pass is not an edit of their content. The core design states
// it for entities; since 0009 it is true of relations too, and the reason
// this function exists rather than a second copy of the loop is that this
// repository's most repeated defect is a rule written once, copied, and
// then fixed in one copy.
//
// **CheckValues, never Validate — an intent, not a behaviour.**
// CheckValues *is* Validate with the map discarded (validate.go), so the
// two return the same verdict on every input and no test can tell this
// sweep's call from the write path's. What the narrower call earns is
// that there is no normalised map in scope to write back: Validate hands
// one back with declared defaults injected, and a later edit that stored
// it would back-fill every row the sweep touched, silently, with values
// no designer chose. TestSchemaChangeDoesNotBackFillDeclaredDefaults and
// TestAnEdgeSchemaChangeDoesNotBackFillDeclaredDefaults pin the outcome
// on each table; this line is what keeps the temptation out of reach.
//
// **A row whose stored jsonb will not decode at all is flagged, not
// returned as an error.** It is content in the database that does not fit
// what the type says, which is exactly what the flag means, and a sweep
// that failed on it would refuse the schema edit — the one outcome this
// rule exists to avoid.
//
// **Both verdicts are written, not only the failures.** A schema edit
// that *widens* a type has to clear the flag on rows it has just made
// legal again, or a designer's list of things to fix never empties. The
// two batches are one loop over a table of (ids, flag) so that neither
// arm can be added to without the other; the statements behind mark carry
// an `invalid <> flag` guard, so a row whose verdict has not changed is
// not rewritten and its updated_at does not move.
//
// **There is no bound on how many rows a sweep reads**, on either table,
// and that is stated rather than hidden: the listings carry no LIMIT, so
// a type with a hundred thousand instances is swept in one pass inside
// the schema edit's own transaction. It is the same shape the entity
// sweep has had since it shipped, and whoever bounds one bounds both —
// which is the point of there being one function to bound.
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
