package web

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// This file mechanises the one line this whole sub-project turns on:
//
//	A tool description teaches one call. A skill page teaches what spans
//	calls — a sequence, a decision taken before any call, or a
//	consequence that only appears later.
//
// Its operational form, which is what the scanner below implements, is
// the one-tool test: a sentence that names exactly one registered tool
// and makes a claim about that tool's arguments, admitted values,
// defaults, bounds or refusals belongs in that tool's description. The
// bundle may route to it, and may quote it verbatim with attribution; it
// may not reword it.
//
// It is not a test file, deliberately. The pages of this bundle are
// written against this notion of a sentence and of an attributed quote,
// and rules only a test file can see are rules no page author can read.

// bundleSentence is one sentence of one bundle file, carrying the
// position that lets a failure say "modelling/naming.md:41" rather than
// "somewhere in the bundle".
type bundleSentence struct {
	Path string
	Line int
	// Text is the sentence with its markdown decoration stripped and its
	// whitespace flattened onto one line, which is the form every
	// comparison in this file is made in: bundle prose is hard-wrapped
	// and a registered description is not, so a byte-for-byte comparison
	// would fail on every real quote and the guard would be turned off
	// within a week of being written.
	Text string
	// InQuote is true when the sentence sits inside an attributed quote
	// block — the one place a claim about a single tool may appear in the
	// bundle.
	InQuote bool
	// QuoteFor is the tool the enclosing block is attributed to. It is
	// **not** trusted: TestTheBundleRestatesNoToolDescription requires it
	// to name a registered tool and requires the sentence to be a verbatim
	// substring of that tool's description, so an attribution cannot be
	// forged by writing the header and then writing whatever one likes
	// underneath it.
	QuoteFor string
}

// quoteOpener recognises the one attribution form the bundle uses:
//
//	> **From `views.validate`'s own description:**
//	> Every refusal is addressed by JSON pointer into the document.
//
// One form, closed, because a family of forms is a family of ways to
// look attributed without being checkable.
var quoteOpener = regexp.MustCompile("^>\\s*\\*\\*From `([a-z_]+(?:\\.[a-z_]+)+)`'s own description:\\*\\*\\s*$")

// indexBullet recognises the generated tool index's own line shape:
//
//   - `entities.get` — Read one entity by its address.
//
// That bullet is an attributed quote in every sense this file cares
// about — it names the tool and carries text out of that tool's
// description — so it is read as one rather than exempted. The
// difference matters: an exemption would let reference/tools.md say
// anything at all about a tool, while this makes every bullet on the
// page subject to the same verbatim-substring check as a hand-written
// quote. Reword a description without regenerating and the page goes
// red here as well as in TestToolReferenceIsCurrent.
var indexBullet = regexp.MustCompile("^-\\s+`([a-z_]+(?:\\.[a-z_]+)*)`\\s+—\\s+(.*)$")

// toolShaped matches a token that looks like a tool name. Whole tokens
// only: matching by substring is how this repository has broken four
// guards in a row, most recently by reading `views.list_assets` as a
// mention of `views.list`, so the scanner never asks "does this text
// contain that name" and instead asks "which whole tokens are there,
// and which of them does the server register".
var toolShaped = regexp.MustCompile(`[a-z_]+(?:\.[a-z_]+)+`)

// claimMarkers is the closed list of words that turn a mention of a tool
// into a claim about its contract. Closed on purpose: a heuristic would
// drift, and each of these appears in refusals, bounds or defaults that
// a registered description already states.
var claimMarkers = []string{
	"must", "is required", "required to", "is refused", "refuses",
	"defaults to", "default is", "at most", "no more than",
	"never", "always", "returns", "accepts", "takes", "one of",
}

// markerPatterns compiles claimMarkers with word boundaries, so
// "takes" is a marker and "mistakes" is not, and "must" is a marker and
// "mustard" is not.
var markerPatterns = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(claimMarkers))
	for _, marker := range claimMarkers {
		out = append(out, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(marker)+`\b`))
	}
	return out
}()

// claimMarkerIn returns the first claim marker the sentence carries, or
// "" for a sentence that makes no claim.
func claimMarkerIn(text string) string {
	for i, pattern := range markerPatterns {
		if pattern.MatchString(text) {
			return claimMarkers[i]
		}
	}
	return ""
}

