package metamodel

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// This file is the other half of the schema-evolution rule.
//
// **The half that already existed is not revisited.** revalidate.go
// states it and Task 9 verified it holds: a type's field schema may be
// edited at any time, and the rows stored against it are neither
// rejected nor back-filled — they are re-checked and the ones that no
// longer fit are flagged. A validation pass is not an edit of a
// designer's content. That decision is right and this file changes
// nothing about it.
//
// **The half that was missing is what a designer does next.** Adding a
// required field to a type under two hundred rows flags all two hundred,
// correctly, and then nothing writable into the *type* makes them fit
// again: Schema.Check refuses `required` together with a `default`,
// correctly, because that pair is a self-contradiction. So the only
// recovery on the shipped surface was to rewrite two hundred rows one at
// a time, each carrying its own expected_version — and taking the field
// back out flagged all two hundred a *second* time, because the value
// they now hold has become an unknown field. Task 9's end-to-end test
// walks all four steps of that loop, because it is what the agent skill
// bundle would otherwise have to teach.
//
// **What a repair may do.**
//
//   - Read only rows the type's current schema rejects — `invalid = true`
//     and of the named type. Both statements behind it carry that
//     predicate.
//   - Write only `fields`. A row's key and name are read from the stored
//     row and handed straight back; an edge's endpoints likewise.
//   - Apply exactly two operations, both stated by the caller in this
//     call: drop the values the type no longer declares, and set the
//     values the caller names. Nothing else is derived, guessed or
//     defaulted by this file.
//   - Clear the invalid flag — by *re-validating*, never by fiat. Every
//     row goes back through the ordinary write path, so the flag comes
//     off for the one reason it ever comes off: the row now fits.
//   - Report every row it could not fix, at its own key, with the
//     schema's own complaint.
//
// **What a repair may not do, and how each is prevented.**
//
//   - It may not run as a side effect of a schema edit. UpsertEntityType
//     and UpsertRelationType call revalidate and nothing else; this
//     function is reachable only from a tool a designer invokes, with
//     values a designer chose. TestASchemaEditRepairsNothingByItself is
//     the standing check that the back door stays shut.
//   - It may not touch a valid row, which is what stops it becoming a
//     bulk content editor wearing a repair's name. The selection is the
//     enforcement.
//   - It may not accept per-row values. One `set` covers the whole
//     selection, because a repair is one decision about what a newly
//     required field means. Per-row values are entities.upsert, with the
//     version claim that belongs to editing content.
//   - It may not create a row, rename one, or move an edge's endpoints.
//   - It may not skip validation. There is no "mark these valid"
//     argument, and there is nowhere to add one: the flag is written by
//     the write path, from the schema.
//
// **It is not a new power.** A repair does exactly what an agent could
// already do with a listing and a batch of upserts — it is that loop,
// with one decision instead of two hundred round trips, run on the
// server. That is the property that keeps it from becoming a back-fill
// by the back door: there is no value it can put in a row that
// entities.upsert could not, and no row it can reach that a designer has
// not already been told is broken.

// The bounds on one repair pass.
//
// A pass is a listing and a batch in one call, so its ceiling is the
// batch's: MaxBulkItems, the same number every other batch on this
// surface answers to, since the rows it reads are the items it writes.
// The default is the relation listing's, deliberately in the middle:
// large enough that a two-hundred-row type is two calls, small enough
// that a first attempt against a type nobody has looked at does not
// rewrite five hundred rows before the caller has read a single failure.
const (
	defaultRepairBatch int32 = 100
	maxRepairBatch           = MaxBulkItems
)

// DefaultRepairBatch and MaxRepairBatch are the two numbers a repair
// pass answers to. Exported for the two tool descriptions built over
// them, for the reason MaxBulkItems is: a bound a caller cannot read is
// a bound a caller trips over.
const (
	DefaultRepairBatch = defaultRepairBatch
	MaxRepairBatch     = maxRepairBatch
)

