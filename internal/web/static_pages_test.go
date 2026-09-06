package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/web"
)

// The four properties of Task 15's routing that live in the *arrangement*
// of this package rather than in any one request, and that a browser
// would otherwise be the first thing to discover.
//
// Every route in this sub-project serves a static shell and resolves
// nothing server-side, which makes the shell-to-route join the one thing
// a Go test can hold about it — and the one thing nobody would notice
// breaking until a page 404ed.

// pageModules is every module under static/pages/, which is where the
// seven page modules live. It is a walk rather than a list for the
// reason every roster in this package is: a list is what the next file
// forgets to join.
func pageModules(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join("static", "pages", "*.js"))
	if err != nil {
		t.Fatalf("glob page modules: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("found no module under internal/web/static/pages: this test would pass on an empty tree")
	}
	sort.Strings(found)
	return found
}

// shellFiles is every HTML shell on disk.
func shellFiles(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join("static", "*.html"))
	if err != nil {
		t.Fatalf("glob shells: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("found no HTML shell under internal/web/static")
	}
	sort.Strings(found)
	return found
}

// concreteURL turns a registered pattern into a URL a request can be
// made at, by giving every path variable a value. The values are
// deliberately ugly — a slug with an uppercase letter, a key with a
// hyphen — because a route that only works for lowercase words is a
// route that breaks on the first real game.
var pathVariable = regexp.MustCompile(`\{[^}]+\}`)

func concreteURL(pattern string) string {
	path := strings.TrimPrefix(pattern, "GET ")
	path = strings.ReplaceAll(path, "/{$}", "/")
	return pathVariable.ReplaceAllString(path, "Some-Key")
}

// TestEveryShellIsReachableByItsRoute is the join a browser would
// otherwise make first.
//
// A shell added to internal/web/static without a route is a page that
// exists, is embedded in the binary, is refused by the /static/ file
// server (deliberately — see TestNoShellIsServedTwice below) and is
// reachable at no URL at all. Nothing else in this package can see that:
// the shell compiles into the embed, every existing test goes on
// passing, and the only symptom is a 404 in front of a designer.
//
// It asserts both halves. Every shell has an entry in server.go's
// shellRoutes table, and every entry really serves that shell's bytes at
// that pattern — driven through a real request, so a table that named
// the wrong file fails here too.
func TestEveryShellIsReachableByItsRoute(t *testing.T) {
	routes := web.ShellRoutesForTest()
	dispatching := web.DispatchingShellsForTest()
	server, _, _ := newTestServer(t)
	if len(dispatching) != 1 {
		t.Fatalf("%d shell route(s) are exempt from the byte comparison below, want exactly 1: "+
			"an exemption nobody bounds is where the next unserved shell hides", len(dispatching))
	}

	// The table is keyed by pattern, because a shell may be served at
	// more than one address — index.html is the picker at "/" and at
	// "/games" — so the join back to the files on disk is built here.
	patterns := map[string][]string{}
	for pattern, file := range routes {
		patterns[file] = append(patterns[file], pattern)
	}

	for _, shell := range shellFiles(t) {
		name := filepath.Base(shell)
		served, ok := patterns[name]
		if !ok {
			t.Errorf("%s is embedded in the binary and no route serves it: a shell with no route is a page "+
				"that 404s in a browser and passes every test in this package", name)
			continue
		}
		want, err := os.ReadFile(shell)
		if err != nil {
			t.Fatalf("read %s: %v", shell, err)
		}
		for _, pattern := range served {
			if dispatching[pattern] {
				// handleRoot decides between the picker, a single game
				// and the sign-in page before it serves anything, so a
				// bare GET is not a test of it — auth_test.go and
				// api_projects_test.go drive those three decisions. What
				// is asserted here is the half this test owns: the shell
				// has a route at all.
				continue
			}
			url := concreteURL(pattern)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
			if rec.Code != http.StatusOK {
				t.Errorf("GET %s (for %s) answered %d, want 200", url, name, rec.Code)
				continue
			}
			if rec.Body.String() != string(want) {
				t.Errorf("GET %s served %d bytes, which are not %s's %d", url, rec.Body.Len(), name, len(want))
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("GET %s served as %q, want text/html", url, ct)
			}
		}
	}

	// And the table holds nothing that is not a shell, so an entry left
	// behind by a deleted page is a failure rather than a dead row.
	for _, name := range routes {
		if _, err := os.Stat(filepath.Join("static", name)); err != nil {
			t.Errorf("shellRoutes names %s, which is not a file under internal/web/static", name)
		}
	}
}

