package markdown

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/neverbot/maestro/internal/metamodel"
)

// The bounds on one document's content. Every one of them is checked
// before a byte reaches Postgres, and every one is reported as the
// caller's own argument at path `content`.
const (
	// MaxBodyBytes bounds the whole content a caller may submit,
	// frontmatter included.
	MaxBodyBytes = 1 << 20

	// MaxFrontmatterBytes bounds the fenced block alone. YAML parsing is
	// the one place in this package where a caller's bytes drive an
	// arbitrary-depth parser, and a bound is cheaper than reasoning
	// about it. It is checked before the parser runs, not after.
	MaxFrontmatterBytes = 16 << 10

	// MaxTitleLen and MaxSummaryLen bound the two *derived* columns, in
	// runes.
	MaxTitleLen   = 200
	MaxSummaryLen = 500
)

// fence is the frontmatter delimiter. Three hyphens on a line of their
// own, YAML's own document marker and the convention every markdown
// tool uses.
const fence = "---"

// Content is one document's content, split into the parts the row
// stores.
type Content struct {
	Frontmatter     map[string]any
	FrontmatterJSON []byte
	Body            string
	Title           string
	Summary         string
}

// SplitContent bounds a caller's content, splits any frontmatter off it,
// and derives the title and summary the row stores.
func SplitContent(path, content string) (Content, error) {
	if len(content) > MaxBodyBytes {
		return Content{}, invalidInput("content", fmt.Sprintf(
			"is too long (%d bytes, the most a document takes is %d): "+
				"split it into several documents rather than one enormous one",
			len(content), MaxBodyBytes))
	}
	// metamodel.CheckText is the judgement, this package's is the
	// wording. Sharing the scan is what keeps a third copy of it from
	// being written — errors.go takes the metamodel dependency precisely
	// to avoid "a second, drifting copy of each", and this was one.
	if fault, bad := metamodel.CheckText(content, "\n\r\t"); bad {
		if fault.InvalidUTF8 {
			return Content{}, invalidInput("content",
				"is not valid UTF-8: a byte in it does not decode as any character, "+
					"and Postgres refuses that outright")
		}
		return Content{}, invalidInput("content", fmt.Sprintf(
			"holds a control character (%U at byte %d): prose may carry newlines, "+
				"carriage returns and tabs, and nothing else below U+0020",
			fault.Rune, fault.Offset))
	}
	// A leading byte-order mark, refused by name rather than reinterpreted.
	if strings.HasPrefix(content, "\ufeff") {
		return Content{}, invalidInput("content",
			"opens with a byte-order mark (U+FEFF): it sits before the --- fence, so "+
				"frontmatter would not be recognised — write the content without it")
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

	// The two keys the derivation reads are the two the body's own
	// allowance does not cover. A body keeps its newlines because it is
	// prose; a title and a summary are rendered as one line — a page
	// heading, a listing column, a picker row — which is the same
	// reasoning metamodel's textProblem gives for refusing a newline in a
	// name. Storing a two-line title would put the break somewhere no
	// surface can show it, so it is refused where the caller can fix it.
	for _, key := range []string{"title", "summary"} {
		value, ok := out.Frontmatter[key].(string)
		if !ok {
			continue
		}
		if fault, bad := metamodel.CheckText(value, ""); bad && !fault.InvalidUTF8 {
			return Content{}, invalidInput("content", fmt.Sprintf(
				"has a frontmatter `%s` holding a control character (%U at byte %d): "+
					"it becomes one line of a listing, so write it on one line — "+
					"the body below the fence is where prose keeps its line breaks",
				key, fault.Rune, fault.Offset))
		}
	}

	out.Title = truncateRunes(deriveTitle(out.Frontmatter, body, path), MaxTitleLen)
	out.Summary = truncateRunes(deriveSummary(out.Frontmatter, body), MaxSummaryLen)
	return out, nil
}

// parseFrontmatter decodes one fenced block into the mapping the jsonb
// column stores.
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
		// Named in the caller's own vocabulary, not Go's. An agent told
		// its frontmatter is "[]interface {}" is being shown the type of
		// the variable we decoded into, which is a fact about this
		// function and not about the document.
		shape := "a single value"
		if _, isList := anyValue.([]any); isList {
			shape = "a list"
		}
		return nil, invalidInput("content", fmt.Sprintf(
			"opens with a frontmatter block that is %s rather than a set of "+
				"key/value pairs: write `title: Duskwood` lines, or drop the leading "+
				"--- fence so the whole thing is prose", shape))
	}
	return nil, invalidInput("content", fmt.Sprintf(
		"opens with a frontmatter block that is not valid YAML (%v): "+
			"fix it, or drop the leading --- fence so the whole thing is prose", err))
}

