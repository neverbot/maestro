package web_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// TestTheDrawingsHoleIsReservedBeforeItArrives is a source guard over
// one CSS rule, and it is a source guard because the thing it protects
// is invisible in every other kind of test.
func TestTheDrawingsHoleIsReservedBeforeItArrives(t *testing.T) {
	t.Parallel()
	styles, err := os.ReadFile("static/styles.css")
	assert.Must(t, err == nil, "read styles.css: %v", err)
	rule := regexp.MustCompile(`#view-root:empty\s*\{[^}]*\}`)
	found := rule.FindString(string(styles))
	if found == "" {
		t.Fatal("no `#view-root:empty` rule: the drawing's hole is not reserved, so the page jumps " +
			"when the picture arrives")
	}
	assert.Should(t, strings.Contains(found, "min-height"), "the reservation sets no min-height:\n%s", found)

	canvas, err := os.ReadFile("static/components/mst-canvas.js")
	assert.Must(t, err == nil, "read mst-canvas.js: %v", err)
	// The numbers mst-canvas sizes itself with, taken from its own
	// source rather than repeated here: a reservation that stops
	// matching the box it reserves for is the same jump again.
	for _, size := range []string{"70vh", "22rem"} {
		assert.Must(t, strings.Contains(string(canvas), size), "mst-canvas no longer sizes itself with %s, so this guard is comparing "+
			"the reservation against a number nothing uses", size)
		assert.Should(t, strings.Contains(found, size), "the reservation does not use %s, which is what the canvas will take:\n%s", size, found)
	}
}
