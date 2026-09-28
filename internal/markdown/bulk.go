package markdown

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// This file is the document half of a bulk write. **The batch machinery
// itself is not here** — the two modes and what each promises, the
// per-item loop, the cancellation contract, the up-front repeated-
// identity check and the mapping from a domain error to a wire code all
// live in internal/metamodel/bulk.go, and this file supplies the five
// functions that file deliberately does not know (metamodel.BulkSpec).
type DocumentWrite struct {
	Path    string    `json:"path"`
	ID      uuid.UUID `json:"id"`
	Version int32     `json:"version"`
}

// WriteBulkResult is what a batch of writes answers with.
type WriteBulkResult struct {
	Succeeded []dbq.Document          `json:"-"`
	Written   []DocumentWrite         `json:"written"`
	Failed    []metamodel.BulkFailure `json:"failed"`
}

// WriteMany creates or updates a batch of documents in the requested
// mode.
func (s *Service) WriteMany(ctx context.Context, projectID uuid.UUID,
	items []WriteInput, mode metamodel.BulkMode,
) (WriteBulkResult, error) {
	written, failed, err := metamodel.BulkUpsert(
		ctx, s.withTx, items, mode, s.documentBulkSpec(projectID))
	var (
		rows    []dbq.Document
		reports []DocumentWrite
	)
	for _, w := range written {
		rows = append(rows, w.row)
		reports = append(reports, DocumentWrite{
			Path: w.row.Path, ID: w.row.ID, Version: w.row.CurrentVersion,
		})
	}
	return WriteBulkResult{Succeeded: rows, Written: reports, Failed: failed}, err
}

// documentBulkSpec is the document half of a bulk write: everything
// metamodel/bulk.go deliberately does not know.
func (s *Service) documentBulkSpec(projectID uuid.UUID) metamodel.BulkSpec[WriteInput, writtenDocument] {
	return metamodel.BulkSpec[WriteInput, writtenDocument]{
		Identity: func(in WriteInput) string {
			return metamodel.FoldedIdentity(in.Path)
		},
		Key: func(in WriteInput) string { return in.Path },
		Repeated: func(i, first int, in WriteInput) metamodel.FieldError {
			return metamodel.FieldError{
				Path: fmt.Sprintf("items[%d].path", i),
				Message: fmt.Sprintf(
					"%q is already addressed by item %d of this batch, and paths are matched "+
						"without regard to case: the two items are one document, so the second "+
						"would overwrite the first — merge them into one item, or give one of "+
						"the two a different path", in.Path, first),
			}
		},
		Write: func(ctx context.Context, q *dbq.Queries, in WriteInput) (writtenDocument, error) {
			// The per-item judgement runs here, inside the item's own
			// attempt, rather than over the whole batch beforehand: an
			// item with a bad path is that item's failure at that item's
			// index, and the rest of the batch is undisturbed. It is
			// Write's own checkWrite and not a second copy of it.
			content, err := checkWrite(in)
			if err != nil {
				return writtenDocument{}, err
			}
			return s.writeOneWith(ctx, q, projectID, in, content)
		},
		Publish: func(written writtenDocument) { s.publishWrite(projectID, written) },
	}
}
