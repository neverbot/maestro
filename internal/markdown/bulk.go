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
//
// That is not a preference. A second copy of that loop would arrive with
// its own duplicate detector, its own context guard and its own copy of
// every finding two review rounds closed over there, to be fixed twice
// from then on — the single most repeated defect in this repository. It
// works unchanged for prose because this package's sentinels are
// *aliases* of the metamodel's (errors.go), so failureFor's existing
// arms code a document's version conflict `version_conflict` and a
// missing entity `not_found` without knowing documents exist.

// DocumentWrite is one document a batch landed: the path the caller
// addressed it by, the row's id, and the version the write produced.
//
// **Version is what makes the report usable rather than decorative.**
// Every write in this domain is a claim about a version, so an agent
// seeding sixteen documents and then editing one of them needs the
// number this call produced; without it the follow-up edit costs a read
// per document, which is the round-trip cost the batch existed to
// remove. It is metamodel.BulkWrite's shape applied to a document, with
// the path standing in for (type_key, key).
//
// It carries no body, no title and no frontmatter: a batch of four
// hundred documents echoing its own input back is the largest payload
// this server could produce, and the caller already has every byte of
// it. A caller that wants the stored document reads it.
type DocumentWrite struct {
	Path    string    `json:"path"`
	ID      uuid.UUID `json:"id"`
	Version int32     `json:"version"`
}

// WriteBulkResult is what a batch of writes answers with.
//
// Succeeded is the rows themselves, for a caller inside this process,
// and is `json:"-"` for the reason metamodel.BulkResult's is: it carries
// bodies, and the wire answer is Written.
type WriteBulkResult struct {
	Succeeded []dbq.Document          `json:"-"`
	Written   []DocumentWrite         `json:"written"`
	Failed    []metamodel.BulkFailure `json:"failed"`
}

// WriteMany creates or updates a batch of documents in the requested
// mode.
//
// **Every item carries its own expected_version, and that is the whole
// shape of this call: a batch is a list of claims about versions, not a
// list of rows.** A single Write already requires one (WriteInput's own
// comment argues why 0 spells a create and an absent one is refused),
// and a batch makes the requirement *more* load-bearing rather than
// less. There is no version a batch could share — sixteen documents are
// at sixteen versions — so an optional one would have to mean "whatever
// is there", which is exactly the silent overwrite the single-document
// rule exists to prevent, multiplied by the batch size. An item that
// omits it fails alone, at `expected_version`, with the other fifteen
// landing.
//
// **A version conflict is reported by index, like a schema violation,
// and this is decided rather than inherited.** The honest reading is
// that it is per-item and per-item fixable — the caller re-reads that
// one document, merges, and re-sends that one item, while the other
// items' claims were true and their rows are stored — which is the same
// shape as a bad path or a malformed frontmatter block. The argument
// against is that a conflict is not a fault in the caller's argument but
// the world having moved, so a batch that meets one is working from a
// stale picture and should be refused whole. That argument is real, and
// it is what BulkAtomic is for: a caller who wants "all my claims or
// none" says so and gets one transaction. Making partial mode behave
// that way would leave no mode that lands the good rows, which is the
// mode a seeding pass actually needs — "a first seeding pass always has
// a few bad rows and losing the other four hundred helps nobody"
// (metamodel.BulkPartial).
//
// **What a batched conflict does not carry is the current body**, and
// that is a property of the shape rather than a choice made here. A
// batch failure is a metamodel.BulkFailure — an index, a key, a code and
// a message — with nowhere for a ConflictError's Details to travel, so
// the caller is told the version to merge onto and not the prose to
// merge. Nothing here reads WriteInput.IncludeCurrent and nothing sets
// it: the batch tool does not offer the argument, because an argument
// that changes nothing is worse than an absent one. Four hundred
// conflicts echoing four hundred bodies would be the largest answer this
// server could produce, so this is the right way round — and it is why
// docs.write is not deprecated by this call: merging prose is a
// one-document job.
//
// The rest — partial versus atomic, the cancellation contract, a path
// repeated inside one batch — is metamodel.BulkUpsert's and is not
// restated here.
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
//
// **Identity is the path and nothing else, folded**, because that is
// what documents' unique index folds — `UNIQUE (project_id,
// lower(path))` in 0007_documents.sql — and metamodel.FoldedIdentity is
// what does the folding, so the Go-side fold and the SQL one cannot
// disagree. Its length prefix is redundant for a single part and is
// used anyway rather than calling strings.ToLower here: a second way of
// spelling "the identity of a row" is how the two folds drift apart.
// The fold is exact for a path because CheckPath admits ASCII only, the
// same premise the entity and relation keys rest on.
//
// **Key is the path too, and that is not the same claim.** Identity is
// what the index folds; Key is what a failure report names so a caller
// can find the item it sent, and it is the caller's own spelling rather
// than the folded one — a caller that sent `Lore/Hogger.md` is told
// about `Lore/Hogger.md`.
//
// Repeated cannot reuse the entity wording: a document has no key
// separate from its address, so "give one of the two a different key" is
// advice about a field that does not exist, and the two items are one
// document whose second write would silently overwrite the first.
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
