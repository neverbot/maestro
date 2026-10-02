package web_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// sourceLocale is the language every string is written in first. The
// others are measured against it.
const sourceLocale = "en"

func catalogue(t *testing.T, locale string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("static", "i18n", locale+".json"))
	assert.NoErr(t, err, "read "+locale)
	var out map[string]string
	assert.NoErr(t, json.Unmarshal(raw, &out), "parse "+locale)
	assert.Must(t, len(out) > 0, "%s.json carries no strings", locale)
	return out
}

// shippedLocales reads the list i18n.js declares, which is what the
// browser negotiates against.
func shippedLocales(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("static", "i18n.js"))
	assert.NoErr(t, err, "read i18n.js")
	m := regexp.MustCompile(`export const LOCALES = \[([^\]]*)\]`).FindStringSubmatch(string(src))
	assert.Must(t, m != nil, "i18n.js declares no LOCALES; this guard reads that list")
	var out []string
	for _, part := range strings.Split(m[1], ",") {
		if tag := strings.Trim(strings.TrimSpace(part), `"`); tag != "" {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

// **A key missing from a translation is a hole nobody sees.** The
// browser has no runtime fallback by design: it fetches one catalogue
// and no other, so a Spanish reader meeting a key English has and
// Spanish does not would read the key itself. This is what makes that
// impossible rather than unlikely.
func TestEveryLocaleCarriesEveryString(t *testing.T) {
	t.Parallel()
	source := catalogue(t, sourceLocale)

	for _, locale := range shippedLocales(t) {
		if locale == sourceLocale {
			continue
		}
		t.Run(locale, func(t *testing.T) {
			other := catalogue(t, locale)
			var missing, extra []string
			for key := range source {
				if _, ok := other[key]; !ok {
					missing = append(missing, key)
				}
			}
			for key := range other {
				if _, ok := source[key]; !ok {
					extra = append(extra, key)
				}
			}
			sort.Strings(missing)
			sort.Strings(extra)
			assert.Must(t, len(missing) == 0, "%s.json does not carry %s", locale, strings.Join(missing, ", "))
			// An extra key is a string nothing reads, or English's own
			// copy renamed and this one not carried along.
			assert.Must(t, len(extra) == 0, "%s.json carries %s, which English does not", locale, strings.Join(extra, ", "))
		})
	}
}

// A placeholder that exists in one language and not another is a
// sentence that renders with `{game}` in it.
func TestEveryLocaleFillsTheSamePlaceholders(t *testing.T) {
	t.Parallel()
	placeholder := regexp.MustCompile(`\{(\w+)\}`)
	names := func(line string) []string {
		var out []string
		for _, m := range placeholder.FindAllStringSubmatch(line, -1) {
			out = append(out, m[1])
		}
		sort.Strings(out)
		return out
	}

	source := catalogue(t, sourceLocale)
	for _, locale := range shippedLocales(t) {
		if locale == sourceLocale {
			continue
		}
		other := catalogue(t, locale)
		for key, line := range source {
			want, got := names(line), names(other[key])
			assert.Must(t, strings.Join(want, ",") == strings.Join(got, ","),
				"%s: %s fills %v and English fills %v", locale, key, got, want)
		}
	}
}

// Every locale the browser offers has a file, and every file is offered.
func TestEveryShippedLocaleHasACatalogue(t *testing.T) {
	t.Parallel()
	declared := shippedLocales(t)
	entries, err := os.ReadDir(filepath.Join("static", "i18n"))
	assert.NoErr(t, err, "read static/i18n")

	var onDisk []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			onDisk = append(onDisk, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(onDisk)
	assert.Must(t, strings.Join(declared, ",") == strings.Join(onDisk, ","),
		"i18n.js offers %v and static/i18n holds %v", declared, onDisk)
}

// **A shell carries no English.** Every translatable element ships empty
// with a `data-i18n` key, so a reader never meets a language they did
// not ask for, not even for the frame before the module runs. A heading
// written straight into the markup is invisible to this product's one
// translation mechanism and reads English to everybody for ever.
//
// The `<noscript>` notice is the exception and the only one: no module
// will ever run to fill it, so its words are the markup's own.
func TestNoShellHardCodesAHeading(t *testing.T) {
	t.Parallel()
	shells, err := filepath.Glob(filepath.Join("static", "*.html"))
	assert.NoErr(t, err, "glob shells")
	assert.Must(t, len(shells) > 10, "found %d shells; this guard is reading the wrong place", len(shells))

	// One expression per level: Go's regexp has no backreference, which
	// is what a `</\1>` would need.
	checked := 0
	for _, level := range []string{"h1", "h2", "h3"} {
		heading := regexp.MustCompile(`(?s)<` + level + `([^>]*)>(.*?)</` + level + `>`)
		for _, shell := range shells {
			raw, err := os.ReadFile(shell)
			assert.NoErr(t, err, "read "+shell)
			for _, m := range heading.FindAllStringSubmatch(string(raw), -1) {
				attrs, text := m[1], strings.TrimSpace(m[2])
				checked++
				// The wordmark is a name and not a word: it is "Maestro"
				// in every language, like any other product's.
				if text == "Maestro" {
					continue
				}
				if strings.Contains(attrs, "data-i18n") {
					assert.Must(t, text == "", "%s: a heading with a key also carries %q", filepath.Base(shell), text)
					continue
				}
				assert.Should(t, text == "", "%s: <%s>%s</%s> is English in the markup; give it a data-i18n key",
					filepath.Base(shell), level, text, level)
			}
		}
	}
	assert.Must(t, checked > 20, "read %d headings out of the shells; the scan stopped matching", checked)
}

// moduleSources are the front end's own modules: the shells are guarded
// above, and a vendored engine is not ours to translate.
func moduleSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(filepath.Join("static"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		if strings.Contains(path, string(filepath.Separator)+"vendor"+string(filepath.Separator)) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Comments argue; they are not read by anybody in a browser.
		src := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(raw), "")
		src = regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(src, "")
		out[path] = src
		return nil
	})
	assert.NoErr(t, err, "walk static")
	assert.Must(t, len(out) > 20, "found %d modules; this guard is reading the wrong place", len(out))
	return out
}

// **A count says its noun in the reader's language.** countLabel takes
// both words from its caller, which is what makes "1 entity" and "2
// entities" right in English and "1 elemento" right in Spanish — and
// also what let twenty call sites pass English nouns into a Spanish
// sentence. The nouns live in the catalogue under `unit.`.
func TestNoModuleCountsInEnglish(t *testing.T) {
	t.Parallel()
	literal := regexp.MustCompile(`countLabel\([^,()]+,\s*"`)
	var found []string
	for path, src := range moduleSources(t) {
		for _, m := range literal.FindAllString(src, -1) {
			found = append(found, filepath.Base(path)+": "+strings.TrimSpace(m))
		}
	}
	sort.Strings(found)
	assert.Must(t, len(found) == 0, "a count names its noun in the markup's language: %s", strings.Join(found, "; "))
}

// **A module writes no sentence of its own.** Every word a reader meets
// comes from a catalogue, so a page that assigns prose straight to an
// element is a string one language has and the other does not. Short
// values are admitted: a class name, an attribute value and a protocol
// token are not prose.
func TestNoModuleHardCodesASentence(t *testing.T) {
	t.Parallel()
	// textContent is how this front end puts words on a screen, and the
	// four named keys are how it hands words to a shared component.
	prose := regexp.MustCompile(`(?:textContent\s*=\s*|\b(?:heading|sentence|label|placeholder)\s*:\s*)"((?:[^"\\]|\\.)*)"`)
	words := regexp.MustCompile(`[A-Za-z]{2,}\s+[A-Za-z]`)
	var found []string
	for path, src := range moduleSources(t) {
		for _, m := range prose.FindAllStringSubmatch(src, -1) {
			if !words.MatchString(m[1]) {
				continue
			}
			found = append(found, filepath.Base(path)+": "+strconv.Quote(m[1]))
		}
	}
	sort.Strings(found)
	assert.Must(t, len(found) == 0, "a module says in English what the catalogue should say: %s", strings.Join(found, "; "))
}
