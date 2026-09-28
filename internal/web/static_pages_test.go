package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/web"
)

// The four properties of Task 15's routing that live in the *arrangement*
// of this package rather than in any one request, and that a browser
// would otherwise be the first thing to discover.
func pageModules(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join("static", "pages", "*.js"))
	assert.Must(t, err == nil, "glob page modules: %v", err)
	assert.Must(t, len(found) != 0, "found no module under internal/web/static/pages: this test would pass on an empty tree")
	sort.Strings(found)
	return found
}

// shellFiles is every HTML shell on disk.
func shellFiles(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join("static", "*.html"))
	assert.Must(t, err == nil, "glob shells: %v", err)
	assert.Must(t, len(found) != 0, "found no HTML shell under internal/web/static")
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
func TestEveryShellIsReachableByItsRoute(t *testing.T) {
	t.Parallel()
	routes := web.ShellRoutesForTest()
	dispatching := web.DispatchingShellsForTest()
	server, _, _ := newTestServer(t)
	// Two exemptions, and both are named. "/" is handleRoot's decision
	// between the picker, one game and the sign-in page; "/" without a
	// method is the catch-all every unmatched address falls to, which by
	// definition is not reached at the pattern it is registered under.
	// TestAnUnknownAddressIsStillThisProduct drives the second one for
	// real.
	assert.Must(t, len(dispatching) == 2, "%d shell route(s) are exempt from the byte comparison below, want exactly 2: "+
		"an exemption nobody bounds is where the next unserved shell hides", len(dispatching))

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
		assert.Must(t, err == nil, "read %s: %v", shell, err)
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
			assert.Should(t, rec.Body.String() == string(want), "GET %s served %d bytes, which are not %s's %d", url, rec.Body.Len(), name, len(want))
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
func TestThePickerHasAnAddressThatDoesNotRedirect(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile(filepath.Join("static", "app.js"))
	assert.Must(t, err == nil, "read static/app.js: %v", err)
	match := gamesPathRegexp.FindSubmatch(source)
	assert.Must(t, match != nil, "static/app.js declares no GAMES_PATH: the header's switcher has nowhere to point")
	gamesPath := string(match[1])

	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()
	user, _ := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "picker@example.test", DisplayName: "Picker", Password: "password12345",
	})
	if _, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "picker@example.test")

	// The control: "/" is still the shortcut it was, for the same caller
	// in the same request. If this stopped redirecting, the assertion
	// below would be proving nothing.
	root := httptest.NewRecorder()
	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootReq.AddCookie(cookie)
	srv.ServeHTTP(root, rootReq)
	assert.Must(t, root.Code == http.StatusFound && root.Header().Get("Location") == "/g/azeroth", "GET / answered %d to /g/azeroth=%q, want a 302 into the one game: the single-game "+
		"shortcut this fix had to keep is gone", root.Code, root.Header().Get("Location"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, gamesPath, nil)
	req.AddCookie(cookie)
	srv.ServeHTTP(rec, req)
	assert.Must(t, rec.Code == http.StatusOK, "GET %s answered %d, want 200: the game switcher links here from inside every game",
		gamesPath, rec.Code)
	want, err := os.ReadFile(filepath.Join("static", "index.html"))
	assert.Must(t, err == nil, "read static/index.html: %v", err)
	assert.Should(t, rec.Body.String() == string(want), "GET %s served %d bytes, which are not the picker's %d", gamesPath, rec.Body.Len(), len(want))
}

// TestNoShellIsServedTwice keeps the new shells inside the /static/ file
// server's refusal.
func TestNoShellIsServedTwice(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)
	for _, shell := range shellFiles(t) {
		url := "/static/" + filepath.Base(shell)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		assert.Should(t, rec.Code == http.StatusNotFound, "GET %s answered %d, want 404: a shell reachable under /static/ is a shell reachable "+
			"without the dispatch its own route performs", url, rec.Code)
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
func TestNoPageURLContainsAUUID(t *testing.T) {
	t.Parallel()
	var offences []string
	scanned := 0
	for _, module := range pageModules(t) {
		raw, err := os.ReadFile(module)
		assert.Must(t, err == nil, "read %s: %v", module, err)
		scanned++
		for _, line := range codeLines(string(raw)) {
			if uuidLiteral.MatchString(line.code) {
				offences = append(offences, filepath.ToSlash(module)+":"+strconv.Itoa(line.number)+": "+line.text)
				continue
			}
			// **A page address, not an API one.** The rule is about the
			// URLs a designer reads, types and recognises; an endpoint
			// is addressed however the server spells it, and one of
			// them — DELETE /api/invites/{invite} — takes the id
			// because an invitation has no key and never gets one. A
			// guard that refused that line would be a guard asking the
			// front end to invent an address the server does not serve.
			if idInPath.MatchString(line.code) && !strings.Contains(line.code, `"/api/`) {
				offences = append(offences, filepath.ToSlash(module)+":"+strconv.Itoa(line.number)+": "+line.text)
			}
		}
	}
	assert.Must(t, scanned != 0, "scanned no page module: this test would pass on an empty tree")
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("%d line(s) in the page modules put an id in an address:\n%s\n"+
			"every route in this product is addressed by slug and every row under it by key",
			len(offences), strings.Join(offences, "\n"))
	}
	t.Logf("scanned %d page module(s); none addresses a row by id", scanned)
}

