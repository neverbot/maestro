package web_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestTheHint drives internal/web/jstest/hint_test.mjs, which holds the
// behaviour: the explanation opens on focus as well as on hover, Escape
// closes it, the trigger names the panel, and the sentence arrives as
// text.
func TestTheHint(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/hint_test.mjs")
}

// TestTheHintPanelIsASurfaceAndNotABox is the other half, and it is a
// source guard because a stylesheet a shadow root adopts is invisible to
// every harness in this directory: the behaviour test above builds the
// element against a stub with no CSS in it at all.
//
// What it holds is the part a reviewer cannot see by reading the
// component: that the panel's chrome is the product's tokens rather than
// a second opinion about what a floating sheet looks like, and that the
// transition animates opacity only. A panel that animated its own
// position or size would animate layout, which docs/design.md forbids by
// name.
func TestTheHintPanelIsASurfaceAndNotABox(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("static/components/mst-hint.js")
	if err != nil {
		t.Fatalf("read mst-hint.js: %v", err)
	}
	source := string(raw)

	panel := regexp.MustCompile(`(?s)\.panel \{.*?\n\}`).FindString(source)
	if panel == "" {
		t.Fatal("no `.panel` rule in mst-hint.js: the hint has no sheet, and styles.css cannot reach " +
			"inside a shadow root to give it one")
	}
	// Every value the panel draws itself with is a token the page
	// defines, because a custom property is the one thing that does
	// cross the shadow boundary.
	for _, token := range []string{
		"var(--paper)", "var(--ink)", "var(--line)", "var(--radius)", "var(--shadow-2)", "var(--sans)",
	} {
		if !strings.Contains(panel, token) {
			t.Errorf("the panel does not draw itself with %s:\n%s", token, panel)
		}
	}
	transition := regexp.MustCompile(`transition: ([^;]+);`).FindStringSubmatch(panel)
	if transition == nil {
		t.Fatalf("the panel states no transition at all:\n%s", panel)
	}
	for _, property := range []string{"top", "left", "right", "width", "height", "padding", "margin", "transform"} {
		if strings.Contains(transition[1], property) {
			t.Errorf("the panel animates %s, which is layout: %s", property, transition[1])
		}
	}

	// The focus ring, which is what makes the keyboard path visible. The
	// behaviour test proves the trigger is in the tab order; nothing but
	// this says a reader can see where they are.
	if !strings.Contains(source, ".trigger:focus-visible") {
		t.Error("the trigger draws no focus ring, so a keyboard reader cannot see what they have focused")
	}
}
