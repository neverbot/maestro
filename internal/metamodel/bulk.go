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
//
// It was extracted from entities.go, unchanged, before relations were
// written. Everything in here was hardened by two review rounds — the
// cancellation guard alone accounts for three of Task 4's corrections —
// and all of it was entity-shaped: a second copy would have arrived with
// relations carrying its own duplicate detector, its own context guard
// and its own copy of every finding those rounds closed, to be reviewed
// and fixed twice from then on. What actually differs between the two is
// small and named in BulkSpec: what identifies a row, what a repetition
// of it should say, and how one item is written.
//
// **It is exported for the third kind.** internal/markdown's docs.write
// batch is a document-shaped BulkSpec and nothing else: it inherits the
// two modes, the cancellation contract, the repeated-identity refusal
// and the wire-code mapping rather than restating any of them. That is
// also why failureFor's arms are the *shared* vocabulary — markdown's
// sentinels are aliases of this package's (internal/markdown/errors.go),
// so a document's version conflict is coded version_conflict by the same
// switch that codes an entity's.

// WithTx is the one thing the batch driver needs from a domain service:
// a way to run a function inside a transaction, rolling back unless it
// returns nil.
//
// It is a function and not a *Service because the driver is shared by
// two packages now rather than by two files of one. internal/markdown
// declares a withTx of exactly this shape (its own, over its own pool),
// and internal/projects and internal/identity each declare a third and a
// fourth; passing the method value is what lets a second domain reuse
// this loop instead of growing a copy of it, which is what happened to
// the keyset cursor before internal/paging existed.
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
//
// Index and Key are what let a caller retry only what failed: an agent
// seeding four hundred rows re-sends the handful named here rather than
// the batch. Message is the item's own error, and what it holds depends
// on the fault: a field path and a rule where the item's own arguments
// or values are wrong (`schema_violation: fields.min_level: expected
// number, got string`), and otherwise the plain reason the item was
// refused, which may name nothing of the item at all (`not_found: no
// entity type "quest" in this game`). What it never holds is another
// item's values: a batch report is the one place a row's content could
// leak into a neighbour's error, and
// TestBulkPartialLandsTheGoodRowsAndReportsTheRest pins that it does
// not.
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
//
// The three identity-shaped fields are separate on purpose. Identity is
// what the *database's* unique index folds on, and it differs per kind —
// (type key, key) for entities, (relation type, source, target) for
// relations, the path for a document — while Key is only what a failure
// report names, so that a caller can find the item it sent. Repeated
// writes the message, because
// the advice a caller needs differs with what identity means: "give one
// of the two a different key" says nothing useful about a pair of edges
// that share their endpoints.
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
//
// It is exported because a bound a caller cannot read is a bound a
// caller trips over, which is the same argument MaxSearchQuery makes:
// the three tool descriptions built over this driver state the number,
// so an agent composing a batch knows the edge before it hits it.
//
// **What made a bound necessary.** Task 9 sent a 5,000-item atomic
// batch and it was accepted: one transaction held open for 3.1 seconds,
// answering with 515 KB. Nothing refused it, nothing warned about it,
// and the caller's only signal that it had asked for something the
// server should not do was how long the answer took. An atomic batch is
// one transaction by construction, so its size is directly how long
// every other writer against those rows waits — and it is the one
// caller-supplied bound on this surface that Postgres was left to
// discover. Every other one is checked in Go before the database sees
// it: the search query at 4 KiB (MaxSearchQuery), a page limit clamped
// to its cap (paging.Size), the request body at 4 MiB (maxBodyBytes).
// This is that rule carried to the last place it was missing.
//
// **500, because that is already this surface's answer to "the most
// rows one call moves".** MaxEntityPage and MaxRelationPage are both
// 500; an agent that can read 500 rows in a call can write 500 in a
// call, and one number is one thing to learn. It is also comfortably
// above every batch this repository's own end-to-end seeding sends
// (200), so the bound refuses nothing a real seeding run does.
//
// **It is refused, not clamped**, which is the opposite of what a page
// limit gets and deliberately so. paging.Size clamps because a caller
// asking for more rows than it may have still has a correct answer —
// the cap's worth of rows, plus a cursor for the rest. A batch has no
// such answer: silently writing the first 500 items of 5,000 and
// reporting success would leave 4,500 rows unwritten with nothing in
// the result saying so, and silently writing all of them is the
// behaviour this bound exists to stop. So it is invalid_input at path
// `items`, naming both numbers, and the caller splits.
const MaxBulkItems = 500

