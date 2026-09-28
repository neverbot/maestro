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
	// ErrLimitExceeded is here rather than in the package that first
	// needed it. internal/views declared it, and internal/analysis needs
	// exactly the same value: "a declared limit is above its hard cap;
	// lower it, the cap is in the error" is word for word both domains'
	// refusal, and a second value spelling the same code would not match
	// under errors.Is — so internal/web's one existing arm would miss it
	// and it would reach an agent as internal_error, which is the trap
	// ValidationError.Is's own comment describes.
	ErrLimitExceeded = errors.New(CodeLimitExceeded)
)

// CodeLimitExceeded is the wire code ErrLimitExceeded names. It is a
// const rather than a literal for the reason CodeInvalidInput is one: a
// sibling domain building the error must not spell the string.
const CodeLimitExceeded = "limit_exceeded"

// ErrActorNotInGame is what the database's own backstop against a
// cross-game actor is reported as.
var ErrActorNotInGame = errors.New("the actor recorded on this write does not belong to this game")

// FieldError is one problem with one field.
type FieldError struct {
	Path    string
	Message string
}

func (e FieldError) Error() string { return e.Path + ": " + e.Message }

// ValidationError carries every problem found in one pass, so a seeding agent
// fixes all of them at once instead of discovering them one round-trip apart.
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
const (
	CodeSchemaViolation = codeSchemaViolation
	CodeInvalidInput    = codeInvalidInput
)

// code is the wire code these problems belong under, defaulting to
// schema_violation so the value validator, which predates the split and
// reports nothing else, needs no change.
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
func (e *ValidationError) Is(target error) bool {
	switch e.code() {
	case codeSchemaViolation:
		return target == ErrSchemaViolation
	case codeInvalidInput:
		return target == ErrInvalidInput
	case CodeLimitExceeded:
		// The third code a ValidationError may carry, and it arrived with
		// ErrLimitExceeded when internal/analysis needed the same value
		// internal/views already had. **A code exported here that no
		// error here can carry would be a sentinel nothing matches**: a
		// caller building &ValidationError{Code: CodeLimitExceeded} — the
		// natural thing to do, since this file offers both halves — would
		// produce an error errors.Is answers false for, which reaches an
		// agent as internal_error. That is exactly the trap the default
		// arm below describes, sprung by this package's own vocabulary,
		// and carrying the code one step further than the sentinel is
		// what closes it.
		return target == ErrLimitExceeded
	default:
		return false
	}
}

// SchemaError carries every problem found in a schema *declaration*, at
// field_schema[<i>] paths.
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
