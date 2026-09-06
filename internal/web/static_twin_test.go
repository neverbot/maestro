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

// The escaping perimeter for a templating library.
//
// internal/web/static_sinks_test.go holds the DOM-shaped half: no own
// asset assigns `innerHTML`, `outerHTML`, `insertAdjacentHTML`,
// `setHTMLUnsafe`, `createContextualFragment` or `document.write`,
// outside the one allowed sink in doc.js. That list is complete for a
// module that touches the DOM directly, and it is blind to the way this
// front end now writes markup, which is Lit.
//
// **Lit's escape hatches are imports, not property assignments.**
// `unsafeHTML`, `unsafeSVG` and the static-html tags (`literal`,
// `unsafeStatic`, and `html` re-exported from `lit/static-html.js`) each
// take a string and put it into the parsed markup, which is exactly the
// hole the sink perimeter exists to close and which none of its
// spellings match. The text twin is where this stops being theoretical:
// it renders more of the game's own words than any other surface —
// names, keys, types, projected values and the column headings the query
// chose — and a game's words are hostile input. This repository has
// already shipped one stored cross-site scripting defect, an autolink
// path with no dangerous-URL check beside an ordinary link path that had
// one, so "the ordinary path is safe" is a sentence with a history here.
//
// What makes the property hold today is arrangement rather than
// vigilance: those directives live in packages this repository has not
// vendored, and internal/web/static_vendor_test.go's map guards mean an
// unmapped bare specifier resolves to nothing in a browser. This test is
// the part that fails *loudly* rather than at runtime, on the day
// somebody vendors one.
//
// internal/web/jstest/twin_test.mjs holds the other side: that every
// game string the twin renders is bound in child position, where Lit
// commits it as a Text node, and that none of them appears in the
// component's own markup.
var unsafeDirectiveRE = regexp.MustCompile(
	`\bunsafeHTML\b|\bunsafeSVG\b|\bunsafeStatic\b|\bstatic-html\b|\bstaticHtml\b|\bwithStatic\b`)

// ownModuleExtensions is what this scan reads. It is spelled out here
// rather than shared with the sink perimeter's walk for the reason Task
// 2 recorded about the outbound-URL scan: a shared list narrowed in one
// place narrows the scan and its own expectation together, and the
// mutation meant to turn it red leaves it green.
var ownModuleExtensions = map[string]bool{".js": true, ".mjs": true}

// scanForUnsafeDirectives walks internal/web/static, skipping the
// vendored subtree — lit-core.min.js is upstream code pinned by hash and
// is not ours to edit — and returns what it read alongside every hit.
func scanForUnsafeDirectives(t *testing.T) (scanned []string, offences []string) {
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
		if !ownModuleExtensions[filepath.Ext(path)] {
			return nil
		}
		scanned = append(scanned, filepath.ToSlash(path))
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range codeLines(string(raw)) {
			if unsafeDirectiveRE.MatchString(line.code) {
				offences = append(offences, path+":"+strconv.Itoa(line.number)+": "+line.text)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static: %v", err)
	}
	return scanned, offences
}

func TestNoOwnModuleReachesForARawHTMLDirective(t *testing.T) {
	scanned, offences := scanForUnsafeDirectives(t)
	if len(scanned) == 0 {
		t.Fatal("scanned no module under internal/web/static: a walk that reads nothing guards nothing")
	}
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("%d raw-HTML directive(s) in this front end's own modules:\n%s\n"+
			"a game's own words reach these templates, and a directive that parses a string as markup is "+
			"how they stop being text",
			len(offences), strings.Join(offences, "\n"))
	}
	t.Logf("raw-HTML directive scan read %d own module(s)", len(scanned))
}

// TestTheRawDirectiveScanReadsWhatItClaimsTo is the guard on the guard,
// in both directions: it finds a planted import, it does not read the
// component that renders the most hostile strings in this repository as
// one, and it does not mistake a mention in a comment for a call —
// which matters because this file's own doc comment names every
// spelling it looks for, and mst-twin.js's names the property.
func TestTheRawDirectiveScanReadsWhatItClaimsTo(t *testing.T) {
	planted := `import { unsafeHTML } from "lit/directives/unsafe-html.js";`
	if !unsafeDirectiveRE.MatchString(planted) {
		t.Errorf("the scan missed a planted directive import: %q", planted)
	}
	for _, benign := range []string{
		"return html`<td>${cell.text}</td>`;",
		`import { LitElement, css, html, nothing } from "lit";`,
	} {
		if unsafeDirectiveRE.MatchString(benign) {
			t.Errorf("the scan read ordinary Lit as a raw-HTML directive: %q", benign)
		}
	}

	// The comment strip is what keeps this file and mst-twin.js from
	// failing on their own prose, and codeLines is where it happens.
	commented := "// unsafeHTML is exactly what this component must never reach for\nconst a = 1;\n"
	for _, line := range codeLines(commented) {
		if unsafeDirectiveRE.MatchString(line.code) {
			t.Errorf("the scan read a comment as a call: %q", line.text)
		}
	}

	// And it really did open the twin, rather than passing because the
	// walk skipped the directory the components live in.
	scanned, _ := scanForUnsafeDirectives(t)
	want := "static/components/mst-twin.js"
	found := false
	for _, path := range scanned {
		if path == want {
			found = true
		}
	}
	if !found {
		t.Errorf("the raw-HTML directive scan never read %s; it read %v", want, scanned)
	}
}
