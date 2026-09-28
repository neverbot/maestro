package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// **app.js binds by id at module scope, and every shell imports it.**
func TestNoShellStealsAnIdAppJSBindsAtModuleScope(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("static/app.js")
	assert.Must(t, err == nil, "read app.js: %v", err)

	// Only the module-scope lookups: a `getElementById` inside a
	// function runs when that function is called, on the page that
	// called it, and is nobody else's business. Module scope is column
	// zero in this file.
	moduleScope := regexp.MustCompile(`(?m)^(?:const|let|var) \w+ = document\.getElementById\("([a-z0-9-]+)"\)`)
	bound := map[string]bool{}
	for _, m := range moduleScope.FindAllStringSubmatch(withoutComments(string(source)), -1) {
		bound[m[1]] = true
	}
	assert.Must(t, len(bound) != 0, "app.js binds nothing at module scope, so this guard holds nothing")

	// The two shells app.js drives, and the only ones allowed to carry
	// the ids it binds.
	itsOwn := map[string]bool{"login.html": true, "index.html": true}

	idRE := regexp.MustCompile(`id="([a-z0-9-]+)"`)
	htmlComments := regexp.MustCompile(`(?s)<!--.*?-->`)
	entries, err := os.ReadDir("static")
	assert.Must(t, err == nil, "read static/: %v", err)
	var offences []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".html") || itsOwn[entry.Name()] {
			continue
		}
		body, err := os.ReadFile(filepath.Join("static", entry.Name()))
		assert.Must(t, err == nil, "read %s: %v", entry.Name(), err)
		// Comments out first: this guard's own first run failed on the
		// note in admin.html explaining why the id is not `invite`,
		// which is the same shape of mistake as the one it catches.
		markup := htmlComments.ReplaceAllString(string(body), " ")
		for _, m := range idRE.FindAllStringSubmatch(markup, -1) {
			if bound[m[1]] {
				offences = append(offences, entry.Name()+` carries id="`+m[1]+`", which app.js binds for the sign-in and picker shells`)
			}
		}
	}
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("%d shell id(s) collide with app.js's own:\n%s", len(offences), strings.Join(offences, "\n"))
	}
}
