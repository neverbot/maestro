package web_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestTheCreateGameDisclosureIsWhatEachScreenNeeds is a source guard over
// two CSS rules, and it is a source guard because both are invisible to
// every other kind of test: the JavaScript sets an attribute and the
// browser draws a glyph, and nothing in between fails when either rule
// is deleted.
//
// The two defects it holds, both found by a person looking at the
// screen:
//
//   - **On an account with no games the summary had nothing to do but
//     undo.** Creating a game is the only action that screen offers, and
//     the control above the form only put the form away. app.js marks
//     the disclosure `data-only-action` in that one branch;
//     `display: none` is what makes the mark mean something, and it
//     takes the summary out of the tab order too, so the control cannot
//     be reached and then shut on a screen with nothing else on it.
//   - **Over a list the control stays, and it has to say which way it
//     goes.** A "+ New game" that was still "+" once it had opened the
//     form read as a button that had stopped working. Open, it is a
//     minus, off `details[open]` — the browser's own state rather than a
//     second copy of it that can go stale.
func TestTheCreateGameDisclosureIsWhatEachScreenNeeds(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("static/styles.css")
	if err != nil {
		t.Fatalf("read styles.css: %v", err)
	}
	styles := string(raw)

	only := regexp.MustCompile(`#new-game\[data-only-action\]\s*>\s*summary\s*\{[^}]*\}`).FindString(styles)
	if only == "" {
		t.Fatal("no `#new-game[data-only-action] > summary` rule: app.js marks the disclosure on the " +
			"screen where the form is the only action, and nothing reads the mark, so the control " +
			"that only hides the form is still on the page")
	}
	if !strings.Contains(only, "display: none") {
		t.Errorf("the disclosure is not removed on that screen, only restyled:\n%s", only)
	}

	open := regexp.MustCompile(`#new-game\[open\]\s*>\s*summary::before\s*\{[^}]*\}`).FindString(styles)
	if open == "" {
		t.Fatal("no `#new-game[open] > summary::before` rule: the control says `+` whether the form " +
			"is open or closed, so nothing on screen says the same click closes it again")
	}
	// \2212 is MINUS SIGN. A hyphen would sit at a different height and
	// a different width from the plus it replaces.
	if !strings.Contains(open, `\2212`) {
		t.Errorf("the open state does not swap the plus for a minus sign:\n%s", open)
	}

	app, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	if !strings.Contains(string(app), `"data-only-action"`) {
		t.Fatal("app.js no longer sets data-only-action, so the rule above is a selector nothing matches")
	}
}
