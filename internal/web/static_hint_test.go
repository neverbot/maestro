package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// TestTheHint drives internal/web/jstest/hint_test.mjs, which holds the
// behaviour: the explanation opens on focus as well as on hover, Escape
// closes it, the trigger names the panel, and the sentence arrives as
// text.
func TestTheHint(t *testing.T) {
	t.Parallel()
	runJSTest(t, "jstest/hint_test.mjs")
}

// TestNoStyleSheetIsCutInHalfByABacktick is the guard for a defect that
// landed three times in one afternoon and is invisible to `node --check`.
func TestNoStyleSheetIsCutInHalfByABacktick(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("static/components/*.js")
	assert.Must(t, err == nil, "glob components: %v", err)
	assert.Must(t, len(files) != 0, "no component modules found")
	// The opening of a CSS constant: `const NAME_CSS = ` followed by the
	// backtick that opens the literal, and the line that closes it.
	opens := regexp.MustCompile("^(?:export )?const [A-Z_]*CSS = `$")
	// A backtick that is not escaped. `[^\\]` and not a lookbehind,
	// which Go's regexp does not have; a literal starting with one is
	// caught by the first alternative.
	bareBacktick := regexp.MustCompile("(^|[^\\\\])`")
	for _, path := range files {
		raw, err := os.ReadFile(path)
		assert.Must(t, err == nil, "read %s: %v", path, err)
		inside := false
		for n, line := range strings.Split(string(raw), "\n") {
			if !inside {
				inside = opens.MatchString(line)
				continue
			}
			if line == "`;" {
				inside = false
				continue
			}
			// An escaped backtick is fine and mst-canvas.js uses them:
			// what ends the literal is a bare one. `${` is the same hole
			// from the other side — it interpolates rather than closing,
			// and a sheet is not a place for a value.
			assert.Should(t, !bareBacktick.MatchString(line) && !strings.Contains(line, "${"), "%s:%d: a backtick or an interpolation inside a CSS literal ends it, and the "+
				"rest of the sheet is then read as JavaScript:\n\t%s", path, n+1, line)
		}
		assert.Should(t, !inside, "%s: a CSS literal is never closed", path)
	}
}