// toolsNamedIn returns the registered tools a sentence names, sorted and
// deduplicated.
func toolsNamedIn(text string, registered map[string]string) []string {
	seen := map[string]bool{}
	for _, token := range toolShaped.FindAllString(text, -1) {
		if _, ok := registered[token]; ok {
			seen[token] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// flatten puts a fragment on one line with single spaces. Every
// comparison against a registered description happens in this form,
// on both sides.
func flatten(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// scanBundle splits every file of the bundle into sentences.
//
// It walks the whole tree with fs.WalkDir rather than a list of pages,
// for the reason every guard in this package does: a rule that watches
// the files it was written for stops watching the moment somebody adds
// one.
//
// Markdown headings, list items and table cells are sentences too — a
// claim does not stop being a claim for being a bullet — and JSON files
// are scanned through their string values, so a genre transcript that
// grows an explanatory comment restating a bound is caught by the same
// rule as a page of prose.
//
// **Paragraphs are joined before they are split.** The bundle is
// hard-wrapped at seventy-odd columns, so a sentence about a tool
// routinely spans two lines with the tool's name on one and the claim
// marker on the other. A line-at-a-time splitter sees two harmless
// halves and reports nothing, which is a scanner that has stopped
// working while looking exactly like a bundle that is clean.
func scanBundle(fsys fs.FS) ([]bundleSentence, error) {
	var out []bundleSentence
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".json":
			sentences, err := scanJSON(name, body)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			out = append(out, sentences...)
		default:
			out = append(out, scanMarkdown(name, string(body))...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// block is one run of lines that belong to a single sentence stream: a
// paragraph, a bullet, a table cell, a heading, or the body of one
// attributed quote.
type block struct {
	line     int
	text     string
	inQuote  bool
	quoteFor string
}

func scanMarkdown(name, body string) []bundleSentence {
	var out []bundleSentence
	var blocks []block
	var current *block
	quoteFor := ""

	flush := func() {
		if current != nil {
			blocks = append(blocks, *current)
			current = nil
		}
	}
	// start opens a new block, because a bullet, a heading and a table
	// row each end the previous sentence stream even with no blank line
	// between them.
	start := func(line int, text string) {
		flush()
		current = &block{line: line, text: text, inQuote: quoteFor != "", quoteFor: quoteFor}
	}

	for i, raw := range strings.Split(body, "\n") {
		line := i + 1
		trimmed := strings.TrimSpace(raw)

		if strings.HasPrefix(trimmed, "```") {
			// A fence line is not prose. Its contents are: a vocabulary
			// fence and a worked example both belong to the page, and a
			// restatement hidden in a comment inside a fence is still a
			// restatement.
			flush()
			continue
		}
		if trimmed == "" {
			flush()
			quoteFor = ""
			continue
		}
		if match := quoteOpener.FindStringSubmatch(trimmed); match != nil {
			flush()
			quoteFor = match[1]
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			// Inside a quote block; strip the marker and keep going.
			text := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			if text == "" {
				flush()
				continue
			}
			if current == nil || !current.inQuote {
				start(line, text)
			} else {
				current.text += " " + text
			}
			current.inQuote = quoteFor != ""
			current.quoteFor = quoteFor
			continue
		}
		// A line outside a `>` block ends any quote that was open.
		quoteFor = ""

		if match := indexBullet.FindStringSubmatch(trimmed); match != nil {
			// The generated index's own shape: attributed by naming the
			// tool, and held to the verbatim rule like any other quote.
			start(line, match[2])
			current.inQuote = true
			current.quoteFor = match[1]
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			start(line, strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
			flush()
			continue
		}
		if strings.HasPrefix(trimmed, "|") {
			// A table row: every cell is its own sentence stream, so a
			// claim in one cell is not diluted by the cell beside it.
			flush()
			for _, cell := range strings.Split(strings.Trim(trimmed, "|"), "|") {
				cell = strings.TrimSpace(cell)
				if cell == "" || strings.Trim(cell, "-: ") == "" {
					continue
				}
				blocks = append(blocks, block{line: line, text: cell})
			}
			continue
		}
		if isListItem(trimmed) {
			start(line, stripListMarker(trimmed))
			continue
		}
		if current == nil {
			start(line, trimmed)
		} else {
			current.text += " " + trimmed
		}
	}
	flush()

	for _, b := range blocks {
		for _, sentence := range splitSentences(b.text) {
			out = append(out, bundleSentence{
				Path: name, Line: b.line, Text: sentence,
				InQuote: b.inQuote, QuoteFor: b.quoteFor,
			})
		}
	}
	return out
}

var listMarker = regexp.MustCompile(`^(?:[-*+]\s+|\d+\.\s+)`)

func isListItem(line string) bool { return listMarker.MatchString(line) }

func stripListMarker(line string) string {
	return strings.TrimSpace(listMarker.ReplaceAllString(line, ""))
}

// scanJSON walks a transcript's string values. Keys are walked too: a
// key is authored text like any other, and `"entities.upsert takes at
// most 500"` is no less a restatement for being a field name.
func scanJSON(name string, body []byte) ([]bundleSentence, error) {
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	var out []bundleSentence
	var walk func(node any)
	walk = func(node any) {
		switch value := node.(type) {
		case string:
			for _, sentence := range splitSentences(value) {
				out = append(out, bundleSentence{Path: name, Line: 0, Text: sentence})
			}
		case []any:
			for _, item := range value {
				walk(item)
			}
		case map[string]any:
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(key)
				walk(value[key])
			}
		}
	}
	walk(decoded)
	return out, nil
}

// splitSentences cuts one flattened fragment into sentences.
//
// The rule is a full stop, question mark or exclamation mark followed by
// a space or by the end of the fragment — which is what keeps the dot
// inside `entities.upsert` from cutting a sentence in half, since that
// dot is followed by a letter. Abbreviations that end in a dot and are
// followed by a space are the one form that rule gets wrong, and they
// are skipped from a closed list.
func splitSentences(fragment string) []string {
	flat := flatten(fragment)
	if flat == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(flat); i++ {
		switch flat[i] {
		case '.', '!', '?':
		default:
			continue
		}
		if i+1 < len(flat) && flat[i+1] != ' ' {
			continue
		}
		if flat[i] == '.' && isAbbreviationEnd(flat[:i+1]) {
			continue
		}
		if piece := strings.TrimSpace(flat[start : i+1]); piece != "" {
			out = append(out, piece)
		}
		start = i + 1
	}
	if piece := strings.TrimSpace(flat[start:]); piece != "" {
		out = append(out, piece)
	}
	return out
}

// restatement is one crossing of the line, with everything a
// contributor needs to fix it without reading this file.
type restatement struct {
	Sentence bundleSentence
	Tool     string
	Marker   string
	Reason   string
}

func (r restatement) String() string {
	subject := fmt.Sprintf("a claim about exactly one tool (%s, marker %q)", r.Tool, r.Marker)
	if r.Marker == "" {
		subject = fmt.Sprintf("text attributed to one tool (%s)", r.Tool)
	}
	return fmt.Sprintf(
		"%s:%d: %q is %s and belongs in that tool's description: %s. Route to the tool "+
			"instead, or quote it verbatim under \"> **From `%s`'s own description:**\".",
		r.Sentence.Path, r.Sentence.Line, r.Sentence.Text, subject, r.Reason, r.Tool)
}

// auditRestatement is the one-tool test, applied to a whole bundle.
//
// Two rules, one per side of the quote marker:
//
//   - **Outside a quote**, a sentence that names exactly one registered
//     tool and carries a claim marker is a crossing. Two tools is a
//     sequence, which is the bundle's own subject; no marker is routing,
//     which the bundle is for.
//   - **Inside an attributed quote**, every sentence must be a verbatim
//     substring — whitespace flattened — of the description it is
//     attributed to, marker or no marker. That half is not about what the
//     page claims but about whether its copy is still a copy, and it is
//     checked on every quoted sentence because the interesting failure is
//     the one nobody edited: the description reworded underneath a page
//     that has not changed.
//
// The quoted half is the reason the attributed tool is a claim subject in
// its own right. A quote's body routinely does not repeat the tool's name
// — the generated index's bullets never do, since the name is in the
// bullet's own backticks — so a rule that only judged sentences naming a
// tool would exempt exactly the text most likely to drift.
//
// Both halves of the quote exemption are load-bearing and both have been
// broken before in this repository. Attribution to a tool the server
// does not register is refused, so the header cannot be forged by
// naming something that has no description to check against. An empty
// sentence, or an attributed tool whose description is empty, is refused
// rather than admitted — `strings.Contains(anything, "")` is true, and a
// guard whose exemption is satisfied by emptiness is a guard that can be
// switched off by deleting text.
func auditRestatement(sentences []bundleSentence, registered map[string]string) []restatement {
	var out []restatement
	for _, sentence := range sentences {
		named := toolsNamedIn(sentence.Text, registered)
		marker := claimMarkerIn(sentence.Text)
		if sentence.InQuote {
			out = append(out, judgeQuote(sentence, named, marker, registered)...)
			continue
		}
		if len(named) == 1 && marker != "" {
			out = append(out, restatement{
				Sentence: sentence, Tool: named[0], Marker: marker,
				Reason: "it is not inside an attributed quote block",
			})
		}
	}
	return out
}

// judgeQuote holds an attributed quote to being a quote.
//
// The order of the checks is the point. A quoted sentence passes on one
// condition only — it is a whitespace-flattened verbatim substring of
// the description it is attributed to — and every other outcome is a
// report. In particular a quote whose body mentions some *other* tool is
// fine when it is verbatim (registered descriptions cross-reference each
// other constantly, and the generated index carries their first
// sentences), and is a mis-attribution when it is not.
func judgeQuote(sentence bundleSentence, named []string, marker string, registered map[string]string) []restatement {
	subject := sentence.QuoteFor
	if len(named) == 1 {
		subject = named[0]
	}
	fail := func(reason string) []restatement {
		return []restatement{{Sentence: sentence, Tool: subject, Marker: marker, Reason: reason}}
	}
	description, ok := registered[sentence.QuoteFor]
	if !ok {
		return fail(fmt.Sprintf("the quote is attributed to %q, which this server does not "+
			"register, so there is no description for it to be a quote of", sentence.QuoteFor))
	}
	text := flatten(sentence.Text)
	if text == "" || flatten(description) == "" {
		return fail("the quote or the description it claims to copy is empty, and every " +
			"string contains the empty string")
	}
	if strings.Contains(flatten(description), text) {
		return nil
	}
	if len(named) == 1 && named[0] != sentence.QuoteFor {
		return fail(fmt.Sprintf("the quote is attributed to %s and the claim is about %s",
			sentence.QuoteFor, named[0]))
	}
	return fail("it is inside a quote attributed to that tool, but it is not a verbatim " +
		"substring of that tool's registered description: either the page reworded it, or " +
		"the description was reworded underneath the page")
}

// toolToken is a `dotted.token` found inside backticks somewhere in the
// bundle, with where it was found.
type toolToken struct {
	Path string
	Line int
	Name string
}

// backtickedToken matches the whole content of a pair of backticks when
// that content is dotted and otherwise word-shaped. Anchored to the
// backticks so `reference/tools.md` — which carries a slash — is not
// read as a tool name at all.
var backtickedToken = regexp.MustCompile("`([a-z_]+(?:\\.[a-z_]+)+)`")

// fileExtensions are the trailing segments that make a dotted token a
// filename rather than a tool. The bundle names its own pages
// constantly (`skill.md`, `mmorpg.json`), and without this every one of
// them would be reported as an unregistered tool — a false positive
// that gets a scanner deleted, which is how this repository lost a
// denylist that read `request` as `quest`.
var fileExtensions = map[string]bool{
	"md": true, "json": true, "go": true, "sql": true, "zip": true,
	"yaml": true, "yml": true, "html": true, "css": true, "js": true,
	"txt": true, "svg": true, "png": true, "toml": true,
}

// bundleToolTokens collects every backticked, tool-shaped token in the
// bundle, with its position. It walks the whole tree, markdown and JSON
// alike: a transcript is authored text too.
//
// This is what makes the ship-order rule mechanical rather than
// remembered. A page that mentions `analysis.cycles` before any
// analysis tool is registered is a failing build, not a review comment.
func bundleToolTokens(fsys fs.FS) ([]toolToken, error) {
	var out []toolToken
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			for _, match := range backtickedToken.FindAllStringSubmatch(line, -1) {
				token := match[1]
				segments := strings.Split(token, ".")
				if fileExtensions[segments[len(segments)-1]] {
					continue
				}
				out = append(out, toolToken{Path: name, Line: i + 1, Name: token})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// backtickedWord matches the whole content of a pair of backticks when it
// is word-shaped, with or without dots. Unlike backtickedToken it is not
// a guess at what a tool name looks like: it is only ever used with the
// registered table in hand, which is why it can afford to admit `search`
// and `whoami` — two registered tools with no dot in their names, and
// therefore two tools a dotted-token scan is structurally blind to.
var backtickedWord = regexp.MustCompile("`([a-z_]+(?:\\.[a-z_]+)*)`")

// bundleToolMentions finds every mention of a registered tool in the
// bundle, by name, in backticks.
//
// It exists because the dotted-token scan above cannot see an unprefixed
// tool: `whoami` and `search` carry no dot, and a routing guard built on
// the dotted scan alone would report them as unrouted forever while
// looking like it had checked them.
func bundleToolMentions(fsys fs.FS, registered map[string]string) ([]toolToken, error) {
	var out []toolToken
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			for _, match := range backtickedWord.FindAllStringSubmatch(line, -1) {
				if _, ok := registered[match[1]]; ok {
					out = append(out, toolToken{Path: name, Line: i + 1, Name: match[1]})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
