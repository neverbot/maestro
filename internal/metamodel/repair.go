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
type RepairInput struct {
	TypeKey     string
	Set         map[string]any
	DropUnknown bool
	Limit       int32
	Actor       Actor
}

// RepairResult reports what one pass did.
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
