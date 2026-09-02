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
// is the correct pairing for a condition an agent cannot fix. **Task 7
// decides whether it earns a wire code of its own**; until something can
// be done about it from the outside, one would only invite a caller to
// retry.
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
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Error())
	}
	return fmt.Sprintf("schema_violation: %s", strings.Join(parts, "; "))
}

// Is makes errors.Is(err, ErrSchemaViolation) true for validation failures.
func (e *ValidationError) Is(target error) bool { return target == ErrSchemaViolation }

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

// VersionConflictError reports the version the caller must merge onto.
type VersionConflictError struct {
	Current int32
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version_conflict: current version is %d", e.Current)
}

// Is makes errors.Is(err, ErrVersionConflict) true.
func (e *VersionConflictError) Is(target error) bool { return target == ErrVersionConflict }