// TestEveryShellCarriesTheImportMapAndTheStylesheet is the third thing a
// shell can be missing and still look fine in a diff.
func TestEveryShellCarriesTheImportMapAndTheStylesheet(t *testing.T) {
	t.Parallel()
	for _, shell := range shellFiles(t) {
		raw, err := os.ReadFile(shell)
		assert.Must(t, err == nil, "read %s: %v", shell, err)
		src := string(raw)
		name := filepath.Base(shell)
		assert.Should(t, strings.Contains(src, `<script type="importmap">`), "%s declares no import map: every bare specifier on it fails to resolve", name)
		assert.Should(t, strings.Contains(src, `<link rel="stylesheet" href="/static/styles.css">`), "%s does not link the stylesheet", name)
		assert.Should(t, strings.Contains(src, `<meta charset="utf-8">`), "%s declares no charset: a game's own words are UTF-8 and a sniffed encoding mangles them", name)
		assert.Should(t, strings.Contains(src, "<noscript>"), "%s has no noscript notice: with scripting off it renders as a blank page that says nothing", name)
		assert.Should(t, strings.Contains(src, `<script type="module" src="/static/`), "%s loads no module: a shell with no script is a page that never fills in", name)
	}
}

// TestEveryPageModuleIsLoadedByAShell is the other direction, and it is
// the one that catches a module nobody reaches.
func TestEveryPageModuleIsLoadedByAShell(t *testing.T) {
	t.Parallel()
	loaded := map[string]bool{}
	for _, shell := range shellFiles(t) {
		raw, err := os.ReadFile(shell)
		assert.Must(t, err == nil, "read %s: %v", shell, err)
		for _, hit := range // A hyphen is part of a module name: `not-found.js` was invisible
		// to this scan while being loaded by the shell it belongs to, which
		// is the shape of hole this test exists to close.
		regexp.MustCompile(`src="/static/pages/([a-z-]+\.js)"`).FindAllStringSubmatch(string(raw), -1) {
			loaded[hit[1]] = true
		}
	}
	assert.Must(t, len(loaded) != 0, "no shell loads a page module: this test would pass on an empty tree")
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
	assert.Should(t, !(loaded[shared]), "a shell loads %s directly; it is the plumbing the seven page modules import, not a page", shared)
}

// TestTheNarrowFallbackIsWiredAtBothCallSites is a source-shape guard,
// which is the weakest kind of check in this repository and the correct
// answer for what it holds.
func TestTheNarrowFallbackIsWiredAtBothCallSites(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("static", "pages", "view.js"))
	assert.Must(t, err == nil, "read static/pages/view.js: %v", err)
	src := string(raw)

	assert.Should(t, strings.Contains(src, "watchWidth(surface, options);"), "viewPage no longer installs the width watch: a narrow window would draw a picture with every write armed")
	// The `finally` is the point and not the call: a redraw that threw
	// must still leave the fallback applied, and a call placed after the
	// three returns of drawPicture would miss two of them.
	assert.Should(t, regexp.MustCompile(`(?s)finally\s*\{\s*applyWidth\(state, state\.narrow === true\);`).MatchString(src), "draw no longer re-applies the fallback in a finally: a redraw would put the drawing back and re-arm the writes")
	assert.Should(t, strings.Contains(src, "state.arrangement.setDrawn(!fell);"), "applyWidth no longer disarms the arrangement: hiding the canvas in CSS alone leaves a keyboard nudging a drawing nobody can see")
}

// TestAnUnknownAddressIsStillThisProduct drives the catch-all for real,
// because the shell table above cannot: its entry is registered by hand
// and is reached at no pattern of its own.
func TestAnUnknownAddressIsStillThisProduct(t *testing.T) {
	t.Parallel()
	server, _, _ := newTestServer(t)
	shell, err := os.ReadFile(filepath.Join("static", "not-found.html"))
	assert.Must(t, err == nil, "read the shell: %v", err)

	t.Run("a person gets the page", func(t *testing.T) {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/g/azeroth/images", nil))
		assert.Should(t, rec.Code == http.StatusNotFound, "status = %d, want 404: a missing page that answers 200 is a missing page "+
			"no crawler, link checker or `curl -f` can see", rec.Code)
		if got := rec.Body.String(); got != string(shell) {
			t.Errorf("an unknown address did not serve not-found.html: it answered %q", got[:min(len(got), 60)])
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q, want text/html", ct)
		}
	})

	t.Run("a client gets JSON", func(t *testing.T) {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nothing-here", nil))
		assert.Should(t, rec.Code == http.StatusNotFound, "status = %d, want 404", rec.Code)
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("an API caller was handed something that is not JSON: %v", err)
		}
		assert.Should(t, body.Error == "not_found" && body.Message != "", "body = %+v, want the coded envelope every other refusal uses", body)
	})

	t.Run("a known address under the wrong method is still 405", func(t *testing.T) {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
		assert.Should(t, rec.Code == http.StatusMethodNotAllowed, "status = %d, want 405: the catch-all swallowed the distinction between "+
			"\"no such address\" and \"not that way\"", rec.Code)
	})

	t.Run("a known address is untouched", func(t *testing.T) {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		assert.Should(t, rec.Code == http.StatusOK, "status = %d, want 200: the catch-all is shadowing a real route", rec.Code)
	})
}
