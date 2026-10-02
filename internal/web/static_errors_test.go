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

// errorSlugMax is how much of a message the browser's slug keeps. It is
// declared in static/i18n.js and read from there, because a guard that
// carries its own copy of the number proves nothing about the code.
func errorSlugMax(t *testing.T) int {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("static", "i18n.js"))
	assert.NoErr(t, err, "read i18n.js")
	m := regexp.MustCompile(`export const ERROR_SLUG_MAX = (\d+);`).FindStringSubmatch(string(src))
	assert.Must(t, m != nil, "i18n.js declares no ERROR_SLUG_MAX; this guard reads that number")
	n, err := strconv.Atoi(m[1])
	assert.NoErr(t, err, "parse ERROR_SLUG_MAX")
	return n
}

// errorKey is errorKey() from static/i18n.js, in Go. The two have to
// agree, which is what makes a key written here reach a reader there.
func errorKey(message string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(message) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > max {
		slug = strings.Trim(slug[:max], "-")
	}
	if slug == "" {
		return ""
	}
	return "error." + slug
}

// writeErrorCall finds one `writeError(w, status, code, "message"` and
// says whether the message is followed by a `+`, which makes it a prefix
// with a runtime tail rather than a whole sentence.
var writeErrorCall = regexp.MustCompile(`(?s)writeError\(\s*[^,]+,\s*[^,]+,\s*(?:errCode[A-Za-z]+|"[a-z_]+"),\s*("(?:[^"\\]|\\.)*")(\s*\+)?`)

// composedMessages are the refusals whose sentence is finished at
// runtime, so no fixed string names them and the reader meets the
// server's English. They are listed rather than ignored: a new one is a
// new hole and this guard is where it is noticed.
var composedMessages = []string{
	"only a game manager may ",
	"role must be one of ",
	"the request body must be ",
	"this instance reads ",
	"your role in this game is ",
	"that token belongs to somebody else; it is revoked by the person who created it ",
}

// serverMessages reads every message this package's handlers write,
// split into the whole sentences and the composed prefixes.
func serverMessages(t *testing.T) (whole []string, composed []string) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	assert.NoErr(t, err, "glob the package")
	seen := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		assert.NoErr(t, err, "read "+name)
		for _, m := range writeErrorCall.FindAllStringSubmatch(string(raw), -1) {
			var message string
			assert.NoErr(t, json.Unmarshal([]byte(m[1]), &message), "unquote a message in "+name)
			if seen[message] {
				continue
			}
			seen[message] = true
			if strings.TrimSpace(m[2]) == "+" {
				composed = append(composed, message)
				continue
			}
			whole = append(whole, message)
		}
	}
	sort.Strings(whole)
	sort.Strings(composed)
	return whole, composed
}

// **A refusal a reader cannot read is the one sentence that matters
// most.** The wire stays English — one code carries several sentences,
// so the code cannot pick the words — and the browser looks the message
// itself up by slug. A message reworded on the server silently stops
// matching its entry, which is exactly what this fails on.
func TestEveryRefusalIsTranslated(t *testing.T) {
	t.Parallel()
	max := errorSlugMax(t)
	whole, _ := serverMessages(t)
	assert.Must(t, len(whole) > 40, "found %d messages; this guard is reading the wrong place", len(whole))

	source := catalogue(t, sourceLocale)
	byKey := map[string]string{}
	var missing, wrong []string
	for _, message := range whole {
		key := errorKey(message, max)
		// Two messages that slug to one key would translate to whichever
		// of them the catalogue holds, which is a wrong sentence rather
		// than an English one.
		if other, clash := byKey[key]; clash {
			t.Fatalf("%q and %q slug to the same key %q; raise ERROR_SLUG_MAX", other, message, key)
		}
		byKey[key] = message
		got, ok := source[key]
		if !ok {
			missing = append(missing, key+" ("+message+")")
			continue
		}
		if got != message {
			wrong = append(wrong, key+": catalogue says "+strconv.Quote(got)+", the server says "+strconv.Quote(message))
		}
	}
	assert.Must(t, len(missing) == 0, "no catalogue entry for %s", strings.Join(missing, "; "))
	// English is the source, so its entry is the message itself. A
	// difference here is a message reworded on one side only.
	assert.Must(t, len(wrong) == 0, "%s", strings.Join(wrong, "; "))
}

// Every `error.` key in the catalogues is one of those messages, or the
// one the browser writes itself when it cannot reach the server at all.
func TestNoErrorKeyIsUnread(t *testing.T) {
	t.Parallel()
	max := errorSlugMax(t)
	whole, _ := serverMessages(t)
	known := map[string]bool{
		// Written in the browser, which is where "the server did not
		// answer" is the only thing anybody knows.
		"error.unreachable":    true,
		"error.noAccessToGame": true,
		"error.gameNotFound":   true,
	}
	for _, message := range whole {
		known[errorKey(message, max)] = true
	}
	var extra []string
	for key := range catalogue(t, sourceLocale) {
		if strings.HasPrefix(key, "error.") && !known[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	assert.Must(t, len(extra) == 0, "no refusal reaches %s", strings.Join(extra, ", "))
}

// The composed refusals, named. They reach a reader in English because
// their sentence is finished at runtime; this fails when the set changes
// so the decision is made again rather than inherited.
func TestComposedRefusalsAreTheOnesNamed(t *testing.T) {
	t.Parallel()
	_, composed := serverMessages(t)
	want := append([]string{}, composedMessages...)
	sort.Strings(want)
	assert.Must(t, strings.Join(composed, "|") == strings.Join(want, "|"),
		"the handlers compose %q and this guard names %q", composed, want)
}
