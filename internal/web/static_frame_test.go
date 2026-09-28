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

// TestTheComponentScanReadsEveryComponent is the other half: the test
// above passes when it finds no offence and finds none both when the
// components are silent and when it never opened one. Naming the three
// that exist today is deliberate — a component deleted or renamed
// without this list being updated is a change somebody should have to
// look at — and the count assertion is what catches the next one
// arriving.
func TestTheComponentScanReadsEveryComponent(t *testing.T) {
	t.Parallel()
	found := componentFiles(t)
	want := []string{
		filepath.Join("static", "components", "mst-canvas.js"),
		filepath.Join("static", "components", "mst-dialog.js"),
		filepath.Join("static", "components", "mst-ground.js"),
		filepath.Join("static", "components", "mst-hint.js"),
		filepath.Join("static", "components", "mst-picker.js"),
		filepath.Join("static", "components", "mst-save-as.js"),
		filepath.Join("static", "components", "mst-table.js"),
		filepath.Join("static", "components", "mst-twin.js"),
		filepath.Join("static", "components", "mst-view-frame.js"),
	}
	assert.Must(t, len(found) == len(want), "the component scan read %v, want %v: a component this list does not name is a component "+
		"whose silence nobody has argued for", found, want)
	for i, path := range want {
		if found[i] != path {
			t.Errorf("the component scan read %q where %q was expected", found[i], path)
		}
	}
}

// TestTheTemplateTextScanReadsWhatItClaimsTo is the guard on the guard,
// in both directions. A scanner that found nothing because it matched
// nothing would pass the test above on any file at all, which is the
// failure mode this repository has hit twice: a guard that audits
// itself.
func TestTheTemplateTextScanReadsWhatItClaimsTo(t *testing.T) {
	t.Parallel()
	speaks := "const label = html`<div class=\"footer\">complete picture</div>`;"
	if got := scanTemplateText(speaks); len(got) != 1 || got[0] != "complete picture" {
		t.Errorf("the scan missed a hard-coded sentence: %q", got)
	}

	silent := "const label = html`<div class=\"footer\">${footer.text}</div>`;"
	if got := scanTemplateText(silent); len(got) != 0 {
		t.Errorf("the scan reported an interpolation as prose: %q", got)
	}

	// The three spellings that must not be mistaken for a text node: a
	// comparison, an arrow function, and a sentence inside a comment.
	for name, src := range map[string]string{
		"a comparison": "if (rows.length > 0) return rows;",
		"an arrow":     "rows.map((row) => row.pointer);",
		"a comment":    "// this view is a complete picture of the game\nconst a = 1;",
	} {
		if got := scanTemplateText(src); len(got) != 0 {
			t.Errorf("the scan read %s as prose: %q", name, got)
		}
	}

	// And the css block really is skipped, rather than the scan happening
	// to find nothing in it.
	styled := "static styles = css`.a > .b { color: red; }`;\nconst h = html`<p>a word</p>`;"
	if got := scanTemplateText(styled); len(got) != 1 || got[0] != "a word" {
		t.Errorf("the css skip ate the templates after it, or the selector was read as prose: %q", got)
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

// TestTheSlotHidingScanReadsWhatItClaimsTo is the guard on the guard, in
// both directions: a pattern that matched nothing would pass the test
// above on the very source it was written against.
func TestTheSlotHidingScanReadsWhatItClaimsTo(t *testing.T) {
	t.Parallel()
	for name, planted := range map[string]string{
		"the shape that shipped": "return html`<div class=\"canvas\" aria-hidden=\"true\"><slot></slot></div>`;",
		"the attribute last":     "html`<div><span aria-hidden=\"true\"></span><slot></slot></div>`",
		"across a line":          "html`<div aria-hidden=\"true\">\n  <slot></slot>\n</div>`",
	} {
		assert.Should(t, hidesASlot.MatchString(planted), "the scan missed %s: %q", name, planted)
	}
	for name, allowed := range map[string]string{
		"a slot with nothing hidden":         "html`<div class=\"canvas\"><slot></slot></div>`",
		"a hidden thing in another template": "html`<span aria-hidden=\"true\">x</span>`;\nhtml`<slot></slot>`",
	} {
		assert.Should(t, !hidesASlot.MatchString(allowed), "the scan objects to %s, which is fine: %q", name, allowed)
	}
}
