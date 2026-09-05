package web

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
// hand-written list — a per-tool test is eleven chances to forget the
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
