package metamodel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// This file holds the batch machinery every bulk write in this
// repository shares: the two modes and what each promises, the per-item loop and
// its cancellation contract, the up-front duplicate check, and the
// mapping from a domain error to a wire code.
type WithTx = func(context.Context, func(*dbq.Queries) error) error

// BulkMode decides how a batch behaves when one item fails.
type BulkMode string

// The two bulk modes.
const (
	// BulkPartial lands the valid items and reports the rest. Default,
	// because a first seeding pass always has a few bad rows and losing the
	// other four hundred helps nobody.
	BulkPartial BulkMode = "partial"
	// BulkAtomic runs the whole batch in one transaction.
	BulkAtomic BulkMode = "atomic"
)

// BulkFailure is one rejected item of a batch.
type BulkFailure struct {
	Index   int    `json:"index"`
	Key     string `json:"key"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BulkSpec is everything the shared batch driver cannot know about one
// kind of row: what a row of this kind is, how two items of a batch are
// recognised as the same row, what to say when they are, how one is
// written, and what to announce once it has landed.
type BulkSpec[In, Out any] struct {
	// Identity folds one item to the value the unique index would fold
	// it to. Two items sharing it are one row.
	Identity func(In) string
	// Key names the item in a failure report.
	Key func(In) string
	// Repeated is the problem to report for the item at index i, whose
	// row was already addressed by the item at index first.
	Repeated func(i, first int, in In) FieldError
	// Write performs one item against a queries handle that is already
	// inside a transaction, and publishes nothing.
	Write func(ctx context.Context, q *dbq.Queries, in In) (Out, error)
	// Publish announces one row that has landed. Called only after the
	// transaction that wrote it has committed.
	Publish func(Out)
}

// MaxBulkItems bounds, in items, one batch on this surface — every
// batch, of every kind, in either mode.
const MaxBulkItems = 500

// BulkUpsert runs a batch in the requested mode.
func BulkUpsert[In, Out any](
	ctx context.Context, withTx WithTx, items []In, mode BulkMode, spec BulkSpec[In, Out],
) ([]Out, []BulkFailure, error) {
	// Before the mode switch, so both modes and all three kinds — entities,
	// relations and internal/markdown's documents, which reach this driver
	// through the same call — are bounded by one check rather than by
	// three that can drift. This is also before any transaction is opened:
	// an over-large batch is the caller's own argument and is reported as
	// one, not as a slow success.
	if len(items) > MaxBulkItems {
		return nil, nil, &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
			Path: "items",
			Message: fmt.Sprintf("a batch carries at most %d items; this one carries %d — "+
				"split it", MaxBulkItems, len(items)),
		}}}
	}

	switch mode {
	case BulkAtomic:
		rows, err := bulkAtomic(ctx, withTx, items, spec)
		return rows, nil, err
	case BulkPartial, "":
		// The empty mode is the documented default. An omitted argument is
		// not a typo, and the spec names partial as the default.
		return bulkPartial(ctx, withTx, items, spec)
	default:
		// Anything else is refused rather than read as partial. Task 7
		// builds this value straight from an agent-supplied string, so a
		// typo would otherwise silently downgrade an all-or-nothing
		// request into one that lands rows the caller asked to have
		// rolled back — a failure the caller has no way of seeing.
		return nil, nil, &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
			Path:    "mode",
			Message: fmt.Sprintf("must be %q or %q", BulkPartial, BulkAtomic),
		}}}
	}
}

// bulkPartial writes each item in its own transaction and reports the
// ones that failed.
func bulkPartial[In, Out any](
	ctx context.Context, withTx WithTx, items []In, spec BulkSpec[In, Out],
) ([]Out, []BulkFailure, error) {
	repeats := repeatedIdentities(items, spec.Identity, spec.Repeated)
	var (
		rows   []Out
		failed []BulkFailure
	)
	for i, in := range items {
		// Before anything else this loop does with an item, including
		// the faults it can diagnose without touching the database.
		// Nothing else stops this loop: every item has its own
		// transaction, so a cancelled caller would otherwise turn a
		// 500-row batch into 500 failed round trips whose report nobody
		// is left to read, and the call would still return nil, which
		// reads as "the batch ran". The repeat check below used to run
		// first and `continue` without consulting the context at all, so
		// a batch whose trailing item was a repeat returned that nil
		// after the caller had gone — the exact failure this guard
		// exists to prevent, escaping through the one arm that never
		// asked. Whatever had already landed is returned with the error,
		// because rows landing before the cancellation is partial mode's
		// contract rather than a fact to hide.
		if err := ctx.Err(); err != nil {
			return rows, failed, fmt.Errorf("bulk upsert stopped at item %d: %w", i, err)
		}

		// A repeated row is the item's own fault and needs no round trip
		// to diagnose. In partial mode it fails alone: the first
		// occurrence is a perfectly good item, and refusing the batch
		// over it would throw away the other three hundred rows, which
		// is the failure this mode exists to prevent.
		if problem, ok := repeats[i]; ok {
			failed = append(failed, failureFor(ctx, i, spec.Key(in),
				&ValidationError{Code: codeInvalidInput, Fields: []FieldError{problem}}))
			continue
		}

		var written Out
		err := withTx(ctx, func(q *dbq.Queries) error {
			var err error
			written, err = spec.Write(ctx, q, in)
			return err
		})
		if err != nil {
			// The guard above only catches a cancellation that lands
			// *between* two items. The ordinary case is the other one:
			// the caller goes away while an item is in flight, that
			// item's own transaction fails with the cancellation, and
			// failureFor would file it as internal_error — the code
			// reserved for what nobody planned for. One cancellation
			// would then surface twice, once as this stop error and once
			// as a server fault against an item whose only problem was
			// that nobody was left to hear about it. The cancellation
			// can also land between the write and the commit, where the
			// two are indistinguishable from in here.
			if stopped := ctx.Err(); stopped != nil {
				return rows, failed, fmt.Errorf("bulk upsert stopped at item %d: %w", i, stopped)
			}
			failed = append(failed, failureFor(ctx, i, spec.Key(in), err))
			continue
		}
		rows = append(rows, written)
		spec.Publish(written)
	}
	return rows, failed, nil
}

// bulkAtomic writes the whole batch in one transaction, or none of it.
func bulkAtomic[In, Out any](
	ctx context.Context, withTx WithTx, items []In, spec BulkSpec[In, Out],
) ([]Out, error) {
	// Refused before anything is written, and refused whole. An atomic
	// batch that names one row twice cannot be satisfied as submitted —
	// the caller asked for n rows and at most n-1 can exist — and there
	// is no per-item answer to give in a mode that lands everything or
	// nothing. Doing it up front also makes the report deterministic:
	// left to the writes, a repetition surfaces as whichever conflict
	// the two items' versions happen to produce, and two items chaining
	// the versions the other will leave behind both commit, so the
	// result comes back carrying the same row id twice and the caller
	// is told two rows landed where one exists.
	if repeats := repeatedIdentities(items, spec.Identity, spec.Repeated); len(repeats) > 0 {
		fields := make([]FieldError, 0, len(repeats))
		for i := range items {
			if problem, ok := repeats[i]; ok {
				fields = append(fields, problem)
			}
		}
		return nil, &ValidationError{Code: codeInvalidInput, Fields: fields}
	}

	var rows []Out
	err := withTx(ctx, func(q *dbq.Queries) error {
		rows = nil
		for i, in := range items {
			written, err := spec.Write(ctx, q, in)
			if err != nil {
				// The index and the key name the item to fix, and %w keeps
				// the code — schema_violation, invalid_input, whichever —
				// matchable through the wrapping.
				return fmt.Errorf("item %d (%q): %w", i, spec.Key(in), err)
			}
			rows = append(rows, written)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, written := range rows {
		spec.Publish(written)
	}
	return rows, nil
}

// FoldedIdentity builds the string a BulkSpec.Identity returns from the
// parts of a row's identity, folding case as the unique indexes over
// these keys do (all of them are UNIQUE (…, lower(key)), and
// strings.ToLower is exact for them because rowKeyPattern admits ASCII
// only).
func FoldedIdentity(parts ...string) string {
	var b strings.Builder
	for _, part := range parts {
		part = strings.ToLower(part)
		fmt.Fprintf(&b, "%d:%s", len(part), part)
	}
	return b.String()
}

// repeatedIdentities finds the items of a batch that address a row an
// earlier item already addressed, keyed by index and carrying the
// problem to report.
func repeatedIdentities[In any](
	items []In, identity func(In) string, repeated func(i, first int, in In) FieldError,
) map[int]FieldError {
	first := make(map[string]int, len(items))
	var repeats map[int]FieldError
	for i, in := range items {
		id := identity(in)
		if at, seen := first[id]; seen {
			if repeats == nil {
				repeats = make(map[int]FieldError)
			}
			repeats[i] = repeated(i, at, in)
			continue
		}
		first[id] = i
	}
	return repeats
}

// failureFor maps a domain error to the wire shape of a bulk failure.
const (
	retryableFailureMessage = "the database refused this item over contention; send it again, " +
		"and send fewer rows at a time if a batch keeps producing these"
	internalFailureMessage = "internal error"
)

// It takes a context only to log those two arms. Withholding a message
// from the caller must not lose it: `mcpErrorFor` pairs the same
// withholding with a `slog` line so an operator seeing a run of these
// knows which lock or which broken statement, and a batch failure that
// vanished into a fixed string with nothing written down would be
// strictly worse than the leak it replaced.
func failureFor(ctx context.Context, index int, key string, err error) BulkFailure {
	f := BulkFailure{Index: index, Key: key, Message: err.Error()}
	switch {
	case errors.Is(err, ErrInvalidInput):
		f.Code = "invalid_input"
	case errors.Is(err, ErrSchemaViolation):
		f.Code = "schema_violation"
	case errors.Is(err, ErrInvalidSchema):
		f.Code = "invalid_schema"
	case errors.Is(err, ErrVersionConflict):
		f.Code = "version_conflict"
	case errors.Is(err, ErrNotFound):
		f.Code = "not_found"
	case errors.Is(err, ErrEndpointTypeMismatch):
		f.Code = "endpoint_type_mismatch"
	case errors.Is(err, ErrInUse):
		f.Code = "in_use"
	case IsRetryable(err):
		f.Code = "retryable"
		f.Message = retryableFailureMessage
		slog.WarnContext(ctx, "bulk item hit database contention",
			"index", index, "key", key, "error", err)
	default:
		f.Code = "internal_error"
		f.Message = internalFailureMessage
		slog.ErrorContext(ctx, "bulk item failed", "index", index, "key", key, "error", err)
	}
	return f
}
