package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// The escaping perimeter for a templating library.
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
	assert.Must(t, err == nil, "walk static: %v", err)
	return scanned, offences
}

func TestNoOwnModuleReachesForARawHTMLDirective(t *testing.T) {
	t.Parallel()
	scanned, offences := scanForUnsafeDirectives(t)
	assert.Must(t, len(scanned) != 0, "scanned no module under internal/web/static: a walk that reads nothing guards nothing")
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
	t.Parallel()
	planted := `import { unsafeHTML } from "lit/directives/unsafe-html.js";`
	assert.Should(t, unsafeDirectiveRE.MatchString(planted), "the scan missed a planted directive import: %q", planted)
	for _, benign := range []string{
		"return html`<td>${cell.text}</td>`;",
		`import { LitElement, css, html, nothing } from "lit";`,
	} {
		assert.Should(t, !unsafeDirectiveRE.MatchString(benign), "the scan read ordinary Lit as a raw-HTML directive: %q", benign)
	}

	// The comment strip is what keeps this file and mst-twin.js from
	// failing on their own prose, and codeLines is where it happens.
	commented := "// unsafeHTML is exactly what this component must never reach for\nconst a = 1;\n"
	for _, line := range codeLines(commented) {
		assert.Should(t, !unsafeDirectiveRE.MatchString(line.code), "the scan read a comment as a call: %q", line.text)
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
	assert.Should(t, found, "the raw-HTML directive scan never read %s; it read %v", want, scanned)
}
