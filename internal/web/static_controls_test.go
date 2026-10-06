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

// cssRules reads one stylesheet as (selector, body) pairs with the
// comments taken out, which is all these guards read it for.
func cssRules(t *testing.T, name string) [][2]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("static", name))
	assert.NoErr(t, err, "read "+name)
	src := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(raw), "")
	var out [][2]string
	for _, m := range regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(src, -1) {
		out = append(out, [2]string{strings.Join(strings.Fields(m[1]), " "), m[2]})
	}
	assert.Must(t, len(out) > 10, "%s parsed into %d rules; this guard is reading the wrong place", name, len(out))
	return out
}

// **A control that undresses the button has to undress it on hover too.**
// The vocabulary dresses a bare `<button>` as the primary button — ink
// fill, paper text — so every control that is something else resets it:
// the ghost, the chip, the tab, the link-button. A reset that stops at
// the resting state leaves `button:hover` to repaint the control in ink
// while its own rule keeps the text ink: a black box with an invisible
// word in it. That shipped on every sortable column heading in the
// product and was reported from a browser, which is the only place a
// hover state exists.
func TestEveryUndressedControlStaysUndressedUnderThePointer(t *testing.T) {
	t.Parallel()
	var rules [][2]string
	for _, sheet := range []string{"controls.css", "styles.css"} {
		rules = append(rules, cssRules(t, sheet)...)
	}

	undress := regexp.MustCompile(`background:\s*(none|transparent)`)
	paints := regexp.MustCompile(`background`)
	// The class a rule is about: the last one in the selector, which is
	// the control being dressed.
	lastClass := regexp.MustCompile(`\.([A-Za-z][\w-]*)[^.{]*$`)

	resting := map[string]string{}
	hovered := map[string]bool{}
	for _, rule := range rules {
		sel, body := rule[0], rule[1]
		for _, part := range strings.Split(sel, ",") {
			part = strings.TrimSpace(part)
			m := lastClass.FindStringSubmatch(part)
			if m == nil {
				continue
			}
			switch {
			case strings.Contains(part, ":hover") && paints.MatchString(body):
				hovered[m[1]] = true
			case !strings.Contains(part, ":hover") && !strings.Contains(part, ":active") && undress.MatchString(body):
				// Only controls: a `<div>` with no background is not a
				// button and has no hover to lose.
				if strings.Contains(part, "button") || strings.HasSuffix(part, "-button") ||
					strings.Contains(part, "sort") || strings.Contains(part, "chip") ||
					strings.Contains(part, "tab") || strings.Contains(part, "ghost") ||
					strings.Contains(part, "sign-out") || strings.Contains(part, "btn") {
					resting[m[1]] = part
				}
			}
		}
	}
	assert.Must(t, len(resting) >= 3, "found %d controls that undress the button; this guard is reading the wrong place", len(resting))

	var bare []string
	for class, sel := range resting {
		if !hovered[class] {
			bare = append(bare, sel)
		}
	}
	sort.Strings(bare)
	assert.Must(t, len(bare) == 0,
		"%s reset the button's fill and say nothing about the pointer, so button:hover repaints them in ink",
		strings.Join(bare, "; "))
}

// TestTheDrawnBooleanWearsTokensAndSitsWhereItIsRead holds the two
// decisions a drawn boolean carries that no module test can see. The
// colours, because a hex here is a ninth data colour nobody argued for;
// and the two alignments, because the mark is centred under the heading
// that names it in a column and stays beside its label on the page where
// the label is the thing it belongs to.
func TestTheDrawnBooleanWearsTokensAndSitsWhereItIsRead(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		".mark-yes":                       "color: var(--",
		".mark-no":                        "color: var(--",
		".catalogue-cell.marked":          "justify-content: center",
		"dl.fields dd.field-value.marked": "justify-content: flex-start",
	}
	seen := map[string]bool{}
	for _, rule := range cssRules(t, "styles.css") {
		body, ok := want[rule[0]]
		if !ok {
			continue
		}
		seen[rule[0]] = true
		flat := strings.Join(strings.Fields(rule[1]), " ")
		assert.Should(t, strings.Contains(flat, body), "%s is %q, want it to carry %q", rule[0], flat, body)
	}
	for selector := range want {
		assert.Should(t, seen[selector], "no rule in styles.css selects %s: the guard reads a selector the "+
			"stylesheet no longer has", selector)
	}
}
