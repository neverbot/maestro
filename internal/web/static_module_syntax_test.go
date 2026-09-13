package web_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// **Every module this repository ships must parse as a module.**
//
// `node --check` does not answer that question: it parses a file as a
// script, where a stray backtick inside a template literal is often
// still valid, so it has said "fine" three times over a file the browser
// refused outright. Each time the symptom was the same and took the same
// twenty minutes to trace: a comment written inside a Lit `css` or
// `html` template used backticks around an identifier, closed the
// template, and broke the whole module graph — every harness failing
// with `Unexpected identifier 'h1'`, and the page rendering three
// quarters of itself in a browser.
//
// `node --input-type=module --check` reads the same bytes as a module
// and refuses them, which is what a browser does.
//
// Mutation: put backticks around a word in a comment inside any `css` or
// `html` template in internal/web/static/components and this fails
// naming the file and the line.
func TestEveryModuleParsesAsAModule(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed; this guard needs the runtime the browser is closest to")
	}
	modules := ownModules(t)
	if len(modules) == 0 {
		t.Fatal("no module was examined, so this guard holds nothing")
	}
	for _, module := range modules {
		source, err := os.ReadFile(module)
		if err != nil {
			t.Fatalf("read %s: %v", module, err)
		}
		cmd := exec.Command("node", "--input-type=module", "--check")
		cmd.Stdin = strings.NewReader(string(source))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s does not parse as a module:\n%s", module, out)
		}
	}
}
