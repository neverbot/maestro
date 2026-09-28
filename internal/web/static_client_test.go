package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/neverbot/maestro/internal/assert"
)

// The source-shape guards over internal/web/static/client.js — the one
// module in this front end that talks to the server.

const clientModule = "static/client.js"

// maxClientLiteral bounds a string literal in the data client. The
// longest protocol token it needs is a path fragment; anything three
// times that length is prose, whether or not it uses spaces to say so.
// It is the second half of the whitespace rule below, and exists because
// the first half reads an escape as its two characters rather than as
// the whitespace it stands for.
const maxClientLiteral = 48

// TestTheDataClientComposesNoSentence is the mechanical form of this
// module's central rule: a refusal reaches a designer as the server's
// own code, pointer and message, unmodified.
func TestTheDataClientComposesNoSentence(t *testing.T) {
	t.Parallel()
	literals := clientStringLiterals(t)
	assert.Must(t, len(literals) >= 20, "found only %d string literal(s) in %s: the scanner below is not reading the module, "+
		"so this test would pass whatever the module said", len(literals), clientModule)
	var offences []string
	for _, lit := range literals {
		if strings.ContainsFunc(lit.text, unicode.IsSpace) || len(lit.text) > maxClientLiteral {
			offences = append(offences, clientModule+":"+strconv.Itoa(lit.line)+": "+strconv.Quote(lit.text))
		}
	}
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("%d string literal(s) in %s carry whitespace, which is what a sentence carries:\n%s\n"+
			"the server writes the sentences; this client carries its code, pointer and message unmodified "+
			"and adds navigation only",
			len(offences), clientModule, strings.Join(offences, "\n"))
	}
	t.Logf("%s holds %d string literal(s), none of them a sentence", clientModule, len(literals))
}

// TestTheSentenceGuardReadsWhatItClaimsTo is the guard on the guard, and
// it has two halves because the test above reports nothing in two
// indistinguishable cases: the module is clean, or the scanner never saw
// its strings.
func TestTheSentenceGuardReadsWhatItClaimsTo(t *testing.T) {
	t.Parallel()
	literals := clientStringLiterals(t)
	seen := map[string]bool{}
	for _, lit := range literals {
		seen[lit.text] = true
	}
	for _, want := range []string{
		"view.positions", "view.upserted", "view.removed", "resync",
		"content-type", "text/event-stream", "/api/games/", "reread", "band", "gone", "ignore",
	} {
		assert.Should(t, seen[want], "the scanner never saw the literal %q, which %s certainly contains", want, clientModule)
	}

	fixture := "" +
		"// a comment saying something with spaces in it\n" +
		"const a = \"view.positions\";\n" +
		"const b = \"could not run the view\";\n" +
		"const c = 'the game moved under this query';\n"
	found := stringLiteralsIn(fixture)
	var spaced []string
	for _, lit := range found {
		if strings.ContainsFunc(lit.text, unicode.IsSpace) {
			spaced = append(spaced, lit.text)
		}
	}
	sort.Strings(spaced)
	want := []string{"could not run the view", "the game moved under this query"}
	assert.Should(t, strings.Join(spaced, "|") == strings.Join(want, "|"), "the scanner found %q in the fixture, want %q: it must catch a sentence in code and "+
		"must not read one out of a comment", spaced, want)
}

// networkPrimitives are the spellings of "reach the server" a browser
// offers. `fetch` as a bare word (not `fetchImpl`, not `fetchAPI`),
// EventSource, XMLHttpRequest, WebSocket, sendBeacon and importScripts.
var networkPrimitives = regexp.MustCompile(
	`\bfetch\b|\bEventSource\b|\bXMLHttpRequest\b|\bWebSocket\b|\bsendBeacon\b|\bimportScripts\b`)

// modulesAllowedToFetch is the whole list of own modules that may name a
// network primitive, each with the reason it is on the list.
var modulesAllowedToFetch = map[string]string{
	"static/client.js":                    "the data client: one fetcher for every call and the stream",
	"static/app.js":                       "the shipped page bundle, whose wrappers every other page calls",
	"static/components/control-styles.js": "the one control stylesheet, read once and adopted by every shadow root",
}

