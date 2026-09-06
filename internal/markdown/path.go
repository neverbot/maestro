package markdown

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/neverbot/maestro/internal/metamodel"
)

// MaxPathLen bounds a document path, in bytes.
//
// Exported for the rule the metamodel established for MaxSearchQuery: a
// bound a caller cannot read is a bound a caller trips over. The docs
// tool descriptions are built with these values interpolated rather than
// typed out, so a description cannot go on promising a bound that moved.
//
// 200 bytes is generous for `scripts/act-two/hogger-confrontation` and
// short enough that a path sits in a URL, in a filename an export would
// produce, and in a listing column without wrapping.
const MaxPathLen = 200

// MaxPathSegments bounds the depth of a path.
//
// Slashes are a naming convention for humans and a filter prefix; they
// imply no folder table and no inherited anything (spec §6). What the
// bound buys is that `documents.path` cannot be used to build an
// arbitrarily deep tree the UI would then have to render, and that a
// prefix filter has a bounded number of prefixes to offer.
const MaxPathSegments = 8

// pathSegmentPattern is the rule for one segment of a path.
//
// It is metamodel's rowKeyPattern plus the dot, and the dot earns its
// place: `design/v1.2/tone.md` and `scripts/act-2.5` are ordinary
// spellings, and a path is not an identifier the way a row key is. What
// stays excluded is what a row key excludes and why:
//
//   - No space or percent, so a path drops into a URL segment without
//     escaping and cannot be mistaken for an encoded separator.
//   - No leading punctuation, so no segment can look like a flag, an
//     option or a relative path to a shell or a query parser. That, plus
//     the explicit "." / ".." refusal below, is what keeps a path from
//     ever traversing anything if an export ever writes these to disk.
//   - ASCII only, so two Unicode normalisations of one accented word
//     cannot coexist as two paths — lower() in documents_path_key folds
//     case, not normalisation form, so the index would let both through.
//
// Being ASCII-only is also what makes the path the one caller input in
// this package with no separate UTF-8 check: an invalid byte decodes as
// U+FFFD, which is outside this class, so the segment cannot match. The
// "invalid utf-8" case of TestEveryBadPathIsInvalidInputAtPathPath pins
// that, and it is stated here because the rule everywhere else in this
// codebase is to check the encoding first and explicitly.
//
// Digits may lead a segment: `notes/1999_season` is a perfectly good
// path for a racing game.
var pathSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// CheckPath refuses a document path a caller should not have sent,
// reporting it as invalid_input at the argument's own path.
//
// **It is invalid_input and not a code of its own.** The spec proposed
// `invalid_path`; this plan refuses it in "Open questions settled",
// because the existing vocabulary already has exactly this shape — a
// caller argument, at a field path, fixable in place — and a code per
// argument is how a vocabulary rots.
//
// At most one problem is reported for one path. The conditions are
// ordered from most to least fundamental, exactly as
// metamodel.rowKeyProblems orders its three: telling a caller their
// empty path is also not a handle helps nobody.
//
// **The bound is on the path a caller sends, and it is unrelated to
// what the database stores or indexes.** MaxPathLen is 200 bytes;
// `documents.path` is an unbounded `text` column and the search vector
// generated over `documents` does not include the path at all, so no
// truncation can happen behind this bound.
func CheckPath(path string) error {
	switch {
	case path == "":
		return invalidInput("path", "is required: a document is addressed by its path")
	case len(path) > MaxPathLen:
		return invalidInput("path", fmt.Sprintf(
			"must be at most %d bytes, and this one is %d", MaxPathLen, len(path)))
	case strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/"):
		return invalidInput("path", "must not begin or end with a slash: "+
			`write "lore/duskwood", not "/lore/duskwood/"`)
	}

	segments := strings.Split(path, "/")
	if len(segments) > MaxPathSegments {
		return invalidInput("path", fmt.Sprintf(
			"must have at most %d segments, and this one has %d", MaxPathSegments, len(segments)))
	}
	for _, segment := range segments {
		switch {
		case segment == "":
			return invalidInput("path", "has an empty segment: two slashes in a row address nothing")
		case segment == "." || segment == "..":
			return invalidInput("path", `has a segment "." or "..": a path is a name, not a route`)
		case !pathSegmentPattern.MatchString(segment):
			return invalidInput("path", fmt.Sprintf(
				"has the segment %q, and every segment must be letters, digits, underscores, "+
					"hyphens or dots, starting with a letter or a digit", segment))
		}
	}
	return nil
}

// pathProblems is CheckPath in the shape a caller collecting several
// problems at once needs: the field errors rather than a wrapped error,
// so a write refusing both its path and its kind reports both in one
// answer instead of making an agent fix one, call again, and learn about
// the other. Write is its only caller;
// TestEveryProblemWithOneWriteIsReportedInOnePass pins the shape.
//
// CheckPath only ever answers with a *metamodel.ValidationError built by
// invalidInput, so the assertion below cannot fail; a nil v would panic
// here rather than silently drop the problem, which is the right way
// round for an invariant this package owns on both sides.
func pathProblems(path string) []metamodel.FieldError {
	return pathProblemsAt("path", path)
}

// pathProblemsAt is pathProblems for a call whose path argument is not
// called "path".
//
// Move has two of them, `from` and `to`, and reporting both at "path"
// would hand a caller two field errors with one name and leave it to
// guess which end each belongs to — the exact discrimination
// MissingError's own comment says a field path exists to provide. The
// judgement is CheckPath's and is not duplicated; only the label moves.
// TestEveryProblemWithOneMoveIsReportedAtItsOwnEnd pins that a call with
// two bad paths hears about both, at "from" and at "to".
func pathProblemsAt(field, path string) []metamodel.FieldError {
	err := CheckPath(path)
	if err == nil {
		return nil
	}
	var v *metamodel.ValidationError
	_ = errors.As(err, &v)
	fields := make([]metamodel.FieldError, len(v.Fields))
	for i, f := range v.Fields {
		f.Path = field
		fields[i] = f
	}
	return fields
}
