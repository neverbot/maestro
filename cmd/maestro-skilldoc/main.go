// Command maestro-skilldoc regenerates the skill bundle's tool index,
// internal/skill/files/reference/tools.md, from the tools the server
// registers.
//
// It is the sqlc pattern this repository already runs in `make check`:
// a generated artefact committed to the tree, and a test
// (TestToolReferenceIsCurrent) that goes red when the source moved and
// the artefact did not. Run it from the repository root:
//
//	go run ./cmd/maestro-skilldoc
//
// **It builds a server with every optional domain service present.**
// The views tools are registered only when a views service is
// (TestTheViewsToolsAreAbsentWithoutAViewsService pins that), and the
// same holds for the metamodel and the markdown tools, so a generator
// with a nil service would quietly emit a file missing whole domains.
// The services are constructed over a nil connection pool because
// nothing here executes a query: registration reads only the tool
// definitions.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/neverbot/maestro/internal/web"
)

func main() {
	out := flag.String("o", filepath.Join("internal", "skill", "files", "reference", "tools.md"),
		"where to write the generated tool index, relative to the working directory")
	flag.Parse()

	reference := web.NewToolReferenceServer().ToolReference()
	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "maestro-skilldoc: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, []byte(reference), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "maestro-skilldoc: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "maestro-skilldoc: wrote %s\n", *out)
}
