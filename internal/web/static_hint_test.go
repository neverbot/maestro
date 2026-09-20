package web_test

import (
	"os"
	"path/filepath"
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

// TestEveryControlAnswersThePointerAndThePress is the guard for a defect
// a person reported as "no button reacts to anything", which was true
// and was general.
//
// `.ghost:hover` painted `--ground`, and `body` *is* `--ground`: every
// secondary control standing on a page changed, on hover, to exactly the
// colour already behind it. The same rule was in control-styles.js, so
// the controls inside every shadow root had the same nothing. And
// `:active` appeared nowhere at all, so a control gave one answer to
// "the pointer is over me" and to "you are pressing me".
//
// Both files are read, because a control vocabulary that disagrees
// across the shadow boundary is the defect control-styles.js exists to
// prevent.
func TestEveryControlAnswersThePointerAndThePress(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"static/styles.css", "static/components/control-styles.js"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		source := string(raw)

		hover := regexp.MustCompile(`(?s)\.ghost:hover:not\(:disabled\)[^{]*\{([^}]*)\}`).FindStringSubmatch(source)
		if hover == nil {
			t.Errorf("%s: a ghost button no longer answers the pointer at all", path)
			continue
		}
		// The page's own background. A hover that paints it is a hover
		// nobody can see.
		if strings.Contains(hover[1], "var(--ground)") {
			t.Errorf("%s: the ghost hover paints --ground, which is the page's own background:\n%s",
				path, hover[1])
		}
		if !strings.Contains(source, ":active:not(:disabled)") {
			t.Errorf("%s: no control has a pressed state", path)
		}
	}
}

// TestNoStyleSheetIsCutInHalfByABacktick is the guard for a defect that
// landed three times in one afternoon and is invisible to `node --check`.
//
// Every component in this product ships its CSS as a template literal,
// because `adoptedStyleSheets` takes a string and the server's policy
// refuses a built `<style>` element. A backtick inside that CSS — and
// this repository's prose puts identifiers in backticks everywhere, so
// a comment saying `--ground` does it — **ends the literal**. The rest
// of the sheet is then parsed as JavaScript, where `--ground` is a
// decrement of an undeclared name, and the browser refuses the whole
// module with "Invalid left-hand side expression in postfix operation".
//
// It survives `node --check`, because the text after the stray backtick
// often happens to parse: the file is valid JavaScript that means
// something entirely different. What it does not survive is being
// opened, and by then it has taken down every module that imports it —
// control-styles.js is imported by every component, so one backtick
// blanked five screens.
func TestNoStyleSheetIsCutInHalfByABacktick(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("static/components/*.js")
	if err != nil {
		t.Fatalf("glob components: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no component modules found")
	}
	// The opening of a CSS constant: `const NAME_CSS = ` followed by the
	// backtick that opens the literal, and the line that closes it.
	opens := regexp.MustCompile("^(?:export )?const [A-Z_]*CSS = `$")
	// A backtick that is not escaped. `[^\\]` and not a lookbehind,
	// which Go's regexp does not have; a literal starting with one is
	// caught by the first alternative.
	bareBacktick := regexp.MustCompile("(^|[^\\\\])`")
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		inside := false
		for n, line := range strings.Split(string(raw), "\n") {
			if !inside {
				inside = opens.MatchString(line)
				continue
			}
			if line == "`;" {
				inside = false
				continue
			}
			// An escaped backtick is fine and mst-canvas.js uses them:
			// what ends the literal is a bare one. `${` is the same hole
			// from the other side — it interpolates rather than closing,
			// and a sheet is not a place for a value.
			if bareBacktick.MatchString(line) || strings.Contains(line, "${") {
				t.Errorf("%s:%d: a backtick or an interpolation inside a CSS literal ends it, and the "+
					"rest of the sheet is then read as JavaScript:\n\t%s", path, n+1, line)
			}
		}
		if inside {
			t.Errorf("%s: a CSS literal is never closed", path)
		}
	}
}
