package metamodel

import (
	"fmt"
	"regexp"
)

// maxRowKeyLen caps the keys that address rows. Sixty-four matches
// maxKeyLen, the field-key cap, for the same reasons: generous for a
// readable handle, short enough to sit in a URL path segment, an export
// filename or a view-query token without anyone thinking about it.
const maxRowKeyLen = 64

// rowKeyPattern is the rule for the keys that *address rows* — entity-type
// keys, relation-type keys and entity keys. It is deliberately wider than
// keyPattern, the rule for the field keys inside a row's jsonb, and the
// difference is not an inconsistency:

var rowKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// rowKeyProblems validates a key that addresses a row, reporting the
// problem at the caller's path ("key") so an agent sees where it is.
func rowKeyProblems(path, key string) []FieldError {
	switch {
	case key == "":
		return []FieldError{{Path: path, Message: "is required"}}
	case len(key) > maxRowKeyLen:
		return []FieldError{{
			Path:    path,
			Message: fmt.Sprintf("must be at most %d characters", maxRowKeyLen),
		}}
	case !rowKeyPattern.MatchString(key):
		return []FieldError{{
			Path:    path,
			Message: "must be letters, digits, underscores or hyphens, starting with a letter or a digit",
		}}
	}
	return nil
}

// keyRespellingError is what a caller sees when their key matches an
// existing row's key only case-insensitively.
func keyRespellingError(path, requested, stored string) error {
	return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: path,
		Message: fmt.Sprintf(
			"%q already exists here spelled %q, and keys are matched without regard to case: "+
				"use %q to update it, or pick a key that differs by more than capitalisation",
			requested, stored, stored),
	}}}
}

// RowKeyProblems is rowKeyProblems under an exported name, for
// internal/markdown, which addresses entities by (type key, key) when it
// attaches a document to one and must bound both before they reach
// Postgres.
func RowKeyProblems(path, key string) []FieldError { return rowKeyProblems(path, key) }
