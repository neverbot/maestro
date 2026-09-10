package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The guard for the defect the 2026-09-09 interface audit measured: the
// design system stopped at the shadow boundary.
//
// `styles.css` states what a button and a field look like with element
// selectors, and an element selector does not cross into a shadow root.
// Five of the six controls on the view screen were therefore rendering as
// browser defaults — `2px outset` bevels, an `#e9e9ed` fill, a text field
// filled `#ffffff`, which this identity forbids outright — while every
// token was correct and nothing anywhere was red.
//
// **Why this is a source guard and not a browser one.** What the audit
// found can only be *measured* in a browser, and this repository has no
// browser harness: the jstest files drive real modules against a DOM stub
// that computes no styles. A test that stubbed `getComputedStyle` would
// assert its own stub. So this holds the mechanical precondition instead
// — every shadow root adopts the shared sheet, and no component states
// the control vocabulary a second time — and the measurement itself was
// made by hand in Firefox and recorded in the task. The precondition is
// what regresses when somebody adds the seventh component; the
// measurement is what proved the fix.

const componentDir = "static/components"

// sharedControlSheet is the one file allowed to say what a control looks
// like. Every other component adopts it.
const sharedControlSheet = "control-styles.js"

func componentSources(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(".", componentDir))
	if err != nil {
		t.Fatalf("read %s: %v", componentDir, err)
	}
	out := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".js") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(".", componentDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		out[entry.Name()] = string(body)
	}
	if len(out) == 0 {
		t.Fatalf("no component sources found under %s", componentDir)
	}
	return out
}

// A component is anything that puts a shadow root on the page: a
// LitElement, whose `static styles` Lit adopts for it, or a plain element
// that calls attachShadow and adopts a constructible sheet itself. Both
// paths exist in this front end and both need the shared vocabulary.
var (
	litComponentRE  = regexp.MustCompile(`extends LitElement`)
	attachShadowRE  = regexp.MustCompile(`attachShadow\(`)
	spreadsSharedRE = regexp.MustCompile(`static styles = \[\s*unsafeCSS\(CONTROL_CSS\)`)
	adoptsSharedRE  = regexp.MustCompile(`adoptControlStyles\(`)
)

// TestEveryShadowRootAdoptsTheSharedControls is the precondition. A
// component that renders a control and adopts nothing is the exact state
// mst-ground shipped in: a file input reporting `border: 0px none` on a
// screen whose stylesheet had said otherwise for the whole build.
func TestEveryShadowRootAdoptsTheSharedControls(t *testing.T) {
	var missing []string
	for name, body := range componentSources(t) {
		if name == sharedControlSheet {
			continue
		}
		isComponent := litComponentRE.MatchString(body) || attachShadowRE.MatchString(body)
		if !isComponent {
			continue
		}
		if spreadsSharedRE.MatchString(body) || adoptsSharedRE.MatchString(body) {
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("%d component(s) put a shadow root on the page without adopting %s:\n  %s\n\n"+
			"A shadow root inherits the page's custom properties and none of its rules, so a\n"+
			"control in there renders as the platform's, not as this product's. Spread\n"+
			"`controlStyles` into `static styles`, or call `adoptControlStyles(shadow)`.",
			len(missing), sharedControlSheet, strings.Join(missing, "\n  "))
	}
}

// bareControlRuleRE finds a rule whose whole selector is a control
// element: `button {`, `input, select {`. A scoped refinement such as
// `th button {` or `.menu button {` is not one — those position a control
// the shared sheet has already dressed, which is the intended division.
var bareControlRuleRE = regexp.MustCompile(`(?m)^\s*(button|input|select|textarea)(\s*,\s*(button|input|select|textarea))*\s*\{`)

// TestNoComponentRestatesTheControlVocabulary keeps the fix from being
// undone by the thing that caused it: a component deciding for itself
// what a button is. Two of them did, with `font: inherit` and their own
// margins, which is how a control ends up nearly right on one screen and
// bevelled on the next.
func TestNoComponentRestatesTheControlVocabulary(t *testing.T) {
	for name, body := range componentSources(t) {
		if name == sharedControlSheet {
			continue
		}
		if loc := bareControlRuleRE.FindStringIndex(body); loc != nil {
			line := 1 + strings.Count(body[:loc[0]], "\n")
			t.Errorf("%s:%d states the control vocabulary itself: %q\n"+
				"What a control looks like belongs in %s, which this component adopts.\n"+
				"Scope the rule to what it is positioning (`.menu button { … }`) instead.",
				name, line, strings.TrimSpace(body[loc[0]:loc[1]]), sharedControlSheet)
		}
	}
}

// TestTheSharedControlSheetSpellsNoColour is the same rule
// static_tokens_test.go holds over the stylesheet, applied to the sheet
// that now dresses every control in the product. A literal here would be
// a colour with no theme and no guard.
func TestTheSharedControlSheetSpellsNoColour(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(".", componentDir, sharedControlSheet))
	if err != nil {
		t.Fatalf("read %s: %v", sharedControlSheet, err)
	}
	literal := regexp.MustCompile(`(?m)^[^*/\n]*:[^;\n]*#[0-9a-fA-F]{3,8}`)
	if loc := literal.FindStringIndex(string(body)); loc != nil {
		line := 1 + strings.Count(string(body)[:loc[0]], "\n")
		t.Errorf("%s:%d spells a colour literally: %q", sharedControlSheet, line,
			strings.TrimSpace(string(body)[loc[0]:loc[1]]))
	}
}