// TestOnlyTheDataClientAndTheShippedBundleFetch holds the architectural
// rule Task 3 introduces, at the moment it becomes checkable: this
// sub-project's modules — renderers, layout, components, pages — are
// pure functions over data, and the one that is not is named here.
func TestOnlyTheDataClientAndTheShippedBundleFetch(t *testing.T) {
	t.Parallel()
	found := map[string][]string{}
	scanned := 0
	err := filepath.WalkDir("static", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(path)
		if d.IsDir() {
			// The vendored subtree is skipped for static_sinks_test.go's
			// reason: it is upstream code, pinned by SHA-256 to a release
			// this project vetted, and not ours to edit.
			if slashed == vendorDir {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".js", ".mjs":
		default:
			return nil
		}
		scanned++
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range codeLines(string(raw)) {
			if networkPrimitives.MatchString(line.code) {
				found[slashed] = append(found[slashed], strconv.Itoa(line.number)+": "+line.text)
			}
		}
		return nil
	})
	assert.Must(t, err == nil, "walk static: %v", err)
	assert.Must(t, scanned != 0, "scanned no module under internal/web/static: this test would pass on an empty tree")

	for path, hits := range found {
		if _, ok := modulesAllowedToFetch[path]; !ok {
			sort.Strings(hits)
			t.Errorf("%s reaches the server directly:\n%s\nevery call and the stream go through %s, "+
				"which is what keeps every other module a pure function over data",
				path, strings.Join(hits, "\n"), clientModule)
		}
	}
	for path, reason := range modulesAllowedToFetch {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s is allowed to fetch (%s) and does not exist", path, reason)
			continue
		}
		assert.Should(t, len(found[path]) != 0, "%s is allowed to fetch (%s) and names no network primitive: "+
			"an allowance nothing uses is a permission waiting for the next module", path, reason)
	}
	t.Logf("scanned %d own module(s); %d of them reach the server", scanned, len(found))
}

// navigationSinks are the spellings of "take the user somewhere else".
var navigationSinks = regexp.MustCompile(
	`\blocation\b|\bpushState\b|\breplaceState\b|\bwindow\s*\.\s*open\b`)

// TestTheDataClientNavigatesNothing pins the half of the removed-view
// rule that a harness can only observe by not observing it: a
// `view.removed` produces a sentence and a link back, and nothing is
// auto-navigated. The harness asserts no navigation happened for that
// one event; this asserts the module has no way to navigate at all, for
// any event, including ones added later.
func TestTheDataClientNavigatesNothing(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(clientModule)
	assert.Must(t, err == nil, "read %s: %v", clientModule, err)
	lines := codeLines(string(raw))
	assert.Must(t, len(lines) != 0, "%s has no code lines: this test would pass on an empty file", clientModule)
	var offences []string
	for _, line := range lines {
		if navigationSinks.MatchString(line.code) {
			offences = append(offences, strconv.Itoa(line.number)+": "+line.text)
		}
	}
	assert.Must(t, len(offences) <= 0, "%s can navigate:\n%s\na removed view is a sentence and a link back; the designer chooses",
		clientModule, strings.Join(offences, "\n"))
}

// clientLiteral is one string literal found in a module.
type clientLiteral struct {
	line int
	text string
}

func clientStringLiterals(t *testing.T) []clientLiteral {
	t.Helper()
	raw, err := os.ReadFile(clientModule)
	assert.Must(t, err == nil, "read %s: %v", clientModule, err)
	return stringLiteralsIn(string(raw))
}

// stringLiteralsIn returns every string literal in JavaScript source,
// comments removed first by codeLines (static_sinks_test.go) so a doc
// comment discussing an error sentence is not read as one.
func stringLiteralsIn(src string) []clientLiteral {
	var out []clientLiteral
	for _, line := range codeLines(src) {
		runes := []rune(line.code)
		for i := 0; i < len(runes); i++ {
			quote := runes[i]
			if quote != '"' && quote != '\'' && quote != '`' {
				continue
			}
			var text strings.Builder
			i++
			for ; i < len(runes); i++ {
				if runes[i] == '\\' {
					// An escape is kept as its two characters rather
					// than resolved, so the frame separator "\n\n" —
					// which is protocol and not prose — is not read as
					// whitespace. A sentence disguised entirely in \n
					// escapes would slip past that, which is what the
					// length bound below is for.
					text.WriteRune(runes[i])
					i++
					if i < len(runes) {
						text.WriteRune(runes[i])
					}
					continue
				}
				if runes[i] == quote {
					break
				}
				text.WriteRune(runes[i])
			}
			out = append(out, clientLiteral{line: line.number, text: text.String()})
		}
	}
	return out
}
