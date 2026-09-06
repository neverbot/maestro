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
)

// The source-shape guards over internal/web/static/client.js — the one
// module in this front end that talks to the server.
//
// Two of them exist because the properties they hold have no runtime
// signature any harness can see. A client that composed one error
// sentence would pass every check in
// internal/web/jstest/client_test.mjs that did not happen to provoke
// that exact refusal, and a second module that started fetching would
// pass all of them.

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
//
// The rule is easy to state and impossible to hold by review, because
// breaking it is one convenient string. So it is held by shape instead:
// **no string literal in client.js contains whitespace.** A sentence has
// spaces; a protocol token — a path segment, a header name, an event
// kind, a decision, a reason — does not. The module is written to that
// constraint deliberately, which is why its own reasons are spelled
// `placement.moved` and `kind.unhandled` rather than as English.
//
// What this cannot catch is a one-word sentence ("failed") smuggled into
// a message field. What it does catch is every sentence anybody would
// actually write, at the cost of one convention this file's own comment
// explains — and the alternative on offer, a comment asking future
// readers not to write prose here, is the thing this repository's
// standing failure list calls a mechanism nothing reads.
func TestTheDataClientComposesNoSentence(t *testing.T) {
	literals := clientStringLiterals(t)
	if len(literals) < 20 {
		t.Fatalf("found only %d string literal(s) in %s: the scanner below is not reading the module, "+
			"so this test would pass whatever the module said", len(literals), clientModule)
	}
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
//
// The first half pins literals the module is known to contain, so a
// scanner that silently returned an empty set fails. The second half
// feeds the scanner a fixture that *does* carry a sentence — including
// one in a comment, which must not be read as a literal — and asserts it
// is found. A guard that cannot be made to fail is not a guard.
func TestTheSentenceGuardReadsWhatItClaimsTo(t *testing.T) {
	literals := clientStringLiterals(t)
	seen := map[string]bool{}
	for _, lit := range literals {
		seen[lit.text] = true
	}
	for _, want := range []string{
		"view.positions", "view.upserted", "view.removed", "resync",
		"content-type", "text/event-stream", "/api/games/", "reread", "band", "gone", "ignore",
	} {
		if !seen[want] {
			t.Errorf("the scanner never saw the literal %q, which %s certainly contains", want, clientModule)
		}
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
	if strings.Join(spaced, "|") != strings.Join(want, "|") {
		t.Errorf("the scanner found %q in the fixture, want %q: it must catch a sentence in code and "+
			"must not read one out of a comment", spaced, want)
	}
}

// networkPrimitives are the spellings of "reach the server" a browser
// offers. `fetch` as a bare word (not `fetchImpl`, not `fetchAPI`),
// EventSource, XMLHttpRequest, WebSocket, sendBeacon and importScripts.
//
// This is a different question from static_vendor_test.go's outbound-URL
// scan, and both are needed: that one asks *where* a module reaches (an
// instance with no outbound route must work completely), this one asks
// *which module* reaches at all.
var networkPrimitives = regexp.MustCompile(
	`\bfetch\b|\bEventSource\b|\bXMLHttpRequest\b|\bWebSocket\b|\bsendBeacon\b|\bimportScripts\b`)

// modulesAllowedToFetch is the whole list of own modules that may name a
// network primitive, each with the reason it is on the list.
//
// client.js is this sub-project's answer: one module owns every call and
// the stream, so a component is a function from data to marks. app.js is
// the shipped page bundle that predates it and wraps every call the four
// existing HTML shells make (fetchAPI, postJSON, fetchGames); doc.js
// reaches the server only through those, and is deliberately *not* on
// this list, which is what makes the list mean something.
var modulesAllowedToFetch = map[string]string{
	"static/client.js": "the data client: one fetcher for every call and the stream",
	"static/app.js":    "the shipped page bundle, whose wrappers every other page calls",
}

// TestOnlyTheDataClientAndTheShippedBundleFetch holds the architectural
// rule Task 3 introduces, at the moment it becomes checkable: this
// sub-project's modules — renderers, layout, components, pages — are
// pure functions over data, and the one that is not is named here.
//
// It also keeps the allowance from outliving its reason: every entry
// must name a file that exists and that really does name a primitive, so
// a module that stopped fetching cannot leave a permission behind for
// the next one.
func TestOnlyTheDataClientAndTheShippedBundleFetch(t *testing.T) {
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
	if err != nil {
		t.Fatalf("walk static: %v", err)
	}
	if scanned == 0 {
		t.Fatal("scanned no module under internal/web/static: this test would pass on an empty tree")
	}

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
		if len(found[path]) == 0 {
			t.Errorf("%s is allowed to fetch (%s) and names no network primitive: "+
				"an allowance nothing uses is a permission waiting for the next module", path, reason)
		}
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
	raw, err := os.ReadFile(clientModule)
	if err != nil {
		t.Fatalf("read %s: %v", clientModule, err)
	}
	lines := codeLines(string(raw))
	if len(lines) == 0 {
		t.Fatalf("%s has no code lines: this test would pass on an empty file", clientModule)
	}
	var offences []string
	for _, line := range lines {
		if navigationSinks.MatchString(line.code) {
			offences = append(offences, strconv.Itoa(line.number)+": "+line.text)
		}
	}
	if len(offences) > 0 {
		t.Fatalf("%s can navigate:\n%s\na removed view is a sentence and a link back; the designer chooses",
			clientModule, strings.Join(offences, "\n"))
	}
}

// clientLiteral is one string literal found in a module.
type clientLiteral struct {
	line int
	text string
}

func clientStringLiterals(t *testing.T) []clientLiteral {
	t.Helper()
	raw, err := os.ReadFile(clientModule)
	if err != nil {
		t.Fatalf("read %s: %v", clientModule, err)
	}
	return stringLiteralsIn(string(raw))
}

// stringLiteralsIn returns every string literal in JavaScript source,
// comments removed first by codeLines (static_sinks_test.go) so a doc
// comment discussing an error sentence is not read as one.
//
// A template literal's whole body counts as one literal, interpolations
// included. That is conservative in the loud direction — a template
// mixing two values with a space between them is reported — which is the
// right way round for a guard whose whole subject is composed text.
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
