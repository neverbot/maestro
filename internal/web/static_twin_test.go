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
