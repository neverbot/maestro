package web

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/neverbot/maestro/internal/views"
)

// paramDeclaration matches one line of static/render/params.js.
var paramDeclaration = regexp.MustCompile(`^export const PARAM_[A-Z0-9_]+ = "([a-z0-9_]+)";$`)

// checkRendererParams refuses to serve when the drawing modules and the
// catalogue disagree about what a renderer's knobs are called.
//
// The names live twice by necessity: the server validates a stored view
// and describes the catalogue to an agent, and the browser reads the
// same keys out of a view it is about to draw. Nothing in either
// language makes them agree, and a rename carried to one side only is
// invisible — the write is accepted, the picture comes out without that
// knob, and no request fails. This is the one thing that notices, and it
// notices at start-up rather than on the screen of whoever opens the
// view next.
func checkRendererParams() error {
	raw, err := fs.ReadFile(assets, "render/params.js")
	if err != nil {
		return fmt.Errorf("read render/params.js: %w", err)
	}

	declared := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if m := paramDeclaration.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			declared[m[1]] = true
		}
	}
	if len(declared) == 0 {
		return fmt.Errorf("render/params.js declares no parameter: this check would pass on an empty file")
	}

	known := map[string]bool{}
	for _, r := range views.RendererCatalogue() {
		for _, p := range r.Params {
			known[p.Name] = true
		}
	}

	var missing, unknown []string
	for name := range known {
		if !declared[name] {
			missing = append(missing, name)
		}
	}
	for name := range declared {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unknown)

	switch {
	case len(missing) > 0 && len(unknown) > 0:
		return fmt.Errorf("render/params.js does not declare %s, and declares %s which no renderer takes",
			strings.Join(missing, ", "), strings.Join(unknown, ", "))
	case len(missing) > 0:
		return fmt.Errorf("render/params.js does not declare %s: a view carrying it would draw without that knob",
			strings.Join(missing, ", "))
	case len(unknown) > 0:
		return fmt.Errorf("render/params.js declares %s, which no renderer takes",
			strings.Join(unknown, ", "))
	}
	return nil
}
