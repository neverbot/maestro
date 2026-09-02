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
// way its design documents do, and the spelling the designer chose is the
// one stored — Maestro neither folds it nor rewrites it. What the folding
// index buys is that a re-seed under a different casing addresses the
// existing row rather than creating a twin beside it; it does not update
// it silently, and it is not meant to. keyRespellingError below refuses
// exactly that write, naming both spellings, because a changed casing is
// far likelier to be a typo than a deliberate rename of a handle other
// rows and documents already refer to.
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
//
// What this rule reasons about is path *escaping*, not path
// *collision*, and the difference is deliberate. "new", "index", "id",
// "null", "select", "games" and "types" are all valid keys, and none of
// them collides with anything.
//
// **Task 8 decided this, and it chose the route shape over a word list
// here.** Its three options were a reserved-word list checked in this
// file, a route shape that cannot collide, or accepting the collision
// and resolving it in the router. The REST surface addresses every row
// behind a fixed discriminator — /api/games/{game}/types/by-key/{key},
// .../types/by-id/{id}, .../entities/by-key/{type}/{key} — so a key
// never occupies a segment a literal could also claim, and "by-key" and
// "by-id" are themselves perfectly legal keys, because the discriminator
// sits one segment earlier than any key ever does. A word list was
// rejected for the reason this comment already gave: it would forbid
// "new" to a game with a perfectly good reason to name a type that, and
// a key rule tightened after a game is seeded costs renames. Resolving
// in the router was rejected because Go's ServeMux prefers a literal
// segment over a wildcard silently — a /types/new page added later would
// take an existing type offline with nothing failing anywhere. See
// internal/web/api_metamodel.go's header, and
// TestARouteShapedKeyIsStillAddressable, which declares a type for each
// of the dangerous words and addresses it both ways.

var rowKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// rowKeyProblems validates a key that addresses a row, reporting the
// problem at the caller's path ("key") so an agent sees where it is.
//
// A key problem is a ValidationError, not a SchemaError: the caller is
// writing a row, not declaring a schema, and the sentinel split states
// exactly that difference (see SchemaError's doc comment).
//
// It hands back the problems rather than a wrapped error so that a caller
// checking a key *and* a row's descriptive columns reports both in one
// ValidationError, rather than making an agent fix the key, call again,
// and only then learn the label was empty too. At most one problem is
// ever reported for a single key: the three conditions are ordered from
// most to least fundamental, and telling a caller their empty key is also
// not a handle helps nobody.
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
	return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
		Path: path,
		Message: fmt.Sprintf(
			"%q already exists here spelled %q, and keys are matched without regard to case: "+
				"use %q to update it, or pick a key that differs by more than capitalisation",
			requested, stored, stored),
	}}}
}