// gamesPathRegexp reads the picker's address out of app.js's own
// constant, so the test below joins the front end's spelling to the
// server's routing table rather than repeating a literal that could
// drift from either.
var gamesPathRegexp = regexp.MustCompile(`export const GAMES_PATH = "([^"]+)";`)

// TestThePickerHasAnAddressThatDoesNotRedirect is the server half of the
// bug the author found on the first real session: signed in, they landed
// in one game and could reach no other, because the only address that
// listed their games was "/" — and "/" is a shortcut that sends a caller
// with one game straight back into it.
//
// The header's game switcher points at app.js's GAMES_PATH, and this is
// what makes that link a page rather than a 404: the constant is read
// out of the module and driven at this server, by a caller with exactly
// one game, which is precisely the caller "/" refuses to show a list to.
// Delete the route and this fails; rename the constant without adding
// the route and this fails too, which is the join a shipped-dead wiring
// slips through.
func TestThePickerHasAnAddressThatDoesNotRedirect(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("static", "app.js"))
	if err != nil {
		t.Fatalf("read static/app.js: %v", err)
	}
	match := gamesPathRegexp.FindSubmatch(source)
	if match == nil {
		t.Fatal("static/app.js declares no GAMES_PATH: the header's switcher has nowhere to point")
	}
	gamesPath := string(match[1])

	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "picker@studio.com", DisplayName: "Picker", Password: "password12345",
	})
	if _, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "picker@studio.com")

	// The control: "/" is still the shortcut it was, for the same caller
	// in the same request. If this stopped redirecting, the assertion
	// below would be proving nothing.
	root := httptest.NewRecorder()
	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootReq.AddCookie(cookie)
	srv.ServeHTTP(root, rootReq)
	if root.Code != http.StatusFound || root.Header().Get("Location") != "/g/azeroth" {
		t.Fatalf("GET / answered %d to /g/azeroth=%q, want a 302 into the one game: the single-game "+
			"shortcut this fix had to keep is gone", root.Code, root.Header().Get("Location"))
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, gamesPath, nil)
	req.AddCookie(cookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s answered %d, want 200: the game switcher links here from inside every game",
			gamesPath, rec.Code)
	}
	want, err := os.ReadFile(filepath.Join("static", "index.html"))
	if err != nil {
		t.Fatalf("read static/index.html: %v", err)
	}
	if rec.Body.String() != string(want) {
		t.Errorf("GET %s served %d bytes, which are not the picker's %d", gamesPath, rec.Body.Len(), len(want))
	}
}

// TestNoShellIsServedTwice keeps the new shells inside the /static/ file
// server's refusal.
//
// Every shell is reachable only at the routes shellRoutes declares,
// through a handler with its own dispatch logic — handleRoot's
// single-game shortcut and its redirect for an anonymous caller in
// particular. A second URL under
// /static/ would bypass all of that, which is precisely the defect a
// review found once: /static/index.html, wired through http.FileServerFS
// with no filtering, skipped handleRoot entirely.
//
// It enumerates the shells rather than naming four, so a shell added
// tomorrow is inside the rule the day it lands.
func TestNoShellIsServedTwice(t *testing.T) {
	server, _, _ := newTestServer(t)
	for _, shell := range shellFiles(t) {
		url := "/static/" + filepath.Base(shell)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s answered %d, want 404: a shell reachable under /static/ is a shell reachable "+
				"without the dispatch its own route performs", url, rec.Code)
		}
	}
}

// uuidLiteral is a uuid anywhere in a source line, in the canonical
// hyphenated spelling an id is written in on the wire.
var uuidLiteral = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// idInAPath is a template literal or a concatenation that puts an
// object's `id` into a path position: `/g/${game.id}`, `"/g/" + game.id`,
// `encodeURIComponent(row.id)` inside a path. The three spellings are
// separate alternatives rather than one loose pattern, because a loose
// one would report `node.id` in `joinEdges` — which is an index key and
// not an address, and is the one legitimate use of an id in this front
// end.
var idInPath = regexp.MustCompile(`/\$\{[A-Za-z_][A-Za-z0-9_.]*\.id\b|/" \+ [A-Za-z_][A-Za-z0-9_.]*\.id\b|/\$\{encodeURIComponent\([A-Za-z_][A-Za-z0-9_.]*\.id\)`)

