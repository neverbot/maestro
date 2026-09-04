// Package views is Maestro's saved-view domain: a JSON query document
// that compiles to bounded SQL over the metamodel, a renderer chosen from
// a fixed catalogue, and the coordinates a human dragged.
//
// It depends on internal/metamodel deliberately and acyclically: a query
// names entity types, relation types and their declared fields, so the
// dependency is real, and nothing in the metamodel mentions views. Taking
// it means this package reuses that one's FieldError, ValidationError,
// sentinels, key grammar and text bounds rather than growing a second,
// drifting copy of each — the same reasoning internal/markdown's package
// comment records.
package views

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/neverbot/maestro/internal/metamodel"
)

// The four wire codes this sub-project adds, and their sentinels.
//
// Four, and not five: the spec's §5.5 also proposes `query_timeout`, and
// it deliberately does not ship. SQLSTATE 57014 (query_canceled) is
// already in internal/metamodel's retryableSQLStates and already maps to
// the `retryable` code, whose meaning — change nothing and resend — is
// the right first advice for a statement timeout, and whose message this
// sub-project extends with which bound to lower when resending stops
// helping (execute.go). A code that names the same recovery as an
// existing code is how a vocabulary rots.
//
// Each of the four is a code because each names a *different recovery*:
//
//   - query_invalid — the document is wrong. Fix the pointer this error
//     names and send it again. Every structural and type failure.
//   - renderer_requirements — the document is fine and the renderer
//     cannot consume it. Pick another renderer, or change the query so it
//     produces what this one needs.
//   - limit_exceeded — a declared limit is above its hard cap. Lower it;
//     the cap is in the error.
//   - query_stale — the document names something this game no longer
//     has. Repair the query, or re-run with on_stale: "best_effort".
const (
	CodeQueryInvalid         = "query_invalid"
	CodeRendererRequirements = "renderer_requirements"
	CodeLimitExceeded        = "limit_exceeded"
	CodeQueryStale           = "query_stale"
)

var (
	ErrQueryInvalid         = errors.New(CodeQueryInvalid)
	ErrRendererRequirements = errors.New(CodeRendererRequirements)
	ErrLimitExceeded        = errors.New(CodeLimitExceeded)
	ErrQueryStale           = errors.New(CodeQueryStale)
)

// Sentinels lists every sentinel this package answers with, so
// internal/web's TestEveryViewsSentinelHasAWireCode (Task 15) can iterate
// it rather than repeating a list that a fifth sentinel would silently
// fall out of. The order is the declaration order and carries no meaning.
// TestSentinelsListsEveryCodeThisPackageAnswersWith pins that the list
// and the code constants stay in step.
func Sentinels() []error {
	return []error{ErrQueryInvalid, ErrRendererRequirements, ErrLimitExceeded, ErrQueryStale}
}

// The sentinels this package reuses rather than redeclaring. They are
// aliases of the metamodel's, not new values, so internal/web's existing
// mcpErrorFor and writeDomainError arms catch them with no change.
var (
	ErrNotFound        = metamodel.ErrNotFound
	ErrVersionConflict = metamodel.ErrVersionConflict
	ErrInvalidInput    = metamodel.ErrInvalidInput
	ErrInUse           = metamodel.ErrInUse
	// ErrActorNotInGame is not in Sentinels() and that is deliberate: it
	// is not a wire code. The actor on a write is resolved by the
	// transport from the credential the call arrived with and is never
	// caller-supplied, so an agent can do nothing about it — the argument
	// metamodel/errors.go makes and metamodel/bulk.go's failureFor
	// repeats. internal_error is the correct report for it.
	ErrActorNotInGame = metamodel.ErrActorNotInGame
)

// QueryError is a refusal of a query document, carrying one or more
// problems, each addressed by a **JSON pointer** into the document rather
// than by a Go field path.
//
// A pointer is the shape this sub-project adds to
// metamodel.FieldError.Path. It is the right address here for the reason
// the spec's §2.1 gives: an agent that gets `/traverse/0/where/value`
// knows exactly which of five traversal steps to re-write, and no amount
// of prose gets it there as reliably. internal/web's fieldDetails
// publishes it as details.fields[].path with no change — a pointer is a
// string like any other path.
//
// Is maps Code onto exactly one sentinel and defaults nothing: an
// unrecognised code matches nothing, exactly as metamodel.ValidationError
// behaves, and TestAnUnknownCodeMatchesNothingRatherThanDefaulting pins
// it.
type QueryError struct {
	Code   string
	Fields []metamodel.FieldError
	// Stale is the staleness report, and is set on query_stale and on
	// nothing else. It is carried *beside* Fields rather than instead of
	// it because the two are read by different readers: internal/web
	// publishes Fields as details.fields[].path, which is what an agent
	// acts on, while a UI banding a warning over a picture wants the
	// codes and the was/now pair. staleQuery fills both from one list.
	Stale []Diagnostic
}

func (e *QueryError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Error())
	}
	if len(parts) == 0 {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, strings.Join(parts, "; "))
}

func (e *QueryError) Is(target error) bool {
	switch e.Code {
	case CodeQueryInvalid:
		return target == ErrQueryInvalid
	case CodeRendererRequirements:
		return target == ErrRendererRequirements
	case CodeLimitExceeded:
		return target == ErrLimitExceeded
	case CodeQueryStale:
		return target == ErrQueryStale
	default:
		return false
	}
}

// invalidQuery is the one constructor every structural and type refusal
// in this package goes through, so a future refusal cannot reach a caller
// as a bare error and therefore as internal_error — the failure mode
// metamodel.searchQueryProblem's own comment records.
func invalidQuery(pointer, message string) error {
	return &QueryError{Code: CodeQueryInvalid, Fields: []metamodel.FieldError{
		{Path: pointer, Message: message},
	}}
}

// invalidQueryProblems is invalidQuery for a pass that found several
// problems at once, so an agent fixes all of them in one round trip
// instead of discovering them one call apart. Validation is a
// whole-document pass for exactly this reason.
func invalidQueryProblems(problems []metamodel.FieldError) error {
	return &QueryError{Code: CodeQueryInvalid, Fields: problems}
}

// pointer builds an RFC 6901 JSON pointer from path parts. Ints are
// array indices; strings are member names and are escaped, because a
// member name is only ever one of this package's own constants today but
// a `fields` map key is a game's own and may hold a slash.
func pointer(parts ...any) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteByte('/')
		switch v := p.(type) {
		case int:
			fmt.Fprintf(&b, "%d", v)
		case string:
			b.WriteString(strings.ReplaceAll(strings.ReplaceAll(v, "~", "~0"), "/", "~1"))
		default:
			panic(fmt.Sprintf("views: a pointer part must be an int or a string, got %T", p))
		}
	}
	return b.String()
}

// pointerLess orders two JSON pointers the way a reader walks the
// document rather than the way strings sort: segment by segment, and a
// segment that is an array index against another index numerically. Plain
// string order puts /from/10 before /from/2, which is a problem list that
// jumps about in any query long enough for the order to matter.
//
// A pass that already walks in document order does not need this and does
// not sort — resolveInto is one. It is for the passes that cannot:
// checkQuery reports the whole-document text bounds before the per-member
// checks, so its problems do not arrive in document order to begin with.
// TestASetOfProblemsIsOrderedByIndexNotByPointerString pins it.
func pointerLess(a, b string) bool {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		ai, aNum := strconv.Atoi(as[i])
		bi, bNum := strconv.Atoi(bs[i])
		if aNum == nil && bNum == nil {
			return ai < bi
		}
		return as[i] < bs[i]
	}
	return len(as) < len(bs)
}
