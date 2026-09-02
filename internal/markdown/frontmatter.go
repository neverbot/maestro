package markdown

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// The bounds on one document's content. Every one of them is checked
// before a byte reaches Postgres, and every one is reported as the
// caller's own argument at path `content`.
const (
	// MaxBodyBytes bounds the whole content a caller may submit,
	// frontmatter included.
	//
	// 1 MiB is far more prose than any real document — a long quest
	// script is tens of kilobytes — and comfortably under
	// maxContentRequestBodyBytes (4 MiB, api_metamodel.go), so a caller
	// that trips this gets a field-level refusal it can act on rather
	// than a 413 over the whole request.
	//
	// **It is eight times the bound the search index applies, and it is
	// not a backstop behind it.** 0007_documents.sql generates
	// `documents.search` over `left(body_md, 131072)`, so for any body
	// between 131072 characters and this bound the database's `left()`
	// is the binding constraint: such a document is stored whole, read
	// back whole, and *indexed by its head alone*, with nothing
	// recording the truncation. That is a deliberate trade — the body is
	// prose a designer must get back byte for byte, and a 128 K-character
	// head is more than enough to find a document by — and Task 9's
	// search tool description discloses it the way the metamodel's own
	// search discloses its query bound.
	MaxBodyBytes = 1 << 20

	// MaxFrontmatterBytes bounds the fenced block alone. YAML parsing is
	// the one place in this package where a caller's bytes drive an
	// arbitrary-depth parser, and a bound is cheaper than reasoning
	// about it. It is checked before the parser runs, not after.
	MaxFrontmatterBytes = 16 << 10

	// MaxTitleLen and MaxSummaryLen bound the two *derived* columns, in
	// runes.
	//
	// They truncate rather than refuse, which is the opposite of every
	// other bound here, and the difference is who chose the value: a
	// path or a body is what the caller wrote, and a refusal tells them
	// to change it; a title is what Maestro derived, and refusing a
	// document because the sentence Maestro picked out of it was long
	// would be refusing a caller for our own choice. Truncation is by
	// rune, so it cannot split a UTF-8 sequence.
	//
	// Both are far below the 131072-character bound
	// 0007_documents.sql's generated search vector applies to
	// `left(title, …)` and `left(summary, …)`, so unlike the body these
	// two are always indexed whole: the database's `left()` can never
	// bite a value this function produced.
	MaxTitleLen   = 200
	MaxSummaryLen = 500
)

// fence is the frontmatter delimiter. Three hyphens on a line of their
// own, YAML's own document marker and the convention every markdown
// tool uses.
const fence = "---"

// Content is one document's content, split into the parts the row
// stores.
//
// **Body is exactly what the caller wrote after the frontmatter block**,
// byte for byte including its trailing newline, because the body an
// agent reads back must be the body it wrote (spec §6). Nothing here
// normalises line endings, trims trailing whitespace or re-wraps
// anything: a script's blank lines are the writing.
//
// FrontmatterJSON is the same mapping as Frontmatter, encoded for the
// jsonb column, and is `{}` — never empty, never null — when there was
// no frontmatter. Both are carried because the row stores the JSON and
// the derivation below reads the map.
type Content struct {
	Frontmatter     map[string]any
	FrontmatterJSON []byte
	Body            string
	Title           string
	Summary         string
}

