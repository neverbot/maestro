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
//
//   - keyPattern forbids upper case because two field keys differing only
//     by case would be two distinct keys in a jsonb object, and nothing
//     downstream would catch the collision.
//   - A row key cannot produce that collision at all. Every uniqueness
//     index over these keys is UNIQUE (project_id, lower(key)) — the
//     database folds case for them (0004_metamodel.sql) — so "Hogger" and
//     "hogger" are one key, by construction, in the only place it matters.
//
// So both rules deliver the same guarantee, "no two keys differ only by
// case", through the mechanism each context actually has. Forbidding upper
// case in row keys as well would buy nothing and cost the thing the
// folding index was chosen for: a game whose own vocabulary capitalises
// its handles ("Hogger", "Elwynn_Forest", "GP_Monaco") can spell them the
// way its design documents do, and a re-seed that changes the casing
// updates the existing row instead of failing.
//
// What the pattern does exclude earns its place:
//
//   - No dot, slash, space or percent, so a key drops into a REST path
//     segment (Task 8 addresses a type as /games/<slug>/types/<key>) and
//     into a view query without escaping, and cannot be mistaken for a
//     path separator or a nested reference.
//   - No leading punctuation, so no key can look like a flag, an option
//     or a relative path to a shell, a CLI or a query parser.
//   - ASCII only, so two Unicode normalisations of one accented word
//     cannot coexist as two keys — lower() folds case, not normalisation
//     form, so the index would let both through.
//
// Digits are allowed to lead: "1999_season" and "500_miles" are ordinary
// entity keys for a racing game, and there is no reason to make a designer
// rename their content to satisfy an identifier convention Maestro does
// not otherwise impose.
var rowKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// checkRowKey validates a key that addresses a row, reporting the problem
// at the caller's path ("key") so an agent sees where it is.
//
// A key problem is a ValidationError, not a SchemaError: the caller is
// writing a row, not declaring a schema, and the sentinel split states
// exactly that difference (see SchemaError's doc comment).
func checkRowKey(path, key string) error {
	switch {
	case key == "":
		return &ValidationError{Fields: []FieldError{{Path: path, Message: "is required"}}}
	case len(key) > maxRowKeyLen:
		return &ValidationError{Fields: []FieldError{{
			Path:    path,
			Message: fmt.Sprintf("must be at most %d characters", maxRowKeyLen),
		}}}
	case !rowKeyPattern.MatchString(key):
		return &ValidationError{Fields: []FieldError{{
			Path:    path,
			Message: "must be letters, digits, underscores or hyphens, starting with a letter or a digit",
		}}}
	}
	return nil
}

// keyRespellingError is what a caller sees when their key matches an
// existing row's key only case-insensitively.
//
// This is the collision the folding index makes possible, and it is the
// one place the row-key policy has to say something a designer can act on.
// Silently updating the differently-spelled row would let a typo'd capital
// overwrite content; letting the database raise it would surface as a raw
// unique-violation with no field path, or — since the upsert carries an ON
// CONFLICT clause — as a version conflict, which says nothing about the
// actual problem. Naming both spellings and both remedies is the whole
// answer a designer needs, and they never have to know an index folds
// case.
func keyRespellingError(path, requested, stored string) error {
	return &ValidationError{Fields: []FieldError{{
		Path: path,
		Message: fmt.Sprintf(
			"%q already exists here spelled %q, and keys are matched without regard to case: "+
				"use %q to update it, or pick a key that differs by more than capitalisation",
			requested, stored, stored),
	}}}
}