// RepairInput is one repair pass over the flagged rows of one type.
//
// TypeKey names an entity type for RepairEntities and a relation type
// for RepairRelations; everything else means the same thing on both.
//
// Set is the values to write into every row the pass touches. Its keys
// must be declared by the type's current schema — a key that is not is
// refused up front rather than failing identically on every row.
//
// DropUnknown removes the values the type no longer declares. It is the
// answer to the second half of the four-step loop: taking a field back
// out of a schema flags every row that still carries its value, and
// there is no other way to write "forget this value" for a field the
// schema no longer has a name for.
//
// Limit bounds one pass. There is no cursor, and there does not need to
// be one: a repaired row leaves the selection, so calling again reads
// what the last call did not fix. A caller loops while Repaired is
// non-empty and stops when it is not — at which point Failed names
// everything left and why.
type RepairInput struct {
	TypeKey     string
	Set         map[string]any
	DropUnknown bool
	Limit       int32
	Actor       Actor
}

// RepairResult reports what one pass did.
//
// Scanned is how many flagged rows the pass read, which is what tells a
// caller whether the limit was the binding constraint: Scanned equal to
// the limit means there may be more behind it, exactly as a search's
// Truncated does.
//
// Repaired and Failed are the batch's own report, so a row that could
// not be made to fit comes back with the same index, key, code and
// message it would have come back with from entities.upsert. Index is
// the index within *this pass*, which is the only numbering that exists
// here — the caller sent no list.
type RepairResult struct {
	Scanned  int           `json:"scanned"`
	Repaired []BulkWrite   `json:"repaired"`
	Failed   []BulkFailure `json:"failed"`
}

// RelationRepairResult is RepairResult for edges, and differs only in
// what a written row is named by: an edge has no key, so it reports the
// RelationWrite the edge batch reports.
type RelationRepairResult struct {
	Scanned  int             `json:"scanned"`
	Repaired []RelationWrite `json:"repaired"`
	Failed   []BulkFailure   `json:"failed"`
}

// RepairEntities repairs the entities of one type that its schema
// rejects. See this file's header for what a repair may and may not do.
func (s *Service) RepairEntities(ctx context.Context, projectID uuid.UUID, in RepairInput) (RepairResult, error) {
	typ, err := s.EntityTypeByKey(ctx, projectID, in.TypeKey)
	if err != nil {
		return RepairResult{}, err
	}
	schema, err := ParseSchema(typ.FieldSchema)
	if err != nil {
		return RepairResult{}, err
	}
	if err := checkRepairRequest(schema, in); err != nil {
		return RepairResult{}, err
	}

	rows, err := s.q.ListInvalidEntitiesOfType(ctx, dbq.ListInvalidEntitiesOfTypeParams{
		ProjectID: projectID, EntityTypeID: typ.ID, Limit: repairBatchSize(in.Limit),
	})
	if err != nil {
		return RepairResult{}, fmt.Errorf("list flagged entities: %w", err)
	}

	items := make([]EntityInput, 0, len(rows))
	for _, row := range rows {
		version := row.Version
		items = append(items, EntityInput{
			TypeKey: typ.Key,
			// The stored key and the stored name, never a caller's. A
			// repair edits values; the row it writes back is the row it
			// read, with its fields changed.
			Key:             row.Key,
			Name:            row.Name,
			Fields:          repairedFields(schema, row.Fields, in),
			ExpectedVersion: &version,
			Actor:           in.Actor,
		})
	}

	// Partial, always. The whole point of a pass is that the rows one
	// `set` cannot fix are named rather than allowed to refuse the rows it
	// can — a designer narrowing a schema is repairing content that is
	// already known to be broken, and an all-or-nothing repair of broken
	// content would be a pass that never lands anything until it lands
	// everything.
	out, err := s.UpsertEntities(ctx, projectID, items, BulkPartial)
	return RepairResult{Scanned: len(rows), Repaired: out.Written, Failed: out.Failed}, err
}

