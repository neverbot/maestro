package markdown_test

import (
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/markdown"
)

// TestAFieldsSingleNewlineIsALineBreak is the whole reason this renderer
// is a second configuration: the values a game actually holds separate
// their lines with one \n, and the document renderer reads that as one
// paragraph.
func TestAFieldsSingleNewlineIsALineBreak(t *testing.T) {
	t.Parallel()
	got, err := markdown.RenderField("first line\nsecond line")
	assert.NoErr(t, err, "RenderField")
	assert.Must(t, strings.Contains(got, "<br"), "got %q, want a line break between the two lines", got)

	// And the document renderer still does not, because a wrapped
	// paragraph in a document is one paragraph.
	doc, err := markdown.Render("first line\nsecond line")
	assert.NoErr(t, err, "Render")
	assert.Must(t, !strings.Contains(doc, "<br"), "the document renderer grew hard wraps: %q", doc)
}

// TestAFieldCannotCarryMarkupOfItsOwn is the safety half. A field value
// is written by an agent and read by a browser that inserts this string,
// so the renderer admitting a tag would be the injection that costs.
func TestAFieldCannotCarryMarkupOfItsOwn(t *testing.T) {
	t.Parallel()
	got, err := markdown.RenderField("<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>")
	assert.NoErr(t, err, "RenderField")
	assert.Must(t, !strings.Contains(got, "<script"), "a script tag survived: %q", got)
	assert.Must(t, !strings.Contains(got, "onerror"), "an event attribute survived: %q", got)
}

// TestAFieldsLinksAreHeldToTheSameSchemes pins that the transformer the
// document renderer carries is on this one too: a javascript: href in a
// field value would otherwise be a live link on the entity page.
func TestAFieldsLinksAreHeldToTheSameSchemes(t *testing.T) {
	t.Parallel()
	got, err := markdown.RenderField("[go](javascript:alert(1)) and [home](https://example.com)")
	assert.NoErr(t, err, "RenderField")
	// `href="#"` and not merely the absence of the scheme: goldmark drops
	// a scheme it does not know on its own, so an assertion that the
	// string is gone passes with the transformer removed and proves
	// nothing. The rewrite this renderer promises is the one the document
	// renderer makes, and `#` is what it writes.
	assert.Must(t, strings.Contains(got, `href="#"`), "the refused destination is %q, want it rewritten to #", got)
	assert.Must(t, strings.Contains(got, "https://example.com"), "an http link was rewritten too: %q", got)
}
