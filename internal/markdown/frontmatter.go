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
	//
	// 1 MiB is far more prose than any real document — a long quest
	// script is tens of kilobytes — and under maxContentRequestBodyBytes
	// (4 MiB, api_metamodel.go), so a caller that trips this usually gets
	// a field-level refusal it can act on rather than a 413 over the
	// whole request. **Usually, not always**: the body arrives inside a
	// JSON string, and Go's encoder escapes `<`, `>` and `&` as six-byte
	// `\u003c` sequences, so an angle-bracket-heavy body of about 700 KiB
	// already exceeds 4 MiB on the wire and is refused by size before
	// this bound is ever consulted. The field-level refusal is what a
	// caller gets for ordinary prose, not a guarantee for every encoding.
	//
	// **It is looser than the bound the search index applies, and it is
	// not a backstop behind it.** 0007_documents.sql generates
	// `documents.search` over `left(body_md, 131072)`, so for any body
	// between 131072 characters and this bound the database's `left()`
	// is the binding constraint: such a document is stored whole, read
	// back whole, and *indexed by its head alone*, with nothing
	// recording the truncation. The two bounds are not the same unit —
	// 1048576 **bytes** here against 131072 **characters** there — so
	// the ratio is 8 for ASCII prose and about 2.7 for CJK. The
	// load-bearing half holds at every encoding: the Go bound is the
	// looser one, so the database's silent truncation is what binds
	// first. That is a deliberate trade — the body is
	// prose a designer must get back byte for byte, and a 128 K-character
	// head is more than enough to find a document by — and Task 9's
	// search tool description discloses it the way the metamodel's own
	// search discloses its query bound.
	MaxBodyBytes = 1 << 20

	// MaxFrontmatterBytes bounds the fenced block alone. YAML parsing is
	// the one place in this package where a caller's bytes drive an
	// arbitrary-depth parser, and a bound is cheaper than reasoning
	// about it. It is checked before the parser runs, not after.
	//
	// **It is not what defends against a billion-laughs expansion, and
	// must not be mistaken for it.** Measured against every bomb shape —
	// nested aliases, an alias fan-out, a merge-key chain —
	// gopkg.in/yaml.v3's own alias budget refuses each one in single-digit
	// milliseconds, long before memory moves, and it does so at any block
	// size including ones far under this bound. What this bound buys is
	// bounding *parse work* on a block no document needs: a caller that
	// wants to spend a second of CPU on 16 MiB of legal YAML cannot. If
	// gopkg.in/yaml.v3 is ever swapped for a parser without an alias
	// guard, that defence leaves with it and this constant will not
	// replace it — check the replacement's own guard first.
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
	// metamodel.CheckText is the judgement, this package's is the
	// wording. Sharing the scan is what keeps a third copy of it from
	// being written — errors.go takes the metamodel dependency precisely
	// to avoid "a second, drifting copy of each", and this was one.
	//
	// The allowance is the one thing this caller states for itself: a
	// body keeps its newlines, its carriage returns and its tabs, because
	// it is prose stored byte for byte and a CRLF document is an ordinary
	// document. Every other text in Maestro is one line and keeps none of
	// the three.
	//
	// The encoding is checked before the control scan, and that ordering
	// is CheckText's own: ranging over a string turns an invalid byte
	// into U+FFFD, which is not a control character, so a scan alone lets
	// an invalid sequence through and Postgres then refuses it with
	// SQLSTATE 22021 as an untyped server fault over the caller's own
	// bytes. TestInvalidUTF8IsReportedBeforeAControlCharacter pins the
	// order with an input carrying both problems.
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
	//
	// U+FEFF is category Cf, not Cc, so the scan above passes it — and
	// then the opening fence no longer sits at byte 0, so a document that
	// plainly opens with frontmatter is silently taken as prose whose
	// first line is the mark and the fence together. Every derived value
	// is then wrong and nothing says why.
	//
	// It is refused rather than stripped for the reason CheckText gives
	// for a control character: deleting part of what a caller wrote
	// answers a question it did not ask, and here the deletion would be
	// invisible in a value the caller reads back. Only a *leading* mark
	// is refused; inside the body it is an ordinary zero-width character
	// and prose is not ours to judge.
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
	//
	// Only a string is checked: a `title` holding a number or a list is
	// inert frontmatter that does not supply the derivation at all
	// (stringValue), and refusing it would be interpreting frontmatter,
	// which this package does not do.
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
//
// **An unterminated fence is body, not an error.** A document that opens
// with a horizontal rule is legal markdown and a designer will write
// one; refusing it would make that document unwritable, and the refusal
// would name a feature the author was not using. A fence terminated by
// *end of file* is a different thing entirely and closes the block: see
// closingFence.
//
// The opening fence must be the very first line of the content, and the
// closing fence must be a line that is exactly "---" — so a body line of
// "----" or "--- " does not close the block, and a "---" line halfway
// down a document does not open one.
//
// Both fences come in an LF and a CRLF spelling, and **each is resolved
// by position rather than by the order the spellings are listed in**.
// That is the whole of correction 11: taking the first spelling that
// matches anywhere let an LF "---" further down the body beat the CRLF
// fence that really closed the block, so the block ran past it,
// yaml.Unmarshal kept only its first document, and the prose in between
// was deleted from the body with nothing said. Mixed line endings are
// routine in agent-assembled content.
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
//
// **The earliest match wins**, across every spelling, which is what
// makes a CRLF block above an LF body split where the author wrote it.
//
// **End of file closes a block.** Jekyll, Hugo and goldmark's own
// frontmatter extension all accept "---\ntitle: X\n---" with no trailing
// newline, and a metadata-only document written without one is an
// ordinary thing for an agent composing a JSON string. Treating it as an
// unterminated fence stored the YAML as prose, fell the title back to
// the path and made the summary the literal "---". The rule "an
// unterminated fence is body" is about a *missing* fence; a fence
// terminated by EOF is not missing.
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
//
// **It skips fenced code blocks**, and that is the one piece of markdown
// this derivation knows. Without it a `# ` line inside a fence became
// the title and the fence itself became the summary — so a document
// whose first example happens to contain a sample heading was listed
// under a line out of someone else's snippet. Both derived columns are
// read by ten later tasks and rendered in a listing, and the failure is
// invisible in the stored body.
//
// **This is deliberately a line scan and not goldmark's AST.** Goldmark
// is a declared dependency and Task 11 parses every document with it for
// the reading view, so the fuller derivation is available; it is not
// taken here because this runs on the *write* path for two short strings,
// where a full parse of a megabyte body would be paid on every write and
// every revert, and because handing the derivation to a parser changes
// more than the two cases at hand — setext headings, lazy continuation
// lines, HTML blocks and link reference definitions would all start
// deciding titles, silently and differently from today. The remaining
// gaps are known and small: an indented (four-space) code block is not
// recognised, a fence's info string is not parsed, and a `#` heading
// inside a blockquote or a list item still counts. If Task 11's parse is
// ever moved to the write path, derive from its AST and delete this.
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
