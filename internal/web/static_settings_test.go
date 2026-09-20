package web_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
//
// A transport renamed or an MCP route moved would leave a screen
// confidently handing out a line that cannot work, with every test in
// this package still green — prose about code, in the one place where a
// reader cannot tell it is wrong until their agent fails to connect.
func TestTheInstallCommandSaysWhatThisServerActuallySpeaks(t *testing.T) {
	t.Parallel()
	page, err := os.ReadFile("static/pages/settings.js")
	if err != nil {
		t.Fatalf("read settings.js: %v", err)
	}
	source := string(page)

	server, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	// The route the command points at, read from the server rather than
	// repeated here.
	if !strings.Contains(string(server), `s.route("/mcp"`) {
		t.Fatal("this server no longer serves /mcp at that path, so the command the Agents tab " +
			"hands out points at nothing")
	}
	if !strings.Contains(source, `export const MCP_PATH = "/mcp"`) {
		t.Error("settings.js names a different MCP path from the one server.go registers")
	}
	// `--transport http` is only true because the handler is the
	// streamable HTTP one. An SSE-only server would need a different
	// flag and the same screen would be quietly wrong.
	if !strings.Contains(string(server), "NewStreamableHTTPHandler") {
		t.Fatal("the MCP handler is no longer streamable HTTP, so `--transport http` is no longer the " +
			"right flag in the command the Agents tab hands out")
	}
	if !strings.Contains(source, "--transport http") {
		t.Error("the install command no longer states the transport")
	}

	// The credential travels in a header. A token in a URL is a token in
	// a shell history, a proxy log and this server's own access log.
	if strings.Contains(source, "?token=") || strings.Contains(source, "&token=") {
		t.Error("settings.js puts a token in a query string")
	}

	// The three token routes the screen calls, as the server spells them.
	for _, route := range []string{
		`"POST /api/games/{game}/tokens"`,
		`"GET /api/games/{game}/tokens"`,
		`"DELETE /api/games/{game}/tokens/{token}"`,
	} {
		if !strings.Contains(string(server), route) {
			t.Errorf("the server no longer registers %s, which the Agents tab calls", route)
		}
	}
	client, err := os.ReadFile("static/client.js")
	if err != nil {
		t.Fatalf("read client.js: %v", err)
	}
	for _, call := range []string{"listTokens", "createToken", "revokeToken"} {
		if !strings.Contains(string(client), "async function "+call) {
			t.Errorf("client.js no longer carries %s, so the screen calls nothing", call)
		}
	}
}

// TestTheDialogIsASurfaceAndTrapsItsOwnFocus guards the two halves of
// mst-dialog a harness cannot see: the sheet it adopts (a shadow root's
// styles are invisible to every check in jstest/, which runs against a
// stub with no CSS at all) and the fact that it is reached through one
// shared component rather than hand-rolled per screen.
//
// The behaviour — Escape, the press outside, the secret leaving the page
// on close, Tab staying inside — is pinned in jstest/settings_test.mjs,
// which is where it belongs: it is behaviour, and it is mutation-checked
// there.
func TestTheDialogIsASurfaceAndTrapsItsOwnFocus(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("static/components/mst-dialog.js")
	if err != nil {
		t.Fatalf("read mst-dialog.js: %v", err)
	}
	source := string(raw)

	for _, token := range []string{"var(--paper)", "var(--ink)", "var(--line)", "var(--radius)", "var(--shadow-2)"} {
		if !strings.Contains(source, token) {
			t.Errorf("the dialog's panel does not draw itself with %s", token)
		}
	}
	// The dim behind it is tinted toward the paper's own brown. A
	// neutral black wash over this ground is what makes an interface
	// read as a dashboard, which docs/product.md names as the first
	// anti-reference.
	if strings.Contains(source, "rgba(0, 0, 0") || strings.Contains(source, "rgba(0,0,0") {
		t.Error("the backdrop is a neutral black wash")
	}
	if !strings.Contains(source, `setAttribute("aria-modal", "true")`) {
		t.Error("the dialog does not tell a screen reader that the page behind it is blocked")
	}
	if !strings.Contains(source, `aria-labelledby`) {
		t.Error("the dialog is announced as `dialog` with no name")
	}

	// One dialog, not one per screen. A second hand-rolled modal is the
	// thing this component exists to stop, so the pages are read for a
	// backdrop of their own.
	pages, err := filepath.Glob("static/pages/*.js")
	if err != nil {
		t.Fatalf("glob pages: %v", err)
	}
	for _, path := range pages {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(body), "position: fixed") {
			t.Errorf("%s pins something to the viewport of its own; a floating surface is "+
				"components/mst-dialog.js or components/mst-hint.js, not a page's own CSS", path)
		}
	}
}
