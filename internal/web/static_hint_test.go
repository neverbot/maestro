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
	// **One file, where there were two.** The ghost's rule used to be
	// written in the stylesheet and again in the components' own string,
	// which is why this defect had to be found twice; static/controls.css
	// is now the only place it exists, and control-styles.js is checked
	// for having stopped carrying a second copy.
	for _, path := range []string{"static/controls.css"} {
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

	// The second copy must stay gone. control-styles.js may say what a
	// page stylesheet cannot — `:host`, and the focus ring inside a
	// shadow root — and nothing else: a `button` rule there is the
	// divergence this consolidation removed, growing back.
	raw, err := os.ReadFile("static/components/control-styles.js")
	if err != nil {
		t.Fatalf("read control-styles.js: %v", err)
	}
	for _, selector := range []string{"button {", "button.ghost", "input,", ".chip {"} {
		if strings.Contains(string(raw), selector) {
			t.Errorf("control-styles.js writes `%s` again: the control vocabulary is "+
				"static/controls.css, which every shadow root adopts", selector)
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

// interactiveControls is every named thing in this product a person can
// point at, with the selector that dresses it. It is a list rather than
// a scan because the point is to be *told* when something is added
// without its states: a new control nobody adds here is a new control
// nobody checked, and that is the review this file failed three times in
// one afternoon — the ghost, then the primary, then a tab, each found by
// a person moving a pointer over it.
var interactiveControls = []struct {
	name     string
	selector string
}{
	{"the primary button", "button:hover:not(:disabled)"},
	{"the ghost button", ".ghost:hover:not(:disabled)"},
	{"a button written as a sentence", ".link-button:hover:not(:disabled)"},
	{"a chip", ".chip:hover:not(:disabled)"},
	{"a tab", ".tabs .tab:hover"},
	{"a destination in the strip", ".destinations a:hover"},
	{"the game switcher", ".game-switcher > summary:hover"},
	{"the create-game disclosure", "#new-game > summary:hover"},
	{"a catalogue row", "ul.catalogue li:hover"},
}

// standsOnPaper names the two controls that only ever appear on a
// `--paper` surface, where painting `--ground` is a darkening a reader
// can see rather than the invisible repaint it is on the page itself.
// The exemption is by name, and it is two, so a third one has to be
// argued here rather than inherited.
//
//   - the game switcher lives in the header, which is paper;
//   - a chip belongs to a panel's control strip, never to bare page.
var standsOnPaper = map[string]bool{
	"the game switcher": true,
	"a chip":            true,
	// A catalogue is a paper sheet on the desk — `ul.catalogue` sets
	// `background: var(--paper)` — so a row darkening to the desk's own
	// colour is the sheet being pressed, and visible.
	"a catalogue row": true,
}

// TestEveryControlSaysSomethingWhenPointedAt is the standing version of
// a review that kept being done by hand and kept missing things.
//
// Two ways a hover can exist and say nothing, and this product shipped
// both: painting the colour that is already behind the control
// (`.ghost` painted `--ground`, which is `body`'s background) and
// declaring a property the control does not have (`.link-button:hover`
// set `background: none` on a control with no background). A third,
// weaker, is a change too small to see: the primary's fill moves 1.18:1
// in luminance, which is why it now lifts as well.
func TestEveryControlSaysSomethingWhenPointedAt(t *testing.T) {
	t.Parallel()
	// The control vocabulary and the screens' own rules, read together:
	// a button is dressed by static/controls.css and a tab by the
	// stylesheet, and both are things a person points at.
	raw, err := os.ReadFile("static/styles.css")
	if err != nil {
		t.Fatalf("read styles.css: %v", err)
	}
	shared, err := os.ReadFile("static/controls.css")
	if err != nil {
		t.Fatalf("read controls.css: %v", err)
	}
	styles := string(raw) + "\n" + string(shared)

	for _, control := range interactiveControls {
		rule := ruleFor(styles, control.selector)
		if rule == "" {
			t.Errorf("%s does not answer the pointer at all: no rule for %q",
				control.name, control.selector)
			continue
		}
		body := strings.TrimSpace(rule)
		// Painting the page's own background is the first way to say
		// nothing. The surfaces a control stands on are --ground (the
		// page) and --paper (a panel); a hover that paints either is
		// only legible when it happens to stand on the other, which is
		// not a property a stylesheet can promise.
		if strings.Contains(body, "background: var(--ground)") && !standsOnPaper[control.name] {
			t.Errorf("%s paints --ground on hover, which is the page's own background:\n%s",
				control.name, body)
		}
		// Declaring nothing is the second. A rule whose whole content is
		// `background: none` on a control that has no background is a
		// rule that reads as an answer and is not one.
		if declarationsIn(body) == 1 && strings.Contains(body, "background: none") {
			t.Errorf("%s answers the pointer with a rule that changes nothing:\n%s", control.name, body)
		}
		if declarationsIn(body) == 0 {
			t.Errorf("%s has an empty hover rule", control.name)
		}
	}

	// The primary carries two channels, because one of them is a change
	// of 1.18:1 that a person reported as no change at all.
	primary := ruleFor(styles, "button:hover:not(:disabled)")
	if !strings.Contains(primary, "box-shadow") {
		t.Errorf("the primary button's hover is a fill change and nothing else:\n%s", primary)
	}
}

// ruleFor returns the body of the first rule whose selector list
// contains this exact selector, so `.ghost:hover:not(:disabled)` does
// not match a rule that merely mentions it inside a longer one.
//
// The stylesheet is read with comments stripped first: this file's own
// prose contains braces and selectors, and a scanner that read them
// would report rules nobody wrote.
func ruleFor(styles, selector string) string {
	clean := stripComments(styles)
	for at := 0; ; {
		found := strings.Index(clean[at:], selector)
		if found < 0 {
			return ""
		}
		start := at + found
		at = start + len(selector)
		// The character before must end a selector list or a rule, so
		// `.tab:hover` does not match inside `.mytab:hover`.
		if start > 0 {
			before := clean[start-1]
			if before != '\n' && before != ',' && before != ' ' && before != '}' && before != ';' {
				continue
			}
		}
		rest := strings.TrimLeft(clean[at:], " \t\n")
		if !strings.HasPrefix(rest, "{") && !strings.HasPrefix(rest, ",") {
			continue
		}
		// Walk to the opening brace of this rule, then to its close.
		open := strings.Index(clean[at:], "{")
		if open < 0 {
			return ""
		}
		close := strings.Index(clean[at+open:], "}")
		if close < 0 {
			return ""
		}
		return clean[at+open+1 : at+open+close]
	}
}

func declarationsIn(body string) int {
	count := 0
	for _, part := range strings.Split(stripComments(body), ";") {
		if strings.TrimSpace(part) != "" {
			count++
		}
	}
	return count
}
