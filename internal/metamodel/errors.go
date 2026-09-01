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
