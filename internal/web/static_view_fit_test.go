package web_test

import (
	"os"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// **The fit has to run after the width, and only a source guard can say
// so.**
func TestTheViewIsFittedAfterItsWidthIsApplied(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("static/pages/view.js")
	assert.Must(t, err == nil, "read view.js: %v", err)
	source := withoutComments(string(raw))

	width := strings.Index(source, "applyWidth(state, state.narrow === true)")
	if width < 0 {
		t.Fatal("view.js no longer applies the window's width in draw's finally, " +
			"which is what decides whether the canvas is on screen at all")
	}
	fit := strings.Index(source, "await fitAfterLayout(state")
	if fit < 0 {
		t.Fatal("view.js no longer fits the canvas after a draw, so a picture larger than its " +
			"canvas opens at the origin at 1x with part of the answer outside the clip")
	}
	assert.Should(t, fit >= width, "view.js fits the canvas at byte %d and applies the width at byte %d: the fit "+
		"measures a canvas that is still hidden, gets 0x0, and does nothing", fit, width)

	// And the fit is reached exactly once, from the one place that knows
	// the width has been applied.
	if strings.Count(source, "await fitAfterLayout(state") != 1 {
		t.Error("view.js calls fitAfterLayout more than once: the fit is a decision about the " +
			"first draw of a view, and a second caller is a second answer to it")
	}
	body := drawPictureBody(t, source)
	for _, called := range []string{"fitAfterLayout(", "fitOnce("} {
		assert.Should(t, !strings.Contains(body, called), "drawPicture calls %s, and it runs before the width is applied: the canvas "+
			"it would measure is the hidden one", called)
	}
}

// drawPictureBody is the text of `drawPicture`, from its declaration to
// the declaration that follows it.
func drawPictureBody(t *testing.T, source string) string {
	t.Helper()
	const start = "async function drawPicture("
	from := strings.Index(source, start)
	assert.Must(t, from >= 0, "view.js has no drawPicture: this guard is holding nothing")
	rest := source[from+len(start):]
	next := strings.Index(rest, "\nexport function ")
	if next < 0 {
		return rest
	}
	return rest[:next]
}
