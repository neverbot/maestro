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

// The guards that keep the design pass from decaying one screen at a
// time.

func shellSources(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("static", "*.html"))
	assert.Must(t, err == nil, "glob shells: %v", err)
	out := map[string]string{}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		assert.Must(t, err == nil, "read %s: %v", path, err)
		out[filepath.Base(path)] = string(body)
	}
	assert.Must(t, len(out) != 0, "no shells found under static/")
	return out
}

// emptyStateHole is any element a shell gives an id ending in -empty or
// -miss: by convention in this front end, that id is a hole a page fills
// when a list has nothing in it. The capture is the whole element, so
// the guard below can see whether the shell put anything inside it.
var emptyStateHole = regexp.MustCompile(`<(\w+)([^>]*\bid="[\w-]*(?:-empty|-miss)"[^>]*)>([\s\S]*?)</\w+>`)

// TestEveryEmptyStateIsAHoleAndNotAShape holds the one negative state
// this product has.
func TestEveryEmptyStateIsAHoleAndNotAShape(t *testing.T) {
	t.Parallel()
	var offences []string
	for name, body := range shellSources(t) {
		for _, found := range emptyStateHole.FindAllStringSubmatch(body, -1) {
			tag, attrs, inside := found[1], found[2], found[3]
			if tag != "div" {
				offences = append(offences, name+": <"+tag+attrs+"> is not a div")
				continue
			}
			if strings.Contains(attrs, "class=") {
				offences = append(offences, name+": <div"+attrs+"> carries a class")
			}
			if strings.TrimSpace(inside) != "" {
				offences = append(offences, name+": <div"+attrs+"> has markup inside it")
			}
		}
	}
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("%d negative state(s) are written in the shell:\n  %s\n\n"+
			"A shell declares an empty `<div id=\"…-empty\" hidden></div>` and nothing more. The bold "+
			"ink line, the muted sentence and the at-most-one link that design.md specifies are built "+
			"by negativeState in static/pages/page.js and put in the hole by fillState, at the call "+
			"site that knows the words. A shell that writes its own is the eleventh copy of one shape.",
			len(offences), strings.Join(offences, "\n  "))
	}
}

// holeID pulls the id out of one of those holes.
var holeID = regexp.MustCompile(`\bid="([\w-]*(?:-empty|-miss))"`)

// TestEveryNegativeStateHoleIsFilledBySomething is the other half of the
// guard above, and it is the half that matters.
func TestEveryNegativeStateHoleIsFilledBySomething(t *testing.T) {
	t.Parallel()
	modules, err := filepath.Glob("static/**/*.js")
	assert.Must(t, err == nil, "glob modules: %v", err)
	top, err := filepath.Glob("static/*.js")
	assert.Must(t, err == nil, "glob modules: %v", err)
	modules = append(modules, top...)
	var source strings.Builder
	for _, path := range modules {
		body, err := os.ReadFile(path)
		assert.Must(t, err == nil, "read %s: %v", path, err)
		source.Write(body)
	}
	assert.Must(t, source.Len() != 0, "no front-end modules found under static/")
	filled := source.String()

	var orphans []string
	for name, body := range shellSources(t) {
		for _, found := range emptyStateHole.FindAllStringSubmatch(body, -1) {
			id := holeID.FindStringSubmatch(found[2])
			if id == nil {
				continue
			}
			if !strings.Contains(filled, `fillState(doc, "`+id[1]+`"`) &&
				!strings.Contains(filled, `fillState(document, "`+id[1]+`"`) {
				orphans = append(orphans, name+": #"+id[1])
			}
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		t.Fatalf("%d negative state hole(s) nothing fills:\n  %s\n\n"+
			"A shell declares an empty div and a module fills it through fillState. A hole nothing "+
			"fills is a blank gap on screen where the words should be, and it looks exactly like a "+
			"hole that is correctly empty.",
			len(orphans), strings.Join(orphans, "\n  "))
	}
}

// controlsTheStylesheetMustDress is every control a shell actually puts
// in the light DOM. components/control-styles.js dresses the ones inside
// shadow roots and has its own guard; this is the other half.
var controlsTheStylesheetMustDress = []string{"button", "input", "select"}

// TestTheStylesheetDressesEveryLightDomControl is the light-DOM half of
// the shadow-boundary guard.
func TestTheStylesheetDressesEveryLightDomControl(t *testing.T) {
	t.Parallel()
	// **The page's controls are dressed by static/controls.css**, which
	// styles.css imports and every shadow root adopts: one statement,
	// read by both sides of the boundary. This test reads the pair,
	// because "the page dresses its controls" is now true of the two
	// files together and of neither alone.
	body, err := os.ReadFile(filepath.Join("static", "styles.css"))
	assert.Must(t, err == nil, "read styles.css: %v", err)
	shared, err := os.ReadFile(filepath.Join("static", "controls.css"))
	assert.Must(t, err == nil, "read controls.css: %v", err)
	// And the import is what makes the page read it at all: without that
	// line the vocabulary exists and reaches nothing.
	assert.Must(t, strings.Contains(string(body), `@import url("/static/controls.css")`), "styles.css does not import the control vocabulary, so the page is dressed by nothing")
	sheet := string(body) + "\n" + string(shared)

	var missing []string
	for _, control := range controlsTheStylesheetMustDress {
		// The selector at the start of a rule, alone or in a list:
		// `select {`, `input,\nselect {`. A rule that only ever scopes it
		// to something else (`#compare-form select`) does not count,
		// because the next shell to use one gets nothing.
		bare := regexp.MustCompile(`(?m)^` + control + `\s*[,{]`)
		inList := regexp.MustCompile(`(?m)^` + control + `,$`)
		if !bare.MatchString(sheet) && !inList.MatchString(sheet) {
			missing = append(missing, control)
		}
	}
	assert.Must(t, len(missing) <= 0, "styles.css writes no rule for %s.\n\n"+
		"A control the stylesheet does not dress is a control the platform dresses, and the "+
		"platform's is a bevelled grey box that exists nowhere in this palette. The shadow-root "+
		"half of this is TestEveryShadowRootAdoptsTheSharedControls; this is the page's half.",
		strings.Join(missing, ", "))
}
