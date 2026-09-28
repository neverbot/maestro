package web_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// TestTheGameSettingsScreen drives internal/web/jstest/settings_test.mjs:
// the two tabs, who may mint a token, the command the Agents tab hands
// out, and the revoke that asks first.
func TestTheGameSettingsScreen(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/settings_test.mjs")
}

// TestTheInstallCommandSaysWhatThisServerActuallySpeaks is the guard the
// JavaScript cannot be: the command on that screen tells a designer how
// to reach *this* server, and every fact in it is a fact about Go code
// three directories away.
func TestTheInstallCommandSaysWhatThisServerActuallySpeaks(t *testing.T) {
	t.Parallel()
	page, err := os.ReadFile("static/pages/settings.js")
	assert.Must(t, err == nil, "read settings.js: %v", err)
	source := string(page)

	server, err := os.ReadFile("server.go")
	assert.Must(t, err == nil, "read server.go: %v", err)
	// The route the command points at, read from the server rather than
	// repeated here.
	if !strings.Contains(string(server), `s.route("/mcp"`) {
		t.Fatal("this server no longer serves /mcp at that path, so the command the Agents tab " +
			"hands out points at nothing")
	}
	assert.Should(t, strings.Contains(source, `export const MCP_PATH = "/mcp"`), "settings.js names a different MCP path from the one server.go registers")
	// `--transport http` is only true because the handler is the
	// streamable HTTP one. An SSE-only server would need a different
	// flag and the same screen would be quietly wrong.
	if !strings.Contains(string(server), "NewStreamableHTTPHandler") {
		t.Fatal("the MCP handler is no longer streamable HTTP, so `--transport http` is no longer the " +
			"right flag in the command the Agents tab hands out")
	}
	assert.Should(t, strings.Contains(source, "--transport http"), "the install command no longer states the transport")

	// The credential travels in a header. A token in a URL is a token in
	// a shell history, a proxy log and this server's own access log.
	assert.Should(t, !strings.Contains(source, "?token=") && !strings.Contains(source, "&token="), "settings.js puts a token in a query string")

	// The three token routes the screen calls, as the server spells them.
	for _, route := range []string{
		`"POST /api/games/{game}/tokens"`,
		`"GET /api/games/{game}/tokens"`,
		`"DELETE /api/games/{game}/tokens/{token}"`,
	} {
		assert.Should(t, strings.Contains(string(server), route), "the server no longer registers %s, which the Agents tab calls", route)
	}
	client, err := os.ReadFile("static/client.js")
	assert.Must(t, err == nil, "read client.js: %v", err)
	for _, call := range []string{"listTokens", "createToken", "revokeToken"} {
		assert.Should(t, strings.Contains(string(client), "async function "+call), "client.js no longer carries %s, so the screen calls nothing", call)
	}
}

// TestTheDialogIsASurfaceAndTrapsItsOwnFocus guards the two halves of
// mst-dialog a harness cannot see: the sheet it adopts (a shadow root's
// styles are invisible to every check in jstest/, which runs against a
// stub with no CSS at all) and the fact that it is reached through one
// shared component rather than hand-rolled per screen.
func TestTheDialogIsASurfaceAndTrapsItsOwnFocus(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("static/components/mst-dialog.js")
	assert.Must(t, err == nil, "read mst-dialog.js: %v", err)
	source := string(raw)

	for _, token := range []string{"var(--paper)", "var(--ink)", "var(--line)", "var(--radius)", "var(--shadow-2)"} {
		assert.Should(t, strings.Contains(source, token), "the dialog's panel does not draw itself with %s", token)
	}
	// The dim behind it is tinted toward the paper's own brown. A
	// neutral black wash over this ground is what makes an interface
	// read as a dashboard, which docs/product.md names as the first
	// anti-reference.
	assert.Should(t, !strings.Contains(source, "rgba(0, 0, 0") && !strings.Contains(source, "rgba(0,0,0"), "the backdrop is a neutral black wash")
	assert.Should(t, strings.Contains(source, `setAttribute("aria-modal", "true")`), "the dialog does not tell a screen reader that the page behind it is blocked")
	assert.Should(t, strings.Contains(source, `aria-labelledby`), "the dialog is announced as `dialog` with no name")

	// One dialog, not one per screen. A second hand-rolled modal is the
	// thing this component exists to stop, so the pages are read for a
	// backdrop of their own.
	pages, err := filepath.Glob("static/pages/*.js")
	assert.Must(t, err == nil, "glob pages: %v", err)
	for _, path := range pages {
		body, err := os.ReadFile(path)
		assert.Must(t, err == nil, "read %s: %v", path, err)
		assert.Should(t, !strings.Contains(string(body), "position: fixed"), "%s pins something to the viewport of its own; a floating surface is "+
			"components/mst-dialog.js or components/mst-hint.js, not a page's own CSS", path)
	}
}

// **The People tab's ids, read out of the code that reads them.**
// jstest mounts its own document, so every check in settings_test.mjs
// passes against a page that does not exist: the tab could look for
// `members` for a year while the shell calls it `member-list` and
// nothing would go red. This is the crossing — the ids the code asks
// for, against the shell that has to carry them — and the same shape of
// defect this repository has shipped more than once.
func TestThePeopleTabAsksForIdsTheShellActuallyCarries(t *testing.T) {
	t.Parallel()
	page, err := os.ReadFile("static/pages/settings.js")
	assert.Must(t, err == nil, "read settings.js: %v", err)
	shell, err := os.ReadFile("static/settings.html")
	assert.Must(t, err == nil, "read settings.html: %v", err)
	body := string(page)
	start := strings.Index(body, "export async function peopleTab(")
	assert.Must(t, start >= 0, "peopleTab is gone from settings.js; this guard now protects nothing")
	end := strings.Index(body[start:], "\nexport ")
	if end < 0 {
		end = len(body) - start
	}
	tab := body[start : start+end]

	lookups := 0
	for rest := tab; ; {
		at := strings.Index(rest, `getElementById("`)
		if at < 0 {
			break
		}
		rest = rest[at+len(`getElementById("`):]
		id := rest[:strings.Index(rest, `"`)]
		lookups++
		assert.Should(t, strings.Contains(string(shell), `id="`+id+`"`), "peopleTab reads #%s and settings.html has no such element", id)
	}
	// fillState is given its host by name too, and it is the half that
	// draws the empty states.
	for _, id := range []string{"members-empty", "game-invites-empty"} {
		assert.Should(t, strings.Contains(string(shell), `id="`+id+`"`), "peopleTab fills #%s and settings.html has no such element", id)
	}
	assert.Must(t, lookups >= 8, "found %d id lookups in peopleTab, want at least 8; the scan stopped matching the code", lookups)

	// And the tab itself is reachable: a panel nothing opens is a screen
	// nobody sees.
	for _, id := range []string{"tab-people", "panel-people"} {
		assert.Should(t, strings.Contains(string(shell), `id="`+id+`"`), "settings.html has no #%s, so the People tab cannot be opened", id)
	}
	assert.Should(t, strings.Contains(string(shell), `data-panel="panel-people"`), "the People tab button names no panel, so pressing it opens nothing")
}