// TestNoPageURLContainsAUUID pins the decision the whole product moved
// to and that a single convenient line would undo.
//
// **Every route is addressed by slug, and every row under it by key.**
// A page that built a uuid path would work perfectly — the ids exist,
// the API used to take them — and would produce URLs a designer cannot
// read, cannot type, and cannot recognise as the view they were looking
// at. It would also be undiscoverable: nothing 404s, nothing throws, and
// the only symptom is an address bar full of hex.
//
// The scan covers the page modules and the plumbing they share. It does
// **not** cover render/scene.js, whose `joinEdges` indexes nodes by
// `Node.ID` — that is a join inside one envelope and never an address,
// and its own doc comment says so.
func TestNoPageURLContainsAUUID(t *testing.T) {
	var offences []string
	scanned := 0
	for _, module := range pageModules(t) {
		raw, err := os.ReadFile(module)
		if err != nil {
			t.Fatalf("read %s: %v", module, err)
		}
		scanned++
		for _, line := range codeLines(string(raw)) {
			if uuidLiteral.MatchString(line.code) {
				offences = append(offences, filepath.ToSlash(module)+":"+strconv.Itoa(line.number)+": "+line.text)
				continue
			}
			if idInPath.MatchString(line.code) {
				offences = append(offences, filepath.ToSlash(module)+":"+strconv.Itoa(line.number)+": "+line.text)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no page module: this test would pass on an empty tree")
	}
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("%d line(s) in the page modules put an id in an address:\n%s\n"+
			"every route in this product is addressed by slug and every row under it by key",
			len(offences), strings.Join(offences, "\n"))
	}
	t.Logf("scanned %d page module(s); none addresses a row by id", scanned)
}

// TestTheUUIDScanReadsWhatItClaimsTo is the guard on the guard. The scan
// above reports nothing in two indistinguishable cases — the modules are
// clean, or it never matched anything — and this repository has shipped
// that shape twice.
func TestTheUUIDScanReadsWhatItClaimsTo(t *testing.T) {
	for name, caught := range map[string]string{
		"a uuid in a template":     "const href = `/g/${slug}/e/1e9d6b0c-4f7a-4a9e-8a5b-2c1d3e4f5a6b`;",
		"an id in a template path": "const href = `/g/${game.id}/views`;",
		"an id concatenated":       `const href = "/g/" + game.id;`,
		"an encoded id in a path":  "const href = `/g/${encodeURIComponent(game.id)}`;",
	} {
		if !uuidLiteral.MatchString(caught) && !idInPath.MatchString(caught) {
			t.Errorf("the scan misses %s: %q", name, caught)
		}
	}
	for name, allowed := range map[string]string{
		"an id used as an index key": "if (typeof node.id !== \"string\") continue;",
		"an id in a body":            "const body = { asset_id: asset.id };",
		"a slug in a path":           "const href = `/g/${slug}/v/${key}`;",
		"an id compared":             "if (edge.source === node.id) continue;",
	} {
		if uuidLiteral.MatchString(allowed) || idInPath.MatchString(allowed) {
			t.Errorf("the scan reports %s, which is not an address: %q", name, allowed)
		}
	}
}

// TestEveryShellCarriesTheImportMapAndTheStylesheet is the third thing a
// shell can be missing and still look fine in a diff.
//
// static_vendor_test.go already holds that every shell's import map is
// the *same* map and that it precedes every module script; what it
// cannot see is a shell with no map at all, because its own enumeration
// starts from the shells that have one. A shell without the stylesheet
// renders as unstyled text, and one without the map 404s every bare
// specifier the moment a component is imported.
func TestEveryShellCarriesTheImportMapAndTheStylesheet(t *testing.T) {
	for _, shell := range shellFiles(t) {
		raw, err := os.ReadFile(shell)
		if err != nil {
			t.Fatalf("read %s: %v", shell, err)
		}
		src := string(raw)
		name := filepath.Base(shell)
		if !strings.Contains(src, `<script type="importmap">`) {
			t.Errorf("%s declares no import map: every bare specifier on it fails to resolve", name)
		}
		if !strings.Contains(src, `<link rel="stylesheet" href="/static/styles.css">`) {
			t.Errorf("%s does not link the stylesheet", name)
		}
		if !strings.Contains(src, `<meta charset="utf-8">`) {
			t.Errorf("%s declares no charset: a game's own words are UTF-8 and a sniffed encoding mangles them", name)
		}
		if !strings.Contains(src, "<noscript>") {
			t.Errorf("%s has no noscript notice: with scripting off it renders as a blank page that says nothing", name)
		}
		if !strings.Contains(src, `<script type="module" src="/static/`) {
			t.Errorf("%s loads no module: a shell with no script is a page that never fills in", name)
		}
	}
}

// TestEveryPageModuleIsLoadedByAShell is the other direction, and it is
// the one that catches a module nobody reaches.
//
// A page module under static/pages/ that no shell loads is dead code
// that every test in this package goes on passing over. The one
// exception is the plumbing module the others import, which is loaded
// because they are.
func TestEveryPageModuleIsLoadedByAShell(t *testing.T) {
	loaded := map[string]bool{}
	for _, shell := range shellFiles(t) {
		raw, err := os.ReadFile(shell)
		if err != nil {
			t.Fatalf("read %s: %v", shell, err)
		}
		for _, hit := range regexp.MustCompile(`src="/static/pages/([a-z]+\.js)"`).FindAllStringSubmatch(string(raw), -1) {
			loaded[hit[1]] = true
		}
	}
	if len(loaded) == 0 {
		t.Fatal("no shell loads a page module: this test would pass on an empty tree")
	}
	// page.js is imported by the seven and loaded by none, and that is
	// the whole of the exception.
	const shared = "page.js"
	for _, module := range pageModules(t) {
		name := filepath.Base(module)
		if name == shared || loaded[name] {
			continue
		}
		t.Errorf("static/pages/%s is loaded by no shell: a module nobody reaches is code no test can see", name)
	}
	if loaded[shared] {
		t.Errorf("a shell loads %s directly; it is the plumbing the seven page modules import, not a page", shared)
	}
}

// TestTheNarrowFallbackIsWiredAtBothCallSites is a source-shape guard,
// which is the weakest kind of check in this repository and the correct
// answer for what it holds.
//
// The fallback below tablet width has a runtime half and a wiring half.
// The runtime half — that an undrawn canvas refuses every write, and
// that the page's own keydown wiring writes nothing while it is hidden —
// is driven for real by jstest/writes_test.mjs and jstest/pages_test.mjs,
// which are the checks that matter. The wiring half is two lines with no
// runtime signature a harness can reach: `viewPage` installs the watch,
// and `draw` re-applies the fallback after *every* redraw. Delete
// either and nothing throws, no test the harness can run goes red, and
// the symptom is a picture that quietly comes back — freshly armed —
// the next time a stream event redraws a narrow window.
//
// So it is pinned by its shape. The plan's own learned section names
// this pattern and rates it honestly: a weak guard over a property with
// no runtime signature beats no guard, and pretending otherwise is how
// the property gets deleted twice.
func TestTheNarrowFallbackIsWiredAtBothCallSites(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("static", "pages", "view.js"))
	if err != nil {
		t.Fatalf("read static/pages/view.js: %v", err)
	}
	src := string(raw)

	if !strings.Contains(src, "watchWidth(surface, options);") {
		t.Error("viewPage no longer installs the width watch: a narrow window would draw a picture with every write armed")
	}
	// The `finally` is the point and not the call: a redraw that threw
	// must still leave the fallback applied, and a call placed after the
	// three returns of drawPicture would miss two of them.
	if !regexp.MustCompile(`(?s)finally\s*\{\s*applyWidth\(state, state\.narrow === true\);\s*\}`).MatchString(src) {
		t.Error("draw no longer re-applies the fallback in a finally: a redraw would put the drawing back and re-arm the writes")
	}
	if !strings.Contains(src, "state.arrangement.setDrawn(!fell);") {
		t.Error("applyWidth no longer disarms the arrangement: hiding the canvas in CSS alone leaves a keyboard nudging a drawing nobody can see")
	}
}
