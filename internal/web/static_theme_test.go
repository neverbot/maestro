package web_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The dark token set is stated twice: once inside
// `@media (prefers-color-scheme: dark)` for a person who never chose,
// and once in `:root[data-theme="dark"]` for a person who did. The
// duplication is deliberate — a custom property cannot be aliased into
// two selectors without a third block nothing else in this file needs —
// and it is exactly the shape that rots: a hue tuned in one place and
// not the other is a theme that disagrees with itself, and the only
// person who would ever see it is the one who used the switch.
//
// Mutation: change any value in either block and this fails naming the
// token and both values.
func TestTheTwoDarkBlocksAgree(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("static/styles.css")
	if err != nil {
		t.Fatalf("read the stylesheet: %v", err)
	}
	src := string(raw)

	media := blockAfter(t, src, `:root:not([data-theme="light"]) {`)
	chosen := blockAfter(t, src, `:root[data-theme="dark"] {`)

	if len(media) == 0 {
		t.Fatal("the media query declares no tokens, so this guard holds nothing")
	}
	for name, value := range media {
		other, ok := chosen[name]
		if !ok {
			t.Errorf("%s follows the system's dark theme and is missing from the chosen one", name)
			continue
		}
		if other != value {
			t.Errorf("%s is %q when the system asks for dark and %q when a person does", name, value, other)
		}
	}
	for name := range chosen {
		if _, ok := media[name]; !ok {
			t.Errorf("%s is declared for a chosen dark theme and missing from the system's", name)
		}
	}
}

// **The explicit choice must win in both directions.** The media query
// and `:root[data-theme]` have the same specificity, so a bare
// `@media (prefers-color-scheme: dark) { :root { … } }` written after
// the light block beats `data-theme="light"` and the switch works one
// way only: a person on a dark system who asks for paper keeps getting
// ink. That is the cascade defect this stylesheet has shipped five
// times, and this is the one place a test can hold it.
//
// Mutation: drop the `:not([data-theme="light"])` and this fails.
func TestAChosenLightThemeSurvivesADarkSystem(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("static/styles.css")
	if err != nil {
		t.Fatalf("read the stylesheet: %v", err)
	}
	src := string(raw)
	// `(?s).*?` rather than `\s*`: the block opens with a comment saying
	// why the selector is what it is, and a guard that broke on a comment
	// would be a guard nobody could explain themselves in front of.
	media := regexp.MustCompile(
		`@media \(prefers-color-scheme: dark\) \{(?s:.*?)(:root[^{]*)\{`).FindStringSubmatch(src)
	if media == nil {
		t.Fatal("the stylesheet has no dark media query with a :root selector in it")
	}
	if !strings.Contains(media[1], `:not([data-theme="light"])`) {
		t.Errorf("the dark media query selects %q, which beats an explicit light choice at equal "+
			"specificity: a person on a dark system could not choose paper", strings.TrimSpace(media[1]))
	}
}

// **The theme lands before the first paint, or it is a flash.** The
// content security policy admits no inline script and a module is
// deferred by definition, so the only thing that can write the attribute
// before the body exists is a classic script in the head. A shell that
// forgot it renders in the system's theme and jumps to the chosen one on
// every navigation, which is a defect only a browser can see.
//
// Mutation: add `defer` to the tag in any shell, or drop the tag, and
// this fails naming the shell.
func TestEveryShellAppliesTheThemeBeforeItPaints(t *testing.T) {
	t.Parallel()
	shells, err := os.ReadDir("static")
	if err != nil {
		t.Fatalf("read static/: %v", err)
	}
	seen := 0
	for _, entry := range shells {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".html") {
			continue
		}
		body, err := os.ReadFile("static/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		src := string(body)
		tag := regexp.MustCompile(`<script[^>]*src="/static/theme\.js"[^>]*>`).FindString(src)
		if tag == "" {
			t.Errorf("%s never loads /static/theme.js, so it paints in the system's theme whatever "+
				"the reader chose", entry.Name())
			continue
		}
		if strings.Contains(tag, "defer") || strings.Contains(tag, `type="module"`) {
			t.Errorf("%s loads the theme script as %q: a deferred script runs after the first paint, "+
				"which is the flash it exists to prevent", entry.Name(), tag)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("no shell was examined, so this guard holds nothing")
	}
}

// blockAfter returns the custom properties declared in the block opened
// by the given selector text.
func blockAfter(t *testing.T, src, selector string) map[string]string {
	t.Helper()
	start := strings.Index(src, selector)
	if start < 0 {
		t.Fatalf("the stylesheet has no %q block", selector)
	}
	rest := src[start+len(selector):]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("the %q block is never closed", selector)
	}
	out := map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "--") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(name)] = strings.TrimSuffix(strings.TrimSpace(value), ";")
	}
	return out
}
