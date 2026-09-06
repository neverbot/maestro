// Package metamodel is Maestro's domain: game-declared types and the entities
// and relations that instance them.
package metamodel

import (
	"errors"
	"fmt"
	"strings"
)

// Stable domain errors. Their names match the wire codes the MCP surface
// returns, so a change here is a change to the public contract.
var (
	ErrNotFound             = errors.New("not_found")
	ErrVersionConflict      = errors.New("version_conflict")
	ErrEndpointTypeMismatch = errors.New("endpoint_type_mismatch")
	ErrInUse                = errors.New("in_use")
	ErrSchemaViolation      = errors.New("schema_violation")
	ErrInvalidSchema        = errors.New("invalid_schema")
	ErrInvalidInput         = errors.New("invalid_input")
)

// ErrActorNotInGame is what the database's own backstop against a
// cross-game actor is reported as.
//
// It is deliberately *not* in the block above, and deliberately not a
// wire code. Every error up there is something a caller can act on by
// changing what it sent; this one is not. An Actor is never
// caller-supplied content — internal/web resolves it from the credential
// the request authenticated with, and a token is scoped to exactly one
// game (ProjectScope, api_projects.go) — so a token id arriving against
// another game's project id means the scope resolution above this package
// is wrong, not that a designer typed something. 0004_metamodel.sql's
// composite FOREIGN KEY (updated_by_token_id, project_id) catches it
// regardless, which is the point of putting it in the schema; what this
// sentinel adds is that the refusal says what happened instead of
// surfacing "violates foreign key constraint ... (SQLSTATE 23503)" into
// a log nobody can act on.
//
// Unmapped in internal/web/mcp_errors.go, it therefore reaches an agent
// as internal_error and reaches an operator as a legible log line, which
// is the correct pairing for a condition an agent cannot fix.
//
// **Task 7 decided it does not earn a wire code**, and left it unmapped
// for exactly the reason above: every code in the block at the top of
// this file names something the caller can do — change a value, change a
// key, merge onto a version, resend unchanged — and there is nothing a
// caller can do about a token bound to another game but stop using it,
// which it cannot know from here. Task 7 did add one code for a fault
// the caller does not own (`retryable`, see IsRetryable in service.go),
// so the rule is not "only caller-caused faults get codes"; it is that a
// code exists to name a recovery, and this condition has none on the
// agent's side.
var ErrActorNotInGame = errors.New("the actor recorded on this write does not belong to this game")

// FieldError is one problem with one field.
type FieldError struct {
	Path    string
	Message string
}

func (e FieldError) Error() string { return e.Path + ": " + e.Message }

// ValidationError carries every problem found in one pass, so a seeding agent
// fixes all of them at once instead of discovering them one round-trip apart.
//
// Code names which of two wire codes the problems belong under. Both are
// the same *kind* of failure — a field path and a message a caller can
// act on — but they have two different recoveries, and an agent must not
// have to read paths to tell them apart:
//
//   - schema_violation (the zero value, so Schema.Validate needs no
//     change) is a row of *values* that does not fit a declaration that
//     can stand. Every path is fields.<key>. The fix is to change the
//     values and call entities.upsert again.
//   - invalid_input is a row's own arguments — its key, its label, its
//     colour — being malformed. The paths are key, label, color, icon.
//     The fix is to change that argument, and the call it belongs to may
//     well be types.upsert, where "schema_violation" would send an agent
//     following the skill bundle off to inspect entity values that have
//     nothing to do with it.
//
// This is deliberately not a third *type*. SchemaError is a separate type
// because a declaration failing is a different fault (the schema, not the
// row) and satisfies a different sentinel; key and descriptor problems
// are the same fault as a value problem — the caller's own input, at a
// path, fixable in place — so they share the type that already reports
// exactly that, and differ only in the code they will be published under.
// Task 7 reads Code; it does not re-derive it from path spelling.
type ValidationError struct {
	Code   string
	Fields []FieldError
}

// The two wire codes a ValidationError is published under. They live
// together, and beside the sentinels they name, because a code and its
// sentinel are one decision: codeInvalidInput is every problem with a
// row's own arguments — its key, its label, its colour — and
// codeSchemaViolation is what an unset Code means, so the value
// validator needs no change. See ValidationError.Code.
const (
	codeSchemaViolation = "schema_violation"
	codeInvalidInput    = "invalid_input"
)

// CodeSchemaViolation and CodeInvalidInput are the two wire codes a
// ValidationError is published under, exported so a sibling domain
// package can build one without spelling the string.
//
// internal/markdown reports every caller-argument problem as
// &ValidationError{Code: CodeInvalidInput, …}, which is the whole
// reason these exist: a literal "invalid_input" over there and the
// constant over here would be two spellings of one decision, and
// ValidationError.Is matches nothing for a code it does not recognise —
// so a typo would surface to an agent as internal_error rather than
// failing anywhere a compiler can see it.
const (
	CodeSchemaViolation = codeSchemaViolation
	CodeInvalidInput    = codeInvalidInput
)

// code is the wire code these problems belong under, defaulting to
// schema_violation so the value validator, which predates the split and
// reports nothing else, needs no change.
//
// The default covers the *unset* code and nothing else. A code that is
// set but unrecognised stays as written, so Error and Is agree on it:
// see Is.
func (e *ValidationError) code() string {
	if e.Code == "" {
		return codeSchemaViolation
	}
	return e.Code
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Error())
	}
	return fmt.Sprintf("%s: %s", e.code(), strings.Join(parts, "; "))
}

