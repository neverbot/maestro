package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// Every component's one property with no runtime signature: **a
// component invents no words.**
var textNodeRE = regexp.MustCompile("[^\\s=]>([^<$`]*)")

// scanTemplateText returns every text node the component speaks in its
// own voice. It strips comments first, then the `css` block — a
// stylesheet is not prose and contains `>` in its selectors — and reads
// what is left.
func scanTemplateText(src string) []string {
	body := stripCSSBlock(stripComments(src))
	var offences []string
	for _, hit := range textNodeRE.FindAllStringSubmatch(body, -1) {
		if strings.TrimSpace(hit[1]) == "" {
			continue
		}
		offences = append(offences, strings.TrimSpace(hit[1]))
	}
	return offences
}

// stripCSSBlock removes every css`…` tagged template. Lit stylesheets
// carry no prose and do carry `>` (a child combinator), so leaving them
// in would make the scan above report selectors as sentences.
func stripCSSBlock(src string) string {
	var out strings.Builder
	for {
		at := strings.Index(src, "css`")
		if at < 0 {
			out.WriteString(src)
			return out.String()
		}
		out.WriteString(src[:at])
		rest := src[at+len("css`"):]
		end := strings.IndexByte(rest, '`')
		if end < 0 {
			return out.String()
		}
		src = rest[end+1:]
	}
}

// componentFiles is every component this server ships, discovered rather
// than listed.
// definesAComponent marks a file in the components directory as a
// component rather than as something the components share. A component
// puts an element on the page; a shared module does not, and the scans
// below are about what a designer reads on screen.
var definesAComponent = regexp.MustCompile(`customElements\.define\(|extends LitElement|attachShadow\(`)

func componentFiles(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob(filepath.Join("static", "components", "*.js"))
	assert.Must(t, err == nil, "glob components: %v", err)
	var found []string
	for _, path := range all {
		raw, err := os.ReadFile(path)
		assert.Must(t, err == nil, "read %s: %v", path, err)
		if definesAComponent.Match(raw) {
			found = append(found, path)
		}
	}
	sort.Strings(found)
	return found
}

func TestEveryComponentSpeaksOnlyItsModelsWords(t *testing.T) {
	t.Parallel()
	for _, path := range componentFiles(t) {
		raw, err := os.ReadFile(path)
		assert.Must(t, err == nil, "read %s: %v", path, err)
		if offences := scanTemplateText(string(raw)); len(offences) > 0 {
			t.Errorf("%s writes its own words into %d text node(s): %q\n"+
				"every sentence a designer reads belongs to a model under static/render/, which is where the "+
				"server's own sentences are carried verbatim and where a harness can read them",
				path, len(offences), offences)
		}
	}
}

// hidesASlot finds a template that hides a slot from assistive
// technology: an `aria-hidden="true"` followed, in the same template
// expression, by a `<slot>`.
var hidesASlot = regexp.MustCompile("(?s)aria-hidden=\"true\"[^`]*<slot")

// TestNoComponentHidesASlotFromAssistiveTechnology is the source-shape
// half of the fourth screen finding.
func TestNoComponentHidesASlotFromAssistiveTechnology(t *testing.T) {
	t.Parallel()
	scanned := 0
	for _, path := range componentFiles(t) {
		raw, err := os.ReadFile(path)
		assert.Must(t, err == nil, "read %s: %v", path, err)
		scanned++
		if hit := hidesASlot.FindString(string(raw)); hit != "" {
			t.Errorf("%s hides a slot from assistive technology:\n\t%q\n"+
				"what arrives through a slot is not only the drawing — the canvas brings the arrangement "+
				"menu and the ground panel, the table renderer brings its sort headers — so every control "+
				"in it becomes reachable by keyboard and announced to nobody. Mark the drawing itself "+
				"(mst-canvas.js's emitScene marks the <svg>), never the box it is slotted into",
				path, hit)
		}
	}
	assert.Must(t, scanned != 0, "scanned no component: a walk that reads nothing guards nothing")
	t.Logf("the slot-hiding scan read %d component(s)", scanned)
}
