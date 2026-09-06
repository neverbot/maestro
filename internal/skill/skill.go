// Package skill holds the agent skill bundle: the prose that teaches an
// agent to use Maestro's tool surface, embedded in the binary and served
// as a zip.
//
// **This package holds no contract that a tool description holds.** The
// bundle teaches what spans calls — order, judgement, consequence — and
// routes to the descriptions for everything a single call can state
// about itself. internal/web/skilldoc_test.go enforces that in both
// directions; see the plan section "the one-tool test".
//
// It imports nothing from internal/web, and it must not: web imports
// this package to register skill.install, so the dependency has one
// direction. Guards that need the registered tool table live in
// internal/web and read the bundle through Files().
package skill

import (
	"embed"
	"io/fs"
)

//go:embed files
var embedded embed.FS

// Files is the bundle as it ships, rooted at the bundle's own top level:
// "skill.md", "reference/errors.md", "genres/mmorpg.json".
//
// It is exported for the guards. Every one of them walks this tree with
// fs.WalkDir rather than naming pages, so a page added tomorrow is
// covered without editing a test — the failure mode the last sub-project
// hit nine times was a rule that watched the files it was written for.
func Files() fs.FS {
	sub, err := fs.Sub(embedded, "files")
	if err != nil {
		// Unreachable: the embed directive above is what creates the
		// directory this reads, so a failure here means the binary was
		// built from a tree with no bundle in it.
		panic("skill: embedded bundle is missing: " + err.Error())
	}
	return sub
}