// BulkUpsert runs a batch in the requested mode.
//
// **The partial-failure contract, which is the whole point of the two
// modes.** In BulkPartial every item is its own transaction: item 200
// landing does not depend on item 3, the rows that fit are stored, and
// the ones that do not come back with their index, their key and the
// code that says how to fix them. The caller retries the named items and
// nothing else. In BulkAtomic one bad row rolls the whole batch back and
// the call returns an error naming the item that failed; nothing is
// reported as done, because nothing was.
//
// A partial batch therefore returns a nil error even when items failed —
// the failures are the result, not an error — and an atomic batch returns
// an error with an empty result. The one case that returns both is a
// cancelled context: see bulkPartial.
//
// **A key repeated inside one batch** is refused by repeatedIdentities
// before it can be misdiagnosed, and the two modes answer it differently
// for the reason each mode exists: in partial the later occurrence is a
// per-item invalid_input failure and the rest of the batch is
// undisturbed, and in atomic the whole batch is refused before anything
// is written. Both arguments are at their call sites.
//
// Both modes publish only after their transaction has committed, and
// publish one identity event per row that landed rather than one event
// carrying a count: a count is a value, not an identity, and a
// subscriber's only correct reaction to one of these events is to
// re-read the rows it names.
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
//
// Every part is length-prefixed, including the last, because the driver
// folds an item to one string and none of these parts is pattern-checked
// before it gets here: an entity's type key is answered by a lookup and
// not by a pattern, and an edge's five parts are all lookups. Without the
// prefixes ("ab", "c") and ("a", "bc") meet in the middle, and one is
// refused as a repetition of the other.
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
//
// A seeding agent building a batch from a file produces this by
// accident, and it is the one fault a batch can hold that the database
// cannot diagnose: the two items are one row, so the second is refused
// as a version conflict against a caller that never claimed a version —
// which sends an agent to re-read the row and retry with the version it
// is handed, at which point its own second item quietly overwrites its
// first. The real answer is that the row appears twice, and only the
// batch can see that.
//
// What counts as the same row is the caller's to define, because it is
// whatever the table's unique index folds on and that differs per kind.
// Whatever it is, it must fold exactly as the index does: a Go-side fold
// that disagrees with SQL's lets a repetition through to be misdiagnosed
// by the database, which is the failure this exists to prevent.
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
//
// Every wire code in errors.go has an arm here. The default arm is
// internal_error, which is the honest answer for something nobody
// planned for — and precisely the wrong answer for a malformed key, so
// ErrInvalidInput is matched explicitly: it is a caller's own argument,
// at a path, fixable in place, and reporting it as internal_error tells
// an agent to give up on a call it could have fixed.
//
// ErrActorNotInGame is deliberately *not* given an arm. It is not a wire
// code, for the reason errors.go records — the actor is never
// caller-supplied, so an agent can do nothing about it — and
// internal_error is the correct report for a fault it cannot fix.
//
// IsRetryable is checked *last*, after every domain code and before the
// default. Order matters here and it is the opposite of what "specific
// before general" suggests: a contention SQLSTATE and a domain refusal
// never arrive on the same error, but if one ever did, the domain code
// is the one a caller can act on, and "resend unchanged" is the worst
// possible advice about a row that will be refused again. Checking it
// last also means it only ever intercepts errors that were heading for
// internal_error, which is exactly the set it exists to rescue.
// The two messages a bulk failure states rather than quotes.
//
// **Review finding M2.** `failureFor` used to set `Message: err.Error()`
// unconditionally, before the switch, so the two codes whose text
// describes the server rather than the caller carried the database's own
// words: `canceling statement due to lock timeout (SQLSTATE 55P03)`,
// `relation "entities_secret" does not exist (SQLSTATE 42P01)`, a
// constraint name from a foreign-key violation. `mcpErrorFor`
// (internal/web/mcp_errors.go) withholds exactly that on the single-row
// path, and a test there asserts it — but a batch is where contention
// was actually observed, so this was the likelier route out, not the
// rarer one. The leak also undercut `retryable` itself: the message an
// agent read beside the code was the raw text the design says it must
// not see.
//
// Every other arm keeps `err.Error()`, because every other code names
// something the caller sent and the message is the half it acts on.
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
