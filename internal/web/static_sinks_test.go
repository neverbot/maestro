package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file widens the HTML-sink perimeter that
// TestAppScriptNeverWritesRawHTML (static_appjs_test.go) and
// TestTheDocumentScriptHasExactlyOneHTMLSink (static_docjs_test.go)
// hold between them, and it is a third test rather than an edit to
// either because the two of them answer file-specific questions
// ("app.js has none", "doc.js has exactly one, inside setRenderedHTML")
// that are worth keeping as they are.
//
// **What was wrong with the perimeter.** Those two tests name `app.js`
// and `doc.js` literally, so a third script dropped into
// internal/web/static/ was scanned by neither, and the four HTML shells
// were never scanned at all — a shell may carry an inline <script>, and
// nothing said it did not. Their shared expression also matched only
// four spellings of a sink: it missed `+=`, `setHTMLUnsafe`,
// `Range.createContextualFragment`, `document.writeln` and a property
// reached by a computed name (`el["innerHTML"]`).
//
// **What this test does not catch, stated so the comment above does not
// read as more than it is.** A sink whose property name is assembled at
// runtime from fragments (`el["inner" + "HTML"]`) or from a variable
// (`el[prop]`) is invisible to any grep, this one included; catching
// that needs a parser and a dataflow, which is a different tool. Nor
// does it read anything outside internal/web/static — the Go templates
// and handlers are covered by their own tests. What it does hold is
// that every literal spelling of a DOM markup write, in every asset
// this server ships to a browser, is either absent or named below.
var htmlSinkNames = regexp.MustCompile(
	`\binnerHTML\b|\bouterHTML\b|\binsertAdjacentHTML\b|\bsetHTMLUnsafe\b|` +
		`\bcreateContextualFragment\b|\bdocument\s*\.\s*write\b|\bdocument\s*\.\s*writeln\b`)

// allowedHTMLSinks is the explicit exception list: the whole source line
// of every markup write this server's static assets are allowed to
// contain, keyed by the file it lives in. Matching the whole line rather
// than a line number keeps the exception stable under edits above it,
// and keeps it specific enough that a *second* sink on the same file
// fails here even if it uses the same spelling.
//
// There is exactly one entry, and it is doc.js's one sink — the
// reading view's, whose two companion tests pin that it lives inside
// setRenderedHTML and is only ever fed a rendered view's own `html`
// field.
var allowedHTMLSinks = map[string]map[string]bool{
	"doc.js": {
		`el.innerHTML = typeof html === "string" ? html : "";`: true,
	},
}

// TestNoStaticAssetWritesRawHTMLOutsideTheOneAllowedSink discovers every
// asset under internal/web/static rather than naming any of them, so a
// file added tomorrow is scanned on the day it lands.
func TestNoStaticAssetWritesRawHTMLOutsideTheOneAllowedSink(t *testing.T) {
	scanned := 0
	var offences []string

	err := filepath.WalkDir("static", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch filepath.Ext(path) {
		case ".js", ".mjs", ".html":
		default:
			return nil
		}
		scanned++
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		base := filepath.Base(path)
		for _, hit := range sinkOccurrences(string(raw)) {
			if allowedHTMLSinks[base][hit.text] {
				continue
			}
			offences = append(offences, path+":"+strconv.Itoa(hit.line)+": "+hit.text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static: %v", err)
	}

	// A guard that silently scanned nothing would pass forever. Four
	// shells, two scripts today; the assertion is only that the walk
	// found files at all, so adding one does not fail this line.
	if scanned == 0 {
		t.Fatal("scanned no assets under internal/web/static: the walk found nothing to guard")
	}
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("%d unlisted HTML sink(s) under internal/web/static:\n%s\n"+
			"every markup write must go through doc.js's setRenderedHTML, or be added to allowedHTMLSinks with an argument",
			len(offences), strings.Join(offences, "\n"))
	}
}

type sinkHit struct {
	line int    // 1-based
	text string // the whole trimmed source line
}

// sinkOccurrences returns every line of src naming a DOM markup sink,
// with comments removed first.
//
// **Comments are stripped rather than excluded by the pattern**, which
// is what lets this scan for the bare identifier and so catch `+=` and
// a computed property name — the shared expression in the two older
// tests had to insist on `.innerHTML =` precisely because app.js's own
// comments say "never innerHTML" four times and would otherwise fail it.
// Only whole-line comments are dropped (a line whose first non-space
// characters are `//`, `/*`, `*` or `<!--`, and everything up to the
// matching `-->` for the last): a partial-line strip would have to know
// a `//` inside a string literal from a real comment, and a sink placed
// after code on a line that also carries a trailing comment is still on
// that line's code half, which survives.
func sinkOccurrences(src string) []sinkHit {
	var out []sinkHit
	inBlock := false
	inHTMLComment := false
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		code := trimmed
		switch {
		case inHTMLComment:
			if idx := strings.Index(trimmed, "-->"); idx >= 0 {
				inHTMLComment = false
				code = strings.TrimSpace(trimmed[idx+len("-->"):])
			} else {
				continue
			}
		case inBlock:
			if idx := strings.Index(trimmed, "*/"); idx >= 0 {
				inBlock = false
				code = strings.TrimSpace(trimmed[idx+len("*/"):])
			} else {
				continue
			}
		case strings.HasPrefix(trimmed, "//"):
			continue
		case strings.HasPrefix(trimmed, "<!--"):
			if !strings.Contains(trimmed, "-->") {
				inHTMLComment = true
				continue
			}
			code = ""
		case strings.HasPrefix(trimmed, "/*"):
			if !strings.Contains(trimmed, "*/") {
				inBlock = true
				continue
			}
			code = ""
		case strings.HasPrefix(trimmed, "*"):
			// The continuation line of a block comment written in the
			// doc-comment style. Dropping it costs nothing: a line of
			// real code never starts with a bare `*`.
			continue
		}
		if code == "" {
			continue
		}
		if htmlSinkNames.MatchString(code) {
			out = append(out, sinkHit{line: i + 1, text: trimmed})
		}
	}
	return out
}
