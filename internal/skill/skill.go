// Package skill holds the agent skill bundle: the prose that teaches an
// agent to use Maestro's tool surface, embedded in the binary and served
// as a zip.
package skill

import (
	"embed"
	"io/fs"
)

//go:embed files
var embedded embed.FS

// Files is the bundle as it ships, rooted at the bundle's own top level:
// "skill.md", "reference/errors.md", "genres/mmorpg.json".
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