// SplitContent bounds a caller's content, splits any frontmatter off it,
// and derives the title and summary the row stores.
//
// path is used only as the last fallback for the title, and is not
// re-validated here: CheckPath owns that, and doing it twice would give
// one bad path two different messages depending on which call happened
// to run first.
//
// **Frontmatter is parsed and never interpreted.** It is decoded so it
// can be stored as jsonb and echoed back intact, and so `title` and
// `summary` can be read out of it — which is a derivation of two display
// columns, not a meaning. Nothing else in Maestro ever reads a
// frontmatter key: no link is derived from it, no field, no relation
// (spec §1, §7).
func SplitContent(path, content string) (Content, error) {
	if len(content) > MaxBodyBytes {
		return Content{}, invalidInput("content", fmt.Sprintf(
			"is too long (%d bytes, the most a document takes is %d): "+
				"split it into several documents rather than one enormous one",
			len(content), MaxBodyBytes))
	}
	// Before the control-character scan, because utf8.DecodeRune turns an
	// invalid byte into U+FFFD, which is not a control character — so a
	// scan alone lets an invalid sequence through, and Postgres then
	// refuses it with SQLSTATE 22021 as an untyped server fault over the
	// caller's own bytes. metamodel.checkSearchQuery records the same
	// ordering and the same reason, and
	// TestInvalidUTF8IsRefusedBeforePostgresSeesIt pins it here.
	if !utf8.ValidString(content) {
		return Content{}, invalidInput("content",
			"is not valid UTF-8: a byte in it does not decode as any character, "+
				"and Postgres refuses that outright")
	}
	if i := strings.IndexFunc(content, isForbiddenControl); i >= 0 {
		return Content{}, invalidInput("content", fmt.Sprintf(
			"holds a control character (%U at byte %d): prose may carry newlines, "+
				"carriage returns and tabs, and nothing else below U+0020",
			[]rune(content[i:])[0], i))
	}

	raw, body, err := splitFence(content)
	if err != nil {
		return Content{}, err
	}

	out := Content{Frontmatter: map[string]any{}, Body: body}
	if raw != "" {
		if out.Frontmatter, err = parseFrontmatter(raw); err != nil {
			return Content{}, err
		}
	}

	encoded, err := json.Marshal(out.Frontmatter)
	if err != nil {
		// Reachable, and not a server fault. YAML has values JSON does
		// not: `.nan` and `.inf` decode to float64 NaN and +Inf, which
		// json.Marshal refuses outright — and they are exactly the two
		// values a numeric bound would have compared false against
		// without noticing. It is the caller's frontmatter and the
		// caller's fix, so it is invalid_input.
		// TestFrontmatterHoldingAValueJSONCannotCarryIsRefused pins it.
		return Content{}, invalidInput("content", fmt.Sprintf(
			"has frontmatter holding a value that cannot be stored as JSON (%v): "+
				"values must be text, finite numbers, booleans, or lists or maps of them", err))
	}
	out.FrontmatterJSON = encoded

	out.Title = truncateRunes(deriveTitle(out.Frontmatter, body, path), MaxTitleLen)
	out.Summary = truncateRunes(deriveSummary(out.Frontmatter, body), MaxSummaryLen)
	return out, nil
}

// parseFrontmatter decodes one fenced block into the mapping the jsonb
// column stores.
//
// The happy path is one decode straight into map[string]any, which is
// also what folds a non-string scalar key (`1: one`) into the string key
// jsonb requires. A second decode happens only on failure, and only to
// tell two refusals apart that a caller fixes differently: YAML that
// does not parse at all, and YAML that parses into something that is not
// a mapping — a list, or a bare scalar. Reporting the second as "not
// valid YAML" would send a caller looking for a syntax error that is not
// there.
func parseFrontmatter(raw string) (map[string]any, error) {
	var mapping map[string]any
	err := yaml.Unmarshal([]byte(raw), &mapping)
	if err == nil {
		if mapping == nil {
			// A block of blank lines decodes to a nil map, which is not
			// a failure and not a mapping either.
			mapping = map[string]any{}
		}
		return mapping, nil
	}

	var anyValue any
	if yaml.Unmarshal([]byte(raw), &anyValue) == nil {
		return nil, invalidInput("content", fmt.Sprintf(
			"opens with a frontmatter block that is not a set of key/value pairs (%T): "+
				"write `title: Duskwood` lines, or drop the leading --- fence so the "+
				"whole thing is prose", anyValue))
	}
	return nil, invalidInput("content", fmt.Sprintf(
		"opens with a frontmatter block that is not valid YAML (%v): "+
			"fix it, or drop the leading --- fence so the whole thing is prose", err))
}

