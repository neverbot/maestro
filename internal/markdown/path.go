package markdown

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/neverbot/maestro/internal/metamodel"
)

// MaxPathLen bounds a document path, in bytes.
const MaxPathLen = 200

// MaxPathSegments bounds the depth of a path.
const MaxPathSegments = 8

// pathSegmentPattern is the rule for one segment of a path.
var pathSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// CheckPath refuses a document path a caller should not have sent,
// reporting it as invalid_input at the argument's own path.
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
// problems at once needs: the field errors rather than a wrapped error, so
// a write refusing both its path and its kind reports both in one answer
// instead of making an agent fix one, call again, and learn about the
// other. Write is its only caller; TestDocumentsArea's "every problem with
// one write is reported in one pass" case pins the shape.
func pathProblems(path string) []metamodel.FieldError {
	return pathProblemsAt("path", path)
}

// pathProblemsAt is pathProblems for a call whose path argument is not
// called "path".
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
