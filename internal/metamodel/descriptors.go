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

// colorPattern accepts all four CSS hex forms — #RGB, #RGBA, #RRGGBB and
// #RRGGBBAA — and nothing else. Named CSS colours, rgb()/hsl() functions
// and arbitrary CSS were all considered and rejected for the same reason:
// this column is read by Maestro's own renderers, which need to derive
// contrasting text and border tones from it, and every one of those
// derivations is arithmetic on the channels. Accepting a form the
// renderer cannot decompose means storing a colour that some views
// honour and others silently drop.
//
// That argument excludes named colours and functional notation; it does
// *not* exclude the alpha forms, which are the same channels with a
// fourth appended and decompose exactly as easily. A translucent
// overlay colour is an ordinary thing for a game to want on a zone or a
// faction, and refusing it while calling three and six digits "the two
// CSS hex forms" was simply wrong about CSS Color 4.
var colorPattern = regexp.MustCompile(`^#([0-9A-Fa-f]{3,4}|[0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})$`)

// iconPattern accepts a lower-case icon *name* — a handle into whichever
// icon set the frontend ships — not an image, not markup and not an
// emoji. An emoji would be defensible and is deliberately excluded for
// now: it would make the column two things at once (a name to look up, or
// a literal to print), and every renderer would have to tell them apart.
// The pending visual-identity spec owns that choice; until it lands, one
// meaning.
//
// Underscores are allowed, and the omission was not neutral. Material
// Symbols — the most likely set for a project with no build step — names
// every icon in snake_case (local_fire_department, sports_motorsports),
// so a kebab-only rule would have rejected the whole vocabulary of an
// icon set nobody has chosen yet, silently pre-committing the pending
// spec to a set that spells its names with hyphens. Allowing both also
// makes this the same shape as the project's other two key-shaped rules
// (keyPattern, rowKeyPattern), which both permit `_`; being the only one
// that did not was the tell.
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
//
// **It reports what is wrong, not how to say it.** internal/markdown
// refuses at a different argument path, with a different error type and
// different wording — a document body is prose, a `name` is a line in a
// picker — and only the judgement is shared: the ordering, the
// allowance, and which rune at which byte. internal/markdown's
// SplitContent and this package's textProblem are both callers, and Task
// 3 bounds `kind` and `message` through it rather than writing the scan
// a third time.
//
// **allowedControls is the allowance, as the set of control runes the
// caller may keep.** "" for a value rendered as one line, "\n\t" for
// this package's free-form prose, "\n\r\t" for a markdown body, which
// keeps a CRLF document byte for byte. Nothing outside U+0000-U+001F and
// U+007F is a control character here, so passing a non-control rune in
// the set is inert rather than an escape hatch.
//
// **UTF-8 is checked before the control scan and that ordering is
// load-bearing**, not a style: ranging over a string turns an invalid
// byte into U+FFFD, which is not a control character, so a scan alone
// lets an invalid sequence through to Postgres, which refuses it with
// SQLSTATE 22021 as an untyped server fault over the caller's own bytes.
//
// A control character is refused, not stripped: silently deleting part
// of what a caller wrote answers a question it did not ask.
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

// textProblem is CheckText's judgement in this package's words, and the
// one rule this package applies to every piece of
// caller-supplied prose it stores or renders — an entity or type name, a
// label, a plural, a description, a text or longtext field value, an
// element of a list<text> — and it is the same rule checkSearchQuery
// applies to a search query and validateName applies to a project name
// in internal/projects/projects.go: valid UTF-8, and no control
// character.
//
// Before this existed the package had three different answers to the
// same question -- projects.go refused controls, search.go refused
// controls, and every metamodel name, descriptor and field value refused
// nothing -- so a NUL inside a name reached Postgres unexamined and came
// back as `ERROR: invalid byte sequence for encoding "UTF8" (SQLSTATE
// 22021)`, untyped, over the caller's own argument; the identical NUL
// inside a longtext field value failed a different way, `unsupported
// Unicode escape sequence (SQLSTATE 22P05)`, because jsonb encodes a NUL
// as the six-character escape and Postgres's json input routine refuses
// that escape outright. Neither is a schema_violation Task 4's caller
// could read as "the value is wrong shape" -- both are exactly the NUL
// checkSearchQuery already refuses on the read side, reaching the same
// caller through the write side instead.
//
// **allowNewlineAndTab decides the one asymmetry a single rule cannot
// avoid stating.** A `longtext` field value and a row's `description`
// are free-form prose — a lore document, a designer's notes — and a
// newline in either is the caller's own paragraph break, not malformed
// input; refusing it would make the rule less useful than the bug it
// replaces. Every other text this function sees — a `name`, a `label`,
// a `label_plural`, a `text` field value, one element of a `list<text>`
// — is rendered as a single line (a page title, a game picker, a
// listing row, a tag chip), the same reasoning validateName gives for
// entity and project names alike, so a newline or a tab there is refused
// exactly like any other control character rather than silently kept or
// silently dropped.
//
// A control character is refused, not stripped, for the reason
// checkSearchQuery's doc comment gives: silently deleting part of what a
// caller wrote answers a question it did not ask.
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
//
// It is a second function rather than a call to checkDescriptors with
// empty descriptors because the *path* has to differ: checkDescriptors
// reports at "label", and an agent that sent `name` must be told about
// `name`. Rows whose descriptive columns really are label-shaped —
// relation types, Task 5 — call checkDescriptors directly instead.
//
// name obeys textProblem's single-line rule, not the description one: it
// is rendered as one line — a page title, a game picker, a listing row —
// the same as a label, never as a lore document, so a newline in it is
// refused like any other control character.
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
//
// **The length is counted in runes, not bytes**: a cap measured in bytes
// makes an accented label shorter than an unaccented one for no reason a
// designer could ever guess, and these columns are a game's own prose in
// a game's own language. The message says "characters" because that is
// the unit it counts, and a message naming a unit the check does not use
// is worse than no message.
//
// **Exported because internal/views bounds a saved view's name and
// description against these same two numbers.** It did so with len(),
// which made a Spanish view name 100 characters where a Spanish type
// label is 200 — the exact asymmetry the paragraph above forbids,
// arrived at by copying the byte-counting check from internal/markdown,
// where counting bytes is right because those values are document paths
// and kinds rather than prose. Sharing the judgement is what stops the
// two caps drifting apart again while their numbers stay equal.
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
