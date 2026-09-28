package web_test

import (
	"os"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

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
