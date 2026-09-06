package web

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This file exposes a little of this package's internal routing
// bookkeeping to the external web_test package, and nothing else. It is
// a _test.go file, so none of it is compiled into the binary — the same
// reason internal/metamodel keeps its own test-only helpers out of the
// shipped API.

// ContentWritePatternsForTest returns every game-content route
// registered as a write, in registration order. It exists for
// TestEveryContentWriteRouteRefusesAViewer (api_metamodel_test.go),
// which drives a real viewer at every one of them rather than at a
// hand-written list that a route added tomorrow would not appear on.
func (s *Server) ContentWritePatternsForTest() []string {
	return append([]string(nil), s.contentWritePatterns...)
}

// ScopedToolNamesForTest returns the name of every tool registered
// through addScopedTool on this server. It exists for
// TestEveryDocsToolRefusesAnotherGamesToken (mcp_docs_test.go), which
// drives every docs.* tool at another game's id rather than a
// hand-written list — a per-tool test is twelve chances to forget the
// twelfth, and this is what makes a tool added tomorrow fail that test
// until somebody covers it.
func (s *Server) ScopedToolNamesForTest() []string {
	names := make([]string, 0, len(s.mcpScopedTools))
	for name := range s.mcpScopedTools {
		names = append(names, name)
	}
	return names
}

// RegisteredPatternsForTest returns every pattern registered on this
// server, in registration order. It exists for
// TestTheProseRoutesAreVisibleToTheConventionTests (api_docs_test.go),
// which asserts from a server built with no Markdown service that the
// prose routes are there anyway — the property that keeps
// TestEveryGameScopedRouteGoesThroughRequireProject from going blind to
// them.
func (s *Server) RegisteredPatternsForTest() []string {
	return append([]string(nil), s.registeredPatterns...)
}

// ContentPatternsForTest returns every game-content route, reads and
// writes alike, in registration order. Same caller, same reason:
// TestEveryContentRouteIsRegisteredAsContent compares against this set,
// so a route missing from it is a route that test cannot see.
func (s *Server) ContentPatternsForTest() []string {
	return append([]string(nil), s.contentPatterns...)
}

// ToolDescriptionsForTest returns the description every tool registered
// through addScopedTool was registered with. It exists for
// TestTheViewsToolDescriptionsAreGeneratedRatherThanRestated
// (mcp_views_test.go), which reads the text an agent actually reads: a
// test that called views.RendererDescription() itself would prove the
// function exists, not that its output ever reached a tool.
func (s *Server) ToolDescriptionsForTest() map[string]string {
	out := make(map[string]string, len(s.mcpToolDescriptions))
	for name, description := range s.mcpToolDescriptions {
		out[name] = description
	}
	return out
}

// ShellRoutesForTest returns every route that serves an HTML shell, keyed
// by **pattern** and valued by the shell it serves. It exists for
// TestEveryShellIsReachableByItsRoute (static_pages_test.go), which
// enumerates the shells on disk against it and then drives a real
// request at each pattern: a shell added without a route must fail there
// rather than 404 in a browser.
//
// It is keyed by pattern and not by file because a file is not a key: the
// picker shell (index.html) is served at "/" and at "/games", one of them
// a dispatching shortcut and the other deliberately not, and a map keyed
// by file could only remember one of the two — silently dropping the
// route the browser bug this shape was written for actually needed
// tested.
func ShellRoutesForTest() map[string]string {
	out := make(map[string]string, len(shellRoutes))
	for _, shell := range shellRoutes {
		out[shell.pattern] = shell.file
	}
	return out
}

// DispatchingShellsForTest returns the patterns whose route decides
// something before it serves — handleRoot's redirect in particular — so
// the test above can assert a route exists for them without asserting
// that a bare GET returns their bytes. Keyed by pattern for the reason
// above: "/" dispatches and "/games" does not, and they serve one file.
func DispatchingShellsForTest() map[string]bool {
	out := make(map[string]bool, len(shellRoutes))
	for _, shell := range shellRoutes {
		if shell.dispatches {
			out[shell.pattern] = true
		}
	}
	return out
}

// MCPErrorForTest maps a domain error to its wire result, so
// TestEveryAnalysisSentinelHasAWireCode can drive a package's sentinel
// list through the real mapping rather than repeating it.
func MCPErrorForTest(err error) *mcp.CallToolResult {
	return mcpErrorFor(context.Background(), "test.tool", Caller{}, err)
}

// WriteDomainErrorForTest is the REST twin of the above, so a sentinel
// can be driven through both arms in one test and the two surfaces held
// to answering with one code.
func (s *Server) writeDomainErrorForTest(w http.ResponseWriter, r *http.Request, err error) {
	s.writeDomainError(w, r, err)
}

// WriteDomainErrorForTest builds the minimum server writeDomainError
// needs — it reads no option — and writes the refusal into w.
func WriteDomainErrorForTest(w http.ResponseWriter, r *http.Request, err error) {
	(&Server{}).writeDomainErrorForTest(w, r, err)
}

// ErrorCodesForTest is every wire code this package can answer with, so
// a guard over agent-facing prose can tell a code from a vocabulary word
// without a second list of codes to maintain.
func ErrorCodesForTest() []string {
	return []string{
		errCodeUnauthorized, errCodeInternal, errCodeNotFound, errCodeBadRequest,
		errCodeScopeViolation, errCodeRetryable, errCodeVersionConflict,
		errCodeSchemaViolation, errCodeInvalidSchema, errCodeInvalidInput,
		errCodeEndpointTypeMismatch, errCodeInUse, errCodeQueryInvalid,
		errCodeRendererRequirements, errCodeLimitExceeded, errCodeQueryStale,
		errCodeSemanticsUndeclared, errCodeForbidden,
	}
}
