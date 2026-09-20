package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The guards that keep the design pass from decaying one screen at a
// time.
//
// The token guards already refuse a colour declared in one theme and not
// the other, and a token nothing reads. These are their equivalent for
// the *vocabulary*: a screen that hand-rolls something the product has a
// shared shape for should fail something, rather than looking slightly
// wrong to whoever notices next.
//
// Both of them exist because a review found the fault they now guard,
// twice each, on different screens.

func shellSources(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("static", "*.html"))
	if err != nil {
		t.Fatalf("glob shells: %v", err)
	}
	out := map[string]string{}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		out[filepath.Base(path)] = string(body)
	}
	if len(out) == 0 {
		t.Fatal("no shells found under static/")
	}
	return out
}

// emptyStateHole is any element a shell gives an id ending in -empty or
// -miss: by convention in this front end, that id is a hole a page fills
// when a list has nothing in it. The capture is the whole element, so
// the guard below can see whether the shell put anything inside it.
var emptyStateHole = regexp.MustCompile(`<(\w+)([^>]*\bid="[\w-]*(?:-empty|-miss)"[^>]*)>([\s\S]*?)</\w+>`)

// TestEveryEmptyStateIsAHoleAndNotAShape holds the one negative state
// this product has.
//
// The 2026-09-09 audit counted four different shapes for "there is
// nothing here" on a single screen: a filled box with a coloured left
// stripe, two bare grey sentences, and a nine-line paragraph explaining
// the MCP API in a 200px lane. Three said the same thing in a different
// voice and the fourth wore a treatment the identity bans by name.
//
// The first fix gave them all `class="state"` and left each shell
// writing its own heading and sentence inside it, which is the shared
// *class* and not the shared component: ten copies of one shape, each
// free to drift, and one of them — the entity page's — never rendered at
// all, because that page replaces its content wholesale and nobody
// noticed the markup was dead.
//
// So a shell declares the hole and nothing else. The shape arrives from
// pages/page.js's negativeState, through fillState, at the call site that
// knows what the words are — including the half that depends on who is
// reading. A hole that is empty on screen is visible; markup that is
// present and wrong is not.
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
//
// A hole is only better than markup if something puts a state in it. The
// shell no longer says anything, so a page that forgets its fillState
// call renders a blank gap where "there is nothing here" should be, and
// nothing anywhere is red: an empty div is exactly what an empty div
// looks like. This is the project's "a mechanism nothing reads is a lie"
// rule pointed at its own fix.
func TestEveryNegativeStateHoleIsFilledBySomething(t *testing.T) {
	t.Parallel()
	modules, err := filepath.Glob("static/**/*.js")
	if err != nil {
		t.Fatalf("glob modules: %v", err)
	}
	top, err := filepath.Glob("static/*.js")
	if err != nil {
		t.Fatalf("glob modules: %v", err)
	}
	modules = append(modules, top...)
	var source strings.Builder
	for _, path := range modules {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		source.Write(body)
	}
	if source.Len() == 0 {
		t.Fatal("no front-end modules found under static/")
	}
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
//
// `textarea` is deliberately absent: no shell has one, and a rule for a
// control nothing renders is the "mechanism nothing reads" this project
// deletes rather than keeps. The day a shell gains one, this list gains
// it in the same change — which is a line of this test, which is a
// conversation.
var controlsTheStylesheetMustDress = []string{"button", "input", "select"}

// TestTheStylesheetDressesEveryLightDomControl is the light-DOM half of
// the shadow-boundary guard.
//
// `select` was never written in styles.css. The design pass that found
// five of six controls rendering as browser defaults fixed them inside
// the shadow roots, added a guard for exactly that, and did not carry
// the rule to the page — so the prose page's two version pickers shipped
// as `2px inset` browser defaults filled `#e9e9ed`, a cold blue-grey
// that exists nowhere in this palette, beside a serif page title.
//
// It asserts a rule exists, not that it is right: what a control looks
// like is a design decision and this is a check that one was made.
func TestTheStylesheetDressesEveryLightDomControl(t *testing.T) {
	t.Parallel()
	// **The page's controls are dressed by static/controls.css**, which
	// styles.css imports and every shadow root adopts: one statement,
	// read by both sides of the boundary. This test reads the pair,
	// because "the page dresses its controls" is now true of the two
	// files together and of neither alone.
	body, err := os.ReadFile(filepath.Join("static", "styles.css"))
	if err != nil {
		t.Fatalf("read styles.css: %v", err)
	}
	shared, err := os.ReadFile(filepath.Join("static", "controls.css"))
	if err != nil {
		t.Fatalf("read controls.css: %v", err)
	}
	// And the import is what makes the page read it at all: without that
	// line the vocabulary exists and reaches nothing.
	if !strings.Contains(string(body), `@import url("/static/controls.css")`) {
		t.Fatal("styles.css does not import the control vocabulary, so the page is dressed by nothing")
	}
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
	if len(missing) > 0 {
		t.Fatalf("styles.css writes no rule for %s.\n\n"+
			"A control the stylesheet does not dress is a control the platform dresses, and the "+
			"platform's is a bevelled grey box that exists nowhere in this palette. The shadow-root "+
			"half of this is TestEveryShadowRootAdoptsTheSharedControls; this is the page's half.",
			strings.Join(missing, ", "))
	}
}
