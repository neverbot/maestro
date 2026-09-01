package web_test

import (
	"os"
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
// game.html must only call rememberGame(slug) once GET /api/games has
// confirmed slug is actually reachable, never unconditionally from the
// URL the moment the page loads — a stray or stale /g/{slug} link must
// not be able to overwrite a good remembered value with one that cannot
// be reached. The call site (a statement, "rememberGame(slug);") is
// matched separately from the function's own declaration ("function
// rememberGame(slug) {"), which would otherwise also contain the
// substring "rememberGame(slug)" and defeat a naive count-based check.
func TestRememberGameIsOnlyCalledAfterCorroboration(t *testing.T) {
	source := appScriptSource(t)
	// The call site ("rememberGame(slug);", an argument-list-then-semicolon
	// statement) is distinguished from the function's own declaration
	// ("function rememberGame(slug) {", which ends in a brace, not a
	// semicolon) purely by that trailing character — a plain substring
	// count would otherwise also match the declaration and silently pass
	// even if every real call site were deleted.
	const callSite = "rememberGame(slug);"
	if n := strings.Count(source, callSite); n != 1 {
		t.Fatalf("found %d call sites for %q; want exactly 1", n, callSite)
	}

	foundGameIdx := strings.Index(source, "if (game) {")
	if foundGameIdx == -1 {
		t.Fatal("app.js no longer has the expected if (game) { ... } shape this test depends on")
	}
	callIdx := strings.Index(source, callSite)
	if callIdx < foundGameIdx {
		t.Fatal("rememberGame(slug) is called before app.js confirms the slug is in the caller's own game list")
	}
}