// splitFence separates a leading frontmatter block from the body.
func splitFence(content string) (frontmatter, body string, err error) {
	opening := openingFence(content)
	if opening == 0 {
		return "", content, nil
	}
	rest := content[opening:]

	// A block that closes on the very first line: "---\n---\n", and
	// "---\n---" at end of file. Checked before the search below, because
	// that search looks for a *newline* followed by the fence and there
	// is no newline before this one.
	if closing := openingFence(rest); closing > 0 {
		return "", rest[closing:], nil
	}
	if rest == fence {
		return "", "", nil
	}

	at, width := closingFence(rest)
	if at < 0 {
		return "", content, nil
	}
	block := rest[:at+1]
	if len(block) > MaxFrontmatterBytes {
		return "", "", invalidInput("content", fmt.Sprintf(
			"opens with a frontmatter block of %d bytes, and the most one takes is %d: "+
				"frontmatter is metadata Maestro never reads, so put the material in the body",
			len(block), MaxFrontmatterBytes))
	}
	return block, rest[at+width:], nil
}

// openingFence reports how many bytes of s are a fence line at its very
// start — 0 if there is none. The longest spelling wins, which is the
// same "by position, not by list order" rule closingFence states; for a
// prefix the two spellings cannot both match, so today it only makes the
// answer independent of the order they are written in.
func openingFence(s string) int {
	width := 0
	for _, opening := range []string{fence + "\n", fence + "\r\n"} {
		if strings.HasPrefix(s, opening) && len(opening) > width {
			width = len(opening)
		}
	}
	return width
}

// closingFence finds the closing fence in s: the index of the newline
// that begins it, and the width to skip to reach the body. It returns
// -1 when there is none.
func closingFence(s string) (at, width int) {
	at = -1
	consider := func(i, w int) {
		if i >= 0 && (at < 0 || i < at) {
			at, width = i, w
		}
	}
	for _, closing := range []string{"\n" + fence + "\n", "\n" + fence + "\r\n"} {
		consider(strings.Index(s, closing), len(closing))
	}
	if eof := "\n" + fence; strings.HasSuffix(s, eof) {
		consider(len(s)-len(eof), len(eof))
	}
	return at, width
}

// deriveTitle is the spec's rule, in order: frontmatter if it carries a
// non-empty string `title`, else the first ATX `# ` heading in the body,
// else the path. Never authored twice, so a document always has a
// title and a caller never has to send one.
func deriveTitle(frontmatter map[string]any, body, path string) string {
	if title, ok := stringValue(frontmatter, "title"); ok {
		return title
	}
	title := ""
	eachProseLine(body, func(line string) bool {
		if heading, ok := strings.CutPrefix(line, "# "); ok {
			if heading = strings.TrimSpace(heading); heading != "" {
				title = heading
				return true
			}
		}
		return false
	})
	if title != "" {
		return title
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
	summary := ""
	eachProseLine(body, func(line string) bool {
		line = strings.TrimSpace(line)
		if line == "" || isATXHeading(line) {
			return false
		}
		summary = line
		return true
	})
	return summary
}

// eachProseLine walks the body's lines and calls fn with each one that
// is the document's own prose, stopping as soon as fn returns true.
func eachProseLine(body string, fn func(line string) bool) {
	marker, run := byte(0), 0
	for rest := body; ; {
		line, more, found := strings.Cut(rest, "\n")
		rest = more
		line = strings.TrimRight(line, "\r")

		if char, length := codeFence(line); length > 0 {
			switch {
			case run == 0:
				marker, run = char, length
			case char == marker && length >= run:
				run = 0
			}
		} else if run == 0 && fn(line) {
			return
		}
		if !found {
			return
		}
	}
}

// codeFence reports the marker and length of a code fence line: three or
// more backticks or tildes, after up to three spaces of indentation.
// An unclosed fence runs to the end of the document, which is
// CommonMark's own rule and means a stray fence hides the rest of the
// body from the derivation rather than showing a code sample in a
// listing — the safer of the two failures.
func codeFence(line string) (marker byte, length int) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || trimmed == "" {
		return 0, 0
	}
	marker = trimmed[0]
	if marker != '`' && marker != '~' {
		return 0, 0
	}
	for length < len(trimmed) && trimmed[length] == marker {
		length++
	}
	if length < 3 {
		return 0, 0
	}
	return marker, length
}

// isATXHeading reports whether a line is a markdown heading, which needs
// whitespace after its hashes: `#1 rule of Duskwood` is a sentence, and
// skipping it as a heading made the only line of that document invisible
// to the summary.
func isATXHeading(line string) bool {
	hashes := 0
	for hashes < len(line) && line[hashes] == '#' {
		hashes++
	}
	if hashes == 0 || hashes > 6 {
		return false
	}
	return hashes == len(line) || line[hashes] == ' ' || line[hashes] == '\t'
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