// RepairRelations is RepairEntities for edges.
//
// It exists because 0009 gave relations a field schema's flag and a
// version, so a relation type's schema edit flags edges in exactly the
// way an entity type's edit flags entities — and a repair that covered
// only entities would be this repository's most repeated defect: a rule
// established at one of its two call sites. revalidate is one function
// for the same reason.
func (s *Service) RepairRelations(ctx context.Context, projectID uuid.UUID, in RepairInput) (RelationRepairResult, error) {
	typ, err := s.RelationTypeByKey(ctx, projectID, in.TypeKey)
	if err != nil {
		return RelationRepairResult{}, err
	}
	schema, err := ParseSchema(typ.FieldSchema)
	if err != nil {
		return RelationRepairResult{}, err
	}
	if err := checkRepairRequest(schema, in); err != nil {
		return RelationRepairResult{}, err
	}

	rows, err := s.q.ListInvalidRelationsOfType(ctx, dbq.ListInvalidRelationsOfTypeParams{
		ProjectID: projectID, RelationTypeID: typ.ID, Limit: repairBatchSize(in.Limit),
	})
	if err != nil {
		return RelationRepairResult{}, fmt.Errorf("list flagged relations: %w", err)
	}

	items := make([]RelationInput, 0, len(rows))
	for _, row := range rows {
		version := row.Version
		items = append(items, RelationInput{
			TypeKey:         typ.Key,
			Source:          Ref{TypeKey: row.SourceTypeKey, Key: row.SourceKey},
			Target:          Ref{TypeKey: row.TargetTypeKey, Key: row.TargetKey},
			Fields:          repairedFields(schema, row.Fields, in),
			ExpectedVersion: &version,
			Actor:           in.Actor,
		})
	}

	out, err := s.UpsertRelations(ctx, projectID, items, BulkPartial)
	return RelationRepairResult{Scanned: len(rows), Repaired: out.Written, Failed: out.Failed}, err
}

func repairBatchSize(limit int32) int32 {
	return pageSize(limit, defaultRepairBatch, maxRepairBatch)
}

// checkRepairRequest refuses a pass that cannot mean what it says, before
// it reads a row.
//
// **A pass that states no operation is refused rather than run.** With no
// `set` and no `drop_unknown` a repair rewrites every flagged row with
// the values it already holds, which either changes nothing or — where
// the schema has since grown a default — quietly back-fills that default
// into two hundred rows nobody asked about. Both readings are wrong: the
// first is a mechanism that does nothing, the second is the back-fill
// this whole design refuses. A caller that meant "just re-check them"
// wants a schema edit, which re-checks them for free.
//
// **A `set` key the type does not declare is refused up front**, because
// the alternative is a report in which every row failed with the same
// unknown-field complaint about an argument, not about content — a
// caller's own mistake diagnosed two hundred times as a property of the
// game's data.
func checkRepairRequest(schema Schema, in RepairInput) error {
	var problems []FieldError
	if len(in.Set) == 0 && !in.DropUnknown {
		problems = append(problems, FieldError{
			Path: "set",
			Message: "a repair must state what to change: give set, drop_unknown, or both. " +
				"A pass with neither would rewrite every flagged row with the values it " +
				"already holds",
		})
	}
	declared := make(map[string]bool, len(schema))
	for _, f := range schema {
		declared[f.Key] = true
	}
	// Sorted, so the message a caller sees is a function of the input
	// alone: map iteration order is randomised per range. Schema.Validate
	// sorts its unknown-key report for the same reason.
	for _, key := range slices.Sorted(maps.Keys(in.Set)) {
		if !declared[key] {
			problems = append(problems, FieldError{
				Path:    "set." + key,
				Message: "no such field on this type; a repair writes declared values only",
			})
		}
	}
	if len(problems) > 0 {
		return &ValidationError{Code: codeInvalidInput, Fields: problems}
	}
	return nil
}

// repairedFields is the whole of a repair's policy, in one function
// shared by both kinds.
//
// The order is drop-then-set, and it matters in exactly one case: a
// caller that both drops unknown values and sets a key the schema
// declares gets the set value, because the key is not unknown. A caller
// setting a key the schema does not declare never reaches here —
// checkRepairRequest refuses it.
//
// **A stored value that will not decode at all is repaired from an empty
// map rather than reported.** That is deliberate and it is the one place
// this function invents anything: the row holds jsonb that is not an
// object, so there is no value to preserve, and every readable reading
// of "repair this row" starts from nothing. Whatever the caller's `set`
// then supplies is judged by the write path like any other value, and a
// row that still does not fit comes back in Failed like any other. The
// alternative — refusing the row — would leave content that only a
// direct SQL edit could ever remove.
func repairedFields(schema Schema, stored []byte, in RepairInput) map[string]any {
	values, err := decodeFields(stored)
	if err != nil {
		values = map[string]any{}
	}
	if in.DropUnknown {
		declared := make(map[string]bool, len(schema))
		for _, f := range schema {
			declared[f.Key] = true
		}
		for key := range values {
			if !declared[key] {
				delete(values, key)
			}
		}
	}
	maps.Copy(values, in.Set)
	return values
}
