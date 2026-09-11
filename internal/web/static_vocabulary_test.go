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

// emptyStateElement is any element a shell gives an id ending in -empty
// or -miss: by convention in this front end, that id is a hole a page
// fills when a list has nothing in it.
var emptyStateElement = regexp.MustCompile(`<(\w+)([^>]*\bid="[\w-]*(?:-empty|-miss)"[^>]*)>`)

// TestEveryEmptyStateIsTheSharedShape holds the one negative state this
// product has.
//
// The 2026-09-09 audit counted four different shapes for "there is
// nothing here" on a single screen: a filled box with a coloured left
// stripe, two bare grey sentences, and a nine-line paragraph explaining
// the MCP API in a 200px lane. Three said the same thing in a different
// voice and the fourth wore a treatment the identity bans by name. Two
// later reviews found stragglers. A shape shared by convention is a
// shape the eleventh screen invents again.
func TestEveryEmptyStateIsTheSharedShape(t *testing.T) {
	var offences []string
	for name, body := range shellSources(t) {
		for _, found := range emptyStateElement.FindAllStringSubmatch(body, -1) {
			tag, attrs := found[1], found[2]
			if !strings.Contains(attrs, `class="state"`) && !strings.Contains(attrs, `class="state `) {
				offences = append(offences, name+": <"+tag+attrs+">")
			}
		}
	}
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("%d empty state(s) are not the shared shape:\n  %s\n\n"+
			"An element a page fills when a list is empty carries class=\"state\", which is the bold "+
			"ink line, the muted sentence and at most one link that design.md specifies. A shell that "+
			"writes its own is the fourth voice on a screen that already had three.",
			len(offences), strings.Join(offences, "\n  "))
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
	body, err := os.ReadFile(filepath.Join("static", "styles.css"))
	if err != nil {
		t.Fatalf("read styles.css: %v", err)
	}
	sheet := string(body)

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
