// Command maestro-skilldoc regenerates the two pages of the skill bundle
// that are generated rather than written: the tool index,
// internal/skill/files/reference/tools.md, and the REST route table,
// internal/skill/files/reference/rest.md — both from what the server
// actually registers.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/neverbot/maestro/internal/web"
)

func main() {
	reference := filepath.Join("internal", "skill", "files", "reference")
	out := flag.String("o", filepath.Join(reference, "tools.md"),
		"where to write the generated tool index, relative to the working directory")
	routesOut := flag.String("routes", filepath.Join(reference, "rest.md"),
		"where to write the generated REST route table")
	flag.Parse()

	server := web.NewToolReferenceServer()
	for _, page := range []struct {
		path string
		body string
	}{
		{path: *out, body: server.ToolReference()},
		{path: *routesOut, body: server.RouteReference()},
	} {
		if err := os.MkdirAll(filepath.Dir(page.path), 0o750); err != nil {
			fmt.Fprintf(os.Stderr, "maestro-skilldoc: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(page.path, []byte(page.body), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "maestro-skilldoc: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "maestro-skilldoc: wrote %s\n", page.path)
	}
}
