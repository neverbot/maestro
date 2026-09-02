package markdown_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/metamodel"
)

// fieldProblem pulls the single field problem out of a domain error, so
// every negative test below asserts the path and the message rather than
// merely that something went wrong.
func fieldProblem(t *testing.T, err error) metamodel.FieldError {
	t.Helper()
	var v *metamodel.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("want a *metamodel.ValidationError, got %#v", err)
	}
	if len(v.Fields) != 1 {
		t.Fatalf("want exactly one field problem, got %d: %v", len(v.Fields), v.Fields)
	}
	return v.Fields[0]
}

func TestAValidPathIsAccepted(t *testing.T) {
	for _, path := range []string{
		"bible",
		"lore/duskwood/history",
		"scripts/wanted-hogger",
		"notes/1999_season",
		"design/v1.2/tone.md",
		strings.Repeat("a", markdown.MaxPathLen),
		strings.TrimSuffix(strings.Repeat("a/", markdown.MaxPathSegments), "/"),
	} {
		if err := markdown.CheckPath(path); err != nil {
			t.Fatalf("CheckPath(%q) = %v, want nil", path, err)
		}
	}
}

func TestEveryBadPathIsInvalidInputAtPathPath(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		message string
	}{
		{"empty", "", "is required"},
		{"too long", strings.Repeat("a", markdown.MaxPathLen+1), "must be at most 200 bytes"},
		{"leading slash", "/lore/duskwood", "must not begin or end with a slash"},
		{"trailing slash", "lore/duskwood/", "must not begin or end with a slash"},
		{"empty segment", "lore//duskwood", "has an empty segment"},
		{"dot segment", "lore/./duskwood", `has a segment "." or ".."`},
		{"parent segment", "lore/../secrets", `has a segment "." or ".."`},
		{"too many segments", "a/b/c/d/e/f/g/h/i", "must have at most 8 segments"},
		{"space", "lore/dusk wood", "must be letters, digits, underscores, hyphens or dots"},
		{"non-ascii", "lore/duskwöod", "must be letters, digits, underscores, hyphens or dots"},
		{"percent", "lore/%2e%2e", "must be letters, digits, underscores, hyphens or dots"},
		{"leading punctuation", "lore/-secret", "must be letters, digits, underscores, hyphens or dots"},
		{"control character", "lore/dusk\nwood", "must be letters, digits, underscores, hyphens or dots"},
		// A lone continuation byte. The ASCII-only segment pattern is
		// what refuses it: an invalid byte decodes as U+FFFD, which is
		// outside the character class, so the segment does not match.
		// Pinned as its own case because the rule this codebase settled
		// is that caller text is checked for valid UTF-8 before anything
		// else looks at it, and a path is the one input here whose
		// grammar happens to subsume that check rather than stating it.
		{"invalid utf-8", "lore/dusk\x80wood", "must be letters, digits, underscores, hyphens or dots"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := markdown.CheckPath(tc.path)
			if err == nil {
				t.Fatalf("CheckPath(%q) = nil, want a refusal", tc.path)
			}
			if !errors.Is(err, metamodel.ErrInvalidInput) {
				t.Fatalf("CheckPath(%q) must be invalid_input, got %v", tc.path, err)
			}
			problem := fieldProblem(t, err)
			if problem.Path != "path" {
				t.Fatalf("problem path = %q, want %q", problem.Path, "path")
			}
			if !strings.Contains(problem.Message, tc.message) {
				t.Fatalf("message %q does not contain %q", problem.Message, tc.message)
			}
		})
	}
}

func TestOnlyOneProblemIsReportedForOnePath(t *testing.T) {
	// Ordered from most to least fundamental, exactly as
	// metamodel.rowKeyProblems is: telling a caller their empty path is
	// also not a handle helps nobody.
	err := markdown.CheckPath("")
	if got := fieldProblem(t, err).Message; !strings.Contains(got, "is required") {
		t.Fatalf("an empty path must report emptiness first, got %q", got)
	}
}
