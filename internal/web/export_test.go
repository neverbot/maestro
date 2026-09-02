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