// Is makes errors.Is true against the sentinel this error's own code
// names, and false against the other — so a caller matching
// ErrSchemaViolation never catches a malformed key, and vice versa.
//
// An unrecognised code matches *nothing*, deliberately. Falling back to
// ErrSchemaViolation would make Error and Is disagree — a typo'd
// `Code: "invalid_inptu"` printed the typo and still read as a schema
// violation — and it would resolve that disagreement towards the
// recovery the skill bundle teaches most loudly, sending an agent to fix
// entity values over a fault that has nothing to do with them. Matching
// nothing leaves the error unmapped in mcp_errors.go, so it surfaces as
// internal_error: the honest report for a code no spec documents, and
// one an operator can find in a log.
func (e *ValidationError) Is(target error) bool {
	switch e.code() {
	case codeSchemaViolation:
		return target == ErrSchemaViolation
	case codeInvalidInput:
		return target == ErrInvalidInput
	default:
		return false
	}
}

// SchemaError carries every problem found in a schema *declaration*, at
// field_schema[<i>] paths.
//
// It is deliberately a different error from ValidationError, and satisfies a
// different sentinel. The two failures are told apart by who is at fault: an
// invalid_schema is a type whose declaration cannot stand, a
// schema_violation is a row of values that does not fit a declaration that
// can. The MCP surface returns those as two codes, and a caller must not
// have to match on path spelling to tell them apart. Introducing the
// distinction now is cheap; Task 7 puts it on the wire, and after that it is
// not.
type SchemaError struct {
	Fields []FieldError
}

func (e *SchemaError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Error())
	}
	return fmt.Sprintf("invalid_schema: %s", strings.Join(parts, "; "))
}

// Is makes errors.Is(err, ErrInvalidSchema) true for schema declaration
// failures — and, deliberately, leaves errors.Is(err, ErrSchemaViolation)
// false.
func (e *SchemaError) Is(target error) bool { return target == ErrInvalidSchema }

// RemovedError is what a caller hears when it states an
// `expected_version` for a row that is not there.
//
// **A version claim means "I am editing the row I read at version N."**
// If no such row exists, that belief is false and the honest answer says
// so. Before this error, every upsert in this package read an
// `expected_version` that reached its insert path as *no claim at all*:
// the locked read found nothing, the call took the creation path, and a
// brand-new row appeared under a **new id** with version 1 and no error.
// The way to produce it is not exotic — an update that parks behind a
// committed removal does exactly that — and what it costs is not the
// content, which the caller was resending anyway, but every reference to
// the row that was removed. Relations, view positions, saved-view
// references, prose links and endpoint rules all name a row by id, so
// each of them now names nothing while a row with the same key sits
// there looking fine.
//
// **It says the row was removed, and deliberately not that the version
// is stale.** The two failures have different recoveries and an agent
// must not have to guess which it is holding: `version_conflict` means
// re-read, merge and write again, which cannot terminate here because
// there is nothing to merge onto; `not_found` means decide whether to
// re-create the row deliberately, with no claim, accepting that it is a
// new row that nothing pointing at the old one will follow.
//
// It satisfies ErrNotFound rather than declaring an eighth sentinel: it
// is the same fact every other by-key miss in this package reports —
// this game has no such row — arriving from a write path, and
// internal/web's two surfaces already map that sentinel with no new arm.
//
// **The markdown domain is deliberately different, and must not be
// harmonised with this.** internal/markdown's `writeWith` accepts a
// version claim against a *deleted* path and continues the document:
// deletion there is soft, the tombstone keeps the whole history, and a
// write to the path resurrects the same row — same id, same links,
// version numbering continuing — so nothing a caller would mourn is
// lost, and refusing would leave a caller no way back to its own
// document. (Against a path that has *never* existed, markdown already
// refuses: a non-zero `expected_version` there is `not_found`, which is
// the same rule this error states, reached by the same reasoning.) The
// asymmetry is between a resurrection that keeps the association and one
// that loses it, not between two domains that disagree about
// concurrency. See the creation arm of markdown.writeWith, which carries
// the other half of this note.
//
// Address is the row as the caller addressed it, already quoted or
// otherwise legible — a key, or an edge named by its type and both ends
// — because that is the one part of the call a caller can compare
// against what it holds.
type RemovedError struct {
	Subject string
	Address string
	Claimed int32
}

func (e *RemovedError) Error() string {
	return fmt.Sprintf("%s: no %s %s in this game: it was removed since you read version %d. "+
		"A version claim is a claim about a row that still exists, and nothing can be "+
		"merged onto a row that is gone, so this was refused rather than quietly creating "+
		"a second one — the new row would carry a new id, and every relation, position, "+
		"saved-view reference and attachment that named the old one would go on naming "+
		"nothing. Send this again with no expected_version to create it deliberately",
		ErrNotFound, e.Subject, e.Address, e.Claimed)
}

// Is makes errors.Is(err, ErrNotFound) true, and deliberately leaves
// errors.Is(err, ErrVersionConflict) false: telling a caller to merge is
// the one instruction that cannot work here.
func (e *RemovedError) Is(target error) bool { return target == ErrNotFound }

// VersionConflictError reports the version the caller must merge onto.
type VersionConflictError struct {
	Current int32
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version_conflict: current version is %d", e.Current)
}

// Is makes errors.Is(err, ErrVersionConflict) true.
func (e *VersionConflictError) Is(target error) bool { return target == ErrVersionConflict }