// isForbiddenControl reports whether r is a control character prose may
// not carry. Newline, carriage return and tab are the three a document
// legitimately holds; every other control character in a body is a
// caller assembling text wrongly, and telling it so at the first one is
// more useful than storing whatever it meant. The NUL byte is among
// them, which matters because Postgres refuses a NUL in a text value
// with the same SQLSTATE 22021 an invalid sequence raises.
func isForbiddenControl(r rune) bool {
	switch r {
	case '\n', '\r', '\t':
		return false
	}
	return unicode.IsControl(r)
}

// splitFence separates a leading frontmatter block from the body.
//
// **An unterminated fence is body, not an error.** A document that opens
// with a horizontal rule is legal markdown and a designer will write
// one; refusing it would make that document unwritable, and the refusal
// would name a feature the author was not using.
//
// The opening fence must be the very first line of the content, and the
// closing fence must be a line that is exactly "---" — so a body line of
// "----" or "--- " does not close the block, and a "---" line halfway
// down a document does not open one.
func splitFence(content string) (frontmatter, body string, err error) {
	rest, ok := strings.CutPrefix(content, fence+"\n")
	if !ok {
		rest, ok = strings.CutPrefix(content, fence+"\r\n")
	}
	if !ok {
		return "", content, nil
	}
	// A block that closes on the very first line: "---\n---\n". Checked
	// before the search below, because that search looks for a *newline*
	// followed by the fence and there is no newline before this one.
	for _, closing := range []string{fence + "\n", fence + "\r\n"} {
		if after, found := strings.CutPrefix(rest, closing); found {
			return "", after, nil
		}
	}
	for _, closing := range []string{"\n" + fence + "\n", "\n" + fence + "\r\n"} {
		if i := strings.Index(rest, closing); i >= 0 {
			block := rest[:i+1]
			if len(block) > MaxFrontmatterBytes {
				return "", "", invalidInput("content", fmt.Sprintf(
					"opens with a frontmatter block of %d bytes, and the most one takes is %d: "+
						"frontmatter is metadata Maestro never reads, so put the material in the body",
					len(block), MaxFrontmatterBytes))
			}
			return block, rest[i+len(closing):], nil
		}
	}
	return "", content, nil
}

// deriveTitle is the spec's rule, in order: frontmatter if it carries a
// non-empty string `title`, else the first ATX `# ` heading in the body,
// else the path. Never authored twice, so a document always has a
// title and a caller never has to send one.
func deriveTitle(frontmatter map[string]any, body, path string) string {
	if title, ok := stringValue(frontmatter, "title"); ok {
		return title
	}
	for _, line := range strings.Split(body, "\n") {
		if heading, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "# "); ok {
			if heading = strings.TrimSpace(heading); heading != "" {
				return heading
			}
		}
	}
	return path
}

// deriveSummary is the same rule one level down: frontmatter `summary`,
// else the first line of the body that is neither blank nor a heading,
// else nothing. An empty summary is a fine answer — a document whose
// first line is a heading and whose second is blank has no one-line
// gist, and inventing one would put words in the author's mouth in a
// listing.
func deriveSummary(frontmatter map[string]any, body string) string {
	if summary, ok := stringValue(frontmatter, "summary"); ok {
		return summary
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

// stringValue reads one frontmatter key as a non-empty string. A key
// holding a number, a list or a map is not an error — frontmatter is
// inert and a project may put anything under `title` it likes — it
// simply does not supply the derivation, and the next rule applies.
func stringValue(frontmatter map[string]any, key string) (string, bool) {
	value, ok := frontmatter[key].(string)
	if !ok {
		return "", false
	}
	if value = strings.TrimSpace(value); value == "" {
		return "", false
	}
	return value, true
}

// truncateRunes cuts a derived value to at most n runes, never splitting
// a UTF-8 sequence.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
