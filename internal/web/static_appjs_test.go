package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// appScriptSource reads internal/web/static/app.js straight off disk
// rather than through the running server: the two properties pinned in
// this file are about what the *source* does, not about any one HTTP
// response, and reading the file directly means these tests fail the
// moment the source changes, not only once whatever code path happens to
// exercise the change is hit.
func appScriptSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read static/app.js: %v", err)
	}
	return string(body)
}

// TestAppScriptNeverWritesRawHTML pins the property a live-browser review
// had to verify by hand: a crafted game name (or display name) reaches
// the DOM only as text, never as markup. app.js writes every
// server-supplied string through textContent or an element built with
// createElement — this test pins that by asserting the source contains
// none of the DOM sinks that would instead interpret a string as HTML.
// A future edit that reaches for innerHTML "just this once" — to add a
// link inside a message, say — regresses this silently in the browser
// (the page still renders, just now executes whatever a game name
// contains) unless this test catches it first.
func TestAppScriptNeverWritesRawHTML(t *testing.T) {
	source := appScriptSource(t)
	// Matched as actual usage (an assignment or a call), not as a bare
	// word: app.js's own comments say "never innerHTML" at each call site
	// that could otherwise reach for it, and a naive substring check
	// would trip on those comments before it ever caught a real one.
	sinks := regexp.MustCompile(`\.innerHTML\s*=|\.outerHTML\s*=|\.insertAdjacentHTML\s*\(|document\.write\s*\(`)
	if loc := sinks.FindString(source); loc != "" {
		t.Errorf("app.js uses %q — every DOM write in this file must go through textContent or createElement instead", loc)
	}
}

// TestLastVisitedRedirectIsCorroboratedBeforeItFires pins the second
// property a live-browser review had to verify by hand: the picker never
// redirects off a remembered slug alone, only once the same response
// that returned it has confirmed the slug is still in the caller's own
// game list. The exact source shape asserted here is deliberately
// specific — remembered is checked truthy AND found via games.some(...)
// in one guard, with the redirect nested directly inside it — so that
// weakening the guard (dropping the .some check, or moving the redirect
// out from under it "to simplify") fails this test instead of only
// showing up the next time someone loses access to a game and gets
// bounced toward it anyway.
func TestLastVisitedRedirectIsCorroboratedBeforeItFires(t *testing.T) {
	source := appScriptSource(t)
	guard := regexp.MustCompile(
		`if \(remembered && games\.some\(\(game\) => game\.slug === remembered\)\) \{\s*\n\s*window\.location\.href = `,
	)
	if !guard.MatchString(source) {
		t.Fatal("app.js no longer redirects to the remembered game from directly inside the games.some(...) corroboration check")
	}
}

// TestRememberGameIsOnlyCalledAfterCorroboration pins the companion fix:
// the remembered slug is written only once GET /api/games has confirmed
// the slug is actually reachable, never unconditionally from the URL the
// moment a page loads — a stray or stale /g/{slug} link must not be able
// to overwrite a good remembered value with one that cannot be reached.
//
// **The call site moved in Task 15 and this test moved with it**, which
// is this repository's standing failure pattern caught in the act: the
// game page became `static/pages/home.js` and the slug corroboration
// became `openGame` in `static/pages/page.js`, so a test that kept
// reading app.js would have passed for ever on a file that no longer
// contains the behaviour. It now asserts the call is where the
// corroboration is, and — the half that could not be asserted while the
// two lived in one file — that the whole front end holds exactly **one**
// call site, so a second page cannot start remembering a slug it never
// checked.
func TestRememberGameIsOnlyCalledAfterCorroboration(t *testing.T) {
	const callSite = "rememberGame(slug);"
	// The declaration ("export function rememberGame(slug) {") ends in a
	// brace, not a semicolon, so it is not counted as a call — a plain
	// substring count would otherwise pass even if every real call site
	// were deleted.
	calls := map[string]int{}
	total := 0
	for _, path := range ownModules(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if n := strings.Count(string(raw), callSite); n > 0 {
			calls[path] = n
			total += n
		}
	}
	if total != 1 {
		t.Fatalf("found %d call site(s) for %q across the front end (%v); want exactly 1 — "+
			"a second page remembering a slug is a second page that has to corroborate it first",
			total, callSite, calls)
	}
	if _, ok := calls[openGameModule]; !ok {
		t.Fatalf("the one call site for %q is not in %s, which is where the slug is corroborated "+
			"against the caller's own game list", callSite, openGameModule)
	}

	source := openGameSource(t)
	// The corroboration, the refusal it produces, and the call, in that
	// order. Asserting the order is what stops the call being hoisted
	// above the check "to simplify".
	corroboration := strings.Index(source, "answer.games.find((row) => row.slug === slug)")
	refusal := strings.Index(source, "if (game === null) {")
	call := strings.Index(source, callSite)
	switch {
	case corroboration == -1:
		t.Fatal(openGameModule + " no longer resolves the slug against the caller's own game list")
	case refusal == -1:
		t.Fatal(openGameModule + " no longer refuses a slug that is not in that list")
	case call < corroboration || call < refusal:
		t.Fatalf("%s remembers the slug before it has been corroborated (find at %d, refusal at %d, "+
			"call at %d)", openGameModule, corroboration, refusal, call)
	}
}

// openGameModule is where the slug corroboration lives since Task 15.
const openGameModule = "static/pages/page.js"

func openGameSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(openGameModule)
	if err != nil {
		t.Fatalf("read %s: %v", openGameModule, err)
	}
	return string(body)
}

// ownModules is every module this project wrote, vendored code excluded.
// It is a walk rather than a list for the reason the component roster is
// one: a list is a thing the next file forgets to join.
func ownModules(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir("static", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(path)
		if d.IsDir() {
			if slashed == vendorDir {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".js", ".mjs":
			out = append(out, slashed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("found no own module under internal/web/static: this test would pass on an empty tree")
	}
	return out
}
