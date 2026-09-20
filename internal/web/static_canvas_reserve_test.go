package web_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestTheDrawingsHoleIsReservedBeforeItArrives is a source guard over
// one CSS rule, and it is a source guard because the thing it protects
// is invisible in every other kind of test.
//
// `#view-root` is empty until pages/view.js mounts the frame. Without a
// reserved height the page lays out at its natural size and then jumps
// — measured at 570px, which is 70vh on a 1440×815 window — under a
// reader who has already started reading the sentence above it. Nothing
// fails when that rule is deleted: the picture still arrives, the tests
// still pass, and the page is merely unpleasant in a way no assertion
// sees.
//
// The two halves both matter and both are checked:
//
//   - it is `:empty`, so the reservation lasts exactly as long as the
//     hole does. The table renderer is slotted into the same element and
//     must size itself; a floor that outlived the mount would force a
//     thousand-row table into a 70vh box, which is the defect
//     mst-canvas.js's own comment records from the other direction.
//   - the reserved height is the box mst-canvas gives itself. A
//     reservation of a different size is still a jump, just a smaller
//     one.
func TestTheDrawingsHoleIsReservedBeforeItArrives(t *testing.T) {
	t.Parallel()
	styles, err := os.ReadFile("static/styles.css")
	if err != nil {
		t.Fatalf("read styles.css: %v", err)
	}
	rule := regexp.MustCompile(`#view-root:empty\s*\{[^}]*\}`)
	found := rule.FindString(string(styles))
	if found == "" {
		t.Fatal("no `#view-root:empty` rule: the drawing's hole is not reserved, so the page jumps " +
			"when the picture arrives")
	}
	if !strings.Contains(found, "min-height") {
		t.Errorf("the reservation sets no min-height:\n%s", found)
	}

	canvas, err := os.ReadFile("static/components/mst-canvas.js")
	if err != nil {
		t.Fatalf("read mst-canvas.js: %v", err)
	}
	// The numbers mst-canvas sizes itself with, taken from its own
	// source rather than repeated here: a reservation that stops
	// matching the box it reserves for is the same jump again.
	for _, size := range []string{"70vh", "22rem"} {
		if !strings.Contains(string(canvas), size) {
			t.Fatalf("mst-canvas no longer sizes itself with %s, so this guard is comparing "+
				"the reservation against a number nothing uses", size)
		}
		if !strings.Contains(found, size) {
			t.Errorf("the reservation does not use %s, which is what the canvas will take:\n%s", size, found)
		}
	}
}
