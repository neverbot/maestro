package metamodel

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

// The descriptive columns every row in this domain carries beside its
// key — a label, a plural, a description, a colour, an icon — are checked
// here, in one place, because Tasks 4, 5 and 6 add rows with the same
// columns and the same silence would otherwise be re-shipped three times.
//
// **The decision these limits encode.** Maestro validates the *shape* of
// a descriptor, never its content: it is the game's own prose and Maestro
// has no standing to judge a label. What it does have standing to refuse
// is a descriptor that breaks something downstream:
//
//   - An empty label breaks a listing. ListEntityTypes orders by label,
//     so an unlabelled type sorts to the front of every list a designer
//     sees and identifies itself by nothing. A key is not a substitute:
//     the whole point of a label is that the key is a handle and the
//     label is what a human reads.
//   - An unbounded descriptor is a storage and rendering problem, not a
//     truthfulness one. The caps below are far past any real label and
//     nowhere near a payload: a 5000-character colour was accepted before
//     this file existed.
//   - A colour and an icon are not prose at all. They are consumed by a
//     renderer, so they are constrained to what a renderer can consume —
//     which is also, not coincidentally, what stops an icon from being
//     `<script>`. This is defence in depth and not the escaping strategy:
//     Task 8's templates escape what they render regardless, because a
//     label legitimately contains any character a game's language uses.
//
// Everything but the label is optional, and empty means "not set" rather
// than "set to empty" — a game that wants no colour on a type says so by
// sending none.
const (
	maxLabelLen       = 200
	maxDescriptionLen = 4000
	maxIconLen        = 64
)

// colorPattern accepts the two CSS hex forms and nothing else. Named CSS
// colours, rgb()/hsl() functions and arbitrary CSS were all considered
// and rejected for the same reason: this column is read by Maestro's own
// renderers, which need to derive contrasting text and border tones from
// it, and every one of those derivations is arithmetic on three
// channels. Accepting a form the renderer cannot decompose means storing
// a colour that some views honour and others silently drop.
var colorPattern = regexp.MustCompile(`^#([0-9A-Fa-f]{3}|[0-9A-Fa-f]{6})$`)

// iconPattern accepts a lower-case icon *name* — a handle into whichever
// icon set the frontend ships — not an image, not markup and not an
// emoji. An emoji would be defensible and is deliberately excluded for
// now: it would make the column two things at once (a name to look up, or
// a literal to print), and every renderer would have to tell them apart.
// The pending visual-identity spec owns that choice; until it lands, one
// meaning.
var iconPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

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
	}
	tooLong("label_plural", labelPlural, maxLabelLen, &problems)
	tooLong("description", description, maxDescriptionLen, &problems)

	if color != "" && !colorPattern.MatchString(color) {
		problems = append(problems, FieldError{
			Path:    "color",
			Message: "must be a hex colour such as #c41e3a or #c13",
		})
	}
	if icon != "" {
		if !tooLong("icon", icon, maxIconLen, &problems) && !iconPattern.MatchString(icon) {
			problems = append(problems, FieldError{
				Path: "icon",
				Message: "must be an icon name: lower-case letters, digits or hyphens, " +
					"starting with a letter or a digit",
			})
		}
	}
	return problems
}

// tooLong records an over-length descriptor and reports whether it did.
// The length is counted in runes, not bytes: a cap measured in bytes
// makes an accented label shorter than an unaccented one for no reason a
// designer could ever guess, and these columns are a game's own prose in
// a game's own language.
func tooLong(path, value string, max int, problems *[]FieldError) bool {
	if utf8.RuneCountInString(value) <= max {
		return false
	}
	*problems = append(*problems, FieldError{
		Path:    path,
		Message: fmt.Sprintf("must be at most %d characters", max),
	})
	return true
}
