package metamodel

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The descriptive columns every row in this domain carries beside its
// key — a label, a plural, a description, a colour, an icon — are checked
// here, in one place, because Tasks 4, 5 and 6 add rows with the same
// columns and the same silence would otherwise be re-shipped three times.
const (
	maxLabelLen       = 200
	maxDescriptionLen = 4000
	maxIconLen        = 64
)

// colorPattern accepts all four CSS hex forms — #RGB, #RGBA, #RRGGBB and
// #RRGGBBAA — and nothing else. Named CSS colours, rgb()/hsl() functions
// and arbitrary CSS were all considered and rejected for the same reason:
// this column is read by Maestro's own renderers, which need to derive
// contrasting text and border tones from it, and every one of those
// derivations is arithmetic on the channels. Accepting a form the
// renderer cannot decompose means storing a colour that some views
// honour and others silently drop.
var colorPattern = regexp.MustCompile(`^#([0-9A-Fa-f]{3,4}|[0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})$`)

// iconPattern accepts a lower-case icon *name* — a handle into whichever
// icon set the frontend ships — not an image, not markup and not an
// emoji. An emoji would be defensible and is deliberately excluded for
// now: it would make the column two things at once (a name to look up, or
// a literal to print), and every renderer would have to tell them apart.
// The pending visual-identity spec owns that choice; until it lands, one
// meaning.
var iconPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// TextFault is what CheckText found wrong with one piece of caller text:
// either the value is not valid UTF-8, or it holds a control character
// the caller was not allowed, at Offset and decoding as Rune.
type TextFault struct {
	InvalidUTF8 bool
	Rune        rune
	Offset      int
}

// CheckText is the judgement behind every "is this storable text?" rule
// in Maestro, exported so no package grows a second copy of it.
func CheckText(value, allowedControls string) (TextFault, bool) {
	if !utf8.ValidString(value) {
		return TextFault{InvalidUTF8: true}, true
	}
	for i, r := range value {
		if !unicode.IsControl(r) || strings.ContainsRune(allowedControls, r) {
			continue
		}
		return TextFault{Rune: r, Offset: i}, true
	}
	return TextFault{}, false
}

// textProblem is the one rule this package applies to every piece of
// caller-supplied prose it stores or renders — a name, a label, a
// plural, a description, a text or longtext value, an element of a
// list<text>: valid UTF-8, and no control character. The same rule
// guards a search query here and a project name in internal/projects.
//
// **allowNewlineAndTab is the one asymmetry.** A longtext value and a
// description are free-form prose, where a newline is the caller's own
// paragraph break. Everything else is rendered as a single line — a page
// title, a picker, a listing row, a tag chip — so a newline or a tab
// there is refused like any other control character.
//
// A control character is refused and never stripped: deleting part of
// what a caller wrote answers a question they did not ask.
func textProblem(value string, allowNewlineAndTab bool) string {
	allowed := ""
	if allowNewlineAndTab {
		allowed = "\n\t"
	}
	fault, bad := CheckText(value, allowed)
	if !bad {
		return ""
	}
	if fault.InvalidUTF8 {
		return "is not valid UTF-8: a byte in it does not decode as any character"
	}
	r, i := fault.Rune, fault.Offset
	if allowNewlineAndTab {
		return fmt.Sprintf(
			"holds a control character (%U at byte %d): only a newline or a tab is allowed here",
			r, i)
	}
	return fmt.Sprintf(
		"holds a control character (%U at byte %d): this is one line of text, "+
			"and no control character can be part of it",
		r, i)
}

// checkDescriptors collects every problem with a row's descriptive
// columns, so a seeding agent fixes all of them in one round trip
// instead of one per call. It reports at the wire path of each column,
// matching rowKeyProblems's "key".
func checkDescriptors(label, labelPlural, description, color, icon string) []FieldError {
	var problems []FieldError

	if label == "" {
		problems = append(problems, FieldError{Path: "label", Message: "is required"})
	} else {
		tooLong("label", label, maxLabelLen, &problems)
		if p := textProblem(label, false); p != "" {
			problems = append(problems, FieldError{Path: "label", Message: p})
		}
	}
	tooLong("label_plural", labelPlural, maxLabelLen, &problems)
	if labelPlural != "" {
		if p := textProblem(labelPlural, false); p != "" {
			problems = append(problems, FieldError{Path: "label_plural", Message: p})
		}
	}
	tooLong("description", description, maxDescriptionLen, &problems)
	if description != "" {
		// description is a row's own prose, not a rendered single line —
		// see textProblem's doc comment — so it keeps the newline and tab
		// a longtext field value does.
		if p := textProblem(description, true); p != "" {
			problems = append(problems, FieldError{Path: "description", Message: p})
		}
	}

	if color != "" && !colorPattern.MatchString(color) {
		problems = append(problems, FieldError{
			Path:    "color",
			Message: "must be a hex colour such as #c41e3a, #c13, #c41e3a80 or #c13f",
		})
	}
	if icon != "" {
		if !tooLong("icon", icon, maxIconLen, &problems) && !iconPattern.MatchString(icon) {
			problems = append(problems, FieldError{
				Path: "icon",
				Message: "must be an icon name: lower-case letters, digits, underscores or " +
					"hyphens, starting with a letter or a digit",
			})
		}
	}
	return problems
}

// checkName is the entity flavour of checkDescriptors. An entity carries
// one descriptive column and it obeys the label rule — required, capped
// at maxLabelLen, counted in runes — at its own wire path, so an agent
// sees "name", not "label", for the argument it actually sent.
func checkName(name string) []FieldError {
	var problems []FieldError
	if name == "" {
		problems = append(problems, FieldError{Path: "name", Message: "is required"})
	} else {
		tooLong("name", name, maxLabelLen, &problems)
		if p := textProblem(name, false); p != "" {
			problems = append(problems, FieldError{Path: "name", Message: p})
		}
	}
	return problems
}

// LengthProblem is this repository's one rule for how long a piece of a
// designer's own prose may be, as the message for a value that is over
// the cap or "" for one that is not.
func LengthProblem(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return ""
	}
	return fmt.Sprintf("must be at most %d characters", max)
}

// tooLong records an over-length descriptor and reports whether it did.
func tooLong(path, value string, max int, problems *[]FieldError) bool {
	problem := LengthProblem(value, max)
	if problem == "" {
		return false
	}
	*problems = append(*problems, FieldError{Path: path, Message: problem})
	return true
}
