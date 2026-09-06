package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every component's one property with no runtime signature: **a
// component invents no words.**
//
// Every sentence a designer reads in the frame is built in
// internal/web/static/render/scene.js, which is a pure function a Node
// harness drives and a mutation turns red. The component is supposed to
// be a painter: it decides where each string sits and what it is painted
// with, and never what it says. Nothing at runtime can see the
// difference — a component that hard-coded "complete picture" into its
// footer would render perfectly, pass every check in
// internal/web/jstest/frame_test.mjs (which never loads it) and be
// exactly the drift the whole task exists to prevent, since the next
// five renderers would each grow their own copy.
//
// So it is held by shape, the way internal/web/static_client_test.go
// holds "this module composes no sentence": **every text node in a
// component's templates is either whitespace or an interpolation.** A
// word typed between two tags is a failing test.
//
// The one deliberate exception is the label of the single action the
// diagnostics panel offers, which is a constant at the top of
// mst-view-frame.js and reaches the template as `${RUN_ANYWAY_LABEL}` —
// an interpolation like any other, and named there rather than in the
// model because it names a *control* and not a state of the answer.
//
// **It walks the directory rather than naming a file.** The first
// version of this guard named mst-view-frame.js literally, which is this
// project's standing failure — a rule established and not carried one
// step along — waiting to happen: the text twin landed the next task and
// would have been the first component nobody scanned, and it is the
// component that renders the most game strings of any. Every component
// is read on the day it lands, and TestTheComponentScanReadsEveryComponent
// is what stops the walk quietly finding none.

// textNodeRE finds a candidate text node: a `>` that closes a tag —
// preceded by something that is neither whitespace nor `=`, which is
// what tells `</span>` from a JavaScript `a > b` and from an arrow
// `() =>` — followed by whatever runs until the region ends.
//
// A region ends at `<` (the next tag), at `$` (an interpolation) or at a
// backtick (the end of the template). Anything non-blank before one of
// those is a word the component is saying in its own voice.
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
func componentFiles(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join("static", "components", "*.js"))
	if err != nil {
		t.Fatalf("glob components: %v", err)
	}
	sort.Strings(found)
	return found
}

func TestEveryComponentSpeaksOnlyItsModelsWords(t *testing.T) {
	for _, path := range componentFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
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
//
// mst-canvas.js holds **no Lit template at all**, so the scan above
// passes over it vacuously and the property it stands for is held
// elsewhere: internal/web/jstest/canvas_test.mjs asserts at runtime that
// every character in the emitted SVG tree is a mark's own text, and that
// the canvas stylesheet generates no `content:` of its own. A component
// whose silence no test can see is a component this list should not have
// let in quietly, which is why the argument is written down here.
func TestTheComponentScanReadsEveryComponent(t *testing.T) {
	found := componentFiles(t)
	want := []string{
		filepath.Join("static", "components", "mst-canvas.js"),
		filepath.Join("static", "components", "mst-twin.js"),
		filepath.Join("static", "components", "mst-view-frame.js"),
	}
	if len(found) != len(want) {
		t.Fatalf("the component scan read %v, want %v: a component this list does not name is a component "+
			"whose silence nobody has argued for", found, want)
	}
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
