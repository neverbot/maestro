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

// vendorDir is the one subtree of internal/web/static this perimeter
// does not police. Minified upstream code contains sink spellings —
// lit-core.min.js writes into shadow roots for a living — and it is not
// ours to edit, so a hit there could only ever be waived, and a waiver
// list of minified lines would be unreadable and unmaintainable. What
// stands in its place is static_vendor_test.go: every vendored file is
// pinned by SHA-256 to an exact upstream release, so the question this
// perimeter asks of our own code ("did somebody add a sink?") is
// answered there by "is this byte-for-byte the file we vetted?".
//
// The skip is explicit, and TestTheSinkPerimeterCoversEveryOwnModule
// below asserts it skipped exactly this subtree and nothing else: an
// exclusion nothing checks is how a real module ends up unscanned.
const vendorDir = "static/vendor"

// scanForHTMLSinks walks internal/web/static, skipping the vendored
// subtree, and returns the paths it actually read alongside every
// unlisted sink it found. It returns the scanned list rather than a
// count so a second test can hold what the perimeter covers, which is
// the half that used to be wrong: the two older tests named app.js and
// doc.js literally and a third script was scanned by neither.
func scanForHTMLSinks(t *testing.T) (scanned []string, offences []string) {
	t.Helper()
	err := filepath.WalkDir("static", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if filepath.ToSlash(path) == vendorDir {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".js", ".mjs", ".html":
		default:
			return nil
		}
		scanned = append(scanned, filepath.ToSlash(path))
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
	return scanned, offences
}

// TestNoStaticAssetWritesRawHTMLOutsideTheOneAllowedSink discovers every
// asset under internal/web/static rather than naming any of them, so a
// file added tomorrow is scanned on the day it lands.
func TestNoStaticAssetWritesRawHTMLOutsideTheOneAllowedSink(t *testing.T) {
	scanned, offences := scanForHTMLSinks(t)

	// A guard that silently scanned nothing would pass forever. Four
	// shells, three scripts today; the assertion is only that the walk
	// found files at all, so adding one does not fail this line.
	if len(scanned) == 0 {
		t.Fatal("scanned no assets under internal/web/static: the walk found nothing to guard")
	}
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("%d unlisted HTML sink(s) under internal/web/static:\n%s\n"+
			"every markup write must go through doc.js's setRenderedHTML, or be added to allowedHTMLSinks with an argument",
			len(offences), strings.Join(offences, "\n"))
	}
}

// TestTheSinkPerimeterCoversEveryOwnModule is the guard on the guard.
// The perimeter above passes when it finds nothing, and it finds nothing
// both when the assets are clean and when it never looked at them — the
// exact confusion that let two named files stand in for a directory
// before this file existed, and the exact confusion an added `SkipDir`
// can reintroduce in one line. So this test enumerates the tree by a
// separate walk and asserts three things the perimeter cannot assert
// about itself: every own asset was scanned, no vendored file was, and
// the skipped subtree was not empty (a skip that skips nothing would
// make the second assertion pass for the wrong reason).
func TestTheSinkPerimeterCoversEveryOwnModule(t *testing.T) {
	scanned, _ := scanForHTMLSinks(t)
	seen := map[string]bool{}
	for _, path := range scanned {
		seen[path] = true
	}

	var missing, vendored []string
	vendorFiles := 0
	err := filepath.WalkDir("static", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		slashed := filepath.ToSlash(path)
		inVendor := strings.HasPrefix(slashed, vendorDir+"/")
		if inVendor {
			vendorFiles++
		}
		switch filepath.Ext(path) {
		case ".js", ".mjs", ".html":
		default:
			return nil
		}
		switch {
		case inVendor && seen[slashed]:
			vendored = append(vendored, slashed)
		case !inVendor && !seen[slashed]:
			missing = append(missing, slashed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static: %v", err)
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%d own asset(s) the HTML-sink perimeter never read:\n%s\n"+
			"a module nobody scans is a module that may write markup",
			len(missing), strings.Join(missing, "\n"))
	}
	if len(vendored) > 0 {
		sort.Strings(vendored)
		t.Errorf("the perimeter read %d vendored file(s) it is documented to skip:\n%s",
			len(vendored), strings.Join(vendored, "\n"))
	}
	if vendorFiles == 0 {
		t.Errorf("%s holds no files: the vendor skip above is excluding nothing, so this test's "+
			"second assertion would pass whatever the skip did", vendorDir)
	}
	t.Logf("HTML-sink perimeter read %d own asset(s); %s holds %d file(s), none of them read",
		len(scanned), vendorDir, vendorFiles)
}

type sinkHit struct {
	line int    // 1-based
	text string // the whole trimmed source line
}

// sinkOccurrences returns every line of src naming a DOM markup sink,
// with comments removed first by codeLines.
//
// **Comments are stripped rather than excluded by the pattern**, which
// is what lets this scan for the bare identifier and so catch `+=` and
// a computed property name — the shared expression in the two older
// tests had to insist on `.innerHTML =` precisely because app.js's own
// comments say "never innerHTML" four times and would otherwise fail it.
func sinkOccurrences(src string) []sinkHit {
	var out []sinkHit
	for _, line := range codeLines(src) {
		if htmlSinkNames.MatchString(line.code) {
			out = append(out, sinkHit{line: line.number, text: line.text})
		}
	}
	return out
}

// codeLine is one source line that survived comment stripping: `code` is
// the part codeLines judged to be code, `text` the whole trimmed line as
// it appears in the file, which is what a failure message should quote.
type codeLine struct {
	number int // 1-based
	code   string
	text   string
}

// codeLines splits src into lines and drops the ones that are entirely
// comment. It is shared by this file's HTML-sink perimeter and by
// static_vendor_test.go's outbound-URL scan, because both need the same
// distinction and a second implementation of it would be a second place
// for the distinction to be wrong.
//
// Only whole-line comments are dropped (a line whose first non-space
// characters are `//`, `/*`, `*` or `<!--`, and everything up to the
// matching `-->` or `*/` for the last two): a partial-line strip would
// have to know a `//` inside a string literal from a real comment, and a
// sink or a URL placed after code on a line that also carries a trailing
// comment is still on that line's code half, which survives. The error
// this makes is therefore always in the loud direction — a comment
// sharing a line with code is read as code — which is the right way
// round for a guard.
func codeLines(src string) []codeLine {
	var out []codeLine
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
		out = append(out, codeLine{number: i + 1, code: code, text: trimmed})
	}
	return out
}
